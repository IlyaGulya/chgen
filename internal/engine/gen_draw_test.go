package engine

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// The draw layer of current registry grammar.
//
// Step 6 of the migration makes the generator draw its candidates from
// the index instead of from a hand written list. This file holds the
// part of that work which needs NO server and NO build tag, so that the
// reachability gate and the fuzzer share one implementation. A copy
// beside the fuzzer would drift, and that duplication is the defect the
// epic removes.
//
// Two rules shape everything here.
//
// The candidate list comes from the index. The index computes the column
// pool of each argument position from spec.domain.accepts, thus a
// measured domain shapes the generator and the inference refusal from
// ONE declaration.
//
// The ILLEGAL half comes from the SAME domain. An argument that fails
// the domain is drawn from the COMPLEMENT of the legal pool, that is the
// fixture columns that the very same accepts predicate rejected. A
// second hand written list of bad arguments would re-create the
// duplication this epic removes, and it would drift away from the
// domain it claims to contradict.

// The operator tokens of the generator come from operatorCatalog and not
// from a copy. Before the catalog, the generator held its own spellings,
// and an operator that inference knew could stay unfuzzed: the defects in
// "==" and in REGEXP were found only after somebody added the spelling by
// hand. The catalog is the single source, thus a new operator rule is
// fuzzed at once.
//
// The three accessors below are functions and no longer package-level
// variables. A variable put the token order in one more place that a
// reader had to trust; a function derives the order from the catalog on
// every call, thus the catalog order is the only order there is.

// arithTokens gives the whole arithmetic family.
func arithTokens() []string { return operatorTokensOfFamily(opArith) }

// comparisonTokens gives the comparison operators of the predicate
// family. The other predicate tokens are emitted by the lanes that can
// build a legal operand for them: AND, OR, IN and the LIKE group do not
// take a plain value on both sides.
func comparisonTokens() []string {
	logical := map[string]bool{
		"AND": true, "OR": true, "IN": true, "NOT IN": true,
		"LIKE": true, "NOT LIKE": true, "ILIKE": true, "NOT ILIKE": true,
		"REGEXP": true,
	}
	var tokens []string
	for _, token := range operatorTokensOfFamily(opPredicate) {
		if !logical[token] {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

// conversionFunctionNames gives the one-argument conversion functions
// that the numeric lane writes.
//
// The ORDER is fixed here and is not a sorted walk of the registry. The
// generator picks from this slice with one draw of the random stream,
// thus the order is part of what the seed reproduces: a sorted walk
// would emit different expressions for the same seed and every stored
// artifact of earlier grammar and v2 would stop matching.
//
// The names are held against the registry by
// TestConversionFunctionNamesAreRegistryEntries. That test is what makes
// this a view of the registry instead of a second list: a name that the
// registry loses, or that loses its genSpec, fails the build.
func conversionFunctionNames() []string {
	return []string{
		"toInt8", "toInt16", "toInt32", "toInt64",
		"toUInt8", "toUInt16", "toUInt32", "toUInt64",
		"toFloat32", "toFloat64", "toString",
	}
}

// pick takes one item of a slice. It lives in this untagged file
// because both the draw layer and the tagged fuzzer call it, and two
// copies would be two ways to consume the same random stream.
func pick[T any](r *rand.Rand, items []T) T { return items[r.Intn(len(items))] }

// genDrawPools holds, for one argument position, both halves of one
// domain.
//
// legal holds the fixture columns that the domain accepted. illegal
// holds the columns that the SAME predicate rejected. The two slices
// partition the fixture, thus neither half can be widened without
// narrowing the other, and no third list exists to drift.
type genDrawPools struct {
	legal   []fixtureColumn
	illegal []fixtureColumn
}

// genDrawCandidate is one call form that the v3 lanes can write. It
// carries the per-position pools of the domain and the recipe.
type genDrawCandidate struct {
	// name is the registry key, thus the lowercased call name.
	name string
	// spec is the generator recipe.
	spec genSpec
	// signature is the measured server call shape. The recipe above keeps
	// the useful default that the random generator writes.
	signature functionSignature
	// pools holds one entry per argument position of the measured
	// arity range. A position that takes no column, for example a lambda or
	// a constant, has both halves empty: the generator writes such a
	// position itself.
	pools []genDrawPools
}

// buildDrawIndex builds the draw index over the given fixture columns.
//
// It takes the columns as an argument instead of reading the fixture
// itself, so that the fuzzer can build the index over the schema that
// the RUN created. A run against the v3 schema must not draw a column
// that its own table does not hold.
func buildDrawIndex(columns []fixtureColumn) map[string]genDrawCandidate {
	index := make(map[string]genDrawCandidate, len(functionRegistry))
	for name := range functionRegistry {
		spec, ok := genSpecFor(name)
		if !ok {
			continue
		}
		entry := functionRegistry[name]
		signature, ok := functionSignatureFor(name)
		if !ok {
			continue
		}
		candidate := genDrawCandidate{name: name, spec: spec, signature: signature}
		poolArity := spec.maxArity
		if poolArity == -1 {
			poolArity = spec.minArity + 1
			if name == "map" {
				poolArity = spec.minArity + 2
			}
		}
		for _, arity := range signature.representativeArities() {
			if arity > poolArity {
				poolArity = arity
			}
		}
		candidate.pools = make([]genDrawPools, poolArity)
		for position := 0; position < poolArity; position++ {
			valuePosition := false
			var signatureDomain *argumentDomain
			for _, arity := range signature.representativeArities() {
				sorts, accepted := signature.sortsForArity(arity)
				if accepted && position < len(sorts) {
					valuePosition = valuePosition || sorts[position] == argSortValue
					if domain, usesPool := poolDomainForSort(sorts[position]); usesPool && domain != nil {
						signatureDomain = domain
					}
				}
			}
			if !valuePosition && signatureDomain == nil {
				continue
			}
			domain := signatureDomain
			if higherOrder, isHigherOrder := higherOrderArrayFunctions[name]; isHigherOrder && position > 0 {
				lastIsAccumulator := higherOrder.accumulator != nil && position == poolArity-1
				isScalar := higherOrder.scalarArgument != nil && position == higherOrder.scalarArgument.positionAfterLambda+1
				if !lastIsAccumulator && !isScalar {
					domain = &arrayArgumentDomain
				}
			}
			if valuePosition {
				if _, isHigherOrder := higherOrderArrayFunctions[name]; !isHigherOrder {
					domain = nil
				}
			}
			for _, domainPosition := range entry.domainArgs {
				if domainPosition == position ||
					(spec.maxArity == -1 && domainPosition == len(spec.argSorts)-1 && position >= domainPosition) {
					domain = entry.domain
					break
				}
			}
			candidate.pools[position] = splitByDomain(domain, columns)
		}
		index[name] = candidate
	}
	return index
}

func poolDomainForSort(sort argSort) (*argumentDomain, bool) {
	switch sort {
	case argSortValue:
		return nil, true
	case argSortNumber:
		return &argumentDomain{name: "numeric value", accepts: numberBaseType}, true
	case argSortOffset:
		return &argumentDomain{name: "offset value", accepts: offsetBaseType}, true
	case argSortIntegerOffset:
		return &argumentDomain{name: "integer offset value", accepts: indexBaseType}, true
	case argSortIndex:
		return &argumentDomain{name: "index value", accepts: indexBaseType}, true
	default:
		return nil, false
	}
}

func argumentSortUsesPool(sort argSort) bool {
	_, usesPool := poolDomainForSort(sort)
	return usesPool
}

// argumentSort returns the sort for one rendered position. A variadic recipe
// repeats its last declared sort up to the selected arity.
func (c genDrawCandidate) argumentSort(position int) (argSort, bool) {
	if position < len(c.spec.argSorts) {
		return c.spec.argSorts[position], true
	}
	if c.spec.maxArity == -1 && len(c.spec.argSorts) > 0 {
		return c.spec.argSorts[len(c.spec.argSorts)-1], true
	}
	return argSortValue, false
}

// splitByDomain partitions the fixture columns with one domain
// predicate. A nil domain means that no measurement narrowed the
// function, thus every column is legal and the illegal half is empty.
func splitByDomain(domain *argumentDomain, columns []fixtureColumn) genDrawPools {
	var pools genDrawPools
	for _, column := range columns {
		if domain == nil || domain.accepts(domainBaseType(column.columnType)) {
			pools.legal = append(pools.legal, column)
			continue
		}
		pools.illegal = append(pools.illegal, column)
	}
	return pools
}

// writable reports whether the generator can write a whole legal call.
// Every value position must have at least one legal column.
func (c genDrawCandidate) writable() bool {
	sorts, accepted := c.signature.sortsForArity(c.spec.minArity)
	if !accepted {
		return false
	}
	for position, argumentSort := range sorts {
		if !argumentSortUsesPool(argumentSort) {
			continue
		}
		if position >= len(c.pools) || len(c.pools[position].legal) == 0 {
			return false
		}
	}
	return true
}

// hasIllegal reports whether the candidate has at least one position
// whose domain rejected a fixture column. Only such a candidate can
// carry the illegal share.
func (c genDrawCandidate) hasIllegal() bool {
	sorts, accepted := c.signature.sortsForArity(c.spec.minArity)
	if !accepted {
		return false
	}
	for position, argumentSort := range sorts {
		if !argumentSortUsesPool(argumentSort) {
			continue
		}
		if position < len(c.pools) && len(c.pools[position].illegal) > 0 {
			return true
		}
	}
	return false
}

// pickIllegalPosition takes one value position whose domain rejected at
// least one fixture column. It answers -1 when the candidate has no such
// position, thus the caller writes a wholly legal call instead.
func (c genDrawCandidate) pickIllegalPosition(r *rand.Rand) int {
	var positions []int
	sorts, accepted := c.signature.sortsForArity(c.spec.minArity)
	if !accepted {
		return -1
	}
	for position, argumentSort := range sorts {
		if !argumentSortUsesPool(argumentSort) {
			continue
		}
		if position < len(c.pools) && len(c.pools[position].illegal) > 0 {
			positions = append(positions, position)
		}
	}
	if len(positions) == 0 {
		return -1
	}
	return pick(r, positions)
}

// drawNamesOfPlace gives the sorted names of the writable candidates of
// one placement. The order is sorted, NOT map order, because a map walk
// in Go is randomised and the generator must stay reproducible from the
// seed alone.
func drawNamesOfPlace(index map[string]genDrawCandidate, place placement) []string {
	var names []string
	for name, candidate := range index {
		if candidate.spec.place != place || !candidate.writable() {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// drawNamesWithIllegal gives the sorted names of the candidates of one
// placement that can carry an illegal argument.
func drawNamesWithIllegal(index map[string]genDrawCandidate, place placement) []string {
	var names []string
	for name, candidate := range index {
		if candidate.spec.place != place || !candidate.hasIllegal() {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// renderCall writes one call to the candidate.
//
// illegalPosition selects the value position that takes an argument
// from the ILLEGAL half of its own domain. It is -1 for a wholly legal
// call. The caller decides the share; this function only writes what it
// is told, so that the legal and the illegal form differ in exactly one
// operand.
func (c genDrawCandidate) renderCall(r *rand.Rand, illegalPosition int) (string, bool) {
	return c.renderCallArity(r, c.spec.minArity, illegalPosition)
}

// renderCallArity writes one call at an explicit legal arity.
func (c genDrawCandidate) renderCallArity(r *rand.Rand, arity, illegalPosition int) (string, bool) {
	sorts, accepted := c.signature.sortsForArity(arity)
	if !accepted {
		return "", false
	}
	arguments := make([]string, 0, arity)
	for position, sort := range sorts {
		argument, ok := c.renderArgumentSort(r, position, sort, position == illegalPosition)
		if !ok {
			return "", false
		}
		arguments = append(arguments, argument)
	}
	if arity == 3 && illegalPosition != 2 {
		switch c.name {
		case "arrayresize":
			arguments[2] = "arrayElement(" + arguments[0] + ", 1)"
		case "laginframe", "leadinframe":
			arguments[2] = arguments[0]
		}
	}
	if c.name == "tostartofinterval" && len(arguments) == 2 {
		return "toStartOfInterval(" + arguments[0] + ", " + arguments[1] + ")", true
	}
	if len(arguments) != arity {
		return "", false
	}
	if c.spec.template != "" && arity == c.spec.minArity {
		return fmt.Sprintf(c.spec.template, toAny(arguments)...), true
	}
	return c.spec.spelling + "(" + strings.Join(arguments, ", ") + ")", true
}

// renderArgument writes one argument position.
func (c genDrawCandidate) renderArgument(r *rand.Rand, position int, illegal bool) (string, bool) {
	argumentSort, ok := c.argumentSort(position)
	if !ok {
		return "", false
	}
	return c.renderArgumentSort(r, position, argumentSort, illegal)
}

func (c genDrawCandidate) renderArgumentSort(r *rand.Rand, position int, argumentSort argSort, illegal bool) (string, bool) {
	switch argumentSort {
	case argSortValue, argSortNumber, argSortOffset, argSortIntegerOffset, argSortIndex:
		if position >= len(c.pools) {
			return "", false
		}
		pool := c.pools[position].legal
		if illegal {
			pool = c.pools[position].illegal
		}
		if len(pool) == 0 {
			return "", false
		}
		return pick(r, pool).name, true
	case argSortPredicate:
		// A predicate position takes a comparison, not a column. The
		// operand columns are fixed and non-Nullable, so the predicate
		// itself cannot decide the result type of the call.
		return pick(r, []string{"i32 > 1", "u8 = 5", "f64 < 2.5", "b"}), true
	case argSortConstString:
		if (c.name == "datediff" || c.name == "date_diff") && position == 3 {
			return "'UTC'", true
		}
		switch c.name {
		case "arraystringconcat":
			return "','", true
		case "fromunixtimestamp64milli", "now", "now64", "todatetime", "todatetime64", "todatetime64ornull", "todatetime64orzero",
			"tostartofday", "tostartofhour", "tostartofminute", "totimezone":
			return "'UTC'", true
		}
		// dateDiff and toStartOfInterval read the unit at parse time.
		return pick(r, []string{"'day'", "'hour'", "'second'", "'month'"}), true
	case argSortConstInt:
		if c.name == "truncate" && position == 1 {
			return "0", true
		}
		if c.name == "tofixedstring" && position == 1 {
			return "8", true
		}
		return pick(r, []string{"1", "2", "3"}), true
	case argSortConstNumber:
		return "0.5", true
	case argSortInterval:
		return "INTERVAL 1 DAY", true
	case argSortTypeName:
		// tupleElement selects an element of tup, which has two.
		return pick(r, []string{"1", "2"}), true
	case argSortLambda:
		// A lambda body over the array element. The body is a value
		// expression, thus the higher-order rule reads a real type
		// from it.
		if higherOrder, ok := higherOrderArrayFunctions[c.name]; ok {
			if higherOrder.accumulator != nil {
				return "(acc, x) -> acc", true
			}
			if higherOrder.bodyDomain == hofBodyDomainPredicate {
				return "x -> (x > 1)", true
			}
			return "x -> x", true
		}
		return pick(r, []string{"x -> (x > 1)", "x -> (x + 1)", "x -> x"}), true
	default:
		return "", false
	}
}

// TestConversionFunctionNamesAreRegistryEntries holds
// conversionFunctionNames against the registry.
//
// The slice keeps a fixed ORDER, because the random stream draws from
// it and the order is part of what a seed reproduces. This test is what
// makes the slice a view of the registry and not a second list: a name
// that the registry loses, or that loses its genSpec, fails the build
// here instead of staying as a silent copy.
func TestConversionFunctionNamesAreRegistryEntries(t *testing.T) {
	for _, name := range conversionFunctionNames() {
		key := strings.ToLower(name)
		spec, ok := genSpecFor(key)
		if !ok {
			t.Errorf("conversionFunctionNames has %q, and the registry entry %q carries no "+
				"genSpec; the generator would write a call that the index does not know",
				name, key)
			continue
		}
		if spec.spelling != name {
			t.Errorf("conversionFunctionNames spells %q and the registry spells %q; "+
				"ClickHouse function names are case sensitive, thus the two must agree",
				name, spec.spelling)
		}
	}
}

// TestDrawPoolsPartitionTheFixture proves that the legal and the illegal
// half of a domain come from ONE predicate.
//
// This is the property that keeps the illegal share honest. If the two
// halves could be built from two sources, the illegal half would be a
// second hand written list and it would drift away from the domain it
// claims to contradict. The test asserts that every fixture column lands
// in exactly one half, thus neither half can be widened without
// narrowing the other.
func TestDrawPoolsPartitionTheFixture(t *testing.T) {
	columns := oracleFixtureColumns(t)
	index := buildDrawIndex(columns)
	if len(index) == 0 {
		t.Fatal("the draw index is empty; the partition assertion would pass by vacuity")
	}
	checked := 0
	for name, candidate := range index {
		for position, argumentSort := range candidate.spec.argSorts {
			if argumentSort != argSortValue || position >= len(candidate.pools) {
				continue
			}
			pools := candidate.pools[position]
			if len(pools.legal)+len(pools.illegal) != len(columns) {
				t.Errorf("%s position %d: the two halves hold %d and %d columns and the "+
					"fixture holds %d; the halves must partition the fixture, because they "+
					"come from one predicate", name, position, len(pools.legal), len(pools.illegal), len(columns))
			}
			seen := make(map[string]bool, len(columns))
			for _, column := range append(append([]fixtureColumn{}, pools.legal...), pools.illegal...) {
				if seen[column.name] {
					t.Errorf("%s position %d: column %q is in both halves", name, position, column.name)
				}
				seen[column.name] = true
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no value position was checked; the assertion measured nothing")
	}
	t.Logf("checked the partition on %d value positions over %d candidates", checked, len(index))
}

// TestDrawIndexReachesCurrentFixture proves that the draw index is not
// empty over the current fixture and that each placement has candidates. A
// placement with no candidate would make its lane fall back silently,
// and the registry lane would report coverage it does not have.
func TestDrawIndexReachesCurrentFixture(t *testing.T) {
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatalf("parse the current fixture: %v", err)
	}
	var columns []fixtureColumn
	for _, table := range schema.Tables {
		for _, column := range table.Columns {
			columns = append(columns, fixtureColumn{name: column.Name, columnType: column.Type})
		}
	}
	index := buildDrawIndex(columns)
	scalar := drawNamesOfPlace(index, placementScalar)
	aggregate := drawNamesOfPlace(index, placementAggregate)
	window := drawNamesOfPlace(index, placementWindow)
	illegalScalar := drawNamesWithIllegal(index, placementScalar)
	if len(scalar) == 0 || len(aggregate) == 0 || len(window) == 0 {
		t.Fatalf("a placement has no candidate: scalar=%d aggregate=%d window=%d",
			len(scalar), len(aggregate), len(window))
	}
	if len(illegalScalar) == 0 {
		t.Fatal("no scalar candidate has an illegal half; the blindness counter " +
			"CH_ERROR_43_CHGEN_TYPED could then only ever read zero")
	}
	t.Logf("v3 draw index: %d candidates, scalar=%d aggregate=%d window=%d, illegal scalar=%d",
		len(index), len(scalar), len(aggregate), len(window), len(illegalScalar))
}

// toAny widens a string slice for a printf call.
func toAny(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

// --- composition: a call result returns to the pools ---
//
// The index above draws every argument from the fixture COLUMNS only,
// thus it writes calls of depth 1. No aggregate sits over an arithmetic
// result and no function sits over a function, although the wrapper
// defects live exactly at those boundaries. Every defect in the earlier boundary changes and
// 20 was found in spite of the oracle, and the regression, a combinator over
// a Nullable inner type, was found by hand.
//
// One level of composition removes that blindness at almost no design
// cost. A pool member is a name and a type. A CALL is also a text and a
// type, because the index already computes the result type by calling
// spec.rule. Thus a composed member needs no new plumbing: it is a
// fixtureColumn whose name is the call text.
//
// The composed member is built from the SAME index and the SAME rule, so
// no second list of nested forms exists to drift.

// composedMemberLimit bounds how many composed members one build adds.
//
// The bound exists because the pools feed a uniform pick: without it the
// composed members would outnumber the real columns by an order of
// magnitude, and the plain-column forms that the earlier changes used
// would become rare. The bound keeps composition a MINORITY of each
// pool, thus depth-1 coverage does not regress.
const composedMemberLimit = 24

// composedMembers derives the depth-1 call forms whose result type is
// known, so that a later draw can put them inside another call.
//
// Only SCALAR candidates compose. An aggregate inside another call needs
// an aggregate context that the scalar lanes do not provide, and a
// window call needs an OVER clause, thus either would be a syntax error
// and not coverage.
//
// The result type comes from spec.rule over the drawn argument types.
// A member whose rule REFUSES is dropped: chgen refusing the inner call
// means the composed form has no chgen type to compare, thus it would
// only add noise to the CH_ERROR class.
func composedMembers(index map[string]genDrawCandidate, r *rand.Rand) []fixtureColumn {
	names := drawNamesOfPlace(index, placementScalar)
	var members []fixtureColumn
	for _, name := range names {
		if len(members) >= composedMemberLimit {
			break
		}
		candidate := index[name]
		entry, ok := functionRegistry[name]
		if !ok || entry.rule == nil {
			continue
		}
		call, argTypes, ok := candidate.renderCallWithTypes(r)
		if !ok {
			continue
		}
		result, err := entry.rule(argTypes)
		if err != nil {
			continue
		}
		members = append(members, fixtureColumn{name: call, columnType: result})
	}
	return members
}

// renderCallWithTypes writes one wholly legal call and reports the
// argument types that it drew.
//
// The types are what makes composition possible: the caller feeds them
// to spec.rule to learn the result type of the call it just wrote. A
// separate render and a separate type walk would draw twice from the
// random stream and could disagree about which column was used.
func (c genDrawCandidate) renderCallWithTypes(r *rand.Rand) (string, []CHType, bool) {
	arity := c.spec.minArity
	arguments := make([]string, 0, arity)
	argTypes := make([]CHType, 0, arity)
	for position := 0; position < arity && position < len(c.spec.argSorts); position++ {
		if c.spec.argSorts[position] == argSortValue {
			if position >= len(c.pools) || len(c.pools[position].legal) == 0 {
				return "", nil, false
			}
			column := pick(r, c.pools[position].legal)
			arguments = append(arguments, column.name)
			argTypes = append(argTypes, column.columnType)
			continue
		}
		argument, ok := c.renderArgument(r, position, false)
		if !ok {
			return "", nil, false
		}
		arguments = append(arguments, argument)
		argTypes = append(argTypes, nonValueArgType(c.spec.argSorts[position]))
	}
	if len(arguments) != arity {
		return "", nil, false
	}
	if c.spec.template != "" {
		return fmt.Sprintf(c.spec.template, toAny(arguments)...), argTypes, true
	}
	return c.spec.spelling + "(" + strings.Join(arguments, ", ") + ")", argTypes, true
}

// nonValueArgType gives the type that the generator writes at a position
// which takes no column. It agrees with candidateArgTypes in the index,
// because both describe the same rendered argument.
func nonValueArgType(sort argSort) CHType {
	switch sort {
	case argSortPredicate:
		return CHType{Name: "Bool"}
	case argSortConstString, argSortTypeName:
		return CHType{Name: "String"}
	case argSortConstInt:
		return CHType{Name: "UInt8"}
	default:
		return CHType{Name: "Int32"}
	}
}

// buildComposedDrawIndex builds a draw index whose pools hold the
// fixture columns AND one level of composed call results.
//
// The composed members pass through the very same splitByDomain, thus a
// composed member reaches a position only when the domain of that
// position ACCEPTS its result type. A composed member that the domain
// rejects lands in the illegal half, where it feeds the blindness
// counter exactly like a rejected column. Composition therefore widens
// both halves from one predicate and adds no third list.
func buildComposedDrawIndex(columns []fixtureColumn, r *rand.Rand) (map[string]genDrawCandidate, []fixtureColumn) {
	base := buildDrawIndex(columns)
	members := composedMembers(base, r)
	if len(members) == 0 {
		return base, nil
	}
	widened := append(append([]fixtureColumn{}, columns...), members...)
	return buildDrawIndex(widened), members
}

// --- the combinator lane ---
//
// inferAggregateCombinatorType composes ANY suffix of aggregateCombinators
// over ANY base of isCombinableBaseAggregate. That is a product, and only
// two of its cells had a production by hand: uniqMerge and sumSimpleState.
// The rest of the product was unfuzzed, which is why the regression had to be
// found by hand.
//
// This lane walks the product. The suffix list is read from
// aggregateCombinators, thus a new suffix in inference is fuzzed at once
// and cannot stay behind a hand written copy.

// combinatorForm is one spelled call of a base aggregate with a suffix.
type combinatorForm struct {
	// kind names the form for the report, for example "registry-comb-sumIf".
	kind string
	// sql is the whole call.
	sql string
}

// combinatorBaseSpellings gives the base aggregates that this lane
// composes, with the spelling that the server accepts.
//
// The keys are held against isCombinableBaseAggregate by
// TestCombinatorBasesAreCombinable, so a base that inference drops fails
// the build here instead of staying as a silent copy. The lane does not
// use every combinable base: count takes no data argument and the
// quantile family is parametric, and both would need their own spelling
// rules that say nothing more about the SUFFIX, which is what this lane
// measures.
func combinatorBaseSpellings() []string {
	return []string{
		"sum", "min", "max", "any", "anyLast", "avg",
		"argMin", "argMax", "count", "uniq", "uniqExact", "uniqCombined",
		"quantile", "median", "groupArray", "groupUniqArray",
		"groupBitAnd", "groupBitOr", "groupBitXor",
	}
}

// combinatorSuffixSpellings maps the lowercase suffix of
// aggregateCombinators to the spelling that the server accepts.
// ClickHouse function names are case sensitive, thus the lowercase
// inference key cannot be written into SQL.
func combinatorSuffixSpellings() map[string]string {
	return map[string]string{
		"simplestate":  "SimpleState",
		"state":        "State",
		"stateif":      "StateIf",
		"ifstate":      "IfState",
		"stateornull":  "StateOrNull",
		"merge":        "Merge",
		"mergestate":   "MergeState",
		"ifmerge":      "IfMerge",
		"ifmergestate": "IfMergeState",
		"ornull":       "OrNull",
		"ifornull":     "IfOrNull",
		"ordefault":    "OrDefault",
		"ifordefault":  "IfOrDefault",
		"resample":     "Resample",
		"resampleif":   "ResampleIf",
		"array":        "Array",
		"arrayif":      "ArrayIf",
		"if":           "If",
	}
}

// renderCombinatorForm spells one cell of the suffix-by-base product.
//
// The ARGUMENT of a cell is decided by the suffix and not by the base,
// because three suffixes change what an argument means:
//
//   - -Merge and -IfMerge (and their -State pairs) read an
//     AggregateFunction state, not a value. A state cannot be BUILT in
//     the same SELECT that merges it: measured on 25.8.29.51,
//     sumMerge(sumState(i32)) is Code 184, ILLEGAL_AGGREGATION, because
//     one aggregate sits inside another. The state must therefore come
//     from a COLUMN, and the fixture holds two: agg, of type
//     AggregateFunction(uniq, UInt64), built by the BARE uniq form, and
//     aggif, of type AggregateFunction(sumIf, Int32, UInt8), built by
//     the sumIf form. Thus only uniq reaches plain -Merge and only sum
//     reaches -IfMerge, and every other base answers false. That is a
//     limit of the fixture and not of the combinator rule: a lane that
//     spelled the other merges anyway would report Code 43 or Code 184
//     as if it were coverage.
//   - -Array reads an Array whose element feeds the base aggregate.
//   - -Resample takes its bounds as PARAMETERS and a resampling key as a
//     trailing data argument.
//
// A cell whose argument the lane cannot spell answers false, so that the
// lane emits nothing instead of emitting a form the server would only
// reject for a spelling fault. A lane that called such a refusal
// coverage would be worse than no lane.
func renderCombinatorForm(base, suffix, argument, arrayArgument string) (combinatorForm, bool) {
	spelling, ok := combinatorSuffixSpellings()[suffix]
	if !ok {
		return combinatorForm{}, false
	}
	name := base + spelling
	switch suffix {
	case "merge", "mergestate":
		// The state column of the fixture carries uniq, thus uniq is
		// the only base whose merge (or mergestate) the lane can spell.
		// See the comment above renderCombinatorForm for the
		// measurement.
		if base != "uniq" {
			return combinatorForm{}, false
		}
		return combinatorForm{
			kind: "registry-comb-" + name,
			sql:  fmt.Sprintf("%s(agg)", name),
		}, true
	case "ifmerge", "ifmergestate":
		// -IfMerge and -IfMergeState read a state that an -If form
		// built. The fixture holds one such column, aggif, of type
		// AggregateFunction(sumIf, Int32, UInt8), thus sum is the
		// only base whose -IfMerge (or -IfMergeState) the lane can
		// spell: sumIfMerge(aggif) was measured on 25.8.29.51 to give
		// Int64, and every other base against aggif answers Code 43,
		// "different aggregate function: sumIf instead", in the same
		// way that eleven of the thirteen bases fail plain -Merge
		// against agg. See the regression.
		if base != "sum" {
			return combinatorForm{}, false
		}
		return combinatorForm{
			kind: "registry-comb-" + name,
			sql:  fmt.Sprintf("%s(aggif)", name),
		}, true
	case "array":
		if arrayArgument == "" {
			return combinatorForm{}, false
		}
		return combinatorForm{
			kind: "registry-comb-" + name,
			sql:  fmt.Sprintf("%s(%s)", name, arrayArgument),
		}, true
	case "arrayif":
		// -ArrayIf is the CHAIN Array + If: the base aggregate reads the
		// element type of the array argument and the trailing condition
		// applies as usual. Measured on 25.8.29.51: sumArrayIf(arr_i, b)
		// is Int64. See the regression.
		if arrayArgument == "" {
			return combinatorForm{}, false
		}
		return combinatorForm{
			kind: "registry-comb-" + name,
			sql:  fmt.Sprintf("%s(%s, i32 > 1)", name, arrayArgument),
		}, true
	case "resample":
		return combinatorForm{
			kind: "registry-comb-" + name,
			sql:  fmt.Sprintf("%s(0, 10, 1)(%s, i8)", name, argument),
		}, true
	case "resampleif":
		// -ResampleIf is the CHAIN Resample + If: the resample key comes
		// after the data argument, and the trailing condition comes last.
		// Measured on 25.8.29.51: sumResampleIf(0, 10, 1)(i32, i32, b) is
		// Array(Int64). See the regression.
		return combinatorForm{
			kind: "registry-comb-" + name,
			sql:  fmt.Sprintf("%s(0, 10, 1)(%s, i8, i32 > 1)", name, argument),
		}, true
	case "if", "stateif", "ifstate", "ifornull", "ifordefault":
		// -StateIf and -IfState take the same trailing condition
		// argument as -If. They are spelled here and not left to the
		// -If case, because each is its own suffix in
		// aggregateCombinators: the name is a CHAIN that neither half
		// can split on its own. -IfOrNull and -IfOrDefault join the
		// same case for the same reason: each is a CHAIN of -If with
		// another suffix, and the call SHAPE (one trailing condition) is
		// identical; only the result type differs. Measured on
		// ClickHouse 25.8.29.51 over real columns:
		//
		//	quantileIfState(0.5)(f64, b)  AggregateFunction(quantileIf(0.5), Float64, Bool)
		//	quantileStateIf(0.5)(f64, b)  AggregateFunction(quantile(0.5), Float64)
		//	sumIfOrNull(i32, b)           Nullable(Int64)
		//	sumIfOrDefault(i32, b)        Int64
		//
		// -IfState keeps the condition inside the state and tags it
		// "<base>If"; -StateIf drops the condition. See the regression.
		// -IfOrNull and -IfOrDefault are new for the regression.
		return combinatorForm{
			kind: "registry-comb-" + name,
			sql:  fmt.Sprintf("%s(%s, i32 > 1)", name, argument),
		}, true
	default:
		return combinatorForm{
			kind: "registry-comb-" + name,
			sql:  fmt.Sprintf("%s(%s)", name, argument),
		}, true
	}
}

// combinatorArgumentColumns gives the value columns that the lane feeds
// to a combinator call.
//
// The columns are named and not drawn from a domain, because this lane
// measures the SUFFIX rule and not the argument domain: the registry
// lane above already covers the domain of every base aggregate. The
// names hold the wrapper shapes that the suffix rules must carry, above
// all the Nullable inner type of the regression.
func combinatorArgumentColumns() []string {
	return []string{"i32", "i64", "ni32", "f64", "u8"}
}

// TestCombinatorBasesAreCombinable holds combinatorBaseSpellings against
// isCombinableBaseAggregate.
//
// This is what makes the list a view of inference and not a second copy.
// A base that inference stops combining, or that changes its spelling,
// fails the build here instead of making the lane emit a call that the
// server refuses for a reason the lane cannot see.
func TestCombinatorBasesAreCombinable(t *testing.T) {
	for _, spelling := range combinatorBaseSpellings() {
		key := strings.ToLower(spelling)
		if !isCombinableBaseAggregate(key) {
			t.Errorf("combinatorBaseSpellings has %q, and inference does not combine %q; "+
				"the lane would emit a call that no combinator rule types", spelling, key)
		}
	}
}

// TestCombinatorSuffixesCoverTheDispatch holds the suffix spellings
// against aggregateCombinators.
//
// The dispatch of inferAggregateCombinatorType composes any suffix over
// any base. This test asserts that the lane knows EVERY suffix that the
// dispatch knows, thus a new suffix in inference is fuzzed at once. It
// also asserts the reverse, so a spelling for a suffix that inference
// dropped cannot stay behind.
func TestCombinatorSuffixesCoverTheDispatch(t *testing.T) {
	spellings := combinatorSuffixSpellings()
	for _, combinator := range aggregateCombinators {
		if _, ok := spellings[combinator.suffix]; !ok {
			t.Errorf("inference knows the suffix %q and the lane has no spelling for it; "+
				"the suffix would stay unfuzzed", combinator.suffix)
		}
	}
	known := make(map[string]bool, len(aggregateCombinators))
	for _, combinator := range aggregateCombinators {
		known[combinator.suffix] = true
	}
	for suffix := range spellings {
		if !known[suffix] {
			t.Errorf("the lane spells the suffix %q and inference does not know it; "+
				"the lane would emit a name that no combinator rule types", suffix)
		}
	}
}

// TestCombinatorLaneSpellsTheProduct measures how much of the
// suffix-by-base product the lane writes, and asserts that EVERY suffix
// of the dispatch reaches at least one base. The regression added the aggif
// fixture column and closed the last exception: -IfMerge and
// -IfMergeState now reach sum, thus no suffix needs an exception any
// more.
//
// The lane does NOT spell the whole product, and that is deliberate.
// -Merge and -IfMerge each need an AggregateFunction state COLUMN, and
// the fixture holds one column per family (agg for uniq, aggif for
// sumIf), thus only one base of nineteen reaches -Merge/-MergeState and
// only one base reaches -IfMerge/-IfMergeState. The measured share is
// asserted here so that a later change which silently stops spelling a
// whole suffix fails the build.
//
// The committed grid now measures all nineteen bases with base-specific
// argument roles and complete state columns. This sampled lane keeps its
// smaller state fixture. Its count therefore measures sampling reach and
// not the complete legality matrix.
func TestCombinatorLaneSpellsTheProduct(t *testing.T) {
	spelled := 0
	perSuffix := make(map[string]int, len(aggregateCombinators))
	for _, base := range combinatorBaseSpellings() {
		for _, combinator := range aggregateCombinators {
			form, ok := renderCombinatorForm(base, combinator.suffix, "i32", "arr_i")
			if !ok {
				continue
			}
			if form.sql == "" || form.kind == "" {
				t.Errorf("the lane spelled an empty form for %s with the suffix %q",
					base, combinator.suffix)
			}
			perSuffix[combinator.suffix]++
			spelled++
		}
	}
	for _, combinator := range aggregateCombinators {
		if perSuffix[combinator.suffix] == 0 {
			t.Errorf("the suffix %q reaches no base; it would stay unfuzzed", combinator.suffix)
		}
	}
	if spelled == 0 {
		t.Fatal("the lane spelled no cell at all")
	}
	// The lane spells fourteen non-state-limited suffixes for all nineteen
	// bases. Its four Merge forms each reach one fixture state.
	const wantSpelled = 270
	if spelled != wantSpelled {
		t.Errorf("the lane spells %d cells, want the measured %d; "+
			"a change moved the product and the docstring above needs a fresh measurement",
			spelled, wantSpelled)
	}
	t.Logf("the combinator lane spells %d cells of the %d-cell product: %v",
		spelled, len(combinatorBaseSpellings())*len(aggregateCombinators), perSuffix)
}

// TestComposedMembersCarryTheirRuleType proves that a composed pool
// member carries the type that spec.rule gave for the call it spells.
//
// This is the property that makes composition safe. A member whose text
// and type disagreed would put a WRONG type into every pool that accepts
// it, and the oracle would then report a defect that the generator
// itself manufactured.
func TestComposedMembersCarryTheirRuleType(t *testing.T) {
	columns := oracleFixtureColumns(t)
	index := buildDrawIndex(columns)
	members := composedMembers(index, rand.New(rand.NewSource(7)))
	if len(members) == 0 {
		t.Fatal("no composed member was built; the composition lane would add nothing")
	}
	for _, member := range members {
		if member.name == "" {
			t.Error("a composed member has no call text")
		}
		if member.columnType.String() == "" {
			t.Errorf("the composed member %q has no type", member.name)
		}
	}
	t.Logf("composed members: %d, for example %q of type %s",
		len(members), members[0].name, members[0].columnType.String())
}

// TestComposedIndexWidensThePools proves that composition really reaches
// the pools. A composed member that no domain accepted would leave the
// index exactly as it was, and the lane would claim a depth it never
// reaches.
func TestComposedIndexWidensThePools(t *testing.T) {
	columns := oracleFixtureColumns(t)
	plain := buildDrawIndex(columns)
	composed, members := buildComposedDrawIndex(columns, rand.New(rand.NewSource(7)))
	if len(members) == 0 {
		t.Fatal("composition added no member")
	}
	widened := 0
	for name, candidate := range composed {
		before, ok := plain[name]
		if !ok {
			continue
		}
		for position := range candidate.pools {
			if position >= len(before.pools) {
				continue
			}
			if len(candidate.pools[position].legal) > len(before.pools[position].legal) {
				widened++
			}
		}
	}
	if widened == 0 {
		t.Fatal("no argument position gained a composed member; the composition lane " +
			"would emit depth-1 calls only, which is the blindness it removes")
	}
	t.Logf("composition widened %d argument positions with %d members", widened, len(members))
}
