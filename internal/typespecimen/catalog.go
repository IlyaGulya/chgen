// Package typespecimen measures real ClickHouse columns for type families.
package typespecimen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/IlyaGulya/chgen/internal/apiinventory"
)

// FormatVersion identifies the type specimen catalog format.
const FormatVersion = 2

const (
	clickHouseRefusalClass = "clickhouse_refused"
	notApplicableClass     = "not_applicable"
)

// Status is one real-column verdict.
type Status string

const (
	Accepted          Status = "accepted"
	ExplicitlyRefused Status = "explicitly_refused"
	NotApplicable     Status = "not_applicable"
)

// Catalog records one verdict for every canonical server type family.
type Catalog struct {
	FormatVersion   int                 `json:"format_version"`
	Source          apiinventory.Source `json:"source"`
	InventorySHA256 string              `json:"inventory_sha256"`
	Entries         []Entry             `json:"entries"`
}

// Entry records the real-column probe for one type family.
type Entry struct {
	Family           string `json:"family"`
	Column           string `json:"column"`
	DeclaredType     string `json:"declared_type"`
	Status           Status `json:"status"`
	ObservedType     string `json:"observed_type,omitempty"`
	SeedWitness      string `json:"seed_witness,omitempty"`
	AnalysisWitness  string `json:"analysis_witness,omitempty"`
	ExecutionWitness string `json:"execution_witness,omitempty"`
	RefusalStage     string `json:"refusal_stage,omitempty"`
	RefusalCode      int    `json:"refusal_code,omitempty"`
	RefusalClass     string `json:"refusal_class,omitempty"`
}

// CandidateType returns one stable specimen spelling for a family.
func CandidateType(family string) string {
	special := map[string]string{
		"AggregateFunction":       "AggregateFunction(sum, Int64)",
		"Array":                   "Array(Int32)",
		"DateTime64":              "DateTime64(3)",
		"Decimal":                 "Decimal(18, 4)",
		"Decimal32":               "Decimal32(4)",
		"Decimal64":               "Decimal64(4)",
		"Decimal128":              "Decimal128(4)",
		"Decimal256":              "Decimal256(4)",
		"Enum":                    "Enum('a' = 1, 'b' = 2)",
		"Enum8":                   "Enum8('a' = 1, 'b' = 2)",
		"Enum16":                  "Enum16('a' = 1, 'b' = 2)",
		"FixedString":             "FixedString(8)",
		"LowCardinality":          "LowCardinality(String)",
		"Map":                     "Map(String, Int32)",
		"Nested":                  "Nested(a Int32, b String)",
		"Nullable":                "Nullable(Int32)",
		"Object":                  "Object('json')",
		"SimpleAggregateFunction": "SimpleAggregateFunction(sum, Int64)",
		"Time64":                  "Time64(3)",
		"Tuple":                   "Tuple(Int32, String)",
		"Variant":                 "Variant(Int32, String)",
	}
	if candidate := special[family]; candidate != "" {
		return candidate
	}
	return family
}

// ColumnName returns the stable fixture column for a family.
func ColumnName(family string) string {
	var name strings.Builder
	name.WriteString("api_")
	for index, character := range family {
		if unicode.IsUpper(character) && index > 0 {
			name.WriteByte('_')
		}
		name.WriteRune(unicode.ToLower(character))
	}
	return name.String()
}

// InventoryDigest returns the catalog identity of an API inventory.
func InventoryDigest(inventory apiinventory.Inventory) (string, error) {
	data, err := apiinventory.Marshal(inventory)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// ValidateAgainst checks exact canonical-family coverage and all witnesses.
func (c Catalog) ValidateAgainst(inventory apiinventory.Inventory) error {
	if c.FormatVersion != FormatVersion {
		return fmt.Errorf("type specimen format version is %d, want %d", c.FormatVersion, FormatVersion)
	}
	if c.Source != inventory.Source {
		return fmt.Errorf("type specimen source does not match the API inventory")
	}
	digest, err := InventoryDigest(inventory)
	if err != nil {
		return err
	}
	if c.InventorySHA256 != digest {
		return fmt.Errorf("type specimen inventory digest is stale")
	}
	expected := make(map[string]struct{}, len(inventory.DataTypeFamilies.Canonical))
	for _, family := range inventory.DataTypeFamilies.Canonical {
		expected[family.Name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(c.Entries))
	previous := ""
	for _, entry := range c.Entries {
		if _, found := expected[entry.Family]; !found {
			return fmt.Errorf("type specimen has stale family %q", entry.Family)
		}
		if _, found := seen[entry.Family]; found {
			return fmt.Errorf("type specimen repeats family %q", entry.Family)
		}
		seen[entry.Family] = struct{}{}
		if previous != "" && entry.Family < previous {
			return fmt.Errorf("type specimens are not sorted at %q", entry.Family)
		}
		previous = entry.Family
		if entry.Column != ColumnName(entry.Family) || entry.DeclaredType != CandidateType(entry.Family) {
			return fmt.Errorf("type specimen %q has stale column or declaration", entry.Family)
		}
		switch entry.Status {
		case Accepted:
			if entry.ObservedType == "" || entry.SeedWitness == "" || entry.AnalysisWitness == "" || entry.ExecutionWitness == "" {
				return fmt.Errorf("accepted type specimen %q has an incomplete witness", entry.Family)
			}
			if entry.RefusalStage != "" || entry.RefusalCode != 0 || entry.RefusalClass != "" {
				return fmt.Errorf("accepted type specimen %q also has refusal evidence", entry.Family)
			}
		case ExplicitlyRefused:
			if entry.RefusalStage == "" || entry.RefusalCode == 0 || entry.RefusalClass != clickHouseRefusalClass {
				return fmt.Errorf("refused type specimen %q has incomplete evidence", entry.Family)
			}
		case NotApplicable:
			if entry.RefusalStage != "" || entry.RefusalCode != 0 || entry.RefusalClass != notApplicableClass {
				return fmt.Errorf("not-applicable type specimen %q has incomplete evidence", entry.Family)
			}
		default:
			return fmt.Errorf("type specimen %q has unknown status %q", entry.Family, entry.Status)
		}
	}
	for family := range expected {
		if _, found := seen[family]; !found {
			return fmt.Errorf("type specimen has no verdict for family %q", family)
		}
	}
	return nil
}

// AcceptedEntries returns accepted specimens in stable family order.
func (c Catalog) AcceptedEntries() []Entry {
	var result []Entry
	for _, entry := range c.Entries {
		if entry.Status == Accepted {
			result = append(result, entry)
		}
	}
	sort.Slice(result, func(a, b int) bool { return result[a].Family < result[b].Family })
	return result
}

// Marshal returns the stable catalog representation.
func Marshal(catalog Catalog) ([]byte, error) {
	sort.Slice(catalog.Entries, func(a, b int) bool { return catalog.Entries[a].Family < catalog.Entries[b].Family })
	data, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
