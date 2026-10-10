package apiinventory_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/IlyaGulya/chgen"
	"github.com/IlyaGulya/chgen/internal/apiinventory"
)

var pinnedAPIInventoryPath = func() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("find the API inventory: caller information is not available")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "clickhouse-api-inventory.json")
}()

func TestPortableAPIComparisonRetainsSemanticDifferences(t *testing.T) {
	reference, err := apiinventory.Load(pinnedAPIInventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	candidate := reference
	candidate.Source.BuildID = "different-architecture"
	candidate.Settings = slices.Clone(reference.Settings)
	for i := range candidate.Settings {
		switch candidate.Settings[i].Name {
		case "max_threads", "max_alter_threads", "max_final_threads", "max_parsing_threads":
			candidate.Settings[i].Default = "'auto(128)'"
		}
	}
	difference, err := apiinventory.CompareAPI(reference, candidate)
	if err != nil || !difference.Identical() {
		t.Fatalf("same API on different hardware was refused: %v\n%s", err, difference.String())
	}
	strict, err := apiinventory.Compare(reference, candidate)
	if err != nil || strict.Identical() {
		t.Fatalf("strict provenance comparison lost differences: %v", err)
	}
	for _, field := range []string{"version", "revision", "type", "semantic-default", "explicit-thread-default", "alias"} {
		t.Run(field, func(t *testing.T) {
			changed := candidate
			changed.Settings = slices.Clone(candidate.Settings)
			switch field {
			case "version":
				changed.Source.Version = "25.8.29.52"
			case "revision":
				changed.Source.Revision++
			case "type":
				changed.Settings[0].Type = "different-type"
			case "semantic-default":
				for i := range changed.Settings {
					if changed.Settings[i].Name == "join_use_nulls" {
						changed.Settings[i].Default = "1"
					}
				}
			case "explicit-thread-default":
				for i := range changed.Settings {
					if changed.Settings[i].Name == "max_threads" {
						changed.Settings[i].Default = "1"
					}
				}
			case "alias":
				changed.Settings[0].AliasFor = "different_target"
			}
			difference, err := apiinventory.CompareAPI(reference, changed)
			if err == nil && difference.Identical() {
				t.Fatal("semantic difference was hidden")
			}
		})
	}
}

func TestPinnedClickHouseAPIInventory(t *testing.T) {
	inventory, err := apiinventory.Load(pinnedAPIInventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Source.Version != chgen.MeasuredCHVersion {
		t.Fatalf("inventory server version is %s, want %s", inventory.Source.Version, chgen.MeasuredCHVersion)
	}
	canonical, err := apiinventory.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(pinnedAPIInventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(file, canonical) {
		t.Fatal("the pinned API inventory is not in its canonical form")
	}
}

func TestPinnedClickHouseAPIInventoryRejectsMissingClasses(t *testing.T) {
	reference, err := apiinventory.Load(pinnedAPIInventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*apiinventory.Inventory){
		"built-in canonical functions":   func(i *apiinventory.Inventory) { i.Functions.BuiltIn.Canonical = nil },
		"built-in function aliases":      func(i *apiinventory.Inventory) { i.Functions.BuiltIn.Aliases = nil },
		"canonical data types":           func(i *apiinventory.Inventory) { i.DataTypeFamilies.Canonical = nil },
		"data type aliases":              func(i *apiinventory.Inventory) { i.DataTypeFamilies.Aliases = nil },
		"aggregate function combinators": func(i *apiinventory.Inventory) { i.AggregateFunctionCombinators = nil },
		"table functions":                func(i *apiinventory.Inventory) { i.TableFunctions = nil },
		"settings":                       func(i *apiinventory.Inventory) { i.Settings = nil },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := reference
			mutate(&candidate)
			if _, err := apiinventory.Compare(reference, candidate); err == nil {
				t.Fatal("the inventory gate accepted a missing API class")
			}
		})
	}
}

func TestClickHouseAPIInventoryMatchesServer(t *testing.T) {
	serverURL := os.Getenv("CHGEN_API_INVENTORY_URL")
	if serverURL == "" {
		t.Skip("CHGEN_API_INVENTORY_URL is not set")
	}
	reference, err := apiinventory.Load(pinnedAPIInventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := apiinventory.NewCollector(serverURL).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	difference, err := apiinventory.CompareAPI(reference, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !difference.Identical() {
		t.Fatalf("the ClickHouse API differs from the pinned inventory:\n%s", difference.String())
	}
}
