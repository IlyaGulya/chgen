package oraclereport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testProfileHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// reportForGate builds a report that the gate can read.
func reportForGate() *Report {
	return &Report{
		Version:          ReportVersion,
		Population:       CurrentPopulation(testProfileHash),
		SamplingPlanID:   CurrentProfileID,
		SamplingPlanHash: testProfileHash,
		LaneAttempts:     []byte(`{"test-lane":1}`),
		LaneAccepted:     []byte(`{"test-lane":1}`),
		CHVersion:        "25.8.29.51",
		Seed:             1,
		Generated:        2000,
		FixtureHash:      "fixture-hash-A",
		FixtureSignature: "fixture-A",
		ServerRun:        1000,
		Counts:           map[string]int{"OK": 1900, "MISMATCH": 1},
		MismatchSig:      map[string]int{"comparison: chgen=UInt8 ch=Bool": 4},
		Findings: []Finding{
			{Class: "CH_ERROR_43_CHGEN_TYPED", Expr: "f(x)", Kind: "call", ChgenType: "Int64"},
			{Class: "CH_ERROR", Expr: "g(x)", Kind: "call"},
		},
	}
}

func baselineForGate() *Baseline {
	return &Baseline{
		Version:   baselineVersion,
		CHVersion: "25.8.29.51",
		Cells: map[string]*BaselineCell{
			CellName(CurrentProfileID, 1, 2000): {
				Population:       CurrentPopulation(testProfileHash),
				Seed:             1,
				N:                2000,
				FixtureSignature: "fixture-A",
				FixtureHash:      "fixture-hash-A",
				ServerRun:        1000,
				Mismatch:         []string{"comparison: chgen=UInt8 ch=Bool"},
				Blind43:          []string{"call: chgen=Int64"},
			},
		},
	}
}

// The gate must pass a run whose signatures the baseline holds, even though
// the run and the baseline come from different server runs. That is the whole
// reason this gate exists beside Compare.
func TestGatePassesWhenTheSignaturesAreASubset(t *testing.T) {
	b := baselineForGate()
	r := reportForGate()
	r.ServerRun = 999999 // another instance entirely
	res, err := b.Gate(r)
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if !res.OK() {
		t.Fatalf("expected a pass, got:\n%s", res)
	}
	if res.RunMismatch != 1 || res.RunBlind43 != 1 {
		t.Fatalf("the gate read too little: %s", res)
	}
}

func TestGateRefusesAttemptsWithoutAcceptedCells(t *testing.T) {
	baseline := baselineForGate()
	for name, mutate := range map[string]func(*Report){
		"zero generated cells": func(report *Report) { report.Generated = 0 },
		"attempts without acceptance": func(report *Report) {
			report.LaneAttempts = []byte(`{"test-lane":5}`)
			report.LaneAccepted = []byte(`{"test-lane":0}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			report := reportForGate()
			mutate(report)
			if _, err := baseline.Gate(report); err == nil {
				t.Fatal("coverage gate accepted zero measured cells")
			}
		})
	}
}

// A count that grows inside a known class must NOT fail the gate. The counts
// move with the age of the server, and a gate on a total is invalid here.
func TestGateIgnoresACountChangeInsideAKnownClass(t *testing.T) {
	b := baselineForGate()
	r := reportForGate()
	r.MismatchSig["comparison: chgen=UInt8 ch=Bool"] = 400
	r.Findings = append(r.Findings,
		Finding{Class: "CH_ERROR_43_CHGEN_TYPED", Expr: "f(y)", Kind: "call", ChgenType: "Int64"})
	res, err := b.Gate(r)
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if !res.OK() {
		t.Fatalf("a count change must not fail the gate, got:\n%s", res)
	}
}

func TestGateFailsOnANewMismatchSignature(t *testing.T) {
	b := baselineForGate()
	r := reportForGate()
	r.MismatchSig["arithmetic: chgen=Int64 ch=Float64"] = 1
	res, err := b.Gate(r)
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if res.OK() {
		t.Fatal("expected a failure for a new MISMATCH signature")
	}
	if len(res.NewMismatch) != 1 || res.NewMismatch[0] != "arithmetic: chgen=Int64 ch=Float64" {
		t.Fatalf("the verdict must name the new signature, got %q", res.NewMismatch)
	}
	if !strings.Contains(res.String(), "arithmetic: chgen=Int64 ch=Float64") {
		t.Fatalf("the rendered verdict must show which signature appeared:\n%s", res)
	}
}

func TestGateFailsOnANewBlindnessSignature(t *testing.T) {
	b := baselineForGate()
	r := reportForGate()
	r.Findings = append(r.Findings,
		Finding{Class: "CH_ERROR_43_CHGEN_TYPED", Expr: "h(x)", Kind: "arithmetic", ChgenType: "Float64"})
	res, err := b.Gate(r)
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if res.OK() {
		t.Fatal("expected a failure for a new blindness signature")
	}
	if len(res.NewBlind43) != 1 || res.NewBlind43[0] != "arithmetic: chgen=Float64" {
		t.Fatalf("unexpected new set %q", res.NewBlind43)
	}
}

// A signature that the baseline holds and the run did not reach must NOT fail.
// One seed is one sample, and a class at zero on one run can be present on
// another.
func TestGateDoesNotFailOnAnAbsentSignature(t *testing.T) {
	b := baselineForGate()
	b.Cells[CellName(CurrentProfileID, 1, 2000)].Mismatch = append(
		b.Cells[CellName(CurrentProfileID, 1, 2000)].Mismatch, "logic: chgen=UInt8 ch=Nullable(UInt8)")
	r := reportForGate()
	res, err := b.Gate(r)
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if !res.OK() {
		t.Fatalf("an absent signature must not fail the gate:\n%s", res)
	}
	if len(res.GoneMismatch) != 1 {
		t.Fatalf("the absence must still be reported, got %q", res.GoneMismatch)
	}
}

func TestGateRefusesADifferentServerVersion(t *testing.T) {
	b := baselineForGate()
	r := reportForGate()
	r.CHVersion = "25.9.1.1"
	if _, err := b.Gate(r); err == nil {
		t.Fatal("expected a refusal for a different ClickHouse version")
	}
}

func TestGateRefusesADifferentFixture(t *testing.T) {
	b := baselineForGate()
	r := reportForGate()
	r.FixtureSignature = "fixture-B"
	if _, err := b.Gate(r); err == nil {
		t.Fatal("expected a refusal for a different fixture")
	}
}

func TestGateRefusesIdentityMutations(t *testing.T) {
	for name, mutate := range map[string]func(*Report){
		"missing population": func(r *Report) { r.Population = PopulationIdentity{} },
		"different profile hash": func(r *Report) {
			r.Population = CurrentPopulation("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
			r.SamplingPlanHash = r.Population.ProfileHash
		},
		"missing fixture hash": func(r *Report) { r.FixtureHash = "" },
		"different fixture hash": func(r *Report) {
			r.FixtureHash = "fixture-hash-B"
		},
		"missing server run": func(r *Report) { r.ServerRun = 0 },
	} {
		r := reportForGate()
		mutate(r)
		if _, err := baselineForGate().Gate(r); err == nil {
			t.Errorf("%s: Gate must refuse", name)
		}
	}
}

func TestGateRefusesAnUnknownCell(t *testing.T) {
	b := baselineForGate()
	r := reportForGate()
	r.Seed = 7
	if _, err := b.Gate(r); err == nil {
		t.Fatal("expected a refusal for a cell that the baseline does not hold")
	}
}

// A baseline whose cells are empty gates on nothing. Loading it must fail
// rather than answer "pass" for every run.
func TestLoadBaselineRefusesAnEmptyOrUnknownFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := LoadBaseline(write("empty.json", `{"version":1,"clickhouse_version":"x","cells":{}}`)); err == nil {
		t.Fatal("expected a refusal for a baseline with no cell")
	}
	if _, err := LoadBaseline(write("v9.json", `{"version":9,"clickhouse_version":"x","cells":{"a":{}}}`)); err == nil {
		t.Fatal("expected a refusal for an unknown baseline version")
	}
	if _, err := LoadBaseline(write("extra.json",
		`{"version":1,"clickhouse_version":"x","cells":{"a":{}},"surprise":1}`)); err == nil {
		t.Fatal("expected a refusal for an unknown key")
	}
}

func TestLoadBaselineRefusesRetirementAuditMutations(t *testing.T) {
	for name, mutate := range map[string]func(*UnionBaselineCell){
		"nil record": func(cell *UnionBaselineCell) {
			cell.Retired["old-signature"] = nil
		},
		"missing reason": func(cell *UnionBaselineCell) {
			cell.Retired["old-signature"] = &RetirementRecord{CHVersion: "25.8.29.51"}
		},
		"accepted collision": func(cell *UnionBaselineCell) {
			cell.Retired["comparison: chgen=UInt8 ch=Bool"] = &RetirementRecord{CHVersion: "25.8.29.51", Reason: "chgen-old"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			baseline := baselineForGate()
			cell := &UnionBaselineCell{
				Population:       CurrentPopulation(testProfileHash),
				FixtureHash:      "fixture-hash-A",
				FixtureSignature: "fixture-A",
				Seeds:            []int64{1},
				ServerRun:        1000,
				Mismatch:         []string{"comparison: chgen=UInt8 ch=Bool"},
				Blind43:          []string{},
				Retired:          map[string]*RetirementRecord{},
			}
			mutate(cell)
			baseline.UnionCells = map[string]*UnionBaselineCell{CurrentProfileID: cell}
			data, err := baseline.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "baseline.json")
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadBaseline(path); err == nil {
				t.Fatal("LoadBaseline accepted an invalid retirement audit")
			}
		})
	}
}

// The committed file must round-trip, and every set must be sorted so that a
// git diff shows one signature per line.
func TestMarshalSortsAndWritesEmptySetsAsLists(t *testing.T) {
	b := &Baseline{
		CHVersion: "25.8.29.51",
		Cells: map[string]*BaselineCell{
			CellName(CurrentProfileID, 1, 10): {
				Population: CurrentPopulation(testProfileHash), Seed: 1, N: 10,
				FixtureHash: "fixture-hash-A", FixtureSignature: "fixture-A", ServerRun: 1,
				Mismatch: []string{"b", "a"},
			},
		},
	}
	data, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "\"blind43_signatures\": []") {
		t.Fatalf("an empty set must write as [], not null:\n%s", got)
	}
	if strings.Index(got, `"a"`) > strings.Index(got, `"b"`) {
		t.Fatalf("the signatures must be sorted:\n%s", got)
	}
	p := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBaseline(p); err != nil {
		t.Fatalf("the written file must load back: %v", err)
	}
}

// The blindness signature must come from the uncapped findings list of that
// class and must never read a CH_ERROR entry, which is truncated.
func TestBlind43SignaturesReadOnlyTheBlindnessClass(t *testing.T) {
	r := &Report{Findings: []Finding{
		{Class: "CH_ERROR", Kind: "call", Expr: "a"},
		{Class: "MISMATCH", Kind: "call", ChgenType: "Int8"},
		{Class: "CH_ERROR_43_CHGEN_TYPED", Kind: "call", ChgenType: "Int64"},
		{Class: "CH_ERROR_43_CHGEN_TYPED", Kind: "call", ChgenType: "Int64"},
	}}
	got := Blind43Signatures(r)
	if len(got) != 1 || got[0] != "call: chgen=Int64" {
		t.Fatalf("unexpected signatures %q", got)
	}
}

// unionBaselineForRetire builds a baseline with one union cell that has two
// accepted Blind43 signatures, to exercise retiring exactly one of them.
func unionBaselineForRetire() *Baseline {
	return &Baseline{
		Version:   baselineVersion,
		CHVersion: "25.8.29.51",
		Note:      "existing free-text note, must survive untouched",
		Cells: map[string]*BaselineCell{
			CellName(CurrentProfileID, 1, 2000): {
				Population: CurrentPopulation(testProfileHash), Seed: 1, N: 2000,
				FixtureHash: "fixture-hash-A", FixtureSignature: "fixture-A", ServerRun: 1,
				Mismatch: []string{"other-grammar-cell: chgen=X ch=Y"},
				Blind43:  []string{"other-grammar-cell-blind: chgen=Z"},
			},
		},
		UnionCells: map[string]*UnionBaselineCell{
			CurrentProfileID: {
				Population:       CurrentPopulation(testProfileHash),
				FixtureHash:      "fixture-hash-v3",
				FixtureSignature: "fixture-v3",
				Seeds:            []int64{1, 7, 42, 99},
				ServerRun:        1787326040,
				Mismatch:         []string{"v3-mismatch-untouched: chgen=Int64 ch=Float64"},
				Blind43: []string{
					"v3-fn-equals: chgen=UInt8",
					"v3-fn-quantileStateIf: chgen=AggregateFunction(quantile, UInt64)",
				},
			},
		},
	}
}

func reportForUnion(seed int64) *Report {
	r := reportForGate()
	r.Population = CurrentPopulation(testProfileHash)
	r.FixtureHash = "fixture-hash-v3"
	r.FixtureSignature = "fixture-v3"
	r.Seed = seed
	r.ServerRun = 1787326040
	return r
}

func TestGateUnionRefusesIdentityBoundaryMutations(t *testing.T) {
	valid := func() (*Baseline, []*Report) {
		return unionBaselineForRetire(), []*Report{reportForUnion(1), reportForUnion(7)}
	}
	b, reports := valid()
	if _, err := b.GateUnion(reports); err != nil {
		t.Fatalf("valid union: %v", err)
	}

	for name, mutate := range map[string]func(*Report){
		"missing population": func(r *Report) { r.Population = PopulationIdentity{} },
		"mixed profile hash": func(r *Report) {
			r.Population = CurrentPopulation("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
			r.SamplingPlanHash = r.Population.ProfileHash
		},
		"missing fixture hash": func(r *Report) { r.FixtureHash = "" },
		"mixed fixture hash":   func(r *Report) { r.FixtureHash = "another-hash" },
		"mixed fixture shape":  func(r *Report) { r.FixtureSignature = "another-fixture" },
		"mixed server version": func(r *Report) {
			r.CHVersion = "25.9.1.1"
		},
		"missing server run": func(r *Report) { r.ServerRun = 0 },
		"mixed server run": func(r *Report) {
			r.ServerRun += 7200
		},
	} {
		b, reports := valid()
		mutate(reports[1])
		if _, err := b.GateUnion(reports); err == nil {
			t.Errorf("%s: GateUnion must refuse", name)
		}
	}
}

// A run that no longer shows a signature is the happy path: RetireUnion must
// remove exactly that signature, record the version and the reason, and
// leave every other signature, cell and top-level field untouched.
func TestRetireUnionRemovesExactlyTheNamedSignature(t *testing.T) {
	b := unionBaselineForRetire()
	// The given report does not reproduce "v3-fn-equals" at all.
	report := &Report{
		CHVersion: "25.8.29.51", Seed: 1, Generated: 5000, ServerRun: 999,
		Findings: []Finding{
			{Class: "CH_ERROR_43_CHGEN_TYPED", Kind: "v3-fn-quantileStateIf",
				ChgenType: "AggregateFunction(quantile, UInt64)"},
		},
	}
	touched, err := b.RetireUnion(
		map[string][]*Report{CurrentProfileID: {report}},
		[]RetirementRequest{{ProfileID: CurrentProfileID, Signature: "v3-fn-equals: chgen=UInt8", Reason: "array-element-comparability"}},
		"25.8.29.51",
	)
	if err != nil {
		t.Fatalf("RetireUnion: %v", err)
	}
	if len(touched) != 1 || touched[0] != CurrentProfileID {
		t.Fatalf("expected the current profile, got %v", touched)
	}
	v3 := b.UnionCells[CurrentProfileID]
	if containsString(v3.Blind43, "v3-fn-equals: chgen=UInt8") {
		t.Fatal("the retired signature must be gone from Blind43")
	}
	if !containsString(v3.Blind43, "v3-fn-quantileStateIf: chgen=AggregateFunction(quantile, UInt64)") {
		t.Fatal("the OTHER accepted signature in the same cell must remain")
	}
	rec, ok := v3.Retired["v3-fn-equals: chgen=UInt8"]
	if !ok {
		t.Fatal("the retirement must be recorded in Retired")
	}
	if rec.CHVersion != "25.8.29.51" || rec.Reason != "array-element-comparability" {
		t.Fatalf("unexpected retirement record %+v", rec)
	}
	// Every other part of the baseline is untouched.
	if len(v3.Mismatch) != 1 || v3.Mismatch[0] != "v3-mismatch-untouched: chgen=Int64 ch=Float64" {
		t.Fatalf("the cell's Mismatch list must be untouched, got %v", v3.Mismatch)
	}
	if len(v3.Seeds) != 4 || v3.ServerRun != 1787326040 {
		t.Fatalf("Seeds and ServerRun must be untouched, got %v / %d", v3.Seeds, v3.ServerRun)
	}
	v1 := b.Cells[CellName(CurrentProfileID, 1, 2000)]
	if len(v1.Mismatch) != 1 || len(v1.Blind43) != 1 {
		t.Fatalf("the per-seed Cells entry must be untouched, got %+v", v1)
	}
	if b.Note != "existing free-text note, must survive untouched" {
		t.Fatalf("the top-level Note must be untouched, got %q", b.Note)
	}
	if b.CHVersion != "25.8.29.51" {
		t.Fatalf("CHVersion must be untouched, got %q", b.CHVersion)
	}
}

// Requirement 1: a signature that the given reports still show must not
// retire, and the refusal must name the offending signature.
func TestRetireUnionRefusesASignatureStillPresentInTheRun(t *testing.T) {
	b := unionBaselineForRetire()
	report := &Report{
		CHVersion: "25.8.29.51", Seed: 1, Generated: 5000, ServerRun: 999,
		Findings: []Finding{
			{Class: "CH_ERROR_43_CHGEN_TYPED", Kind: "v3-fn-equals", ChgenType: "UInt8"},
		},
	}
	_, err := b.RetireUnion(
		map[string][]*Report{CurrentProfileID: {report}},
		[]RetirementRequest{{ProfileID: CurrentProfileID, Signature: "v3-fn-equals: chgen=UInt8", Reason: "array-element-comparability"}},
		"25.8.29.51",
	)
	if err == nil {
		t.Fatal("expected a refusal: the run still shows the signature")
	}
	if !strings.Contains(err.Error(), "v3-fn-equals: chgen=UInt8") {
		t.Fatalf("the refusal must name the offending signature, got: %v", err)
	}
	// Nothing may have been mutated by the refused attempt.
	if !containsString(b.UnionCells[CurrentProfileID].Blind43, "v3-fn-equals: chgen=UInt8") {
		t.Fatal("a refused retirement must not remove the signature")
	}
}

// Requirement 2: a signature that is not in the baseline cell must not
// retire, and the refusal must name it (guards against a typo).
func TestRetireUnionRefusesASignatureNotInTheBaseline(t *testing.T) {
	b := unionBaselineForRetire()
	_, err := b.RetireUnion(
		map[string][]*Report{},
		[]RetirementRequest{{ProfileID: CurrentProfileID, Signature: "v3-fn-eqauls: chgen=UInt8", Reason: "typo-test"}},
		"25.8.29.51",
	)
	if err == nil {
		t.Fatal("expected a refusal: the signature is not in the baseline")
	}
	if !strings.Contains(err.Error(), "v3-fn-eqauls: chgen=UInt8") {
		t.Fatalf("the refusal must name the offending signature, got: %v", err)
	}
}

// Requirement 3: -retire-union needs the measured ClickHouse version, the
// same discipline as -update-union, so a retirement can never be a hand
// edit under another name.
func TestRetireUnionRefusesWithoutAMeasuredVersion(t *testing.T) {
	b := unionBaselineForRetire()
	_, err := b.RetireUnion(
		map[string][]*Report{},
		[]RetirementRequest{{ProfileID: CurrentProfileID, Signature: "v3-fn-equals: chgen=UInt8", Reason: "array-element-comparability"}},
		"",
	)
	if err == nil {
		t.Fatal("expected a refusal: no -i-measured-this version given")
	}
}

// Requirement 4: every retired signature needs a reason (a tracking id or a
// short sentence), so a later reader can tell WHY each one retired.
func TestRetireUnionRefusesWithoutAReason(t *testing.T) {
	b := unionBaselineForRetire()
	_, err := b.RetireUnion(
		map[string][]*Report{},
		[]RetirementRequest{{ProfileID: CurrentProfileID, Signature: "v3-fn-equals: chgen=UInt8", Reason: ""}},
		"25.8.29.51",
	)
	if err == nil {
		t.Fatal("expected a refusal: no reason given for the retired signature")
	}
}

// Requirement 5: absence from the run is NEVER sufficient on its own. A
// retirement with zero reports passed for the profile must still succeed
// (an empty run cannot contradict the assertion), proving that absence
// alone is accepted only because a human explicitly asserted the fix via
// RetirementRequest, not because the tool inferred it from silence.
func TestRetireUnionSucceedsWithNoReportsGivenForTheGrammar(t *testing.T) {
	b := unionBaselineForRetire()
	touched, err := b.RetireUnion(
		map[string][]*Report{},
		[]RetirementRequest{{ProfileID: CurrentProfileID, Signature: "v3-fn-equals: chgen=UInt8", Reason: "array-element-comparability"}},
		"25.8.29.51",
	)
	if err != nil {
		t.Fatalf("RetireUnion: %v", err)
	}
	if len(touched) != 1 || touched[0] != CurrentProfileID {
		t.Fatalf("expected the current profile, got %v", touched)
	}
	if containsString(b.UnionCells[CurrentProfileID].Blind43, "v3-fn-equals: chgen=UInt8") {
		t.Fatal("the signature must be retired")
	}
}

// A retired signature must never be retirable a second time from the same
// call, and a request naming a profile the baseline has no union cell for
// must be refused and name the profile.
func TestRetireUnionRefusesAnUnknownProfile(t *testing.T) {
	b := unionBaselineForRetire()
	_, err := b.RetireUnion(
		map[string][]*Report{},
		[]RetirementRequest{{ProfileID: "unknown-profile", Signature: "does-not-matter", Reason: "x"}},
		"25.8.29.51",
	)
	if err == nil {
		t.Fatal("expected a refusal: the profile has no union cell")
	}
	if !strings.Contains(err.Error(), "unknown-profile") {
		t.Fatalf("the refusal must name the profile, got: %v", err)
	}
}

// A legacy baseline has no report-owned population identity. The reader must
// refuse it with an explicit regeneration instruction instead of guessing.
func TestLoadBaselineRefusesLegacyShapeWithConversionInstruction(t *testing.T) {
	old := `{
  "version": 1,
  "clickhouse_version": "25.8.29.51",
  "note": "old shape, no retired key anywhere",
  "cells": {
    "v1/seed-1/n-10": {
      "grammar": "v1",
      "seed": 1,
      "generated_expressions": 10,
      "fixture_signature": "fixture-A",
      "server_run": 1,
      "server_uptime_s": 1,
      "mismatch_signatures": [],
      "blind43_signatures": []
    }
  },
  "union_cells": {
    "v3": {
      "grammar": "v3",
      "seeds": [1, 7],
      "server_run": 1,
      "mismatch_signatures": [],
      "blind43_signatures": ["v3-fn-equals: chgen=UInt8"]
    }
  }
}
`
	p := filepath.Join(t.TempDir(), "old-baseline.json")
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadBaseline(p)
	if err == nil {
		t.Fatal("a legacy baseline without population identity must be archive-only")
	}
	for _, want := range []string{"legacy version 1", "regenerate", "identity"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("legacy refusal does not give the conversion instruction %q: %v", want, err)
		}
	}
}

// The committed baseline of this repository must load, and it must name the
// pinned server. A baseline that stopped loading would silently stop gating.
func TestCommittedBaselineLoads(t *testing.T) {
	b, err := LoadBaseline(filepath.Join("..", "..", "testdata", "oracle-baseline.json"))
	if err != nil {
		t.Fatalf("the committed baseline must load: %v", err)
	}
	if b.CHVersion == "" {
		t.Fatal("the committed baseline must name the ClickHouse version that produced it")
	}
	for name, cell := range b.Cells {
		if cell.FixtureSignature == "" {
			t.Fatalf("cell %s does not name its fixture", name)
		}
		if cell.ServerRun == 0 {
			t.Fatalf("cell %s does not name the server run that produced it", name)
		}
	}
}
