package engine

// This file holds the generator half of the declarative source. It says
// how to WRITE a call to a registry function. It does not say what the
// call means.
//
// Two facts are never declared here.
//
// The argument domain is not declared. The generator asks
// spec.domain.accepts for each fixture column. One measured domain thus
// tightens the inference refusal AND shapes the generator column pool.
//
// The result type is not declared. The generator calls spec.rule with
// the candidate argument types. A declared result would be a second copy
// of the rule, and the two copies would drift.
//
// Each semantic entry has a genSpec. It states the function kind, arity,
// constant positions, and spelling. The semantic registry builder refuses an
// entry without this call shape.

// argSort names the SYNTACTIC kind of one argument position. It is a
// search hint for the generator, not a type. The type comes from the
// domain and from the fixture column.
type argSort int

const (
	// argSortUnset is the zero value. A genSpec must not use it, and
	// TestGenSpecEntriesAreConsistent fails on it.
	argSortUnset argSort = iota
	// argSortValue is an ordinary value expression, for example a
	// fixture column or a nested call. The generator picks it from
	// the column pool of the domain.
	argSortValue
	// argSortNumber is a numeric value expression. It can be a column and
	// does not have to be a constant.
	argSortNumber
	// argSortOffset is a fixed-width number that an offset accepts.
	argSortOffset
	// argSortIntegerOffset is a fixed-width integer window offset.
	argSortIntegerOffset
	// argSortIndex is a fixed-width integer that an index accepts.
	argSortIndex
	// argSortPredicate is a boolean condition, for example the second
	// argument of sumIf. The generator builds a comparison.
	argSortPredicate
	// argSortConstString is a string literal that the server reads at
	// parse time, for example the unit of dateDiff.
	argSortConstString
	// argSortConstInt is an integer literal, for example the offset of
	// substring or the scale of toDateTime64.
	argSortConstInt
	// argSortConstNumber is an integer or floating-point literal, for
	// example the level parameter of quantile.
	argSortConstNumber
	// argSortInterval is an INTERVAL expression with a constant unit.
	argSortInterval
	// argSortLambda is a lambda expression, for example the first
	// argument of arrayExists.
	argSortLambda
	// argSortTypeName is a constant that names a type or an element,
	// for example the selector of tupleElement. It is not a value.
	argSortTypeName
)

// placement says in which syntactic position a call is legal. It cannot
// be derived from the registry: row_number is wrapperOpaque with a fixed
// result, exactly like now(), yet it is legal only with an OVER clause.
type placement int

const (
	// placementUnset is the zero value and is never legal in a
	// genSpec.
	placementUnset placement = iota
	// placementScalar is a call that is legal in any expression.
	placementScalar
	// placementAggregate is a call that needs an aggregate context.
	placementAggregate
	// placementWindow is a call that needs an OVER clause.
	placementWindow
)

// genSpec says how to spell one useful generated call. It is not the complete
// server signature. See functionSignature.
type genSpec struct {
	// spelling is the name as the server accepts it. The registry key
	// is lowercased, and ClickHouse function names are case sensitive,
	// thus the key alone cannot be written into SQL.
	spelling string
	// minArity and maxArity bound the number of arguments. maxArity is
	// -1 for a variadic function, for example concat.
	minArity int
	maxArity int
	// argSorts gives the syntactic kind of each argument position. It
	// holds minArity entries for a fixed-arity function. For a
	// variadic function it describes the leading positions, and the
	// last entry repeats for the remaining positions.
	argSorts []argSort
	// place says where the call is legal.
	place placement
	// template, when not empty, is the call form for a function whose
	// syntax is not "name(a, b)". It is a printf format whose verbs
	// take the rendered arguments in order.
	template string
}

// genSpecFor reports the generator recipe of a function, and false when the
// name is unknown.
func genSpecFor(name string) (genSpec, bool) {
	spec, ok := functionRegistry[name]
	if !ok || spec.gen == nil {
		return genSpec{}, false
	}
	return *spec.gen, true
}

// Shorthands for the common shapes. They keep the registry entries one
// line each, which is what makes a missing entry visible.

// scalarCall is a scalar function that takes n value arguments.
func scalarCall(spelling string, n int) *genSpec {
	return &genSpec{spelling: spelling, minArity: n, maxArity: n, argSorts: valueSorts(n), place: placementScalar}
}

// scalarCallRange is a scalar function whose arity is a range. Every
// argument is a value.
func scalarCallRange(spelling string, min, max int) *genSpec {
	return &genSpec{spelling: spelling, minArity: min, maxArity: max, argSorts: valueSorts(min), place: placementScalar}
}

// aggregateCall is an aggregate function that takes n value arguments.
func aggregateCall(spelling string, n int) *genSpec {
	return &genSpec{spelling: spelling, minArity: n, maxArity: n, argSorts: valueSorts(n), place: placementAggregate}
}

// conditionalAggregateCall is an -If aggregate: n value arguments and
// one trailing predicate.
func conditionalAggregateCall(spelling string, n int) *genSpec {
	sorts := append(valueSorts(n), argSortPredicate)
	return &genSpec{spelling: spelling, minArity: n + 1, maxArity: n + 1, argSorts: sorts, place: placementAggregate}
}

// windowCall is a function that needs an OVER clause.
func windowCall(spelling string, n int) *genSpec {
	return &genSpec{spelling: spelling, minArity: n, maxArity: n, argSorts: valueSorts(n), place: placementWindow}
}

// valueSorts gives n value positions.
func valueSorts(n int) []argSort {
	sorts := make([]argSort, n)
	for i := range sorts {
		sorts[i] = argSortValue
	}
	return sorts
}
