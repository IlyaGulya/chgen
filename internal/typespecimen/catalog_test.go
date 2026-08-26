package typespecimen

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/apiinventory"
)

func TestValidateRejectsMissingFamily(t *testing.T) {
	inventory := testInventory()
	catalog := testCatalog(t, inventory)
	catalog.Entries = catalog.Entries[1:]
	if err := catalog.ValidateAgainst(inventory); err == nil || !strings.Contains(err.Error(), "no verdict") {
		t.Fatalf("ValidateAgainst error = %v, want a missing-family refusal", err)
	}
}

func TestLoadRejectsLegacyRefusalEvidence(t *testing.T) {
	inventory := testInventory()
	data, err := Marshal(testCatalog(t, inventory))
	if err != nil {
		t.Fatal(err)
	}
	mutated := bytes.Replace(data, []byte(`"refusal_class": "clickhouse_refused"`),
		[]byte(`"refusal_evidence": "Code: 370. DB::Exception: copied prose"`), 1)
	if bytes.Equal(mutated, data) {
		t.Fatal("catalog has no refusal class to mutate")
	}
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, mutated, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("legacy refusal evidence was not refused: %v", err)
	}
}

func TestSetRefusalKeepsOnlyCodeStageAndClass(t *testing.T) {
	entry := Entry{Family: "Nothing"}
	if err := setRefusal(&entry, "create", errors.New("Code: 370. DB::Exception: copied prose")); err != nil {
		t.Fatal(err)
	}
	if entry.Status != ExplicitlyRefused || entry.RefusalStage != "create" || entry.RefusalCode != 370 || entry.RefusalClass != clickHouseRefusalClass {
		t.Fatalf("refusal facts = %+v", entry)
	}
	if err := setRefusal(&entry, "create", errors.New("transport failure")); err == nil {
		t.Fatal("setRefusal accepted a refusal without a ClickHouse code")
	}
}

func TestMarshalBindsRefusalCodeAndClass(t *testing.T) {
	inventory := testInventory()
	original := testCatalog(t, inventory)
	originalData, err := Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*Entry){
		"code":  func(entry *Entry) { entry.RefusalCode++ },
		"class": func(entry *Entry) { entry.RefusalClass = notApplicableClass },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := original
			candidate.Entries = append([]Entry(nil), original.Entries...)
			mutate(&candidate.Entries[1])
			candidateData, err := Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(candidateData, originalData) {
				t.Fatalf("catalog bytes did not bind the refusal %s", name)
			}
		})
	}
}

func TestValidateRejectsIncompleteWitnesses(t *testing.T) {
	inventory := testInventory()
	valid := testCatalog(t, inventory)
	tests := map[string]func(*Catalog){
		"accepted analysis": func(c *Catalog) { c.Entries[0].AnalysisWitness = "" },
		"refusal code":      func(c *Catalog) { c.Entries[1].RefusalCode = 0 },
		"refusal class":     func(c *Catalog) { c.Entries[1].RefusalClass = "copied server prose" },
		"unknown status":    func(c *Catalog) { c.Entries[0].Status = "unknown" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Entries = append([]Entry(nil), valid.Entries...)
			mutate(&candidate)
			if err := candidate.ValidateAgainst(inventory); err == nil {
				t.Fatal("ValidateAgainst accepted an incomplete catalog")
			}
		})
	}
}

func testInventory() apiinventory.Inventory {
	return apiinventory.Inventory{
		FormatVersion: apiinventory.FormatVersion,
		Source:        apiinventory.Source{Version: "25.8.29.51", Revision: 1, BuildID: "build"},
		Functions: apiinventory.FunctionInventory{BuiltIn: apiinventory.FunctionSet{
			Canonical: []apiinventory.Function{{Name: "abs", Origin: "System"}},
			Aliases:   []apiinventory.Function{{Name: "absolute", AliasTo: "abs", Origin: "System"}},
		}},
		DataTypeFamilies: apiinventory.DataTypeInventory{
			Canonical: []apiinventory.DataTypeFamily{{Name: "Int32"}, {Name: "Nothing"}},
			Aliases:   []apiinventory.DataTypeFamily{{Name: "INT", AliasTo: "Int32"}},
		},
		AggregateFunctionCombinators: []apiinventory.AggregateFunctionCombinator{{Name: "If"}},
		TableFunctions:               []apiinventory.TableFunction{{Name: "numbers"}},
		Settings:                     []apiinventory.Setting{{Name: "max_threads", Type: "MaxThreads", Tier: "Production"}},
	}
}

func testCatalog(t *testing.T, inventory apiinventory.Inventory) Catalog {
	t.Helper()
	digest, err := InventoryDigest(inventory)
	if err != nil {
		t.Fatal(err)
	}
	return Catalog{
		FormatVersion:   FormatVersion,
		Source:          inventory.Source,
		InventorySHA256: digest,
		Entries: []Entry{
			{
				Family: "Int32", Column: ColumnName("Int32"), DeclaredType: CandidateType("Int32"),
				Status: Accepted, ObservedType: "Int32", SeedWitness: "INSERT", AnalysisWitness: "ANALYZE", ExecutionWitness: "EXECUTE",
			},
			{
				Family: "Nothing", Column: ColumnName("Nothing"), DeclaredType: CandidateType("Nothing"),
				Status: ExplicitlyRefused, RefusalStage: "create", RefusalCode: 370, RefusalClass: clickHouseRefusalClass,
			},
		},
	}
}
