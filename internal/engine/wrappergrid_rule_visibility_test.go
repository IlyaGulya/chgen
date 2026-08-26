package engine

// This file answers the regression question directly: is the instrument
// ABLE to see the defect whose absence it reports?
//
// A cell in the wrapper grid golden is trustworthy evidence about a rule
// only if changing the rule's answer would change that cell's chgen
// type. A rule with no such cell is a rule the census does not measure,
// no matter how many cells sit next to it in AGREE.
//
// This file needs NO server. It replays the SQL of a chosen golden cell
// through the chgen side alone, against the same fixture DDL that the
// grid uses (gridSchemaDDL, defined in wrappergrid_fixture_test.go,
// which itself carries no build tag). It runs in the default `go test
// ./...` suite.
//
// THE FLIP MECHANISM
//
// chgen cannot recompile itself per mutant inside a test process, so
// every rule below is flipped through a seam that already exists at
// package level without any production edit:
//
//   - functionRegistry (registry.go) is a package-level
//     map[string]functionSpec, read fresh on every call through
//     functionRuleFor / functionClassFor. A test may replace one
//     entry's rule or class for the lifetime of a subtest and restore
//     it after, exactly as it would restore a saved global. The
//     production code takes no dependency on the map being immutable;
//     mutating it is an ordinary Go operation, not a new hook.
//   - wrapperTransportOverrides (wrapper_transport.go) is the same
//     shape: a package-level map[string]wrapperTransport that
//     transportForFunction reads fresh on every call. Replacing an
//     entry's wrapperTransport value, including its
//     simpleAggregateWhen/lowCardinalityWhen condition funcs, flips the
//     disposition or the underlying marker rule that the transport
//     delegates to.
//
// Neither seam adds a name-shaped branch, a new indirection layer, or
// a test-only export. It reuses the two maps the resolver already
// consults by name at every call site (registry.go:81-98,
// wrapper_transport.go:616-621). Restoring the original entry in
// t.Cleanup keeps every other subtest and the rest of the suite
// unaffected.
//
// simpleAggregateMarkerSurvives and simpleAggregateMarkerSurvivesValuePreserving
// themselves are unexported functions and are never assigned to a
// variable in production, so they cannot be swapped directly. But
// production never calls them directly either: every call site reaches
// them through a wrapperCondition value stored in a wrapperTransport,
// which in turn sits in one of the two mutable maps above (see
// aggregateKeepsSimpleAggregateCondition, caseFoldingKeepsSimpleAggregateCondition,
// greatestLeastKeepsSimpleAggregateCondition and nullIfTransport.simpleAggregateWhen,
// each of which is either the map value directly or reached only through it).
// Flipping the map entry that carries the condition therefore flips the
// rule's effective answer for every caller of that entry, with no
// change to supertype.go.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// ruleVisibilitySchema parses the grid fixture DDL once per test. It is
// a thin, tag-free twin of gridTestSchema (wrappergrid_shape_test.go),
// kept local so this file does not need to reach into a file owned by
// another change.
func ruleVisibilitySchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, gridSchemaDDL())
	if err != nil {
		t.Fatalf("the grid fixture DDL does not parse: %v", err)
	}
	return schema
}

// chgenTypeOfCell types one cell's SQL exactly as the grid measurement
// does (gridChgenType in wrappergrid_test.go), duplicated here because
// that copy lives behind the fuzzoracle build tag and this file must
// compile in the default suite. ok is false on a chgen refusal, which
// is itself a valid answer and never a test failure by itself.
func chgenTypeOfCell(schema *Schema, sql string) (result string, ok bool) {
	statements, err := clickhouse.NewParser("SELECT " + sql + " FROM g").ParseStmts()
	if err != nil {
		return "", false
	}
	if len(statements) != 1 {
		return "", false
	}
	selectQuery, ok2 := statements[0].(*clickhouse.SelectQuery)
	if !ok2 {
		return "", false
	}
	scope, _, err := resolveScope(selectQuery, schema)
	if err != nil {
		return "", false
	}
	inferred, err := inferExprType(selectQuery.SelectItems[0].Expr, scope)
	if err != nil {
		return "", false
	}
	return inferred.String(), true
}

// goldenCellSQL reads the sql field of one committed golden cell by its
// stable id, so that every assertion below is anchored to a line a
// reviewer can find in testdata/wrapper_grid.golden and not to a
// hand-copied string that could silently drift from it.
//
// The golden format is documented in wrappergrid_test.go: one line per
// cell, tab-separated, "id, verdict, server_type, server_code,
// chgen_type, sql". Reading it needs no server and no build tag; only
// producing it does.
func goldenCellSQL(t *testing.T, id string) string {
	t.Helper()
	raw := readGoldenFileForRuleVisibility(t)
	for _, line := range strings.Split(raw, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, "\t", 6)
		if len(fields) != 6 {
			continue
		}
		if fields[0] == id {
			return fields[5]
		}
	}
	t.Fatalf("cell %s is not in the committed golden; the id must name a real, committed cell", id)
	return ""
}

func readGoldenFileForRuleVisibility(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(gridGoldenPath))
	if err != nil {
		t.Fatalf("read the committed golden: %v", err)
	}
	return string(raw)
}

// ruleVisibilityCase is one measured rule under test.
type ruleVisibilityCase struct {
	// name identifies the rule for the report; it matches the ticket's
	// rule list, and it is the key that ruleVisibilityInvisibleRules
	// looks up.
	name string
	// cellID is the committed golden cell address whose chgen answer
	// must move when the rule flips.
	cellID string
	// flip installs the inverted rule and returns a restore func. It
	// must mutate only a package-level map entry, never a source file.
	flip func(t *testing.T) (restore func())
}

// ruleVisibilityInvisible is one CLOSED, NAMED entry that documents a
// rule this file has PROVEN invisible to every committed golden cell.
//
// This is the same discipline as gridClosureExclusions
// (wrappergrid_shape_test.go): a table that a reader can see in a green
// run, not a log line that only surfaces on failure. Silence is not
// health, and a log line nobody reads is silence.
type ruleVisibilityInvisible struct {
	// reason explains, in one paragraph, the STRUCTURAL cause of the
	// blindness: which call site bypasses the seam, or which enumerator
	// shape never gets produced. "The rule is subtle" is not a reason;
	// a reason names a file and a mechanism.
	reason string
	// trackingID identifies the tracked coverage gap. Every entry must have one:
	// invisible rule is a coverage gap, and a gap without a ticket is a
	// gap nobody owns.
	trackingID string
	// gridEntries names the grid entries whose absence from the golden
	// the claim depends on, spelled as the SECOND path segment of a cell
	// id ("xor", "and"). It is read by
	// TestRuleVisibilityNoCellRulesAreLive, which fails once the golden
	// starts addressing one of them.
	//
	// The field exists so that a "no cell exists" claim is CHECKED
	// rather than trusted. The XOR entry stayed true only while xor had
	// no registry entry, and a prose-only claim would have survived the
	// change that made it false.
	//
	// It is used by ruleVisibilityNoCellRules only.
	// ruleVisibilityInvisibleRules makes the weaker claim that a cell
	// exists and cannot distinguish the flip, which the harness checks
	// directly on every run.
	gridEntries []string
}

// ruleVisibilityInvisibleRules is the CLOSED table of rules that this
// file has proven cannot be distinguished through any committed golden
// cell, each with the reason and the tracking ID.
//
// TestWrapperGridRuleVisibility checks this table in BOTH directions,
// exactly as TestGridClosureExclusionsAreLive checks gridClosureExclusions:
//
//   - a rule NOT named here that turns out invisible FAILS the test. An
//     invisible rule must be diagnosed and named, never silently
//     tolerated.
//   - a rule named here that turns out VISIBLE also FAILS the test. An
//     entry that no longer matches reality is a stale excuse, and the
//     failure forces a reader to delete it rather than let it rot into
//     a permanent, un-re-checked exemption.
var ruleVisibilityInvisibleRules = map[string]ruleVisibilityInvisible{
	// The entry for "greatestLeastTransport: lowCardinality condition
	// (arity 1 keeps)" is DELETED, and the deletion is the record that
	// the regression closed its first finding.
	//
	// The field was dead: functionWrapperFlags resolved the disposition
	// itself and locked it with withResolvedLowCardinality before
	// applyWrapperTransport could read the condition. A later change made the
	// flags function report the FACT instead, thus the transport
	// condition now decides the answer.
	//
	// The witness is the cell fn/greatest/lc/i32. A flip of the
	// condition moves it from LowCardinality(Int32) to Int32. This test
	// FAILED while the entry stayed here, and the failure message named
	// that cell; keep the rule in ruleVisibilityRules below, where the
	// flip is exercised on every run.
	// The entry for "logicOperatorTransport (and/or): lowCardinality
	// disposition (wrapperDrop)" is DELETED, and the deletion is the
	// record that the regression closed.
	//
	// The rule was always right; the ENUMERATOR was blind.
	// gridOperatorProbes rendered "column OP column" with the SAME
	// non-constant column on both sides, and inferBinaryOperationType
	// precomputes lowCardinalityCount==1 && othersConstant BEFORE it
	// asks the transport, thus two non-constant LowCardinality operands
	// always gave a false candidate and no cell could separate
	// wrapperKeep from wrapperDrop.
	//
	// The constant-operand change added this lane. The witness is
	// op/AND/lc-const/i32, "c_lc_i32 AND 3", measured UInt8 on
	// ClickHouse 25.8.29.51: the LowCardinality wrapper is dropped, and
	// a flip of the disposition moves the cell. The rule now lives in
	// ruleVisibilityRules below, where the flip runs on every run.
	//
	// Note that the OLD address op/AND/lc/i32 is still in the golden
	// and is still blind. The visibility case must watch the
	// lc-const address; watching the old one would stay green whatever
	// the disposition said.
	"logicOperatorTransport (xor): lowCardinality disposition (wrapperDrop)": {
		reason: "xor has NO infix token in this grammar (\"1 xor 0\" is Code 62), thus it is " +
			"reachable through the function form only. The function registry gave xor a " +
			"entry, so the golden now holds 29 fn/xor cells and the older \"no cell in any " +
			"form\" claim is retired. The cells still cannot distinguish the disposition, " +
			"because gridFunctionProbes spells every one of them as \"xor(column, column)\" " +
			"with two NON-CONSTANT operands, and the LowCardinality candidate needs exactly " +
			"one LowCardinality operand with every other operand constant. This is the SAME " +
			"enumerator shape that kept the and/or entry here until the constant-operand change. " +
			"It was MEASURED and not assumed: wiring the case to fn/xor/lc/i32 made the " +
			"harness report \"answered the same before and after the flip: UInt8\". It is a " +
			"fixable enumerator gap and not a language fact: on ClickHouse 25.8.29.51 the " +
			"server accepts xor(lci32, 3) and answers UInt8, the exact shape that separates " +
			"wrapperKeep from wrapperDrop, so a constant-operand lane in the FUNCTION " +
			"enumerator would close this in the same way as the operator form.",
		trackingID: "operator-constant-operand",
	},
}

// ruleVisibilityNoCellRules is a SECOND closed table, held apart from
// ruleVisibilityInvisibleRules because its members fail a different
// question. Every entry in ruleVisibilityInvisibleRules has a committed
// golden cell that answers the same before and after the flip; that is
// "a cell exists but cannot distinguish the two answers". An entry here
// instead names a rule for which NO CELL EXISTS AT ALL, in any form, so
// it cannot be wired into the flip-and-compare harness that
// TestWrapperGridRuleVisibility runs. TestXorHasNoGoldenCellOfAnyForm is
// this table's liveness check: it asserts the structural facts the
// reason depends on directly against the committed golden and the
// registry, so the entry cannot go stale without a test noticing.
// The table is EMPTY today, and that is a result rather than an
// oversight. Its only member was the XOR entry, which held while xor had
// no functionRegistry entry at all and thus no cell in any form.
// the regression gave and, or and xor a registry entry, so fn/xor cells
// exist, and the rule moved to a real flip-and-compare case in
// ruleVisibilityRules with the witness fn/xor/lc/i32.
//
// Keep the table and its two-way discipline: the next rule that has no
// cell in any form belongs here with a reason and a tracking item, not in a
// comment.
var ruleVisibilityNoCellRules = map[string]ruleVisibilityInvisible{}

// TestWrapperGridRuleVisibility is the regression: for each measured rule,
// flipping its answer must change the chgen type of at least one
// committed golden cell.
//
//   - A rule NOT named in ruleVisibilityInvisibleRules that turns out
//     invisible FAILS with a loud t.Errorf finding.
//   - A rule NAMED in ruleVisibilityInvisibleRules that stays invisible
//     is reported through t.Log AND through the closed table itself,
//     which a reader sees without needing a failing run.
//   - A rule NAMED in ruleVisibilityInvisibleRules that turns out
//     VISIBLE after all also FAILS, telling the reader to delete the
//     stale entry. This is the same two-way discipline
//     TestGridClosureExclusionsAreLive applies to gridClosureExclusions:
//     the table cannot rot into a permanent, unchecked excuse list in
//     either direction.
func TestWrapperGridRuleVisibility(t *testing.T) {
	schema := ruleVisibilitySchema(t)
	cases := ruleVisibilityCases()

	runRuleVisibilityCases(t, schema, cases)
}

// ruleVisibilityCases builds the list of measured rules under test. It is
// its own function, apart from TestWrapperGridRuleVisibility, so that
// TestRuleVisibilityInvisibleRulesAreLive can read the same case names the
// test itself runs, without the two lists risking drift.
func ruleVisibilityCases() []ruleVisibilityCase {
	return []ruleVisibilityCase{
		{
			// simpleAggregateMarkerSurvives (supertype.go:568) is
			// the TRUE-AGGREGATE marker rule: a Nullable inner type
			// drops the marker. max(safn) types as Nullable(Int32)
			// today; flipping the rule to "survives" must make it keep
			// the marker instead.
			name:   "simpleAggregateMarkerSurvives",
			cellID: "fn/max/safn/i32",
			flip:   flipSimpleAggregateMarkerSurvives,
		},
		{
			// simpleAggregateMarkerSurvivesValuePreserving
			// (supertype.go:636) is the VALUE-PRESERVING SCALAR
			// rule: a Nullable inner type KEEPS the marker.
			// nullIf(saf, saf) types as
			// Nullable(SimpleAggregateFunction(anyLast, Int32)) today
			// (the marker survives, then nullIf's own wrapperAdd puts a
			// Nullable outside it); flipping the rule to "does not
			// survive" must drop the marker and give back a plain
			// Nullable(Int32).
			name:   "simpleAggregateMarkerSurvivesValuePreserving",
			cellID: "fn/nullif/saf/i32",
			flip:   flipSimpleAggregateMarkerSurvivesValuePreserving,
		},
		{
			// Transport disposition: the wrapperTransparent class
			// default for the SimpleAggregateFunction wrapper is
			// wrapperDrop (a value-computing scalar function drops the
			// marker). hex(saf_i32) types as String today; flipping the
			// class default to wrapperKeep must make it keep the
			// marker.
			name:   "wrapperTransparent class: simpleAggregate disposition (wrapperDrop)",
			cellID: "fn/hex/saf/i32",
			flip:   flipTransparentClassSimpleAggregateDrop,
		},
		{
			// Transport disposition: the wrapperAggregate class default
			// for the LowCardinality wrapper is wrapperDrop. max(lc_i32)
			// types as Int32 today; flipping the disposition to
			// wrapperKeep must make it keep LowCardinality.
			name:   "wrapperAggregate class: lowCardinality disposition (wrapperDrop)",
			cellID: "fn/max/lc/i32",
			flip:   flipAggregateClassLowCardinalityDrop,
		},
		{
			// Transport disposition: the wrapperAggregate class default
			// for the Nullable wrapper is wrapperKeep. max(n_i32) types
			// as Nullable(Int32) today; flipping the disposition to
			// wrapperDrop must strip the Nullable.
			name:   "wrapperAggregate class: nullable disposition (wrapperKeep)",
			cellID: "fn/max/n/i32",
			flip:   flipAggregateClassNullableKeep,
		},
		{
			// Transport disposition: stripNested. The aggregate
			// transport removes LowCardinality even INSIDE a container
			// member. argMax(bare_lcarr, bare_lcarr) types as
			// Array(String) today; flipping stripNested off must let
			// the nested LowCardinality survive as
			// Array(LowCardinality(String)).
			name:   "wrapperAggregate class: stripNested",
			cellID: "fn/argmax/bare/lcarr",
			flip:   flipAggregateClassStripNested,
		},
		{
			// Transport disposition: nullIfTransport.nullable is
			// wrapperAdd (nullIf always creates a Nullable, whether or
			// not an argument had one). nullIf(bare_i32, bare_i32) types
			// as Nullable(Int32) today; flipping wrapperAdd to
			// wrapperDrop must give back a plain Int32.
			name:   "nullIfTransport: nullable disposition (wrapperAdd)",
			cellID: "fn/nullif/bare/i32",
			flip:   flipNullIfNullableAdd,
		},
		{
			// Transport disposition: greatestLeastTransport's
			// lowCardinality condition keeps the wrapper at arity 1
			// only.
			//
			// PROVEN INVISIBLE through this seam; see
			// ruleVisibilityInvisibleRules for the reason and the regression
			// for the tracking ID.
			name:   "greatestLeastTransport: lowCardinality condition (arity 1 keeps)",
			cellID: "fn/greatest/lc/i32",
			flip:   flipGreatestLeastLowCardinalityCondition,
		},
		{
			// Transport disposition: caseFoldingTransport keeps
			// LowCardinality (lower/upper are on the "read first
			// argument, no other operand" path and keep the wrapper
			// unconditionally at the disposition level).
			// lower(lc_s) types as LowCardinality(String) today;
			// flipping the disposition to wrapperDrop must give back a
			// plain String.
			name:   "caseFoldingTransport: lowCardinality disposition (wrapperKeep)",
			cellID: "fn/lower/lc/s",
			flip:   flipCaseFoldingLowCardinalityKeep,
		},
		{
			// Transport disposition: logicOperatorTransport removes
			// LowCardinality unconditionally for AND/OR/XOR.
			//
			// This rule was invisible before the constant-operand change, and the cause was
			// the ENUMERATOR, never the rule. gridOperatorProbes
			// rendered "column OP column" with the SAME non-constant
			// column on both sides, and inferBinaryOperationType
			// precomputes lowCardinalityCount==1 && othersConstant
			// BEFORE it asks the transport, thus with two non-constant
			// LowCardinality operands the candidate was always false
			// and wrapperKeep could not be told from wrapperDrop.
			//
			// the regression added the constant-operand lane, which spells
			// the shape that separates the two dispositions. The
			// witness is op/AND/lc-const/i32, "c_lc_i32 AND 3": the
			// server answers UInt8, thus the LowCardinality wrapper is
			// dropped, and a flip to wrapperKeep moves the cell.
			//
			// The old address op/AND/lc/i32 is still in the golden and
			// is still blind. A case that watched it would stay green
			// whatever the disposition said, which is why this entry
			// moved out of ruleVisibilityInvisibleRules and to the new
			// address. XOR keeps its own case below, because its
			// blindness has a DIFFERENT cause.
			name:   "logicOperatorTransport (and/or): lowCardinality disposition (wrapperDrop)",
			cellID: "op/AND/lc-const/i32",
			flip:   flipLogicOperatorLowCardinalityDrop,
		},
		{
			// The xor half of the same disposition. xor has NO infix
			// token in this grammar ("1 xor 0" is Code 62), thus it is
			// reachable through the function form only, and the regression
			// gave it a registry entry, so fn/xor cells now exist.
			//
			// STILL INVISIBLE, for the SAME enumerator reason that kept
			// and/or invisible before the constant-operand change: every fn/xor cell is
			// spelled "xor(column, column)" with two non-constant
			// operands, and the LowCardinality candidate needs exactly
			// one LowCardinality operand with every other operand
			// constant. This was measured, not assumed: a first attempt
			// wired this case to fn/xor/lc/i32 and the harness reported
			// "answered the same before and after the flip: UInt8".
			//
			// It is an enumerator gap and not a language fact. The
			// server accepts the distinguishing shape: measured on
			// ClickHouse 25.8.29.51, xor(lci32, 3) is UInt8, the same
			// answer as xor(lci32, lci32), so a constant-operand lane
			// in the FUNCTION enumerator would separate wrapperKeep
			// from wrapperDrop here. The regression added that lane for the
			// operator form only.
			//
			// See ruleVisibilityInvisibleRules for the entry and the
			// tracking item.
			name:   "logicOperatorTransport (xor): lowCardinality disposition (wrapperDrop)",
			cellID: "fn/xor/lc/i32",
			flip:   flipLogicOperatorXorLowCardinalityDrop,
		},
		{
			// concatFunctionType (infer_function.go:545), branch: arity
			// one is always the string form regardless of argument
			// shape. concat(bare_arr) is not in the golden at arity one
			// over an Array by itself in this exact cell set, so the
			// arity branch is exercised through the registry rule
			// itself: flipping the >=2 array-join branch to also apply
			// at arity 1 changes concat(bare_arr) alone would need a
			// single-argument cell; instead this case flips the
			// array-join branch directly (see below), which is the
			// branch every fn/concat/*/arr cell depends on.
			name:   "concatFunctionType: array-join branch",
			cellID: "fn/concat/bare/arr",
			flip:   flipConcatArrayJoinBranch,
		},
		{
			// concatFunctionType, branch: everything that is not an
			// all-Array or all-Map or all-Tuple call is the string form.
			// concat(bare_i32, bare_i32) types as String today; flipping
			// the string-fallback branch to refuse instead must turn
			// this cell into a chgen refusal.
			name:   "concatFunctionType: string-fallback branch",
			cellID: "fn/concat/bare/i32",
			flip:   flipConcatStringFallbackBranch,
		},
		{
			// concatFunctionType, branch: a Tuple join is refused
			// because chgen cannot express the flattened result shape.
			// concat(bare_tup, bare_tup) is a chgen refusal today (the
			// golden verdict is CHGEN_REFUSES_SERVER_ACCEPTS); flipping
			// the refusal to instead return the naive supertype answer
			// must turn the cell from a refusal into a typed answer.
			name:   "concatFunctionType: Tuple-refuses branch",
			cellID: "fn/concat/bare/tup",
			flip:   flipConcatTupleRefusesBranch,
		},
	}
}

// runRuleVisibilityCases runs the flip-and-compare check for every case,
// consulting ruleVisibilityInvisibleRules to decide whether an invisible
// result is a documented finding or an unexpected one.
func runRuleVisibilityCases(t *testing.T, schema *Schema, cases []ruleVisibilityCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql := goldenCellSQL(t, tc.cellID)

			baseline, baselineOK := chgenTypeOfCell(schema, sql)

			restore := tc.flip(t)
			t.Cleanup(restore)

			flipped, flippedOK := chgenTypeOfCell(schema, sql)

			invisible := baselineOK == flippedOK && baseline == flipped
			entry, documented := ruleVisibilityInvisibleRules[tc.name]

			if invisible && !documented {
				t.Errorf("FINDING: rule %q is INVISIBLE to the committed golden.\n"+
					"cell %s (%s) answered the same before and after the flip: %s (refused=%v).\n"+
					"No committed cell distinguishes this rule's two answers, so the census does not measure it.\n"+
					"Add a ruleVisibilityInvisibleRules entry naming the reason and a tracking ID.",
					tc.name, tc.cellID, sql, baseline, !flippedOK)
				return
			}
			if !invisible && documented {
				t.Errorf("rule %q is in ruleVisibilityInvisibleRules (tracking item %s) but the flip changed "+
					"cell %s (%s) after all: baseline=%q(ok=%v) flipped=%q(ok=%v).\n"+
					"That is GOOD NEWS: delete the stale entry and record the cell as the visibility witness.",
					tc.name, entry.trackingID, tc.cellID, sql, baseline, baselineOK, flipped, flippedOK)
				return
			}
			if invisible {
				t.Logf("DOCUMENTED FINDING (tracking item %s): rule %q is invisible to the committed golden through this seam.\nReason: %s\ncell %s (%s) answered %s (refused=%v) both before and after the flip.",
					entry.trackingID, tc.name, entry.reason, tc.cellID, sql, baseline, !flippedOK)
				return
			}
			t.Logf("rule %q is VISIBLE through cell %s (%s): baseline=%q(ok=%v) flipped=%q(ok=%v)",
				tc.name, tc.cellID, sql, baseline, baselineOK, flipped, flippedOK)
		})
	}
}

// TestRuleVisibilityInvisibleRulesAreLive keeps ruleVisibilityInvisibleRules
// from silently growing stale, the same discipline
// TestGridClosureExclusionsAreLive applies to gridClosureExclusions: every
// key must correspond to a case in TestWrapperGridRuleVisibility, so an
// entry cannot survive after its case is renamed or removed and quietly
// stop being checked.
func TestRuleVisibilityInvisibleRulesAreLive(t *testing.T) {
	known := ruleVisibilityCaseNames()
	for name := range ruleVisibilityInvisibleRules {
		if !known[name] {
			t.Errorf("ruleVisibilityInvisibleRules names %q, "+
				"but no case in TestWrapperGridRuleVisibility carries that name; "+
				"the entry is stale and must be deleted or the case must be restored", name)
		}
	}
}

// ruleVisibilityCaseNames gives the set of case names that
// TestWrapperGridRuleVisibility runs today, read from the same case
// builder the test itself uses, so the two cannot drift apart.
func ruleVisibilityCaseNames() map[string]bool {
	names := map[string]bool{}
	for _, tc := range ruleVisibilityCases() {
		names[tc.name] = true
	}
	return names
}

// TestRuleVisibilityNoCellRulesAreLive keeps ruleVisibilityNoCellRules
// honest in BOTH directions, which is what its retired predecessor
// TestXorHasNoGoldenCellOfAnyForm did for the single XOR entry.
//
// An entry in that table claims "no cell exists for this rule in ANY
// form", which is a stronger claim than the one
// ruleVisibilityInvisibleRules makes. The claim decays the moment a
// registry entry or an enumerator lane gives the name a cell, and a
// decayed claim reads as coverage. That happened: the XOR entry was
// true only while xor had no functionRegistry entry, and the regression gave
// it one, so fn/xor cells appeared and the rule moved to a real
// flip-and-compare case with the witness fn/xor/lc/i32.
//
// The two directions:
//
//   - An entry whose name IS a case in TestWrapperGridRuleVisibility is
//     stale: the rule has a cell now, so it belongs in the harness and
//     not in this table.
//   - An entry that names a grid ENTRY which the golden now addresses is
//     stale for the same reason, and this test names the cell.
func TestRuleVisibilityNoCellRulesAreLive(t *testing.T) {
	known := ruleVisibilityCaseNames()
	for name := range ruleVisibilityNoCellRules {
		if known[name] {
			t.Errorf("ruleVisibilityNoCellRules names %q and "+
				"TestWrapperGridRuleVisibility carries a case with that name; "+
				"a rule with a real flip-and-compare case has a cell, thus the "+
				"no-cell entry is stale and must be deleted", name)
		}
	}
	// The golden addresses an entry as the SECOND path segment of a cell
	// id ("fn/xor/...", "op/AND/..."). Collect them once, then hold every
	// table entry's named grid entry against the set. Matching the
	// segment and not a substring keeps this from misfiring on an
	// unrelated name that merely CONTAINS the entry, such as groupBitXor
	// for xor.
	addressed := map[string]bool{}
	for _, line := range strings.Split(readGoldenFileForRuleVisibility(t), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, "\t", 6)
		if len(fields) != 6 {
			continue
		}
		parts := strings.SplitN(fields[0], "/", 3)
		if len(parts) < 2 {
			continue
		}
		addressed[strings.ToLower(parts[1])] = true
	}
	for name, entry := range ruleVisibilityNoCellRules {
		for _, gridEntry := range entry.gridEntries {
			if addressed[strings.ToLower(gridEntry)] {
				t.Errorf("ruleVisibilityNoCellRules names %q and claims no cell exists, "+
					"but the golden addresses the entry %q; delete the entry and give the "+
					"rule a real flip-and-compare case", name, gridEntry)
			}
		}
	}
}

// --- flips: simpleAggregateMarkerSurvives family ---

// flipSimpleAggregateMarkerSurvives inverts the true-aggregate marker
// rule by replacing the aggregate class's simpleAggregateWhen condition
// (which max reaches through functionClassFor("max") ==
// wrapperAggregate) with its negation. It mutates functionRegistry so
// that "max" carries a private transport whose condition is inverted,
// leaving every other wrapperAggregate function untouched.
func flipSimpleAggregateMarkerSurvives(t *testing.T) func() {
	t.Helper()
	return overrideTransport(t, "max", func(base wrapperTransport) wrapperTransport {
		base.simpleAggregateWhen = func(call wrapperCall) bool {
			return !aggregateKeepsSimpleAggregateCondition(call)
		}
		return base
	})
}

// flipSimpleAggregateMarkerSurvivesValuePreserving inverts the
// value-preserving-scalar marker rule as nullIf reaches it, by
// installing a negated condition on nullIfTransport through the
// wrapperTransportOverrides map.
func flipSimpleAggregateMarkerSurvivesValuePreserving(t *testing.T) func() {
	t.Helper()
	return overrideTransport(t, "nullif", func(base wrapperTransport) wrapperTransport {
		base.simpleAggregateWhen = func(call wrapperCall) bool {
			return !callKeepsValuePreservingScalarMarker(call)
		}
		return base
	})
}

// --- flips: transport dispositions ---

func flipTransparentClassSimpleAggregateDrop(t *testing.T) func() {
	t.Helper()
	return overrideRegistryClassTransport(t, "hex", wrapperTransparent, func(base wrapperTransport) wrapperTransport {
		base.simpleAggregate = wrapperKeep
		return base
	})
}

func flipAggregateClassLowCardinalityDrop(t *testing.T) func() {
	t.Helper()
	return overrideTransport(t, "max", func(base wrapperTransport) wrapperTransport {
		base.lowCardinality = wrapperKeep
		return base
	})
}

func flipAggregateClassNullableKeep(t *testing.T) func() {
	t.Helper()
	return overrideTransport(t, "max", func(base wrapperTransport) wrapperTransport {
		base.nullable = wrapperDrop
		return base
	})
}

func flipAggregateClassStripNested(t *testing.T) func() {
	t.Helper()
	return overrideTransport(t, "argmax", func(base wrapperTransport) wrapperTransport {
		base.stripNested = false
		return base
	})
}

func flipNullIfNullableAdd(t *testing.T) func() {
	t.Helper()
	return overrideTransport(t, "nullif", func(base wrapperTransport) wrapperTransport {
		base.nullable = wrapperDrop
		return base
	})
}

func flipGreatestLeastLowCardinalityCondition(t *testing.T) func() {
	t.Helper()
	return overrideTransport(t, "greatest", func(base wrapperTransport) wrapperTransport {
		base.lowCardinalityWhen = func(wrapperCall) bool { return false }
		return base
	})
}

func flipCaseFoldingLowCardinalityKeep(t *testing.T) func() {
	t.Helper()
	return overrideTransport(t, "lower", func(base wrapperTransport) wrapperTransport {
		base.lowCardinality = wrapperDrop
		return base
	})
}

func flipLogicOperatorLowCardinalityDrop(t *testing.T) func() {
	t.Helper()
	return overrideTransport(t, "and", func(base wrapperTransport) wrapperTransport {
		base.lowCardinality = wrapperKeep
		return base
	})
}

// flipLogicOperatorXorLowCardinalityDrop is the xor half of the same
// disposition. xor has no infix token, thus it is reachable through the
// function form only, and the function form needs its own flip because
// overrideTransport is keyed by NAME.
func flipLogicOperatorXorLowCardinalityDrop(t *testing.T) func() {
	t.Helper()
	return overrideTransport(t, "xor", func(base wrapperTransport) wrapperTransport {
		base.lowCardinality = wrapperKeep
		return base
	})
}

// --- flips: concatFunctionType branches ---

func flipConcatArrayJoinBranch(t *testing.T) func() {
	t.Helper()
	return overrideRule(t, "concat", func(args []CHType) (CHType, error) {
		if len(args) < 2 {
			return CHType{Name: "String"}, nil
		}
		if allCHTypesNamed(args, "Map") {
			return commonCHTypes(args)
		}
		if allCHTypesNamed(args, "Tuple") {
			return CHType{}, fmt.Errorf("concat over Tuple arguments joins the element lists; %s", pinTypeHint)
		}
		// The array-join branch is disabled: an all-Array call now
		// falls through to the string form, exactly like a mixed call.
		return CHType{Name: "String"}, nil
	})
}

func flipConcatStringFallbackBranch(t *testing.T) func() {
	t.Helper()
	return overrideRule(t, "concat", func(args []CHType) (CHType, error) {
		if len(args) < 2 {
			return CHType{Name: "String"}, nil
		}
		if allCHTypesNamed(args, "Array") || allCHTypesNamed(args, "Map") {
			return commonCHTypes(args)
		}
		if allCHTypesNamed(args, "Tuple") {
			return CHType{}, fmt.Errorf("concat over Tuple arguments joins the element lists; %s", pinTypeHint)
		}
		// The string-fallback branch now refuses instead of answering
		// String.
		return CHType{}, fmt.Errorf("rule flipped: string-fallback branch disabled")
	})
}

func flipConcatTupleRefusesBranch(t *testing.T) func() {
	t.Helper()
	return overrideRule(t, "concat", func(args []CHType) (CHType, error) {
		if len(args) < 2 {
			return CHType{Name: "String"}, nil
		}
		if allCHTypesNamed(args, "Array") || allCHTypesNamed(args, "Map") {
			return commonCHTypes(args)
		}
		if allCHTypesNamed(args, "Tuple") {
			// The refusal is disabled: answer the naive (and known
			// wrong, per the production comment) supertype instead of
			// refusing.
			return commonCHTypes(args)
		}
		return CHType{Name: "String"}, nil
	})
}

// --- the two map seams ---

// overrideTransport replaces the wrapperTransportOverrides entry for
// name with mutate(current), where current is the transport that
// transportForFunction(name, ...) gives today (either an existing
// override or the class default). It installs the entry even for a
// name that has no override today, and restores the map to its exact
// prior state (present-with-value, or absent) in the returned func.
//
// This is the ONE mutation point for every disposition and every
// marker-rule flip above: transportForFunction (wrapper_transport.go:616)
// checks this exact map before it falls back to the class default, so
// an override here is what every call site sees.
func overrideTransport(t *testing.T, name string, mutate func(wrapperTransport) wrapperTransport) func() {
	t.Helper()
	class := functionClassFor(name)
	current := transportForFunction(name, class)
	previous, had := wrapperTransportOverrides[name]
	wrapperTransportOverrides[name] = mutate(current)
	return func() {
		if had {
			wrapperTransportOverrides[name] = previous
		} else {
			delete(wrapperTransportOverrides, name)
		}
	}
}

// overrideRegistryClassTransport is overrideTransport for a function
// whose CLASS default is the thing under test, expressed by starting
// mutate from transportForClass(class) rather than from any override
// (there is none for hex today).
func overrideRegistryClassTransport(t *testing.T, name string, class functionWrapperClass, mutate func(wrapperTransport) wrapperTransport) func() {
	t.Helper()
	previous, had := wrapperTransportOverrides[name]
	wrapperTransportOverrides[name] = mutate(transportForClass(class))
	return func() {
		if had {
			wrapperTransportOverrides[name] = previous
		} else {
			delete(wrapperTransportOverrides, name)
		}
	}
}

// overrideRule replaces the type rule of one functionRegistry entry for
// the lifetime of a subtest, leaving every other field of the spec
// (class, strategy, domain, domainArgs, gen) exactly as it was. It
// restores the whole spec afterward.
func overrideRule(t *testing.T, name string, rule functionTypeRule) func() {
	t.Helper()
	previous, had := functionRegistry[name]
	if !had {
		t.Fatalf("overrideRule: %q is not in functionRegistry", name)
	}
	replaced := previous
	replaced.rule = rule
	functionRegistry[name] = replaced
	return func() {
		functionRegistry[name] = previous
	}
}
