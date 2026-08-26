//go:build fuzzoracle

package engine

// Guards for the execution witness of the type oracle.
//
// The oracle asks the server for the type of an expression. If it asks with
// toTypeName alone, ClickHouse answers from ANALYSIS and never runs the
// function. An expression that the server refuses at EXECUTION time then
// still reports a type, and chgen's correct refusal of that expression is
// recorded as CHGEN_ERROR, the class that the report documents as "a defect
// by construction". The class is only that strong while the server side is an
// execution witness.
//
// These tests run against the live server, like the oracle itself, and they
// fail if the witness stops executing.
//
// Run:
//
//	CHGEN_ORACLE_URL=http://localhost:18123 \
//	    go test -tags fuzzoracle -run TestExecWitness -v ./internal/engine

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// execWitnessFixture makes a private database with the current fixture and
// returns an oracle bound to it. It mirrors the setup of TestTypeOracle, so a
// guard here measures the same server surface as a real run.
func execWitnessFixture(t *testing.T) *chOracle {
	return execWitnessFixtureWithDDL(t, oracleSchemaDDL, oracleSeedRow)
}

func execWitnessFixtureWithDDL(t *testing.T, schemaDDL, seedRow string) *chOracle {
	t.Helper()
	baseURL := os.Getenv("CHGEN_ORACLE_URL")
	if baseURL == "" {
		t.Skip("CHGEN_ORACLE_URL is not set; start a disposable ClickHouse and set the URL")
	}
	oracle := &chOracle{url: baseURL, client: &http.Client{Timeout: 60 * time.Second}}
	database := fmt.Sprintf("chgen_execwitness_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := oracle.adminExec("CREATE DATABASE " + database); err != nil {
		t.Fatalf("create database %q: %v", database, err)
	}
	oracle.database = database
	t.Cleanup(func() {
		if _, err := oracle.adminExec("DROP DATABASE IF EXISTS " + database); err != nil {
			t.Logf("drop database %q: %v", database, err)
		}
	})
	if _, err := oracle.exec(schemaDDL); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := oracle.exec(seedRow); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	return oracle
}

// TestExecWitnessSeesExecutionTimeRefusal is the regression guard for the
// defect that the recorded defect records. toStartOfInterval over a Date column
// passes analysis and reports DateTime, and fails at execution with code 43.
// The witness must report it as a refusal, not as a type.
//
// This test FAILS on a toTypeName-only witness, which is exactly the point:
// it is the measurement that proves the defect before the fix.
func TestExecWitnessSeesExecutionTimeRefusal(t *testing.T) {
	oracle := execWitnessFixture(t)

	const expr = "toStartOfInterval(d, INTERVAL 1 HOUR)"

	// The analysis witness. Kept explicit, so the difference between the
	// two witnesses stays on record instead of being folded away.
	analysis, err := oracle.exec("SELECT toTypeName(" + expr + ") FROM t")
	if err != nil {
		t.Fatalf("the analysis probe must succeed, that is the whole defect: %v", err)
	}
	if strings.TrimSpace(analysis) != "DateTime" {
		t.Fatalf("analysis reported %q, expected DateTime; the fixture or the server changed", analysis)
	}

	// The witness that the oracle actually uses.
	got := oracle.typeNames([]string{expr})[0]
	if got.err == "" {
		t.Fatalf("the witness reported type %q for an expression that the server refuses at execution time.\n"+
			"  analysis says: %s\n"+
			"The witness is an ANALYSIS witness, so a correct chgen refusal lands in CHGEN_ERROR\n"+
			"instead of CH_ERROR, and the documented meaning of CHGEN_ERROR does not hold.",
			got.typeName, strings.TrimSpace(analysis))
	}
	if code := clickHouseErrorCode(got.err); code != "43" {
		t.Errorf("expected the refusal to carry code 43, got %q: %s", code, firstLine(got.err))
	}
}

// TestExecWitnessDoesNotInventRefusals is the other half of the guard. Making
// the witness execute must not turn healthy expressions into refusals. In
// particular an aggregate and a bare column cannot share one SELECT once the
// witness executes (code 215, NOT_AN_AGGREGATE), and that is a property of
// the BATCH, not of either expression. If that artifact reached the report it
// would be a harness failure wearing the mask of a server refusal.
func TestExecWitnessDoesNotInventRefusals(t *testing.T) {
	oracle := execWitnessFixture(t)

	// A batch that mixes the two shapes. Every member is valid on its own.
	exprs := []string{
		"i32",
		"sum(i32)",
		"s",
		"avg(f64)",
		"empty(s)",
		"count()",
		"row_number() OVER (ORDER BY i32)",
		"toStartOfDay(dt)",
	}
	results := oracle.typeNames(exprs)
	for i, expr := range exprs {
		if results[i].err != "" {
			t.Errorf("the witness refused %q, which the server accepts: %s",
				expr, firstLine(results[i].err))
		}
		if results[i].typeName == "" {
			t.Errorf("the witness returned no type for %q", expr)
		}
	}
}

// TestExecWitnessKeepsGenuineAggregateRefusal proves that the fix for the
// batching artifact above does not silence the REAL version of the same
// error. `sum(i32) + i32` is refused by the server on its own, under plain
// toTypeName as well, thus it must stay a refusal. Without this guard a fix
// that simply ignores code 215 would pass the previous test and lose a true
// finding.
func TestExecWitnessKeepsGenuineAggregateRefusal(t *testing.T) {
	oracle := execWitnessFixture(t)

	const expr = "sum(i32) + i32"
	if _, err := oracle.exec("SELECT toTypeName(" + expr + ") FROM t"); err == nil {
		t.Fatalf("precondition failed: the server now accepts %q even in analysis", expr)
	}

	got := oracle.typeNames([]string{expr})[0]
	if got.err == "" {
		t.Fatalf("the witness reported type %q for %q, which the server refuses", got.typeName, expr)
	}
	if code := clickHouseErrorCode(got.err); code != "215" {
		t.Errorf("expected code 215, got %q: %s", code, firstLine(got.err))
	}
}

// TestExecWitnessAgreesWithAnalysisOnHealthyExpressions checks that the
// execution witness reports the SAME type string as the analysis witness
// wherever the expression is healthy. The execution witness must add
// refusals, never change types; a changed type would silently move every
// comparison in the report.
func TestExecWitnessAgreesWithAnalysisOnHealthyExpressions(t *testing.T) {
	oracle := execWitnessFixture(t)

	exprs := []string{
		"i32", "u64", "f64", "dec", "s", "fs", "d", "dt", "dt64",
		"ni32", "ns", "arr_i", "m", "lc", "lcn", "tup", "uid", "ip4",
		"i128", "u256", "d128", "dtz64",
		"empty(s)", "toStartOfDay(dt)", "arrayMap(x -> x + 1, arr_i)",
		"CAST(1, 'Decimal(10, 2)')", "(i32 IN (SELECT i32 FROM t))",
	}
	execResults := oracle.typeNames(exprs)
	for i, expr := range exprs {
		if execResults[i].err != "" {
			t.Errorf("the witness refused healthy expression %q: %s", expr, firstLine(execResults[i].err))
			continue
		}
		analysis, err := oracle.exec("SELECT toTypeName(" + expr + ") FROM t")
		if err != nil {
			t.Errorf("analysis probe for %q failed: %v", expr, err)
			continue
		}
		if normalizeTypeName(analysis) != normalizeTypeName(execResults[i].typeName) {
			t.Errorf("witness disagreement for %q: analysis=%s execution=%s",
				expr, strings.TrimSpace(analysis), execResults[i].typeName)
		}
	}
}

// --- round trip: does the OK type NAME exist as a real column type? ---
//
// The tests above guard that the WITNESS executes the EXPRESSION. They do not
// guard the last mile that the recorded defect names: toTypeName is ANALYSIS, not
// EXECUTION, so a type string that toTypeName prints without complaint can
// still be a string that names no legal ClickHouse type at all. tracking item
// the regression is the measured proof: chgen once inferred
// AggregateFunction(quantile(b), Bool) for quantileState(b), which is Code
// 134, PARAMETERS_TO_AGGREGATE_FUNCTIONS_MUST_BE_LITERALS, at CREATE TABLE
// time. That is not a wrong type; it is not a type.
//
// The correction: for every expression that the oracle would classify OK
// (chgen's type string and the witness's type string agree) AND whose type
// holds an AggregateFunction or a parametric spelling, round-trip the type
// through CREATE TABLE. holdsAggregateOrParametricType is the filter; it
// matches "AggregateFunction(" (which is also a substring of
// "SimpleAggregateFunction(", the sole other place a parametric aggregate
// name can appear).

// holdsAggregateOrParametricType reports whether a type name contains an
// AggregateFunction or SimpleAggregateFunction spelling, parametric or not.
// "AggregateFunction(" is checked, not "AggregateFunction", so that a bare
// word inside an unrelated identifier cannot match; no ClickHouse type name
// uses the substring any other way.
func holdsAggregateOrParametricType(typeName string) bool {
	return strings.Contains(typeName, "AggregateFunction(")
}

func TestHoldsAggregateOrParametricType(t *testing.T) {
	for _, tt := range []struct {
		name string
		want bool
	}{
		{"Int32", false},
		{"AggregateFunction(uniq, UInt64)", true},
		{"AggregateFunction(quantile(0.5), Float64)", true},
		{"SimpleAggregateFunction(sum, Int64)", true},
		{"Array(AggregateFunction(uniq, UInt64))", true},
		{"Nullable(Int32)", false},
	} {
		if got := holdsAggregateOrParametricType(tt.name); got != tt.want {
			t.Errorf("holdsAggregateOrParametricType(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// roundTripColumnType asks the server whether typeName can hold a real
// column: CREATE TABLE with one column of that type, in a throwaway table
// that this call also drops. A CREATE failure means the type string names no
// legal ClickHouse type, which is a defect worse than a wrong type: a wrong
// type is still a type.
//
// CREATE TABLE is used, not a bare SELECT or CAST, because the regression's own
// shape was measured to fail identically under toTypeName-of-a-CAST and
// under CREATE TABLE (both raise Code 134 on the same malformed spelling),
// so an analysis-flavoured probe would not add a witness that CREATE TABLE
// does not already give, while CREATE TABLE also matches the ticket's own
// wording ("CREATE TABLE with the chgen type, or SELECT into it") and is the
// simpler one of the two to run and to clean up.
// The first return value is the FINDING: a non-nil value means the type
// string names no legal type. The second is a WARNING about the cleanup
// only, which a caller may log and must never read as a finding.
func roundTripColumnType(o *chOracle, typeName string) (error, error) {
	table := fmt.Sprintf("roundtrip_%d", time.Now().UnixNano())
	_, err := o.exec(fmt.Sprintf("CREATE TABLE %s (x %s) ENGINE = Memory", table, typeName))
	if err != nil {
		return err, nil
	}
	if _, dropErr := o.exec("DROP TABLE IF EXISTS " + table); dropErr != nil {
		// The type round-tripped, thus a failed cleanup is NOT the
		// finding under test and must not read as one. The earlier
		// comment here said that the cleanup error goes to the log,
		// and no log line existed: this function holds no *testing.T
		// and could never write one. A caller that wants the warning
		// gets it through the second return value, so the promise and
		// the code now agree.
		//
		// A leaked throwaway table is worth a word, because the run
		// creates one table per checked type and the fixture database
		// is reused between runs.
		return nil, fmt.Errorf("the type round-tripped, and the cleanup of the throwaway table %s failed: %w", table, dropErr)
	}
	return nil, nil
}

// TestRoundTripCatchesTheChgenImjShape is the fail-then-pass proof that
// roundTripColumnType actually catches the defect class the ticket names,
// not only types that were already going to be fine.
//
// FAIL: AggregateFunction(quantile(b), Bool) is the literal type string that
// the regression measured chgen giving for quantileState(b): the aggregate's own
// parameter slot holds the text of the DATA ARGUMENT, "b", which is not a
// literal. Measured here on 25.8.29.51, CREATE TABLE with that exact string
// fails with Code 134. roundTripColumnType MUST report that failure.
//
// PASS: AggregateFunction(quantile(0.5), Float64) is a type that a real
// quantileState(0.5)(f64) call produces (see
// TestExecWitnessAgreesWithAnalysisOnHealthyExpressions's sibling probes for
// the general shape). roundTripColumnType MUST accept it.
func TestRoundTripCatchesTheChgenImjShape(t *testing.T) {
	oracle := execWitnessFixture(t)

	const badType = "AggregateFunction(quantile(b), Bool)"
	err, cleanupWarn := roundTripColumnType(oracle, badType)
	if cleanupWarn != nil {
		t.Logf("cleanup warning, not a finding: %s", firstLine(cleanupWarn.Error()))
	}
	if err == nil {
		t.Fatalf("roundTripColumnType accepted %q, which is not a legal ClickHouse type; "+
			"the check would have missed the measured aggregate-state defect", badType)
	}
	if code := clickHouseErrorCode(err.Error()); code != "134" {
		t.Errorf("expected code 134 (PARAMETERS_TO_AGGREGATE_FUNCTIONS_MUST_BE_LITERALS), got %q: %s",
			code, firstLine(err.Error()))
	}
	t.Logf("round trip correctly refused the aggregate-state shape: %s", firstLine(err.Error()))

	const goodType = "AggregateFunction(quantile(0.5), Float64)"
	goodErr, goodWarn := roundTripColumnType(oracle, goodType)
	if goodWarn != nil {
		t.Logf("cleanup warning, not a finding: %s", firstLine(goodWarn.Error()))
	}
	if goodErr != nil {
		t.Errorf("roundTripColumnType refused a real type %q: %s", goodType, firstLine(goodErr.Error()))
	}
}

// aggregateAndParametricProbeExprs is a curated set of expressions over the
// earlier fixture, chosen to be the ones most likely to carry an AggregateFunction
// or a parametric spelling in their inferred type: every -State combinator
// the v2 schema and grammar reach, plain and with a parameter list, plus the
// two AggregateFunction and SimpleAggregateFunction fixture columns
// themselves and one -Merge and one -MergeState read back off them.
//
// This list is not the fuzzer's random draw. It exists so the round-trip
// guard runs in the untagged-friendly, single-process shape that the other
// execwitness tests use, without pulling in the fuzzer's grammar, its
// generator state and its 5000-expression default N. See the bounded-cost
// measurement in the tracking item comment: the OK set that actually holds this shape
// is a short, fixed list, not a fraction of a large corpus.
var aggregateAndParametricProbeExprs = []string{
	"agg",
	"sagg",
	"uniqState(u64)",
	"uniqMergeState(agg)",
	"uniqMerge(agg)",
	"quantileState(0.5)(f64)",
	"quantileState(f64)",
	"quantilesState(0.5, 0.9)(f64)",
	"topKState(3)(i32)",
	"groupArrayState(i32)",
	"groupArrayState(3)(s)",
	"sumMapState(arr_i, arr_i)",
	"sumIfState(i32, b)",
	"quantileTDigestState(0.5)(f64)",
	"windowFunnelState(3600)(dt, i32 = 1)",
}

// TestOKAggregateAndParametricTypesRoundTrip is the correction that tracking item
// the regression asks for: for every probe expression that the oracle would file
// as OK (chgen's inferred type name and the witness's executed type name
// parse to the same canonical type tree) and whose type holds an
// AggregateFunction or a parametric
// spelling, CREATE TABLE with that exact type string and require it to
// succeed.
//
// A round-trip failure on an OK cell is filed as a hard test failure, not as
// a new report finding class. The report's OK count already means "chgen and
// the server agree"; this test is the guard that closes the remaining gap in
// that meaning (agreement in ANALYSIS text is not the same as the string
// naming a type that can exist), and it runs on every default `go test
// -tags fuzzoracle` pass over this file, not only inside a full oracle run.
// Promoting a hit to a first-class report class (as CH_ERROR_43_CHGEN_TYPED
// is for the sibling blindness class) is worth doing once a real, current hit
// exists to design the class around; today, after the regression, the probe set
// below has none (see the logged count), and a class with no measured member
// would be exactly the kind of check this project has learned not to trust.
func TestOKAggregateAndParametricTypesRoundTrip(t *testing.T) {
	oracle := execWitnessFixture(t)
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatalf("build schema from oracleSchemaDDL: %v", err)
	}

	execResults := oracle.typeNames(aggregateAndParametricProbeExprs)

	// The probe list is CURATED: a human chose every expression because
	// it reaches an aggregate or a parametric type. Thus a probe that
	// stops reaching the round trip is drift in the probe list, in chgen
	// or in the fixture, and never a normal result.
	//
	// The earlier shape of this loop guarded only against `checked == 0`.
	// That cannot tell "one curated probe of fifteen quietly stopped
	// counting" from a healthy run, and a check that cannot make that
	// distinction is a broken deliverable. The reasons are counted, thus
	// the log names every probe that dropped out and why.
	checked := 0
	skippedChgenErr := 0
	skippedMismatch := 0
	skippedNotAggregate := 0
	for i, expr := range aggregateAndParametricProbeExprs {
		ch := execResults[i]
		if ch.err != "" {
			t.Errorf("witness refused probe expression %q: %s", expr, firstLine(ch.err))
			continue
		}
		chgenType, chgenErr := chgenInferType(schema, expr)
		if chgenErr != nil {
			// A chgen refusal is CHGEN_ERROR territory, not OK, and
			// the round-trip filter only ever looks at OK cells.
			// Some -State combinators here have no registered chgen
			// type rule yet (they need a `-- result:` annotation to
			// resolve at all); that gap is tracked elsewhere and is
			// not this test's concern. Log it so the probe list's
			// live coverage stays visible.
			t.Logf("probe %q is CHGEN_ERROR, not OK: %v; skipped by the round-trip filter", expr, chgenErr)
			skippedChgenErr++
			continue
		}
		if normalizeTypeName(chgenType) != normalizeTypeName(ch.typeName) {
			// A real MISMATCH belongs to the fuzz oracle's own
			// findings, not to this guard; still worth logging so a
			// probe expression that regressed to a mismatch is
			// visible here too.
			t.Logf("probe %q is a MISMATCH, not OK (chgen=%s ch=%s); skipped by the round-trip filter",
				expr, chgenType, ch.typeName)
			skippedMismatch++
			continue
		}
		// This cell is OK: chgen and the witness have one canonical type.
		if !holdsAggregateOrParametricType(ch.typeName) {
			skippedNotAggregate++
			continue
		}
		checked++
		roundTripErr, cleanupWarn := roundTripColumnType(oracle, ch.typeName)
		if cleanupWarn != nil {
			t.Logf("cleanup warning for %q, not a finding: %s", expr, firstLine(cleanupWarn.Error()))
		}
		if roundTripErr != nil {
			t.Errorf("OK cell %q named a type that cannot exist: type=%q ch_error=%s",
				expr, ch.typeName, firstLine(roundTripErr.Error()))
		}
	}
	t.Logf("round-tripped %d of %d probe expressions; skipped: chgen_error=%d mismatch=%d not_aggregate=%d",
		checked, len(aggregateAndParametricProbeExprs),
		skippedChgenErr, skippedMismatch, skippedNotAggregate)

	// The reasons must add up to the whole list. A probe that leaves the
	// loop through some other path would otherwise be invisible, and an
	// invisible drop-out is the failure mode this counting exists to stop.
	if total := checked + skippedChgenErr + skippedMismatch + skippedNotAggregate; total != len(aggregateAndParametricProbeExprs) {
		t.Errorf("the probe outcomes do not add up: %d accounted of %d probes. "+
			"A probe left the loop through an uncounted path, thus the counts above cannot be read",
			total, len(aggregateAndParametricProbeExprs))
	}

	// MISMATCH on a curated probe is drift, not a normal outcome. A human
	// chose each expression because chgen and the server agreed on it, so
	// a mismatch here means either chgen regressed or the probe is stale.
	// The fuzz oracle owns the finding; this test owns the fact that the
	// probe stopped measuring what it was curated to measure.
	if skippedMismatch > 0 {
		t.Errorf("%d curated probe expressions became MISMATCH and no longer reach the round trip; "+
			"the probe list measures less than it claims. Read the per-probe log lines above",
			skippedMismatch)
	}
	if checked == 0 {
		t.Fatalf("no probe expression reached the round-trip check; the probe list or the fixture drifted " +
			"from the schema, and the bounded-cost claim this test exists to verify was not measured")
	}
}
