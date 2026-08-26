// Package apiinventory reads and compares the ClickHouse API surface.
package apiinventory

import (
	"encoding/json"
	"fmt"
	"sort"
)

// FormatVersion identifies the inventory file format.
const FormatVersion = 3

// Inventory is one stable view of a ClickHouse server API.
type Inventory struct {
	FormatVersion                int                           `json:"format_version"`
	Source                       Source                        `json:"source"`
	Functions                    FunctionInventory             `json:"functions"`
	DataTypeFamilies             DataTypeInventory             `json:"data_type_families"`
	AggregateFunctionCombinators []AggregateFunctionCombinator `json:"aggregate_function_combinators"`
	TableFunctions               []TableFunction               `json:"table_functions"`
	Settings                     []Setting                     `json:"settings"`
}

// Source identifies the ClickHouse build that produced an inventory.
type Source struct {
	Version  string `json:"version"`
	Revision uint64 `json:"revision"`
	BuildID  string `json:"build_id"`
}

// FunctionInventory separates server functions by origin and alias status.
type FunctionInventory struct {
	BuiltIn     FunctionSet `json:"built_in"`
	UserDefined FunctionSet `json:"user_defined"`
}

// FunctionSet contains canonical names and aliases from one origin class.
type FunctionSet struct {
	Canonical []Function `json:"canonical"`
	Aliases   []Function `json:"aliases"`
}

// Function contains stable callable metadata from system.functions.
type Function struct {
	Name            string `json:"name"`
	IsAggregate     bool   `json:"is_aggregate"`
	CaseInsensitive bool   `json:"case_insensitive"`
	AliasTo         string `json:"alias_to,omitempty"`
	Origin          string `json:"origin"`
	IntroducedIn    string `json:"introduced_in,omitempty"`
}

// DataTypeInventory separates canonical type families from aliases.
type DataTypeInventory struct {
	Canonical []DataTypeFamily `json:"canonical"`
	Aliases   []DataTypeFamily `json:"aliases"`
}

// DataTypeFamily contains stable metadata from system.data_type_families.
type DataTypeFamily struct {
	Name            string `json:"name"`
	CaseInsensitive bool   `json:"case_insensitive"`
	AliasTo         string `json:"alias_to,omitempty"`
}

// AggregateFunctionCombinator contains stable combinator metadata.
type AggregateFunctionCombinator struct {
	Name       string `json:"name"`
	IsInternal bool   `json:"is_internal"`
}

// TableFunction contains stable metadata from system.table_functions.
type TableFunction struct {
	Name          string `json:"name"`
	AllowReadonly bool   `json:"allow_readonly"`
}

// Setting contains stable API metadata from system.settings.
type Setting struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Default    string `json:"default"`
	AliasFor   string `json:"alias_for,omitempty"`
	IsObsolete bool   `json:"is_obsolete"`
	Tier       string `json:"tier"`
}

// Normalize sorts all name-based lists. It makes the file deterministic.
func (i *Inventory) Normalize() {
	i.Functions.BuiltIn.Canonical = nonNil(i.Functions.BuiltIn.Canonical)
	i.Functions.BuiltIn.Aliases = nonNil(i.Functions.BuiltIn.Aliases)
	i.Functions.UserDefined.Canonical = nonNil(i.Functions.UserDefined.Canonical)
	i.Functions.UserDefined.Aliases = nonNil(i.Functions.UserDefined.Aliases)
	i.DataTypeFamilies.Canonical = nonNil(i.DataTypeFamilies.Canonical)
	i.DataTypeFamilies.Aliases = nonNil(i.DataTypeFamilies.Aliases)
	i.AggregateFunctionCombinators = nonNil(i.AggregateFunctionCombinators)
	i.TableFunctions = nonNil(i.TableFunctions)
	i.Settings = nonNil(i.Settings)
	sortFunctions(i.Functions.BuiltIn.Canonical)
	sortFunctions(i.Functions.BuiltIn.Aliases)
	sortFunctions(i.Functions.UserDefined.Canonical)
	sortFunctions(i.Functions.UserDefined.Aliases)
	sort.Slice(i.DataTypeFamilies.Canonical, func(a, b int) bool {
		return i.DataTypeFamilies.Canonical[a].Name < i.DataTypeFamilies.Canonical[b].Name
	})
	sort.Slice(i.DataTypeFamilies.Aliases, func(a, b int) bool {
		return i.DataTypeFamilies.Aliases[a].Name < i.DataTypeFamilies.Aliases[b].Name
	})
	sort.Slice(i.AggregateFunctionCombinators, func(a, b int) bool {
		return i.AggregateFunctionCombinators[a].Name < i.AggregateFunctionCombinators[b].Name
	})
	sort.Slice(i.TableFunctions, func(a, b int) bool {
		return i.TableFunctions[a].Name < i.TableFunctions[b].Name
	})
	sort.Slice(i.Settings, func(a, b int) bool {
		return i.Settings[a].Name < i.Settings[b].Name
	})
}

func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

func sortFunctions(functions []Function) {
	sort.Slice(functions, func(a, b int) bool { return functions[a].Name < functions[b].Name })
}

// Validate refuses an incomplete or inconsistent inventory.
func (i Inventory) Validate() error {
	if i.FormatVersion != FormatVersion {
		return fmt.Errorf("inventory format version is %d, want %d", i.FormatVersion, FormatVersion)
	}
	if i.Source.Version == "" || i.Source.Revision == 0 || i.Source.BuildID == "" {
		return fmt.Errorf("inventory source identity is incomplete")
	}
	required := []struct {
		name string
		len  int
	}{
		{"functions.built_in.canonical", len(i.Functions.BuiltIn.Canonical)},
		{"functions.built_in.aliases", len(i.Functions.BuiltIn.Aliases)},
		{"data_type_families.canonical", len(i.DataTypeFamilies.Canonical)},
		{"data_type_families.aliases", len(i.DataTypeFamilies.Aliases)},
		{"aggregate_function_combinators", len(i.AggregateFunctionCombinators)},
		{"table_functions", len(i.TableFunctions)},
		{"settings", len(i.Settings)},
	}
	for _, class := range required {
		if class.len == 0 {
			return fmt.Errorf("inventory class %s is empty", class.name)
		}
	}
	if err := validateFunctions("functions.built_in.canonical", i.Functions.BuiltIn.Canonical, "System", false); err != nil {
		return err
	}
	if err := validateFunctions("functions.built_in.aliases", i.Functions.BuiltIn.Aliases, "System", true); err != nil {
		return err
	}
	if err := validateFunctions("functions.user_defined.canonical", i.Functions.UserDefined.Canonical, "", false); err != nil {
		return err
	}
	if err := validateFunctions("functions.user_defined.aliases", i.Functions.UserDefined.Aliases, "", true); err != nil {
		return err
	}
	if err := validateNamed("data_type_families.canonical", dataTypeNames(i.DataTypeFamilies.Canonical)); err != nil {
		return err
	}
	if err := validateNamed("data_type_families.aliases", dataTypeNames(i.DataTypeFamilies.Aliases)); err != nil {
		return err
	}
	if err := validateNamed("aggregate_function_combinators", aggregateFunctionCombinatorNames(i.AggregateFunctionCombinators)); err != nil {
		return err
	}
	for _, item := range i.DataTypeFamilies.Canonical {
		if item.AliasTo != "" {
			return fmt.Errorf("canonical data type %q has alias target %q", item.Name, item.AliasTo)
		}
	}
	for _, item := range i.DataTypeFamilies.Aliases {
		if item.AliasTo == "" {
			return fmt.Errorf("data type alias %q has no target", item.Name)
		}
	}
	if err := validateNamed("table_functions", tableFunctionNames(i.TableFunctions)); err != nil {
		return err
	}
	if err := validateNamed("settings", settingNames(i.Settings)); err != nil {
		return err
	}
	return nil
}

func validateFunctions(class string, functions []Function, requiredOrigin string, aliases bool) error {
	if err := validateNamed(class, functionNames(functions)); err != nil {
		return err
	}
	for _, function := range functions {
		if requiredOrigin != "" && function.Origin != requiredOrigin {
			return fmt.Errorf("function %q in %s has origin %q", function.Name, class, function.Origin)
		}
		if requiredOrigin == "" && function.Origin == "System" {
			return fmt.Errorf("user-defined function %q has System origin", function.Name)
		}
		if aliases && function.AliasTo == "" {
			return fmt.Errorf("function alias %q has no target", function.Name)
		}
		if !aliases && function.AliasTo != "" {
			return fmt.Errorf("canonical function %q has alias target %q", function.Name, function.AliasTo)
		}
	}
	return nil
}

func validateNamed(class string, names []string) error {
	seen := make(map[string]struct{}, len(names))
	previous := ""
	for index, name := range names {
		if name == "" {
			return fmt.Errorf("inventory class %s has an empty name", class)
		}
		if _, found := seen[name]; found {
			return fmt.Errorf("inventory class %s has duplicate name %q", class, name)
		}
		seen[name] = struct{}{}
		if index > 0 && name < previous {
			return fmt.Errorf("inventory class %s is not sorted at %q", class, name)
		}
		previous = name
	}
	return nil
}

func functionNames(items []Function) []string {
	names := make([]string, len(items))
	for index, item := range items {
		names[index] = item.Name
	}
	return names
}

func dataTypeNames(items []DataTypeFamily) []string {
	names := make([]string, len(items))
	for index, item := range items {
		names[index] = item.Name
	}
	return names
}

func tableFunctionNames(items []TableFunction) []string {
	names := make([]string, len(items))
	for index, item := range items {
		names[index] = item.Name
	}
	return names
}

func aggregateFunctionCombinatorNames(items []AggregateFunctionCombinator) []string {
	names := make([]string, len(items))
	for index, item := range items {
		names[index] = item.Name
	}
	return names
}

func settingNames(items []Setting) []string {
	names := make([]string, len(items))
	for index, item := range items {
		names[index] = item.Name
	}
	return names
}

// Marshal returns the stable inventory file representation.
func Marshal(inventory Inventory) ([]byte, error) {
	inventory.Normalize()
	if err := inventory.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
