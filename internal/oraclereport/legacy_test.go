package oraclereport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func legacyArtifact(t *testing.T, mutate func(map[string]any)) ([]byte, LegacyConversionSpec) {
	t.Helper()
	doc := map[string]any{
		"version":               1,
		"population":            map[string]any{"version": 1, "id": "grammar-v3"},
		"clickhouse_version":    "25.8.29.51",
		"seed":                  7,
		"generated_expressions": 2000,
		"server_run":            1787198660,
		"fixture_hash":          "legacy-fixture-hash",
		"fixture_signature":     "rows=1 columns=i8:Int8",
		"mismatch_signatures":   map[string]any{"v3-fn-equals: chgen=UInt8 ch=Bool": 1},
		"findings":              []any{},
	}
	if mutate != nil {
		mutate(doc)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return data, LegacyConversionSpec{
		ArtifactSHA256: hex.EncodeToString(sum[:]),
		PopulationID:   "grammar-v3",
		FixtureHash:    "legacy-fixture-hash",
		FixtureShape:   "rows=1 columns=i8:Int8",
		PrefixMappings: []PrefixMapping{{From: "v3-", To: "current-"}},
	}
}

func TestConvertLegacyArtifactNeedsEveryAllowlistedIdentity(t *testing.T) {
	data, spec := legacyArtifact(t, nil)
	archive, err := ConvertLegacyArtifact(data, spec)
	if err != nil {
		t.Fatalf("convert exact artifact: %v", err)
	}
	if len(archive.Mismatch) != 1 || archive.Mismatch[0] != "current-fn-equals: chgen=UInt8 ch=Bool" {
		t.Fatalf("unexpected mapped signatures %v", archive.Mismatch)
	}

	for name, mutate := range map[string]func(*LegacyConversionSpec){
		"artifact SHA": func(s *LegacyConversionSpec) { s.ArtifactSHA256 = strings.Repeat("b", 64) },
		"population":   func(s *LegacyConversionSpec) { s.PopulationID = "grammar-v2" },
		"fixture hash": func(s *LegacyConversionSpec) { s.FixtureHash = "other" },
		"fixture shape": func(s *LegacyConversionSpec) {
			s.FixtureShape = "rows=1 columns=s:String"
		},
		"kind prefix": func(s *LegacyConversionSpec) {
			s.PrefixMappings = []PrefixMapping{{From: "v2-", To: "current-"}}
		},
	} {
		changed := spec
		mutate(&changed)
		if _, err := ConvertLegacyArtifact(data, changed); err == nil {
			t.Errorf("%s mutation must refuse", name)
		}
	}
}

func TestConvertLegacyArtifactRefusesIncompleteIdentity(t *testing.T) {
	data, spec := legacyArtifact(t, func(doc map[string]any) { doc["server_run"] = 0 })
	if _, err := ConvertLegacyArtifact(data, spec); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete legacy identity must refuse: %v", err)
	}
}

func TestPrefixConversionRefusesCollisionsAndPreservesRetirementAudit(t *testing.T) {
	mappings := []PrefixMapping{{From: "v2-", To: "current-"}, {From: "v3-", To: "current-"}}
	retired := map[string]*RetirementRecord{
		"v2-fn-equals": {CHVersion: "25.8.29.51", Reason: "old-v2"},
		"v3-fn-equals": {CHVersion: "25.8.29.51", Reason: "old-v3"},
	}
	if _, err := MapRetirementAudit(retired, mappings); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("prefix collision must refuse: %v", err)
	}

	mapped, err := MapRetirementAudit(map[string]*RetirementRecord{
		"v3-fn-equals": {CHVersion: "25.8.29.51", Reason: "array-element-comparability"},
	}, mappings)
	if err != nil {
		t.Fatal(err)
	}
	if got := mapped["current-fn-equals"]; got == nil || got.Reason != "array-element-comparability" || got.CHVersion != "25.8.29.51" {
		t.Fatalf("retirement audit changed: %+v", got)
	}
}
