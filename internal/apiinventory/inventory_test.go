package apiinventory

import (
	"strings"
	"testing"
)

func TestMarshalIsDeterministic(t *testing.T) {
	inventory := testInventory()
	inventory.Functions.BuiltIn.Canonical[0], inventory.Functions.BuiltIn.Canonical[1] =
		inventory.Functions.BuiltIn.Canonical[1], inventory.Functions.BuiltIn.Canonical[0]

	first, err := Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("the same inventory produced different bytes")
	}
	if strings.Index(string(first), `"name": "abs"`) > strings.Index(string(first), `"name": "plus"`) {
		t.Fatal("the inventory did not sort function names")
	}
}

func TestValidateRejectsEachRequiredClassWhenItIsEmpty(t *testing.T) {
	tests := map[string]func(*Inventory){
		"built-in canonical functions":   func(i *Inventory) { i.Functions.BuiltIn.Canonical = nil },
		"built-in function aliases":      func(i *Inventory) { i.Functions.BuiltIn.Aliases = nil },
		"canonical data types":           func(i *Inventory) { i.DataTypeFamilies.Canonical = nil },
		"data type aliases":              func(i *Inventory) { i.DataTypeFamilies.Aliases = nil },
		"aggregate function combinators": func(i *Inventory) { i.AggregateFunctionCombinators = nil },
		"table functions":                func(i *Inventory) { i.TableFunctions = nil },
		"settings":                       func(i *Inventory) { i.Settings = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			inventory := testInventory()
			mutate(&inventory)
			if err := inventory.Validate(); err == nil {
				t.Fatal("Validate accepted an inventory with a missing class")
			}
		})
	}
}

func TestValidateSeparatesUserDefinedFunctions(t *testing.T) {
	inventory := testInventory()
	inventory.Functions.UserDefined.Canonical = []Function{{Name: "local_function", Origin: "SQLUserDefined"}}
	if err := inventory.Validate(); err != nil {
		t.Fatalf("Validate refused a separated user-defined function: %v", err)
	}
	inventory.Functions.BuiltIn.Canonical = append(inventory.Functions.BuiltIn.Canonical,
		Function{Name: "wrong_class", Origin: "SQLUserDefined"})
	inventory.Normalize()
	if err := inventory.Validate(); err == nil {
		t.Fatal("Validate accepted a user-defined function in the built-in class")
	}
}

func TestCompareNamesAPIChanges(t *testing.T) {
	reference := testInventory()
	candidate := testInventory()
	candidate.Source.BuildID = "candidate"
	candidate.Functions.BuiltIn.Canonical[0].IsAggregate = true
	candidate.Functions.BuiltIn.Canonical = append(candidate.Functions.BuiltIn.Canonical,
		Function{Name: "new_function", Origin: "System"})
	candidate.TableFunctions = candidate.TableFunctions[1:]
	candidate.AggregateFunctionCombinators[0].IsInternal = true
	candidate.Normalize()

	difference, err := Compare(reference, candidate)
	if err != nil {
		t.Fatal(err)
	}
	report := difference.String()
	for _, want := range []string{
		"SOURCE CHANGED",
		"functions.built_in.canonical",
		"  + new_function",
		"  ~ abs",
		"table_functions",
		"  - file",
		"aggregate_function_combinators",
		"  ~ If",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("difference report does not contain %q:\n%s", want, report)
		}
	}
}

func testInventory() Inventory {
	return Inventory{
		FormatVersion: FormatVersion,
		Source:        Source{Version: "25.8.29.51", Revision: 1, BuildID: "build"},
		Functions: FunctionInventory{BuiltIn: FunctionSet{
			Canonical: []Function{{Name: "abs", Origin: "System"}, {Name: "plus", Origin: "System"}},
			Aliases:   []Function{{Name: "add", AliasTo: "plus", Origin: "System"}},
		}},
		DataTypeFamilies: DataTypeInventory{
			Canonical: []DataTypeFamily{{Name: "String"}, {Name: "UInt8"}},
			Aliases:   []DataTypeFamily{{Name: "BOOL", AliasTo: "Bool"}},
		},
		AggregateFunctionCombinators: []AggregateFunctionCombinator{{Name: "If"}},
		TableFunctions:               []TableFunction{{Name: "file"}, {Name: "numbers"}},
		Settings:                     []Setting{{Name: "max_threads", Type: "MaxThreads", Tier: "Production"}},
	}
}
