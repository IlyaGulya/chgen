package engine

// This file enumerates the CELLS of the combinator grid: the suffix-by-base
// product of inferAggregateCombinatorType, plus the negative dispatch cells
// that the regression adds.
//
// It has NO build tag, so the default `go test ./...` run can check the
// shape of the enumeration and the no-shrink gate without a server. Only
// the MEASUREMENT needs a server, and that part lives behind the
// fuzzoracle tag in aggregate_combinator_grid_test.go.
//
// This grid promotes the SAMPLED combinator lane of the tagged fuzz oracle
// (typeoracle_fuzz_test.go: combinatorDraw, renderCombinatorForm,
// combinatorBaseSpellings, combinatorArgumentColumns) to a committed,
// byte-reproducible measured golden, in the same mechanism as the wrapper
// grid (wrappergrid_test.go, testdata/wrapper_grid.golden). The two grids
// are independent: this one owns its own fixture (table `cg`), its own
// probe/cell/record types (prefixed combGrid) and its own golden file
// (testdata/combinator_grid.golden), so it does not touch any file that
// another change owns.

import (
	"sort"
	"strings"
)

// combGridGoldenPath is the committed golden file.
var combGridGoldenPath = moduleRootPath("testdata", "combinator_grid.golden")

// combGridProbe is one measured cell.
type combGridProbe struct {
	// id is the stable address of the cell, for example
	// "product/sum/if" or "negative/merge-mismatch/sum-over-quantile".
	id string
	// family names the cell's lane: "product" for the suffix-by-base
	// product, "negative" for a negative dispatch cell that this ticket
	// adds.
	family string
	// sql is the measured expression.
	sql string
}

// combGridBases is the closed list of base aggregates that this grid
// composes with every combinator suffix. It is exactly
// combinatorBaseSpellings() from the tagged fuzz oracle: the same
// measured lane, made exhaustive and committed. Held against that list by
// TestCombGridBasesMatchTheFuzzLane, so the two cannot silently diverge.
func combGridBases() []string {
	return append([]string(nil), combinatorBaseSpellings()...)
}

// combGridSuffixes is the closed list of combinator suffixes, spelled as
// the server accepts them. It is exactly combinatorSuffixSpellings() of
// the tagged fuzz oracle, held by TestCombGridSuffixesMatchTheFuzzLane.
func combGridSuffixes() []string {
	return []string{
		"SimpleState", "State", "StateIf", "Merge", "OrNull",
		"OrDefault", "Resample", "Array", "If",
		"MergeState", "IfMerge", "IfMergeState", "IfState",
		"ArrayIf", "ResampleIf", "IfOrNull", "IfOrDefault", "StateOrNull",
	}
}

// combGridProductProbes enumerates the suffix-by-base product: every
// (base, suffix) pair of combGridBases x combGridSuffixes.
//
// The ARGUMENT that a cell reads follows the same measured rule as
// renderCombinatorForm in gen_draw_test.go:
//
//   - -Merge reads a state COLUMN. sumMerge(sumState(i32)) is Code 184,
//     ILLEGAL_AGGREGATION, because a state cannot be built in the SELECT
//     that merges it (measured on 25.8.29.51, see aggregate_combinator.go
//     and gen_draw_test.go). The fixture therefore holds a state column
//     for each measured base name, not one shared column. Each -Merge
//     cell reads the state that its base builds. The median fixture uses
//     the canonical quantile state identity that the server records.
//   - -Array reads an Array whose element feeds the base aggregate.
//   - -Resample takes its bounds as parameters and a resampling key as a
//     trailing data argument.
//   - -If and -StateIf append a trailing condition argument.
//   - every other suffix reads a plain value argument.
//
// A cell whose argument this grid cannot spell is skipped, not spelled as
// a refusal by accident. The current fixture spells every product cell.
// The empty skip gate prevents a later fixture regression from shrinking
// the measured product without a named reason.
func combGridProductProbes() []combGridProbe {
	var probes []combGridProbe
	for _, base := range combGridBases() {
		for _, suffix := range combGridSuffixes() {
			sql, ok := combGridRenderProduct(base, suffix)
			if !ok {
				continue
			}
			probes = append(probes, combGridProbe{
				id:     "product/" + base + "/" + suffix,
				family: "product",
				sql:    sql,
			})
		}
	}
	return probes
}

// combGridMergeStateColumn names the fixture column that holds a state
// built by the given base aggregate name. Every measured base has one.
func combGridMergeStateColumn(base string) (string, bool) {
	fixture, ok := combGridBaseFixtureFor(base)
	if !ok {
		return "", false
	}
	return "c_state_" + fixture.key, true
}

// combGridIfMergeStateColumn gives the state column that a chained -If
// merge form reads for one base, or ok=false when the fixture holds no
// such state.
//
// The chained forms -IfMerge and -IfMergeState need a state that an -If
// form BUILT, which is a different column from the bare state that
// -Merge reads. Measured on ClickHouse 25.8.29.51: sumIfMerge over the
// bare sum state is Code 43, and sumIfMerge over the sumIf state is
// Int64. The fixture stores a separate -If state for every measured base.
func combGridIfMergeStateColumn(base string) (string, bool) {
	fixture, ok := combGridBaseFixtureFor(base)
	if !ok {
		return "", false
	}
	return "c_state_" + fixture.key + "_if", true
}

// combGridRenderProduct spells one cell of the suffix-by-base product, or
// reports ok=false when the grid cannot spell that cell's argument.
func combGridRenderProduct(base, suffix string) (string, bool) {
	fixture, ok := combGridBaseFixtureFor(base)
	if !ok {
		return "", false
	}
	switch suffix {
	case "Merge", "MergeState":
		// -MergeState reads the same state COLUMN as -Merge and returns
		// a state instead of a value, thus it takes the same argument
		// rule. Measured on 25.8.29.51: sumMergeState(c_state_sum) is
		// AggregateFunction(sum, Int32).
		column, ok := combGridMergeStateColumn(base)
		if !ok {
			return "", false
		}
		return combGridRenderCall(fixture, suffix, []string{column}), true
	case "IfMerge", "IfMergeState":
		// The chained -If forms read a state that an -If form BUILT. A
		// state built by the bare form does not satisfy them: measured
		// on 25.8.29.51, sumIfMerge over a bare sum state is Code 43.
		// The fixture stores one such state for every measured base.
		column, ok := combGridIfMergeStateColumn(base)
		if !ok {
			return "", false
		}
		return combGridRenderCall(fixture, suffix, []string{column}), true
	case "Array":
		return combGridRenderCall(fixture, suffix, fixture.arrayArgs), true
	case "ArrayIf":
		// -ArrayIf is the CHAIN Array + If: the base aggregate reads the
		// element type of the array argument, then the trailing
		// condition applies. Measured on 25.8.29.51:
		// sumArrayIf(c_arr_i32, c_bool) is Int64. See the regression.
		return combGridRenderCall(fixture, suffix, appendCopy(fixture.arrayArgs, "c_bool")), true
	case "Resample":
		return combGridRenderCall(fixture, suffix, appendCopy(fixture.dataArgs, "c_u8")), true
	case "ResampleIf":
		// -ResampleIf is the CHAIN Resample + If: the resample key
		// comes after the data argument, then the trailing condition.
		// Measured on 25.8.29.51: sumResampleIf(0, 10, 1)(c_i32, c_u8,
		// c_bool) is Array(Int64). See the regression.
		return combGridRenderCall(fixture, suffix, appendCopy(fixture.dataArgs, "c_u8", "c_bool")), true
	case "If", "StateIf", "IfState", "IfOrNull", "IfOrDefault":
		// The five share ONE spelling because the CALL shape is the
		// same: a data argument and a trailing condition. Only the
		// resulting TYPE differs, and the type is what the grid
		// measures. -IfState keeps the condition inside the state and
		// tags it "<base>If"; -StateIf drops it. -IfOrNull and
		// -IfOrDefault are new for the regression: measured on 25.8.29.51,
		// sumIfOrNull(c_i32, c_bool) is Nullable(Int64) and
		// sumIfOrDefault(c_i32, c_bool) is Int64.
		return combGridRenderCall(fixture, suffix, appendCopy(fixture.dataArgs, "c_bool")), true
	case "StateOrNull":
		// -StateOrNull builds a state tagged "<base>OrNull", with no
		// trailing condition; it is the CHAIN State + OrNull. Measured
		// on 25.8.29.51 from the DECLARED column type of a materialized
		// AggregatingMergeTree table: sumStateOrNull(c_i32) is
		// AggregateFunction(sumOrNull, Int32). See the regression.
		return combGridRenderCall(fixture, suffix, fixture.dataArgs), true
	default:
		return combGridRenderCall(fixture, suffix, fixture.dataArgs), true
	}
}

func appendCopy(values []string, extra ...string) []string {
	result := append([]string(nil), values...)
	return append(result, extra...)
}

func combGridRenderCall(fixture combGridBaseFixture, suffix string, arguments []string) string {
	parameters := []string{}
	if fixture.parameter != "" {
		parameters = append(parameters, fixture.parameter)
	}
	if suffix == "Resample" || suffix == "ResampleIf" {
		parameters = append(parameters, "0", "10", "1")
	}
	name := fixture.base + suffix
	call := name + "(" + strings.Join(arguments, ", ") + ")"
	if len(parameters) > 0 {
		call = name + "(" + strings.Join(parameters, ", ") + ")(" + strings.Join(arguments, ", ") + ")"
	}
	return call
}

// combGridSkippedProductCells names every (base, suffix) pair of the
// closed product that combGridRenderProduct cannot spell, together with
// the reason. This is the explicit record of the SILENT CAP the ticket
// forbids leaving unstated. The current fixture has no skipped cell.
//
// TestCombGridProductAccountsForEveryCell holds this list against the
// live product, so a pair that starts being spellable (state column
// added) must be removed here or the test fails, and a newly-unspellable
// pair cannot vanish without a reason appearing here.
func combGridSkippedProductCells() map[string]string {
	return map[string]string{}
}

// combGridNegativeProbes enumerates the negative dispatch cells that
// the regression adds. Each one is a MEASURED refusal or a MEASURED acceptance
// that the dispatch of -Merge or -OrNull must get right, closing the regression
// and the regression.
func combGridNegativeProbes() []combGridProbe {
	return []combGridProbe{
		// the regression: -Merge must check the aggregate NAME of the state,
		// not only the data argument types. sumMerge over a state built
		// by quantile(0.5) is a NAME mismatch. Measured on 25.8.29.51:
		// Code 43, "Illegal type Date of argument for aggregate function
		// sum" -- the server tries to run sum's merge over the state's
		// stored argument type and fails, because the two aggregates are
		// unrelated. A dispatch that only checked the data argument type
		// of the state (Date) against sum's domain would ALSO refuse
		// this cell, so the cell alone cannot separate "name is checked"
		// from "domain is checked"; see the ALIAS cell below for that.
		{
			id:     "negative/merge-mismatch/sum-over-quantile",
			family: "negative",
			sql:    "sumMerge(c_state_quantile)",
		},
		// The mirror direction: a state built by sum, merged by uniq.
		// This makes the mismatch bidirectional evidence and not a
		// property of one particular pair of names.
		{
			id:     "negative/merge-mismatch/uniq-over-sum",
			family: "negative",
			sql:    "uniqMerge(c_state_sum)",
		},
		// The ALIAS-matched -Merge name. median is an alias of quantile:
		// baseAggregateDisplayName("median") gives "quantile", and the
		// server's own state name for quantileState(0.5) is
		// AggregateFunction(quantile(0.5), Int32). medianMerge over that
		// state is therefore LEGAL, and the server answers Float64 (the
		// ordinary quantile/median result type), not a refusal.
		//
		// This is the cell a PURE STRING comparison of "median" against
		// "quantile" would wrongly refuse: the state's stored name is
		// "quantile", never "median", because ClickHouse canonicalizes
		// the alias when it builds the state. A dispatch that compared
		// literal name text instead of the canonical display name would
		// refuse this cell where the server accepts it -- exactly the
		// class of defect the regression exists to close.
		{
			id:     "negative/merge-alias/median-over-quantile",
			family: "negative",
			sql:    "medianMerge(0.5)(c_state_quantile)",
		},
		// the regression: -OrNull must refuse a result that cannot be inside
		// a Nullable. groupUniqArray's result is Array(T), and an Array
		// cannot sit inside Nullable. Measured on 25.8.29.51: Code 43,
		// "Nested type Array(Int32) cannot be inside Nullable type".
		{
			id:     "negative/ornull-container/groupuniqarray",
			family: "negative",
			sql:    "groupUniqArrayOrNull(c_i32)",
		},
		// A second container-result -OrNull cell, over groupArray, so
		// the container refusal is evidence about the RESULT SHAPE and
		// not a property of one single aggregate name.
		{
			id:     "negative/ornull-container/grouparray",
			family: "negative",
			sql:    "groupArrayOrNull(c_i32)",
		},
		// The scalar control: -OrNull over a scalar-result aggregate
		// must still ACCEPT. Without this cell a blanket refusal of
		// every -OrNull call would also pass the two cells above.
		{
			id:     "negative/ornull-scalar-control/sum",
			family: "negative",
			sql:    "sumOrNull(c_i32)",
		},
		// The combinator ORDER cell (the regression / the note in the regression):
		// quantileStateIf drops the condition from the state's argument
		// list and gives AggregateFunction(quantile(0.5), Float64).
		// quantileIfState keeps it and gives
		// AggregateFunction(quantileIf(0.5), Float64, Bool). Both orders
		// are spelled here so the grid holds the measured fact that
		// combinator ORDER changes the type, not only the base and the
		// suffix set.
		{
			id:     "negative/combinator-order/quantile-stateif",
			family: "negative",
			sql:    "quantileStateIf(0.5)(c_f64, c_bool)",
		},
		{
			id:     "negative/combinator-order/quantile-ifstate",
			family: "negative",
			sql:    "quantileIfState(0.5)(c_f64, c_bool)",
		},
	}
}

// combGridSkippedCell is one sorted, named entry of the skip list. The
// golden header writes this SORTED form, never the raw map, so the
// header is byte-reproducible: Go randomizes map iteration order on
// every process start, and the golden was once observed to reorder its
// "# skipped" lines between two regenerations of the same commit for
// exactly that reason.
type combGridSkippedCell struct {
	name   string
	reason string
}

// combGridSortedSkippedCells gives combGridSkippedProductCells as a
// slice sorted by name, for a reproducible golden header.
func combGridSortedSkippedCells() []combGridSkippedCell {
	reasons := combGridSkippedProductCells()
	names := make([]string, 0, len(reasons))
	for name := range reasons {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]combGridSkippedCell, 0, len(names))
	for _, name := range names {
		out = append(out, combGridSkippedCell{name: name, reason: reasons[name]})
	}
	return out
}

// combGridBuildProbes enumerates every cell of the combinator grid, sorted
// by id so the enumeration is deterministic.
func combGridBuildProbes() []combGridProbe {
	var probes []combGridProbe
	probes = append(probes, combGridProductProbes()...)
	probes = append(probes, combGridNegativeProbes()...)
	sort.Slice(probes, func(i, j int) bool { return probes[i].id < probes[j].id })
	return probes
}
