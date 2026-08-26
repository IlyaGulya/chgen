package oraclereport

import (
	"encoding/json"
	"strings"
	"testing"
)

// baseReport is one plausible oracle report. Every test starts from a copy of
// it, so a test that changes one field changes ONLY that field.
func baseReport() *Report {
	return &Report{
		Version:          ReportVersion,
		Population:       CurrentPopulation(testProfileHash),
		SamplingPlanID:   CurrentProfileID,
		SamplingPlanHash: testProfileHash,
		LaneAttempts:     []byte(`{"test-lane":1}`),
		LaneAccepted:     []byte(`{"test-lane":1}`),
		Date:             "2026-08-19T10:00:00Z",
		CHVersion:        "25.8.29.51",
		Seed:             42,
		Generated:        2000,
		Canaries:         4,
		ServerRun:        1787145821,
		ServerUptimeS:    1330,
		RunDatabase:      "chgen_oracle_1_2",
		FixtureHash:      "fixture-hash",
		FixtureSignature: "rows=1 columns=i8:Int8,s:String",
		Counts: map[string]int{
			"OK": 1457, "CH_ERROR": 347, "MISMATCH": 3, "CHGEN_ERROR": 1,
		},
		MismatchSig:        map[string]int{"call: chgen=Bool ch=UInt8": 3},
		Blind43:            map[string]int{"call": 5},
		CHErrorCodes:       map[string]int{"43": 300, "44": 47},
		CHErrorByKind:      map[string]int{"call": 347},
		CHErrorDigest:      "abc123",
		FindingsCHErrorCap: 200,
		Findings: []Finding{
			{Class: "MISMATCH", Expr: "empty(s)", Kind: "call", ChgenType: "Bool", CHType: "UInt8"},
			{Class: "CHGEN_ERROR", Expr: "weird(s)", Kind: "call", ChgenErr: "unknown function"},
			{Class: "CH_ERROR", Expr: "bad(s)", Kind: "call", CHErr: "Code: 43. ..."},
		},
	}
}

// TestCompareAcceptsTwoReportsOfTheSameRun shows that the comparator does the
// ordinary job: two reports from one server run and one fixture agree.
func TestCompareAcceptsTwoReportsOfTheSameRun(t *testing.T) {
	a, b := baseReport(), baseReport()
	res, err := Compare(a, b, Options{})
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if !res.Identical {
		t.Fatalf("expected agreement, got:\n%s", res)
	}
	if res.CrossInstance {
		t.Fatalf("same server_run must not be marked cross-instance")
	}
	// The verdict must carry the volume, so that a reader can tell
	// agreement from an empty read.
	out := res.String()
	for _, want := range []string{"counts=4 keys/1808 expressions", "findings=3 (comparable 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("verdict does not report how much it read: want %q in\n%s", want, out)
		}
	}
}

// TestCompareRefusesDifferentServerRun is the teeth test. The two reports
// differ in server_run and in NOTHING else. Every count, every digest and
// every finding is byte-equal, so a comparator without teeth would print
// "IDENTICAL" here.
func TestCompareRefusesDifferentServerRun(t *testing.T) {
	a, b := baseReport(), baseReport()
	b.ServerRun = a.ServerRun + 7200 // the server restarted two hours later
	b.ServerUptimeS = 30

	// Prove that the two reports really differ in server_run only.
	stripped := func(r *Report) string {
		c := *r
		c.ServerRun, c.ServerUptimeS = 0, 0
		data, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(data)
	}
	if stripped(a) != stripped(b) {
		t.Fatalf("the fixture of this test is wrong: the two reports differ in more than server_run")
	}

	res, err := Compare(a, b, Options{})
	if err == nil {
		t.Fatalf("comparator has no teeth: it accepted two reports from different server runs and said:\n%s", res)
	}
	for _, want := range []string{"DIFFERENT server runs", "server_run=1787145821", "server_run=1787153021"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal message must name the server runs: want %q in %q", want, err.Error())
		}
	}
}

// TestCompareCrossInstanceIsOptIn shows the only way past the refusal, and
// that the way past is marked in the result.
func TestCompareCrossInstanceIsOptIn(t *testing.T) {
	a, b := baseReport(), baseReport()
	b.ServerRun = a.ServerRun + 7200
	res, err := Compare(a, b, Options{CrossInstance: true})
	if err != nil {
		t.Fatalf("cross-instance comparison must be allowed when asked for: %v", err)
	}
	if !res.CrossInstance {
		t.Fatalf("a cross-instance result must say so")
	}
	if !strings.Contains(res.String(), "different server runs") {
		t.Errorf("cross-instance verdict must warn in its text:\n%s", res)
	}
}

func TestCompareCrossVersionNeedsTwoExplicitOptions(t *testing.T) {
	a, b := baseReport(), baseReport()
	b.CHVersion = "24.8.14.39"
	b.ServerRun += 7200
	if _, err := Compare(a, b, Options{CrossInstance: true}); err == nil {
		t.Fatal("a different ClickHouse version must refuse without CrossVersion")
	}
	if _, err := Compare(a, b, Options{CrossVersion: true}); err == nil {
		t.Fatal("a cross-version comparison must also require CrossInstance")
	}
	res, err := Compare(a, b, Options{CrossInstance: true, CrossVersion: true})
	if err != nil {
		t.Fatalf("explicit cross-version comparison: %v", err)
	}
	if !res.CrossVersion || !strings.Contains(res.String(), "version-boundary measurement") {
		t.Fatalf("the verdict must mark the version boundary:\n%s", res)
	}
}

// TestCompareRefusesAReportWithoutServerRun covers a report written before
// the field existed. Its instance is unknown, thus it cannot be compared.
func TestCompareRefusesAReportWithoutServerRun(t *testing.T) {
	a, b := baseReport(), baseReport()
	a.ServerRun, b.ServerRun = 0, 0
	if _, err := Compare(a, b, Options{}); err == nil {
		t.Fatalf("a report with no server_run must not be comparable")
	}
}

// TestCompareRefusesAnEmptyRead is the guard against "MISMATCH identical=true
// n=0". A file with capitalised keys decodes into a report of zero values; the
// comparator must call that an empty read and not agreement.
func TestCompareRefusesAnEmptyRead(t *testing.T) {
	wrongCase := []byte(`{"Counts":{"OK":1457},"Findings":[],"Seed":42}`)
	if _, err := Decode(wrongCase); err == nil {
		t.Fatal("a legacy or wrong-case report must be refused during strict decode")
	}
}

// TestCompareComparesTheBlindnessFindingList holds the opposite rule for the
// blindness class. CH_ERROR_43_CHGEN_TYPED is a subset of CH_ERROR, but the
// oracle records it in a list of its own that is never capped, thus a
// difference in that list is a real difference and Compare must report it.
func TestCompareComparesTheBlindnessFindingList(t *testing.T) {
	a, b := baseReport(), baseReport()
	blind := Finding{
		Class: "CH_ERROR_43_CHGEN_TYPED", Expr: "toStartOfDay(s)", Kind: "call",
		ChgenType: "DateTime", CHErr: "Code: 43. Illegal type String ...",
	}
	b.Findings = append(append([]Finding{}, b.Findings...), blind)
	res, err := Compare(a, b, Options{})
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if res.Identical {
		t.Fatalf("a blindness finding only in B must be reported")
	}

	// The type that chgen gave is part of the comparison, thus a blind
	// entry with another chgen_type is another finding.
	a.Findings = append(append([]Finding{}, a.Findings...), blind)
	changed := blind
	changed.ChgenType = "Date"
	b.Findings = append(append([]Finding{}, baseReport().Findings...), changed)
	res, err = Compare(a, b, Options{})
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if res.Identical {
		t.Fatalf("a change of chgen_type in a blindness finding must be reported")
	}
}

// TestCompareNeverComparesTheCHErrorFindingList is rule 4. The CH_ERROR
// findings list stops at findings_ch_error_cap while counts["CH_ERROR"] keeps
// counting, so a difference in that list is truncation, not a finding.
func TestCompareNeverComparesTheCHErrorFindingList(t *testing.T) {
	a, b := baseReport(), baseReport()
	b.Findings = []Finding{
		b.Findings[0], b.Findings[1],
		{Class: "CH_ERROR", Expr: "a totally different expression", Kind: "call", CHErr: "Code: 44. ..."},
		{Class: "CH_ERROR", Expr: "and one more", Kind: "call", CHErr: "Code: 47. ..."},
	}
	res, err := Compare(a, b, Options{})
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if !res.Identical {
		t.Fatalf("the CH_ERROR finding list must not be compared, got:\n%s", res)
	}

	// But the uncapped CH_ERROR aggregates MUST be compared.
	b.CHErrorDigest = "different"
	res, err = Compare(a, b, Options{})
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if res.Identical {
		t.Fatalf("ch_error_digest is uncapped and must be compared")
	}
	b = baseReport()
	b.CHErrorCodes = map[string]int{"43": 300, "44": 48}
	res, err = Compare(a, b, Options{})
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if res.Identical {
		t.Fatalf("ch_error_codes is uncapped and must be compared")
	}
}

// TestCompareReportsTheMovedCode44 is the case from the ticket: one and the
// same commit, one fresh instance and one warm instance. With the instances
// named, the comparator refuses; only the deliberate cross-instance call shows
// the code 44 that the warm server produced.
func TestCompareReportsTheMovedCode44(t *testing.T) {
	fresh := baseReport()
	fresh.Counts = map[string]int{"OK": 1457, "CH_ERROR": 347}
	fresh.CHErrorCodes = map[string]int{"43": 347}
	warm := baseReport()
	warm.ServerRun = fresh.ServerRun - 7200
	warm.ServerUptimeS = 7300
	warm.Counts = map[string]int{"OK": 1456, "CH_ERROR": 348}
	warm.CHErrorCodes = map[string]int{"43": 347, "44": 1}

	if _, err := Compare(fresh, warm, Options{}); err == nil {
		t.Fatalf("two instances must not be compared silently")
	}
	res, err := Compare(fresh, warm, Options{CrossInstance: true})
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	out := res.String()
	for _, want := range []string{"ch_error_codes[44]: A=0 B=1", "counts[CH_ERROR]: A=347 B=348", "may be the server, not the code"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in\n%s", want, out)
		}
	}
}

// TestServerRunTolerance covers the jitter of the boot moment. The server
// computes it from two independently truncated integers, so one live instance
// answers with a value that moves by about a second. Inside the tolerance the
// reports are one run; a restart moves the value far outside it.
func TestServerRunTolerance(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delta int64
		same  bool
	}{
		{"equal", 0, true},
		{"one second of truncation jitter", 1, true},
		{"the edge of the tolerance", ServerRunToleranceS, true},
		{"one second past the tolerance", ServerRunToleranceS + 1, false},
		{"a restart after two hours", 7200, false},
	} {
		a, b := baseReport(), baseReport()
		b.ServerRun = a.ServerRun + tc.delta
		_, err := Compare(a, b, Options{})
		if tc.same && err != nil {
			t.Errorf("%s: must be read as one server run, got refusal: %v", tc.name, err)
		}
		if !tc.same && err == nil {
			t.Errorf("%s: must be refused as a different server run", tc.name)
		}
	}
}

// TestCompareRefusesADifferentPopulation covers seed, count and fixture.
func TestCompareRefusesADifferentPopulation(t *testing.T) {
	for name, mutate := range map[string]func(*Report){
		"profile hash": func(r *Report) {
			r.Population = CurrentPopulation("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
			r.SamplingPlanHash = r.Population.ProfileHash
		},
		"scheduler identity": func(r *Report) { r.SamplingPlanHash = strings.Repeat("b", 64) },
		"unknown population": func(r *Report) {
			r.Population = PopulationIdentity{SchemaVersion: PopulationSchemaVersion, ProfileID: "made-up", ProfileHash: testProfileHash}
		},
		"seed":          func(r *Report) { r.Seed = 43 },
		"generated":     func(r *Report) { r.Generated = 5000 },
		"fixture hash":  func(r *Report) { r.FixtureHash = "another-fixture-hash" },
		"fixture shape": func(r *Report) { r.FixtureSignature = "rows=1 columns=i8:Int8" },
		"server version": func(r *Report) {
			r.CHVersion = "24.8.14.39"
		},
	} {
		a, b := baseReport(), baseReport()
		mutate(b)
		if _, err := Compare(a, b, Options{}); err == nil {
			t.Errorf("%s: a different population must be refused", name)
		}
	}
}

func TestDecodeIsStrictAndLegacyReportsAreArchiveOnly(t *testing.T) {
	current, err := json.Marshal(baseReport())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(current); err != nil {
		t.Fatalf("current report: %v", err)
	}
	unknown := strings.Replace(string(current), "{", `{"unknown_identity_field":true,`, 1)
	if _, err := Decode([]byte(unknown)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("strict decode did not refuse an unknown field: %v", err)
	}
	unknownPopulation := strings.Replace(string(current), `"profile_id":"current-combined-v1"`, `"profile_id":"current-combined-v1","label":"current"`, 1)
	if _, err := Decode([]byte(unknownPopulation)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("strict decode did not refuse an unknown population field: %v", err)
	}
	missingPopulation := strings.Replace(string(current), `"population":{"schema_version":1,"profile_id":"current-combined-v1","profile_hash":"`+testProfileHash+`"},`, "", 1)
	if _, err := Decode([]byte(missingPopulation)); err == nil || !strings.Contains(err.Error(), "population identity") {
		t.Fatalf("strict decode did not refuse a missing population identity: %v", err)
	}
	if _, err := Decode(append(current, []byte(` {}`)...)); err == nil || !strings.Contains(err.Error(), "more than one JSON value") {
		t.Fatalf("strict decode did not refuse a second JSON value: %v", err)
	}
	legacy := []byte(`{"date":"2026-08-19T10:00:00Z","clickhouse_version":"25.8.29.51","seed":1,"generated_expressions":2000,"old_unknown_field":true}`)
	_, err = Decode(legacy)
	if err == nil {
		t.Fatal("legacy report must be archive-only")
	}
	for _, want := range []string{"archive-only", "rerun", "do not assign"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("legacy refusal does not contain %q: %v", want, err)
		}
	}
}
