package engine

import (
	"fmt"
	"sort"
	"testing"
)

// The candidate index of the expression generator.
//
// The index answers one question for each function that has a genSpec:
// can the generator build a call to it from the oracle fixture, and what
// result types does that call reach?
//
// The index COMPUTES both facts. It declares neither of them.
//
//   - The column pool of an argument position comes from
//     spec.domain.accepts, applied to each fixture column. A domain is
//     a measured predicate, thus one measurement shapes both the
//     inference refusal and the generator pool.
//   - The result type comes from a call to spec.rule with the candidate
//     argument types. A declared result would be a second copy of the
//     rule, and the two copies would drift apart.
//
// Step 4 of the migration adds the index and the reachability gate. The
// fuzz generator does not draw from the index yet; that is step 6.

// fixtureColumn is one column of the oracle fixture.
type fixtureColumn struct {
	// name is the column name as it is written into SQL.
	name string
	// columnType is the declared type of the column.
	columnType CHType
}

// genCandidate is one call form that the generator can build.
type genCandidate struct {
	// name is the registry key, thus the lowercased call name.
	name string
	// spec is the generator recipe.
	spec genSpec
	// argPools holds, for each argument position of the minimum
	// arity, the fixture columns that the domain accepts. A position
	// that takes no value, for example a lambda or a constant, has a
	// nil pool: the generator writes such a position itself.
	argPools [][]fixtureColumn
	// valuePositions counts the argument positions that need a column.
	valuePositions int
	// results holds the result types that spec.rule gave for the
	// sampled argument type combinations. It is empty when the entry
	// has no rule, which is legal: such a name is routed to its own
	// inference function before the registry lookup.
	results []string
	// ruleErrors holds one message for each sampled combination that
	// the rule refused. A candidate whose every combination is
	// refused reaches no result.
	ruleErrors []string
}

// buildable reports whether the generator can write a whole call. Every
// value position must have at least one column.
func (c genCandidate) buildable() bool {
	for position, argumentSort := range c.spec.argSorts {
		if argumentSort != argSortValue {
			continue
		}
		if position >= len(c.argPools) || len(c.argPools[position]) == 0 {
			return false
		}
	}
	return true
}

// oracleFixtureColumns reads the columns of the widest oracle fixture.
// The widest fixture is the honest measure of reach: a narrower one
// would report a name as unreachable although a later grammar reaches
// it.
func oracleFixtureColumns(t *testing.T) []fixtureColumn {
	t.Helper()
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatalf("parse the oracle fixture: %v", err)
	}
	var columns []fixtureColumn
	for _, table := range schema.Tables {
		for _, column := range table.Columns {
			columns = append(columns, fixtureColumn{name: column.Name, columnType: column.Type})
		}
	}
	if len(columns) == 0 {
		t.Fatal("the oracle fixture gave no column; the index would be empty, " +
			"thus the gate would pass without measuring anything")
	}
	return columns
}

// poolForDomain gives the fixture columns that a domain accepts. A nil
// domain means that no measurement narrowed the function, thus every
// column is a candidate.
func poolForDomain(domain *argumentDomain, columns []fixtureColumn) []fixtureColumn {
	var pool []fixtureColumn
	for _, column := range columns {
		if domain == nil || domain.accepts(domainBaseType(column.columnType)) {
			pool = append(pool, column)
		}
	}
	return pool
}

// buildGenIndex builds the candidate index for every registry entry that
// carries a genSpec.
func buildGenIndex(t *testing.T) map[string]genCandidate {
	t.Helper()
	columns := oracleFixtureColumns(t)
	index := make(map[string]genCandidate, len(functionRegistry))
	for name := range functionRegistry {
		spec, ok := genSpecFor(name)
		if !ok {
			continue
		}
		candidate := genCandidate{name: name, spec: spec}
		registryEntry := functionRegistry[name]
		candidate.argPools = make([][]fixtureColumn, len(spec.argSorts))
		for position, argumentSort := range spec.argSorts {
			if argumentSort != argSortValue {
				continue
			}
			candidate.valuePositions++
			domain := registryEntry.domain
			if higherOrder, isHigherOrder := higherOrderArrayFunctions[name]; isHigherOrder && position > 0 {
				lastIsAccumulator := higherOrder.accumulator != nil && position == len(spec.argSorts)-1
				isScalar := higherOrder.scalarArgument != nil && position == higherOrder.scalarArgument.positionAfterLambda+1
				if !lastIsAccumulator && !isScalar {
					domain = &arrayArgumentDomain
				}
			}
			candidate.argPools[position] = poolForDomain(domain, columns)
		}
		candidate.results, candidate.ruleErrors = candidateResults(registryEntry, candidate)
		index[name] = candidate
	}
	return index
}

// candidateResults calls the rule of the entry over sampled argument
// type combinations and collects the result types that it reaches.
//
// The sample is one combination for each column of the FIRST value
// position, with every other value position held at its own first
// column. A full cross product would be exponential in the arity and
// would measure nothing more: the gate asks whether ANY combination
// reaches a result, not how many do.
func candidateResults(entry functionSpec, candidate genCandidate) ([]string, []string) {
	if entry.rule == nil {
		return nil, nil
	}
	firstValue := -1
	for index, argumentSort := range candidate.spec.argSorts {
		if argumentSort == argSortValue {
			firstValue = index
			break
		}
	}
	seen := make(map[string]bool)
	var results []string
	var ruleErrors []string
	record := func(argTypes []CHType) {
		result, err := entry.rule(argTypes)
		if err != nil {
			ruleErrors = append(ruleErrors, err.Error())
			return
		}
		text := result.String()
		if !seen[text] {
			seen[text] = true
			results = append(results, text)
		}
	}
	if firstValue < 0 {
		// The call has no value argument, for example count(*). The
		// rule still gives a result, thus the sample is one empty
		// argument list.
		record(candidateArgTypes(candidate, nil, -1))
		sort.Strings(results)
		return results, ruleErrors
	}
	for _, column := range candidate.argPools[firstValue] {
		record(candidateArgTypes(candidate, &column, firstValue))
	}
	sort.Strings(results)
	return results, ruleErrors
}

// candidateArgTypes builds one argument type list. The value position
// `varied` takes the given column. Every other value position takes the
// first column of its own pool. A non-value position takes the type that
// the generator would write there.
func candidateArgTypes(candidate genCandidate, varied *fixtureColumn, variedIndex int) []CHType {
	argTypes := make([]CHType, 0, len(candidate.spec.argSorts))
	for index, argumentSort := range candidate.spec.argSorts {
		switch argumentSort {
		case argSortValue:
			if index == variedIndex && varied != nil {
				argTypes = append(argTypes, varied.columnType)
				continue
			}
			pool := candidate.argPools[index]
			if len(pool) == 0 {
				argTypes = append(argTypes, CHType{Name: "Int32"})
				continue
			}
			argTypes = append(argTypes, pool[0].columnType)
		case argSortPredicate:
			argTypes = append(argTypes, CHType{Name: "Bool"})
		case argSortConstString, argSortTypeName:
			argTypes = append(argTypes, CHType{Name: "String"})
		case argSortConstInt:
			argTypes = append(argTypes, CHType{Name: "UInt8"})
		case argSortLambda:
			// A lambda has no type of its own. The generator writes
			// the body, and the rule of a higher-order function is
			// not reached through the registry path.
			argTypes = append(argTypes, CHType{Name: "Int32"})
		default:
			argTypes = append(argTypes, CHType{Name: "Int32"})
		}
	}
	return argTypes
}

// TestGenIndexIsNotEmpty proves that the index measures something. A
// broken fixture parse or a broken accessor would otherwise make every
// later assertion pass by vacuity.
func TestGenIndexIsNotEmpty(t *testing.T) {
	index := buildGenIndex(t)
	specCount := 0
	for name := range functionRegistry {
		if _, ok := genSpecFor(name); ok {
			specCount++
		}
	}
	if len(index) != specCount {
		t.Errorf("the index holds %d candidates and %d registry entries carry a genSpec; "+
			"the index must hold one candidate for each recipe", len(index), specCount)
	}
	if specCount == 0 {
		t.Fatal("no registry entry carries a genSpec; the index would be empty")
	}
	buildableCount := 0
	withResults := 0
	for _, candidate := range index {
		if candidate.buildable() {
			buildableCount++
		}
		if len(candidate.results) > 0 {
			withResults++
		}
	}
	if buildableCount == 0 {
		t.Fatal("no candidate is buildable; the domain pools are empty, " +
			"thus the reachability gate would measure nothing")
	}
	if withResults == 0 {
		t.Fatal("no candidate reached a result type; the rule calls are broken, " +
			"thus the index does not compute the result")
	}
	t.Logf("index: %d candidates, %d buildable, %d reach a result type",
		len(index), buildableCount, withResults)
}

// TestGenIndexComputesResultsFromTheRule proves that the result side of
// the index comes from spec.rule and not from a declared copy. It calls
// the rule directly and compares.
func TestGenIndexComputesResultsFromTheRule(t *testing.T) {
	index := buildGenIndex(t)
	candidate, ok := index["tostring"]
	if !ok {
		t.Fatal("the index has no candidate for tostring")
	}
	direct, err := functionRegistry["tostring"].rule([]CHType{{Name: "Int32"}})
	if err != nil {
		t.Fatalf("call the rule of tostring: %v", err)
	}
	found := false
	for _, result := range candidate.results {
		if result == direct.String() {
			found = true
		}
	}
	if !found {
		t.Errorf("the rule of tostring gives %q, and the index reached %v; "+
			"the index must compute the result from the rule", direct.String(), candidate.results)
	}
}

// candidateSummary renders one candidate for a failure message.
func candidateSummary(candidate genCandidate) string {
	sizes := make([]string, 0, len(candidate.argPools))
	for index, pool := range candidate.argPools {
		sizes = append(sizes, fmt.Sprintf("%d:%s=%d", index, argSortName(candidate.spec.argSorts[index]), len(pool)))
	}
	return fmt.Sprintf("spelling=%s arity=%d..%d pools=%v results=%v",
		candidate.spec.spelling, candidate.spec.minArity, candidate.spec.maxArity, sizes, candidate.results)
}

// argSortName gives a readable name of an argument sort.
func argSortName(value argSort) string {
	switch value {
	case argSortValue:
		return "value"
	case argSortPredicate:
		return "predicate"
	case argSortConstString:
		return "conststring"
	case argSortConstInt:
		return "constint"
	case argSortLambda:
		return "lambda"
	case argSortTypeName:
		return "typename"
	default:
		return "unset"
	}
}
