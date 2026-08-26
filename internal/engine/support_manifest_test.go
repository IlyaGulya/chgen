package engine

import (
	"bytes"
	"os"
	"testing"

	"github.com/IlyaGulya/chgen/internal/apiinventory"
	"github.com/IlyaGulya/chgen/internal/supportmanifest"
)

var (
	supportInventoryPath = moduleRootPath("testdata", "clickhouse-api-inventory.json")
	supportConfigPath    = moduleRootPath("testdata", "clickhouse-support-overrides.json")
	supportManifestPath  = moduleRootPath("testdata", "clickhouse-support-manifest.json")
	supportEvidencePath  = moduleRootPath("testdata", "clickhouse-function-probe-evidence.json")
)

func TestPinnedSupportManifestCoversInventory(t *testing.T) {
	inventory, err := apiinventory.Load(supportInventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := supportmanifest.Load(supportManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	config, err := supportmanifest.LoadConfig(supportConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	roster, err := supportmanifest.LoadFunctionEvidence(functionProbeCatalogPath, supportEvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := supportmanifest.ValidateCurrent(inventory, roster, config, manifest); err != nil {
		t.Fatal(err)
	}
	if err := supportmanifest.ValidateWitnesses(moduleRootPath(), manifest); err != nil {
		t.Fatal(err)
	}
}

func TestPinnedSupportManifestIsCurrent(t *testing.T) {
	inventory, err := apiinventory.Load(supportInventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	config, err := supportmanifest.LoadConfig(supportConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	roster, err := supportmanifest.LoadFunctionEvidence(functionProbeCatalogPath, supportEvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := supportmanifest.Build(inventory, roster, config)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := supportmanifest.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(supportManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, generated) {
		t.Fatal("the support manifest is stale; run go run ./internal/tooling/cmd/supportmanifest")
	}
}

func TestPinnedSupportManifestIncludesSizedFunctionFamilies(t *testing.T) {
	manifest, err := supportmanifest.Load(supportManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"toDecimal64OrNull":  false,
		"toDateTime64OrZero": false,
		"toUInt256":          false,
	}
	for _, entry := range manifest.Entries {
		if _, selected := want[entry.Name]; !selected {
			continue
		}
		if entry.Status != supportmanifest.SupportedMeasured || entry.SemanticFamily != "conversion-scalar" {
			t.Errorf("sized function %s has status %s and family %s", entry.Name, entry.Status, entry.SemanticFamily)
		}
		want[entry.Name] = true
	}
	for name, found := range want {
		if !found {
			t.Errorf("support manifest has no measured sized function %s", name)
		}
	}
}

func TestSupportManifestRejectsLossOfSizedFunctionFamily(t *testing.T) {
	inventory, err := apiinventory.Load(supportInventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	config, err := supportmanifest.LoadConfig(supportConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	roster, err := supportmanifest.LoadFunctionEvidence(functionProbeCatalogPath, supportEvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	delete(roster, "todecimal64ornull")
	manifest, err := supportmanifest.Load(supportManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := supportmanifest.ValidateCurrent(inventory, roster, config, manifest); err == nil {
		t.Fatal("support manifest accepted the loss of a measured sized function family")
	}
}

func TestDistinctCombinatorKeepsPartialProductMatrix(t *testing.T) {
	inventory, err := apiinventory.Load(supportInventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	config, err := supportmanifest.LoadConfig(supportConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	roster, err := supportmanifest.LoadFunctionEvidence(functionProbeCatalogPath, supportEvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := supportmanifest.Load(supportManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for index := range manifest.Entries {
		entry := &manifest.Entries[index]
		if entry.Kind != supportmanifest.KindAggregateFunctionCombinator || entry.Name != "Distinct" {
			continue
		}
		if entry.Status != supportmanifest.PartiallyMeasured ||
			len(entry.AcceptedProducts) != 1 || entry.AcceptedProducts[0] != "countDistinct(i32)" ||
			len(entry.RefusedProducts) != 1 || entry.RefusedProducts[0] != "sumDistinct(i32)" {
			t.Fatalf("Distinct partial products = status %s, accepted %v, refused %v", entry.Status, entry.AcceptedProducts, entry.RefusedProducts)
		}
		entry.Status = supportmanifest.ExplicitlyRefused
		entry.AcceptedProducts = nil
		entry.RefusedProducts = nil
		if err := supportmanifest.ValidateCurrent(inventory, roster, config, manifest); err == nil {
			t.Fatal("support manifest accepted the loss of partial Distinct support")
		}
		return
	}
	t.Fatal("support manifest has no Distinct combinator")
}
