package typeboundary

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/IlyaGulya/chgen/internal/conformance"
)

// TestCatalogNamesAreUnique guards the invariant Run relies on: Compare
// keys on cell name, so two cells sharing a name would silently merge into
// one comparison slot.
func TestCatalogNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, cell := range Catalog {
		if seen[cell.Name] {
			t.Fatalf("duplicate catalog cell name %q", cell.Name)
		}
		seen[cell.Name] = true
	}
	if len(Catalog) == 0 {
		t.Fatal("Catalog is empty")
	}
}

func TestVerdictEqual(t *testing.T) {
	cases := []struct {
		a, b  Verdict
		equal bool
	}{
		{Verdict{TypeName: "Int32"}, Verdict{TypeName: "Int32"}, true},
		{Verdict{TypeName: "Int32"}, Verdict{TypeName: "Int64"}, false},
		{Verdict{ErrorCode: 43}, Verdict{ErrorCode: 43}, true},
		{Verdict{ErrorCode: 43}, Verdict{ErrorCode: 386}, false},
		{Verdict{TypeName: "Int32"}, Verdict{ErrorCode: 43}, false},
	}
	for _, c := range cases {
		if got := c.a.Equal(c.b); got != c.equal {
			t.Errorf("Equal(%v, %v) = %v, want %v", c.a, c.b, got, c.equal)
		}
	}
}

func TestValidateConformanceRejectsEachMissingLane(t *testing.T) {
	input := conformance.Input{ID: "a", Expression: "i", Table: "t"}
	typed := conformance.CanonicalResult("Int32")
	valid := func() *Artifact {
		return &Artifact{
			Version: ArtifactVersion, CatalogNames: []string{"a"}, Cells: map[string]Verdict{"a": {TypeName: "Int32"}},
			FixtureHash: "fixture", SeedHash: "seed", MatrixHash: conformance.MatrixHash([]conformance.Input{input}),
			Conformance: []conformance.Cell{{
				ID: input.ID, Expression: input.Expression, Table: input.Table,
				Chgen: typed, Analysis: typed, Execution: conformance.ExecutionResult{Ran: true},
			}},
		}
	}
	mutations := []struct {
		name   string
		mutate func(*Artifact)
	}{
		{name: "chgen", mutate: func(a *Artifact) { a.Conformance[0].Chgen = conformance.TypeResult{} }},
		{name: "analysis", mutate: func(a *Artifact) { a.Conformance[0].Analysis = conformance.TypeResult{} }},
		{name: "execution", mutate: func(a *Artifact) { a.Conformance[0].Execution = conformance.ExecutionResult{} }},
		{name: "matrix", mutate: func(a *Artifact) { a.MatrixHash = "mutated" }},
	}
	if err := valid().ValidateConformance(); err != nil {
		t.Fatalf("valid conformance artifact: %v", err)
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			artifact := valid()
			mutation.mutate(artifact)
			if err := artifact.ValidateConformance(); err == nil {
				t.Fatal("mutation did not make the deterministic gate refuse")
			}
		})
	}
}

// TestCompareClassifiesEveryMoveKind checks the four move kinds directly,
// on hand-built artifacts sharing one catalog name list, so this test does
// not need a live server.
func TestCompareClassifiesEveryMoveKind(t *testing.T) {
	names := []string{"typed_to_refused", "refused_to_typed", "retyped", "refusal_changed", "unchanged"}
	oldCells := map[string]Verdict{
		"typed_to_refused": {TypeName: "Int32"},
		"refused_to_typed": {ErrorCode: 43},
		"retyped":          {TypeName: "Int32"},
		"refusal_changed":  {ErrorCode: 43},
		"unchanged":        {TypeName: "String"},
	}
	newCells := map[string]Verdict{
		"typed_to_refused": {ErrorCode: 43},
		"refused_to_typed": {TypeName: "Int32"},
		"retyped":          {TypeName: "Int64"},
		"refusal_changed":  {ErrorCode: 386},
		"unchanged":        {TypeName: "String"},
	}
	oldArt := &Artifact{Version: ArtifactVersion, CatalogNames: names, Cells: oldCells}
	newArt := &Artifact{Version: ArtifactVersion, CatalogNames: names, Cells: newCells}

	res, err := Compare(oldArt, newArt)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(res.Moves) != 4 {
		t.Fatalf("expected 4 moves, got %d: %v", len(res.Moves), res.Moves)
	}
	want := map[string]MoveKind{
		"typed_to_refused": TypedToRefused,
		"refused_to_typed": RefusedToTyped,
		"retyped":          Retyped,
		"refusal_changed":  RefusalChanged,
	}
	got := map[string]MoveKind{}
	for _, m := range res.Moves {
		got[m.Cell] = m.Kind
	}
	for cell, kind := range want {
		if got[cell] != kind {
			t.Errorf("cell %q: got kind %s, want %s", cell, got[cell], kind)
		}
	}
	if _, present := got["unchanged"]; present {
		t.Errorf("cell %q must not be reported as a move", "unchanged")
	}
}

func TestCompareRefusesMismatchedCatalog(t *testing.T) {
	oldArt := &Artifact{Version: ArtifactVersion, CatalogNames: []string{"a", "b"}, Cells: map[string]Verdict{"a": {TypeName: "Int32"}, "b": {TypeName: "Int32"}}}
	newArt := &Artifact{Version: ArtifactVersion, CatalogNames: []string{"a", "c"}, Cells: map[string]Verdict{"a": {TypeName: "Int32"}, "c": {TypeName: "Int32"}}}
	_, err := Compare(oldArt, newArt)
	if err == nil {
		t.Fatal("expected Compare to refuse two artifacts with different catalogs, got nil error")
	}
}

func TestCompareRefusesEmptyArtifact(t *testing.T) {
	full := &Artifact{Version: ArtifactVersion, CatalogNames: []string{"a"}, Cells: map[string]Verdict{"a": {TypeName: "Int32"}}}
	empty := &Artifact{Version: ArtifactVersion, CatalogNames: nil, Cells: map[string]Verdict{}}
	if _, err := Compare(empty, full); err == nil {
		t.Fatal("expected Compare to refuse an empty old artifact")
	}
	if _, err := Compare(full, empty); err == nil {
		t.Fatal("expected Compare to refuse an empty new artifact")
	}
}

// testServerURL returns the live ClickHouse URL from CHGEN_PROBE_TEST_URL,
// or skips the test. These tests need a real server, per this project's own
// rule that a probe measures real table columns, never a remembered
// assumption about server behaviour.
func testServerURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("CHGEN_PROBE_TEST_URL")
	if u == "" {
		t.Skip("CHGEN_PROBE_TEST_URL not set; skipping live-server test")
	}
	return u
}

// TestRunAgainstLiveServer runs the full catalog against a live server and
// checks the artifact shape: every declared name has exactly one verdict,
// and every verdict is either a type or a ClickHouse error code, never
// both.
func TestRunAgainstLiveServer(t *testing.T) {
	url := testServerURL(t)
	client := NewClient(url, "default")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	art, err := Run(ctx, client)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(art.Cells) != len(Catalog) {
		t.Fatalf("got %d cells, want %d (one per catalog entry)", len(art.Cells), len(Catalog))
	}
	for _, cell := range Catalog {
		v, ok := art.Cells[cell.Name]
		if !ok {
			t.Errorf("cell %q missing from artifact", cell.Name)
			continue
		}
		hasType := v.TypeName != ""
		hasError := v.ErrorCode != 0
		if hasType == hasError {
			t.Errorf("cell %q: verdict %+v must carry exactly one of type or error code", cell.Name, v)
		}
	}
}

// TestFixedQueryIsStableAcrossTime is the empirical proof behind this
// package's central claim: unlike the type oracle's random sample, a FIXED
// probe cell gives the same verdict on one warm server no matter when it is
// asked. It runs the full catalog twice, forty seconds apart, against the
// SAME live server and asserts the two artifacts compare IDENTICAL.
//
// Forty seconds is short for a CI test but it is exactly the axis under
// study: the type oracle's own instability was measured across HOURS of
// uptime, driven by a shifting random sample, not by a clock tick. A fixed
// query has no sample to shift, so if this test is going to fail at all it
// does not need a long wait to prove it — and a long sleep here would only
// slow down every run of this package's tests for no added evidence.
func TestFixedQueryIsStableAcrossTime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stability probe in -short mode")
	}
	url := testServerURL(t)
	client := NewClient(url, "default")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	first, err := Run(ctx, client)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	time.Sleep(40 * time.Second)
	second, err := Run(ctx, client)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}

	res, err := Compare(first, second)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !res.Identical() {
		t.Errorf("expected a fixed catalog run twice on one live server to compare IDENTICAL, got:\n%s", res)
	}
}
