package apiinventory_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
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
	difference, err := apiinventory.Compare(reference, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !difference.Identical() {
		t.Fatalf("the ClickHouse API differs from the pinned inventory:\n%s", difference.String())
	}
}
