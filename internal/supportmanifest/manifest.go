// Package supportmanifest records measured support for the ClickHouse API.
package supportmanifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/IlyaGulya/chgen/internal/apiinventory"
)

// FormatVersion identifies the support manifest file format.
const FormatVersion = 2

// Status is one explicit support verdict.
type Status string

const (
	// SupportedMeasured means that chgen has a rule and measured evidence.
	SupportedMeasured Status = "supported_measured"
	// ExplicitlyRefused means that chgen has a deliberate refusal.
	ExplicitlyRefused Status = "explicitly_refused"
	// PartiallyMeasured means that measured products have different verdicts.
	PartiallyMeasured Status = "partially_measured"
	// NotApplicable means that the item is outside the chgen API.
	NotApplicable Status = "not_applicable"
	// NotMeasured means that no support claim exists.
	NotMeasured Status = "not_measured"
)

// Kind identifies one inventory class.
type Kind string

const (
	KindFunction                    Kind = "function"
	KindFunctionAlias               Kind = "function_alias"
	KindAggregateFunctionCombinator Kind = "aggregate_function_combinator"
	KindDataType                    Kind = "data_type_family"
	KindDataTypeAlias               Kind = "data_type_alias"
	KindTableFunction               Kind = "table_function"
	KindSetting                     Kind = "setting"
)

// Manifest assigns one support verdict to every inventory item.
type Manifest struct {
	FormatVersion   int                 `json:"format_version"`
	Source          apiinventory.Source `json:"source"`
	InventorySHA256 string              `json:"inventory_sha256"`
	Entries         []Entry             `json:"entries"`
}

// Entry is one support verdict with its evidence and test witness.
type Entry struct {
	Kind             Kind     `json:"kind"`
	Name             string   `json:"name"`
	AliasTo          string   `json:"alias_to,omitempty"`
	Status           Status   `json:"status"`
	SemanticFamily   string   `json:"semantic_family"`
	Evidence         string   `json:"evidence"`
	TestWitness      string   `json:"test_witness"`
	AcceptedProducts []string `json:"accepted_products,omitempty"`
	RefusedProducts  []string `json:"refused_products,omitempty"`
}

// Override changes the default verdict for one exact inventory item.
type Override struct {
	Kind             Kind     `json:"kind"`
	Name             string   `json:"name"`
	Status           Status   `json:"status"`
	SemanticFamily   string   `json:"semantic_family"`
	Evidence         string   `json:"evidence"`
	TestWitness      string   `json:"test_witness"`
	AcceptedProducts []string `json:"accepted_products,omitempty"`
	RefusedProducts  []string `json:"refused_products,omitempty"`
}

// Config contains the strict support overrides.
type Config struct {
	FormatVersion int        `json:"format_version"`
	Overrides     []Override `json:"overrides"`
}

// Build creates a complete manifest from the inventory and measured sources.
func Build(inventory apiinventory.Inventory, functionRoster map[string]FunctionMeasurement, config Config) (Manifest, error) {
	if err := inventory.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("validate API inventory: %w", err)
	}
	if config.FormatVersion != FormatVersion {
		return Manifest{}, fmt.Errorf("support config format version is %d, want %d", config.FormatVersion, FormatVersion)
	}
	inventoryBytes, err := apiinventory.Marshal(inventory)
	if err != nil {
		return Manifest{}, err
	}
	digest := sha256.Sum256(inventoryBytes)
	manifest := Manifest{
		FormatVersion:   FormatVersion,
		Source:          inventory.Source,
		InventorySHA256: hex.EncodeToString(digest[:]),
		Entries:         inventoryEntries(inventory),
	}
	byKey := make(map[string]*Entry, len(manifest.Entries))
	for index := range manifest.Entries {
		entry := &manifest.Entries[index]
		byKey[entryKey(entry.Kind, entry.Name)] = entry
	}

	rosterNames := make(map[string]FunctionMeasurement, len(functionRoster))
	for name, measurement := range functionRoster {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(measurement.Family) == "" || measurement.Family == "unknown" ||
			measurement.Evidence == "" || measurement.TestWitness == "" {
			return Manifest{}, fmt.Errorf("function semantic roster has an incomplete family for %q", name)
		}
		rosterNames[strings.ToLower(name)] = measurement
	}
	for _, entry := range byKey {
		if entry.Kind != KindFunction && entry.Kind != KindFunctionAlias {
			continue
		}
		measurement, measured := rosterNames[strings.ToLower(entry.Name)]
		if !measured {
			continue
		}
		entry.Status = SupportedMeasured
		entry.SemanticFamily = measurement.Family
		entry.Evidence = measurement.Evidence
		entry.TestWitness = measurement.TestWitness
	}

	seenOverrides := make(map[string]struct{}, len(config.Overrides))
	for _, override := range config.Overrides {
		key := entryKey(override.Kind, override.Name)
		if _, exists := seenOverrides[key]; exists {
			return Manifest{}, fmt.Errorf("support config repeats %s", key)
		}
		seenOverrides[key] = struct{}{}
		entry, found := byKey[key]
		if !found {
			return Manifest{}, fmt.Errorf("support override %s is stale because the API inventory has no such item", key)
		}
		entry.Status = override.Status
		entry.SemanticFamily = override.SemanticFamily
		entry.Evidence = override.Evidence
		entry.TestWitness = override.TestWitness
		entry.AcceptedProducts = append([]string(nil), override.AcceptedProducts...)
		entry.RefusedProducts = append([]string(nil), override.RefusedProducts...)
	}
	sortEntries(manifest.Entries)
	if err := manifest.ValidateAgainst(inventory); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func inventoryEntries(inventory apiinventory.Inventory) []Entry {
	var entries []Entry
	addFunctions := func(kind Kind, functions []apiinventory.Function) {
		for _, function := range functions {
			entries = append(entries, defaultEntry(kind, function.Name, function.AliasTo))
		}
	}
	addFunctions(KindFunction, inventory.Functions.BuiltIn.Canonical)
	addFunctions(KindFunctionAlias, inventory.Functions.BuiltIn.Aliases)
	addFunctions(KindFunction, inventory.Functions.UserDefined.Canonical)
	addFunctions(KindFunctionAlias, inventory.Functions.UserDefined.Aliases)
	for _, dataType := range inventory.DataTypeFamilies.Canonical {
		entries = append(entries, defaultEntry(KindDataType, dataType.Name, ""))
	}
	for _, dataType := range inventory.DataTypeFamilies.Aliases {
		entries = append(entries, defaultEntry(KindDataTypeAlias, dataType.Name, dataType.AliasTo))
	}
	for _, combinator := range inventory.AggregateFunctionCombinators {
		entries = append(entries, defaultEntry(KindAggregateFunctionCombinator, combinator.Name, ""))
	}
	for _, tableFunction := range inventory.TableFunctions {
		entries = append(entries, defaultEntry(KindTableFunction, tableFunction.Name, ""))
	}
	for _, setting := range inventory.Settings {
		entries = append(entries, defaultEntry(KindSetting, setting.Name, setting.AliasFor))
	}
	return entries
}

// ValidateCurrent checks a manifest against all classification sources.
func ValidateCurrent(inventory apiinventory.Inventory, functionRoster map[string]FunctionMeasurement, config Config, candidate Manifest) error {
	if err := candidate.ValidateAgainst(inventory); err != nil {
		return err
	}
	expected, err := Build(inventory, functionRoster, config)
	if err != nil {
		return err
	}
	expectedBytes, err := Marshal(expected)
	if err != nil {
		return err
	}
	candidateBytes, err := Marshal(candidate)
	if err != nil {
		return err
	}
	if string(expectedBytes) != string(candidateBytes) {
		return fmt.Errorf("support manifest does not match its classification sources")
	}
	return nil
}

func defaultEntry(kind Kind, name, aliasTo string) Entry {
	return Entry{
		Kind:           kind,
		Name:           name,
		AliasTo:        aliasTo,
		Status:         NotMeasured,
		SemanticFamily: "unclassified-" + strings.ReplaceAll(string(kind), "_", "-"),
		Evidence:       "clickhouse-api-inventory/observed-only",
		TestWitness:    "internal/engine/support_manifest_test.go:TestPinnedSupportManifestCoversInventory",
	}
}

// ValidateAgainst checks the format and its exact inventory coverage.
func (m Manifest) ValidateAgainst(inventory apiinventory.Inventory) error {
	if m.FormatVersion != FormatVersion {
		return fmt.Errorf("support manifest format version is %d, want %d", m.FormatVersion, FormatVersion)
	}
	if m.Source != inventory.Source {
		return fmt.Errorf("support manifest source does not match the API inventory source")
	}
	inventoryBytes, err := apiinventory.Marshal(inventory)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(inventoryBytes)
	if m.InventorySHA256 != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("support manifest inventory digest is stale")
	}
	expected := inventoryEntries(inventory)
	expectedKeys := make(map[string]string, len(expected))
	for _, entry := range expected {
		expectedKeys[entryKey(entry.Kind, entry.Name)] = entry.AliasTo
	}
	seen := make(map[string]struct{}, len(m.Entries))
	previous := ""
	for _, entry := range m.Entries {
		key := entryKey(entry.Kind, entry.Name)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("support manifest repeats %s", key)
		}
		seen[key] = struct{}{}
		expectedAlias, exists := expectedKeys[key]
		if !exists {
			return fmt.Errorf("support manifest has stale item %s", key)
		}
		if entry.AliasTo != expectedAlias {
			return fmt.Errorf("support manifest item %s has alias target %q, want %q", key, entry.AliasTo, expectedAlias)
		}
		if previous != "" && key < previous {
			return fmt.Errorf("support manifest is not sorted at %s", key)
		}
		previous = key
		if !validStatus(entry.Status) {
			return fmt.Errorf("support manifest item %s has unknown status %q", key, entry.Status)
		}
		if entry.SemanticFamily == "" || entry.Evidence == "" || entry.TestWitness == "" {
			return fmt.Errorf("support manifest item %s has incomplete evidence", key)
		}
		if entry.Status == PartiallyMeasured {
			if len(entry.AcceptedProducts) == 0 || len(entry.RefusedProducts) == 0 {
				return fmt.Errorf("support manifest item %s has incomplete partial products", key)
			}
		} else if len(entry.AcceptedProducts) != 0 || len(entry.RefusedProducts) != 0 {
			return fmt.Errorf("support manifest item %s has products without partial status", key)
		}
		if err := validateProducts(key+":accepted", entry.AcceptedProducts); err != nil {
			return err
		}
		if err := validateProducts(key+":refused", entry.RefusedProducts); err != nil {
			return err
		}
		accepted := make(map[string]struct{}, len(entry.AcceptedProducts))
		for _, product := range entry.AcceptedProducts {
			accepted[product] = struct{}{}
		}
		for _, product := range entry.RefusedProducts {
			if _, exists := accepted[product]; exists {
				return fmt.Errorf("support manifest item %s accepts and refuses product %q", key, product)
			}
		}
	}
	for key := range expectedKeys {
		if _, exists := seen[key]; !exists {
			return fmt.Errorf("support manifest has no verdict for %s", key)
		}
	}
	return nil
}

func validStatus(status Status) bool {
	switch status {
	case SupportedMeasured, ExplicitlyRefused, PartiallyMeasured, NotApplicable, NotMeasured:
		return true
	default:
		return false
	}
}

func entryKey(kind Kind, name string) string { return string(kind) + ":" + name }

func sortEntries(entries []Entry) {
	for index := range entries {
		sort.Strings(entries[index].AcceptedProducts)
		sort.Strings(entries[index].RefusedProducts)
	}
	sort.Slice(entries, func(a, b int) bool {
		return entryKey(entries[a].Kind, entries[a].Name) < entryKey(entries[b].Kind, entries[b].Name)
	})
}

func validateProducts(class string, products []string) error {
	previous := ""
	for index, product := range products {
		if strings.TrimSpace(product) == "" {
			return fmt.Errorf("support manifest class %s has an empty product", class)
		}
		if index > 0 && product <= previous {
			return fmt.Errorf("support manifest class %s is not unique and sorted at %q", class, product)
		}
		previous = product
	}
	return nil
}

// Marshal returns the stable manifest representation.
func Marshal(manifest Manifest) ([]byte, error) {
	sortEntries(manifest.Entries)
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
