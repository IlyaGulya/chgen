package apiinventory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsRetiredFunctionProse(t *testing.T) {
	for _, field := range []string{"syntax", "arguments", "returned_value", "categories"} {
		t.Run(field, func(t *testing.T) {
			data, err := Marshal(testInventory())
			if err != nil {
				t.Fatal(err)
			}
			mutated := strings.Replace(string(data), `"origin": "System"`, `"origin": "System", "`+field+`": "copied prose"`, 1)
			path := filepath.Join(t.TempDir(), "inventory.json")
			if err := os.WriteFile(path, []byte(mutated), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("Load accepted retired field %q: %v", field, err)
			}
		})
	}
}

func TestFunctionFactsRemainInTheInventoryFormat(t *testing.T) {
	inventory := testInventory()
	inventory.Functions.BuiltIn.Canonical[0].IntroducedIn = "20.1"
	data, err := Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range []string{
		`"name": "abs"`,
		`"is_aggregate": false`,
		`"case_insensitive": false`,
		`"origin": "System"`,
		`"introduced_in": "20.1"`,
		`"alias_to": "plus"`,
	} {
		if !strings.Contains(string(data), fact) {
			t.Errorf("inventory has no retained fact %s", fact)
		}
	}
}

func TestFunctionsQueryDoesNotCollectDescriptions(t *testing.T) {
	for _, column := range []string{"syntax", "arguments", "returned_value", "categories"} {
		if strings.Contains(functionsQuery, column) {
			t.Errorf("functions query collects retired column %q", column)
		}
	}
	for _, column := range []string{"name", "is_aggregate", "case_insensitive", "alias_to", "origin", "introduced_in"} {
		if !strings.Contains(functionsQuery, column) {
			t.Errorf("functions query has no retained column %q", column)
		}
	}
}
