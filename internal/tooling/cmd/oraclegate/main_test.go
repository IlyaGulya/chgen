package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/oraclereport"
)

const cliTestProfileHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func cliReport(seed int64) *oraclereport.Report {
	return &oraclereport.Report{
		Version:          oraclereport.ReportVersion,
		Population:       oraclereport.CurrentPopulation(cliTestProfileHash),
		SamplingPlanID:   oraclereport.CurrentProfileID,
		SamplingPlanHash: cliTestProfileHash,
		LaneAttempts:     []byte(`{"test-lane":1}`),
		LaneAccepted:     []byte(`{"test-lane":1}`),
		CHVersion:        "25.8.29.51",
		Seed:             seed,
		Generated:        2000,
		ServerRun:        1000,
		FixtureHash:      "fixture-hash",
		FixtureSignature: "fixture-shape",
		Counts:           map[string]int{"OK": 1},
	}
}

func TestReportFlagAcceptsNoExternalProfileLabel(t *testing.T) {
	var reports paths
	if err := reports.Set("report.json"); err != nil {
		t.Fatalf("bare report path: %v", err)
	}
	if err := reports.Set("current-combined-v1=report.json"); err == nil || !strings.Contains(err.Error(), "no profile label") {
		t.Fatalf("external profile label must refuse: %v", err)
	}
}

func TestBaselineUpdateRefusesSameProfileNameWithDifferentHashes(t *testing.T) {
	first, second := cliReport(1), cliReport(7)
	second.Population = oraclereport.CurrentPopulation(strings.Repeat("b", 64))
	second.SamplingPlanHash = second.Population.ProfileHash
	err := writeBaseline(
		filepath.Join(t.TempDir(), "baseline.json"),
		"25.8.29.51",
		"",
		[]string{"first.json", "second.json"},
		[]*oraclereport.Report{first, second},
	)
	if err == nil || !strings.Contains(err.Error(), "sampling profile") {
		t.Fatalf("different profile hashes must refuse: %v", err)
	}
}

func TestUnionUpdateRepairsProfileCellsAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	oldReport := cliReport(1)
	if err := writeBaseline(path, oldReport.CHVersion, "", []string{"old.json"}, []*oraclereport.Report{oldReport}); err != nil {
		t.Fatal(err)
	}
	if err := writeUnionBaseline(path, oldReport.CHVersion, "", "", []string{"old.json"}, []*oraclereport.Report{oldReport}); err != nil {
		t.Fatal(err)
	}
	newReport := cliReport(1)
	newReport.Population = oraclereport.CurrentPopulation(strings.Repeat("b", 64))
	newReport.SamplingPlanHash = newReport.Population.ProfileHash
	if err := writeUnionBaseline(path, newReport.CHVersion, "", "", []string{"new.json"}, []*oraclereport.Report{newReport}); err != nil {
		t.Fatal(err)
	}
	baseline, err := oraclereport.LoadBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	cell := baseline.UnionCells[oraclereport.CurrentProfileID]
	if cell == nil || cell.Population != newReport.Population {
		t.Fatal("union update kept the old profile identity")
	}
	for _, seedCell := range baseline.Cells {
		if seedCell.Population != newReport.Population {
			t.Fatal("union update kept an old per-seed profile identity")
		}
	}
}

func TestUnionUpdatePreservesAcceptedSignaturesThatTheNewSampleMisses(t *testing.T) {
	oldReport := cliReport(1)
	oldMismatch := "old-mismatch: chgen=Int32 ch=Int64"
	oldBlind := "old-blind: chgen=UInt8"
	oldReport.MismatchSig = map[string]int{oldMismatch: 1}
	oldReport.Findings = []oraclereport.Finding{{
		Class: "CH_ERROR_43_CHGEN_TYPED", Kind: "old-blind", ChgenType: "UInt8",
	}}
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := writeBaseline(path, oldReport.CHVersion, "", []string{"old.json"}, []*oraclereport.Report{oldReport}); err != nil {
		t.Fatal(err)
	}
	if err := writeUnionBaseline(path, oldReport.CHVersion, "", "", []string{"old.json"}, []*oraclereport.Report{oldReport}); err != nil {
		t.Fatal(err)
	}

	newReport := cliReport(1)
	newReport.Population = oraclereport.CurrentPopulation(strings.Repeat("b", 64))
	newReport.SamplingPlanHash = newReport.Population.ProfileHash
	newMismatch := "new-mismatch: chgen=Date ch=DateTime"
	newBlind := "new-blind: chgen=String"
	newReport.MismatchSig = map[string]int{newMismatch: 1}
	newReport.Findings = []oraclereport.Finding{{
		Class: "CH_ERROR_43_CHGEN_TYPED", Kind: "new-blind", ChgenType: "String",
	}}
	if err := writeUnionBaseline(path, newReport.CHVersion, "", "", []string{"new.json"}, []*oraclereport.Report{newReport}); err != nil {
		t.Fatal(err)
	}

	baseline, err := oraclereport.LoadBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	cell := baseline.UnionCells[oraclereport.CurrentProfileID]
	for _, signature := range []string{oldMismatch, newMismatch} {
		if !contains(cell.Mismatch, signature) {
			t.Errorf("union update lost mismatch signature %q", signature)
		}
	}
	for _, signature := range []string{oldBlind, newBlind} {
		if !contains(cell.Blind43, signature) {
			t.Errorf("union update lost blindness signature %q", signature)
		}
	}
}

func TestUnionUpdateRefusesFixtureChangeBeforeItDropsAcceptedSignatures(t *testing.T) {
	report := cliReport(1)
	report.MismatchSig = map[string]int{"old-mismatch: chgen=Int32 ch=Int64": 1}
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := writeBaseline(path, report.CHVersion, "", []string{"old.json"}, []*oraclereport.Report{report}); err != nil {
		t.Fatal(err)
	}
	if err := writeUnionBaseline(path, report.CHVersion, "", "", []string{"old.json"}, []*oraclereport.Report{report}); err != nil {
		t.Fatal(err)
	}

	changed := cliReport(1)
	changed.FixtureHash = "another-fixture-hash"
	changed.FixtureSignature = "another-fixture-shape"
	err := writeUnionBaseline(path, changed.CHVersion, "", "", []string{"changed.json"}, []*oraclereport.Report{changed})
	if err == nil || !strings.Contains(err.Error(), "cannot preserve") {
		t.Fatalf("fixture change must refuse before it drops accepted signatures: %v", err)
	}
}

func TestRetirementFlagAcceptsOnlyTheCurrentProfile(t *testing.T) {
	var values retirements
	if err := values.Set(oraclereport.CurrentProfileID + ":v3-fn-equals: chgen=UInt8=array-element-comparability"); err != nil {
		t.Fatalf("current profile: %v", err)
	}
	if err := values.Set("v3:v3-fn-equals: chgen=UInt8=array-element-comparability"); err == nil {
		t.Fatal("a grammar label must not select a current retirement cell")
	}
}

func TestUnionUpdatePreservesRetirementAudit(t *testing.T) {
	report := cliReport(1)
	signature := "v3-fn-equals: chgen=UInt8"
	record := &oraclereport.RetirementRecord{CHVersion: report.CHVersion, Reason: "array-element-comparability"}
	path := writeCLIUnionBaseline(t, report, map[string]*oraclereport.RetirementRecord{signature: record})
	if err := writeUnionBaseline(path, report.CHVersion, "", "", []string{"report.json"}, []*oraclereport.Report{report}); err != nil {
		t.Fatalf("update union baseline: %v", err)
	}
	loaded, err := oraclereport.LoadBaseline(path)
	if err != nil {
		t.Fatalf("load updated baseline: %v", err)
	}
	got := loaded.UnionCells[oraclereport.CurrentProfileID].Retired[signature]
	if got == nil || *got != *record {
		t.Fatalf("union update lost the retirement audit: got %+v, want %+v", got, record)
	}
}

func TestUnionUpdateRefusesRetiredAcceptedCollision(t *testing.T) {
	report := cliReport(1)
	signature := "v3-fn-equals: chgen=UInt8 ch=Bool"
	report.MismatchSig = map[string]int{signature: 1}
	path := writeCLIUnionBaseline(t, report, map[string]*oraclereport.RetirementRecord{
		signature: {CHVersion: report.CHVersion, Reason: "array-element-comparability"},
	})
	err := writeUnionBaseline(path, report.CHVersion, "", "", []string{"report.json"}, []*oraclereport.Report{report})
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("retired and accepted signature collision must refuse: %v", err)
	}
}

func writeCLIUnionBaseline(t *testing.T, report *oraclereport.Report, retired map[string]*oraclereport.RetirementRecord) string {
	t.Helper()
	baseline := &oraclereport.Baseline{
		CHVersion: report.CHVersion,
		Cells: map[string]*oraclereport.BaselineCell{
			oraclereport.CellName(report.Population.ProfileID, report.Seed, report.Generated): oraclereport.CellFromReport(report),
		},
		UnionCells: map[string]*oraclereport.UnionBaselineCell{
			report.Population.ProfileID: {
				Population:       report.Population,
				FixtureHash:      report.FixtureHash,
				FixtureSignature: report.FixtureSignature,
				Seeds:            []int64{report.Seed},
				ServerRun:        report.ServerRun,
				Mismatch:         []string{},
				Blind43:          []string{},
				Retired:          retired,
			},
		},
	}
	data, err := baseline.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
