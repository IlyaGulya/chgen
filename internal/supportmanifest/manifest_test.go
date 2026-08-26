package supportmanifest

import (
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/apiinventory"
)

func TestBuildAssignsOneStatusToEveryInventoryItem(t *testing.T) {
	inventory := testInventory()
	manifest, err := Build(inventory, testMeasurements("dedicated"), testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(manifest.Entries), 8; got != want {
		t.Fatalf("manifest has %d entries, want %d", got, want)
	}
	if err := manifest.ValidateAgainst(inventory); err != nil {
		t.Fatal(err)
	}
	report := Coverage(manifest)
	if report.StatusCounts[SupportedMeasured] != 1 {
		t.Fatalf("supported and measured count is %d, want 1", report.StatusCounts[SupportedMeasured])
	}
	if report.StatusCounts[ExplicitlyRefused] != 1 {
		t.Fatalf("explicit refusal count is %d, want 1", report.StatusCounts[ExplicitlyRefused])
	}
	if report.StatusCounts[NotApplicable] != 1 {
		t.Fatalf("not applicable count is %d, want 1", report.StatusCounts[NotApplicable])
	}
	if report.StatusCounts[NotMeasured] != 5 {
		t.Fatalf("not measured count is %d, want 5", report.StatusCounts[NotMeasured])
	}
	if report.Measured != 2 || report.SupportedMeasured != 1 {
		t.Fatalf("measured counts are measured=%d supported=%d, want 2 and 1", report.Measured, report.SupportedMeasured)
	}
}

func TestBuildRejectsStaleOverride(t *testing.T) {
	config := testConfig()
	config.Overrides[0].Name = "RemovedType"
	if _, err := Build(testInventory(), testMeasurements("dedicated"), config); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("Build error = %v, want a stale override refusal", err)
	}
}

func TestBuildRejectsUnknownFunctionFamily(t *testing.T) {
	if _, err := Build(testInventory(), testMeasurements("unknown"), testConfig()); err == nil {
		t.Fatal("support manifest accepted an unknown function semantic family")
	}
}

func TestBuildDoesNotExcludeSettingsWithoutAnOverride(t *testing.T) {
	config := testConfig()
	config.Overrides = config.Overrides[:1]
	manifest, err := Build(testInventory(), testMeasurements("dedicated"), config)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest.Entries {
		if entry.Kind == KindSetting && entry.Status != NotMeasured {
			t.Fatalf("setting %s has status %s, want %s", entry.Name, entry.Status, NotMeasured)
		}
	}
}

func TestValidateCurrentRejectsWholeClassExclusion(t *testing.T) {
	inventory := testInventory()
	config := testConfig()
	config.Overrides = config.Overrides[:1]
	manifest, err := Build(inventory, testMeasurements("dedicated"), config)
	if err != nil {
		t.Fatal(err)
	}
	for index := range manifest.Entries {
		if manifest.Entries[index].Kind != KindSetting {
			continue
		}
		manifest.Entries[index].Status = NotApplicable
		manifest.Entries[index].SemanticFamily = "server-configuration"
		manifest.Entries[index].Evidence = "unapproved-class-exclusion"
	}
	if err := ValidateCurrent(inventory, testMeasurements("dedicated"), config, manifest); err == nil {
		t.Fatal("ValidateCurrent accepted a whole-class exclusion without an override")
	}
}

func TestValidateCurrentRejectsUnsupportedPassThrough(t *testing.T) {
	inventory := testInventory()
	config := testConfig()
	manifest, err := Build(inventory, testMeasurements("dedicated"), config)
	if err != nil {
		t.Fatal(err)
	}
	mutated := false
	for index := range manifest.Entries {
		entry := &manifest.Entries[index]
		if entry.Status != NotMeasured {
			continue
		}
		entry.Status = SupportedMeasured
		entry.SemanticFamily = "mutated-pass-through"
		entry.Evidence = "mutated-without-measurement"
		mutated = true
		break
	}
	if !mutated {
		t.Fatal("manifest has no unsupported item to mutate")
	}
	if err := ValidateCurrent(inventory, testMeasurements("dedicated"), config, manifest); err == nil {
		t.Fatal("ValidateCurrent accepted unsupported API as measured support")
	}
}

func TestValidateCurrentRejectsMissingCombinator(t *testing.T) {
	inventory := testInventory()
	manifest, err := Build(inventory, testMeasurements("dedicated"), testConfig())
	if err != nil {
		t.Fatal(err)
	}
	for index, entry := range manifest.Entries {
		if entry.Kind != KindAggregateFunctionCombinator {
			continue
		}
		manifest.Entries = append(manifest.Entries[:index], manifest.Entries[index+1:]...)
		if err := manifest.ValidateAgainst(inventory); err == nil {
			t.Fatal("ValidateAgainst accepted a missing aggregate function combinator")
		}
		return
	}
	t.Fatal("manifest has no aggregate function combinator")
}

func TestPartialStatusRejectsOneProductInBothVerdicts(t *testing.T) {
	inventory := testInventory()
	config := testConfig()
	config.Overrides = append(config.Overrides, Override{
		Kind:             KindAggregateFunctionCombinator,
		Name:             "If",
		Status:           PartiallyMeasured,
		SemanticFamily:   "partial-test",
		Evidence:         "measured-products",
		TestWitness:      "partial_test.go:TestPartial",
		AcceptedProducts: []string{"sumIf(i32, b)"},
		RefusedProducts:  []string{"sumIf(i32, b)"},
	})
	if _, err := Build(inventory, testMeasurements("dedicated"), config); err == nil {
		t.Fatal("Build accepted one product in both partial verdict lists")
	}
}

func TestPartialStatusNeedsAcceptedAndRefusedProducts(t *testing.T) {
	inventory := testInventory()
	config := testConfig()
	config.Overrides = append(config.Overrides, Override{
		Kind:             KindAggregateFunctionCombinator,
		Name:             "If",
		Status:           PartiallyMeasured,
		SemanticFamily:   "partial-test",
		Evidence:         "measured-products",
		TestWitness:      "partial_test.go:TestPartial",
		AcceptedProducts: []string{"countIf(u8)"},
		RefusedProducts:  []string{"sumIf(s)"},
	})
	manifest, err := Build(inventory, testMeasurements("dedicated"), config)
	if err != nil {
		t.Fatal(err)
	}
	report := Coverage(manifest)
	if report.PartiallyMeasured != 1 || report.Measured != 3 {
		t.Fatalf("partial coverage counts = partial %d, measured %d, want 1 and 3", report.PartiallyMeasured, report.Measured)
	}
	for index := range manifest.Entries {
		entry := &manifest.Entries[index]
		if entry.Kind != KindAggregateFunctionCombinator || entry.Name != "If" {
			continue
		}
		entry.AcceptedProducts = nil
		if err := manifest.ValidateAgainst(inventory); err == nil {
			t.Fatal("ValidateAgainst accepted partial support without an accepted product")
		}
		return
	}
	t.Fatal("manifest has no aggregate function combinator")
}

func TestValidateRejectsEveryInvalidState(t *testing.T) {
	inventory := testInventory()
	valid, err := Build(inventory, testMeasurements("dedicated"), testConfig())
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Manifest){
		"missing item": func(m *Manifest) { m.Entries = m.Entries[1:] },
		"unknown status": func(m *Manifest) {
			m.Entries[0].Status = "unknown"
		},
		"empty evidence": func(m *Manifest) { m.Entries[0].Evidence = "" },
		"empty family":   func(m *Manifest) { m.Entries[0].SemanticFamily = "" },
		"empty witness":  func(m *Manifest) { m.Entries[0].TestWitness = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Entries = append([]Entry(nil), valid.Entries...)
			mutate(&candidate)
			if err := candidate.ValidateAgainst(inventory); err == nil {
				t.Fatal("ValidateAgainst accepted an invalid manifest")
			}
		})
	}
}

func testConfig() Config {
	return Config{FormatVersion: FormatVersion, Overrides: []Override{
		{
			Kind:           KindDataType,
			Name:           "AggregateFunction",
			Status:         ExplicitlyRefused,
			SemanticFamily: "driver-refusal",
			Evidence:       "measured-refusal",
			TestWitness:    "refusal_test.go:TestRefusal",
		},
		{
			Kind:           KindSetting,
			Name:           "max_threads",
			Status:         NotApplicable,
			SemanticFamily: "explicit-scope-exclusion",
			Evidence:       "measured-not-applicable",
			TestWitness:    "scope_test.go:TestScope",
		},
	}}
}

func testMeasurements(family string) map[string]FunctionMeasurement {
	return map[string]FunctionMeasurement{
		"abs": {Family: family, Evidence: "live-test-evidence", TestWitness: "internal/engine/function_probe_live_test.go:TestFunctionProbeCatalogAgainstClickHouse"},
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
			Canonical: []apiinventory.DataTypeFamily{{Name: "AggregateFunction"}, {Name: "Int32"}},
			Aliases:   []apiinventory.DataTypeFamily{{Name: "INT", AliasTo: "Int32"}},
		},
		AggregateFunctionCombinators: []apiinventory.AggregateFunctionCombinator{{Name: "If"}},
		TableFunctions:               []apiinventory.TableFunction{{Name: "numbers"}},
		Settings:                     []apiinventory.Setting{{Name: "max_threads", Type: "MaxThreads", Tier: "Production"}},
	}
}
