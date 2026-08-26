package engine

// This file enumerates the CELLS of the wrapper grid: every probe
// expression that the grid measures, and the address of each one.
//
// It has NO build tag, so that the default `go test ./...` run can check
// the shape of the enumeration, the stability of the cell addresses and
// the no-shrink gate, all without a server. Only the MEASUREMENT needs a
// server, and that part lives behind the fuzzoracle tag in
// wrappergrid_test.go.

import (
	"fmt"
	"sort"
	"strings"
)

// gridGoldenPath is the committed golden file. It is declared here, in
// the untagged half, because the shape gates read the golden header
// without a server and must compile in the default test run.
var gridGoldenPath = moduleRootPath("testdata", "wrapper_grid.golden")

// gridProbe is one measured cell.
//
// A probe is addressed by id, which is a stable text key. The id, not the
// position in a slice, is what the golden file stores, thus a new entry
// in the middle of a registry cannot renumber the whole golden.
type gridProbe struct {
	// id is the stable address of the cell, for example
	// "fn/toString/lc/s". It must be unique and it must not change for
	// a cell that keeps its meaning.
	id string
	// family names the source table of the entry, for example "fn",
	// "sized", "hof", "simplestate" or "op". The no-shrink gate counts
	// per family.
	family string
	// entry is the registry key or the operator token.
	entry string
	// sql is the expression that both sides type. It never contains a
	// literal in the measured position: ClickHouse folds constants, and
	// a folded constant reports a different LowCardinality wrapper.
	sql string
	// aggregate reports whether the expression needs an aggregate
	// context. The measuring side groups a batch by this flag, because
	// a batch that mixes an aggregate with a bare column fails as a
	// whole with Code 215 and that failure belongs to the batch, not to
	// either expression.
	aggregate bool
}

// gridConstantFor renders a constant argument of a given sort. The values
// are ordinary for their position, because an out-of-range constant turns
// a type measurement into a value error.
func gridConstantFor(sort argSort, position int) (string, bool) {
	switch sort {
	case argSortConstString:
		// The unit of dateDiff and of the date arithmetic family. It
		// is the one constant string that the server reads at parse
		// time in this registry.
		return "'day'", true
	case argSortConstInt, argSortOffset, argSortIntegerOffset:
		// A small positive integer is legal as a length, an offset, a
		// scale and a precision alike. A scale above 9 would be
		// refused by toDecimal32, thus the value stays small.
		return "2", true
	case argSortTypeName:
		// The selector of tupleElement. Position 1 of a
		// Tuple(Int32, String) always exists.
		return "1", true
	case argSortPredicate:
		// A predicate over a real column, never a literal.
		return gridColumnName("bare", "i32") + " > 1", true
	case argSortLambda:
		return "x -> x", true
	}
	return "", false
}

// gridElementArgumentEntries names the registry entries whose value
// positions AFTER the first one take the ELEMENT of the first argument,
// and not another value of the first argument's own type.
//
// The list is a property of the SERVER contract, thus it lives beside the
// enumerator until the registry can declare it. See the report of
// the regression for the exact registry field that would replace this list.
//
// Why the list exists. The default enumerator puts the SAME fixture
// column in every value position. For a function of this shape that
// builds an expression that no user would write, and the server answers
// with a refusal about the PAIR of arguments:
//
//	has(c_bare_arr, c_bare_arr)  Code 386  no supertype Int32, Array(Int32)
//	has(c_bare_map, c_bare_map)  Code 386  no supertype String, Map(...)
//
// Code 386 is not evidence that chgen typed something the server refuses,
// yet the grid recorded CHGEN_TYPES_SERVER_REFUSES for those four cells.
// The cure is to send the element, because the server then answers a real
// type:
//
//	has(c_bare_arr, c_bare_i32)  UInt8
//	has(c_bare_map, c_bare_s)    UInt8
//
// A measured cell is better than an excluded one, thus the enumerator is
// corrected and Code 386 is NOT added to the exclusion list.
//
// A survey of the whole registry found exactly ONE entry of this shape.
// Sixteen entries have two or more value positions; the other fifteen
// (the comparison family, argMax, argMin, concat, map, nullif and the
// dateDiff family) want two values of the SAME kind, so the default
// enumerator is right for them. hasAll, hasAny, indexOf, countEqual and
// mapContains have no registry entry at all today. arrayElement already
// declares argSortConstInt in position 2, thus it never sent a container.
var gridElementArgumentEntries = map[string]bool{
	"has": true,
}

// gridElementBaseOf gives the base key of the ELEMENT of a container base
// key, for the purpose that gridElementArgumentEntries serves.
//
// The answers below are measured against the fixture declaration in
// gridBases:
//
//	arr    Array(Int32)                  element Int32  -> base key i32
//	narr   Array(Nullable(Int32))        element Nullable(Int32)
//	lcarr  Array(LowCardinality(String)) element LowCardinality(String)
//	map    Map(String, Int64)   has() tests the KEY, thus String -> s
//
// The two ARRAYS WITH A WRAPPED ELEMENT take the BARE element base, not
// a wrapped one. That is the rule of gridElementColumnFor applied one
// level in: such a cell measures what the function does to the ELEMENT
// WRAPPER OF THE CONTAINER, thus the other argument must hold that
// wrapper constant. A mate that carried its own Nullable or
// LowCardinality would move two things at once.
//
// The bare mate is also the one the server accepts. Measured on
// ClickHouse 25.8.29.51 against real columns of a real table:
//
//	has(Array(Nullable(Int32)) col, Int32 col)          UInt8
//	has(Array(LowCardinality(String)) col, String col)  UInt8
//
// A base that is not a container has no element, and the second return
// value is false. The caller then keeps the default same-column
// rendering, which is correct for every non-container argument.
func gridElementBaseOf(base string) (string, bool) {
	switch base {
	case "arr":
		return "i32", true
	case "narr":
		// The element is Nullable(Int32) and the mate is the BARE
		// Int32 base, so the cell varies the element wrapper of the
		// ARRAY only.
		return "i32", true
	case "lcarr":
		// The element is LowCardinality(String) and the mate is the
		// BARE String base, for the same reason.
		return "s", true
	case "map":
		// has(mp, k) asks whether the MAP HAS THE KEY k. Measured on
		// 25.8.29.51: has(c_bare_map, c_bare_s) is UInt8. The element
		// of a Map for this function is therefore its key type,
		// String.
		return "s", true
	case "tup":
		// A Tuple base reaches this function only through the safarr
		// letter, where the column type is
		// SimpleAggregateFunction(anyLast, Array(Tuple(Int32, String)))
		// and the ELEMENT is the Tuple itself. The mate is therefore
		// the BARE tup column, by the same rule as every other line
		// here: the mate holds the element type and carries no wrapper
		// of its own.
		//
		// This line was absent while no cell of the grid could put a
		// Tuple in front of an element-argument entry. The regression made
		// such a cell reachable, and the missing line then showed as
		// the Code 386 shape that this whole rule exists to prevent.
		// Measured on ClickHouse 25.8.29.51 against real columns:
		//
		//	has(saf_arr_tuple_col, bare_tuple_col)  UInt8
		//	has(saf_arr_tuple_col, saf_arr_tuple_col)
		//	                    Code 386, no supertype
		//
		// The bare tup column is legal because the grid declares it:
		// Tuple(Int32, String) is a base of gridBases.
		return "tup", true
	}
	return "", false
}

// gridBaseIsContainer reports whether a base key names a container type.
//
// It is deliberately WIDER than gridElementBaseOf. A base can be a
// container and still have no element base: tup is one, because
// tupleElement selects by position and no single column holds "the
// element" of a Tuple. The no-repeat rule of the element-argument
// enumerator must still cover such a base, thus the two questions are
// asked by two functions and not by one.
//
// The list names the container bases of gridBases. It is short and
// closed, in the same way as the wrapper alphabet.
func gridBaseIsContainer(base string) bool {
	switch base {
	case "arr", "narr", "lcarr", "tup", "map":
		return true
	}
	return false
}

// gridElementColumnFor gives the fixture column that holds the element of
// a container cell.
//
// The element column is always taken BARE. The wrapper alphabet of the
// grid applies to the CONTAINER argument, which is the argument whose
// wrapper rule the cell measures. Putting the same wrapper on the element
// too would vary two things at once, and a cell that varies two things
// cannot say which one moved the answer.
//
// THE safarr LETTER ADDS AN ARRAY LEVEL, thus it moves the element one
// level out. For every other letter the column type is a wrapper around
// the BASE, so the element of the column is the element of the base. The
// safarr letter spells SimpleAggregateFunction(anyLast, Array(base)), so
// the element of the COLUMN is the BASE ITSELF.
//
// Measured on ClickHouse 25.8.29.51 against real columns of a real
// AggregatingMergeTree table, with a VALUE select:
//
//	column SimpleAggregateFunction(anyLast, Array(Array(Int32)))
//	has(column, Array(Int32) col)  UInt8
//	has(column, Int32 col)         Code 386, no supertype
//
// The rule that took the element of the BASE sent the Int32 mate and
// banked Code 386, which is a refusal about the PAIR of arguments and is
// not type evidence. This was invisible until the regression let a container
// base reach an element-argument entry under the safarr letter.
func gridElementColumnFor(cell gridCell) (string, bool) {
	if cell.wrapper == "safarr" {
		// The letter wraps the base in one Array level, thus the
		// element of the column is the base itself. The mate is taken
		// BARE, by the same rule as every other letter.
		column := gridColumnName("bare", cell.base)
		if !gridHasColumn(column) {
			return "", false
		}
		return column, true
	}
	if cell.base == "tup" {
		// gridElementBaseOf("tup") answers "tup" itself, which is
		// correct ONLY under safarr: there the wrapper adds an Array
		// level, so the mate at the BASE level is a different column
		// than the SimpleAggregateFunction(anyLast, Array(Tuple(...)))
		// container. Under every other wrapper the container column
		// IS the bare tup column, so gridColumnName("bare", "tup")
		// would name the SAME column as the container, and the caller
		// would spell has(c_bare_tup, c_bare_tup): the exact Code 386
		// repeat that this function exists to prevent.
		//
		// the regression made this reachable: before it, the representative
		// chooser never gave an element-argument entry a bare Tuple
		// cell whose domain refused tup, because the domain filter of
		// the regression kept tup out of the candidate list, and even with
		// that filter gone the chooser used to hand the container
		// slot to whichever base arrived first regardless of the
		// entry's own domain. Refusing here, and not only under
		// safarr, is what keeps the mate distinct from the container.
		return "", false
	}
	elementBase, ok := gridElementBaseOf(cell.base)
	if !ok {
		return "", false
	}
	column := gridColumnName("bare", elementBase)
	if !gridHasColumn(column) {
		return "", false
	}
	return column, true
}

// gridProbeColumnsFor gives the fixture columns that a function may take
// in a VALUE position, for one wrapper.
//
// The domain is NOT used to FILTER the base types out. An entry with a
// measured domain and an entry without one both get every base type of
// the grid, for the SAME wrapper. This is the point of the whole census:
// the server answer for a cell is what derives the domain, so a domain
// filter here would let chgen decide which cells exist, and a defect
// where chgen's domain disagrees with the server could never be seen,
// because the disagreeing cell would never be built.
//
// the regression found 240 vanished cells from this filter, for example
// fn/hex/bare/arr, fn/hex/bare/map and fn/hex/bare/tup. Every one of them
// held the verdict CHGEN_TYPES_SERVER_REFUSES before the filter was
// added, and after the filter they did not move to another verdict: the
// golden held no server answer for them at all.
//
// The domain is still used, ORDER ONLY, by the caller through
// gridRepresentativeCells: see that function for why a narrow-domain
// entry must still be able to pick its OWN legal bases as the cell that
// carries its name.
func gridProbeColumnsFor(domain *argumentDomain, wrapper string, schema *Schema) []gridCell {
	var out []gridCell
	for _, cell := range gridColumns() {
		if cell.wrapper != wrapper {
			continue
		}
		out = append(out, cell)
	}
	return out
}

// gridBaseTypeOf gives the base type of a fixture column as chgen parses
// it, with the wrappers removed. The wrappers are removed with the same
// unwrap that inference uses, so the grid asks the domain the question
// that inference asks it.
func gridBaseTypeOf(cell gridCell, schema *Schema) (CHType, bool) {
	table, ok := schema.Tables["g"]
	if !ok {
		return CHType{}, false
	}
	for _, column := range table.Columns {
		if column.Name == cell.column {
			return unwrapAllForGrid(column.Type), true
		}
	}
	return CHType{}, false
}

// unwrapAllForGrid removes LowCardinality, Nullable and
// SimpleAggregateFunction from a type, at any nesting order, and gives
// the base type back.
//
// SimpleAggregateFunction is handled explicitly and NOT by a name-prefix
// test. Its first parameter is the aggregate name and its LAST parameter
// is the value type, so a rule that took the first parameter would read
// the aggregate name as a type.
func unwrapAllForGrid(value CHType) CHType {
	for {
		switch strings.ToLower(value.Name) {
		case "lowcardinality", "nullable":
			if len(value.Params) != 1 {
				return value
			}
			value = value.Params[0]
		case "simpleaggregatefunction":
			if len(value.Params) == 0 {
				return value
			}
			value = value.Params[len(value.Params)-1]
		default:
			return value
		}
	}
}

// gridBuildProbes enumerates every cell of the grid.
//
// The enumeration walks four registry tables and the operator catalog.
// The order is fully deterministic: every map is walked over its SORTED
// key list, and every slice keeps its declared order. A run therefore
// visits the same cells in the same order on every machine, which is what
// makes the golden byte-reproducible.
func gridBuildProbes(schema *Schema) []gridProbe {
	var probes []gridProbe
	probes = append(probes, gridFunctionProbes(schema)...)
	probes = append(probes, gridSizedProbes(schema)...)
	probes = append(probes, gridHigherOrderProbes()...)
	probes = append(probes, gridSimpleStateProbes()...)
	probes = append(probes, gridOperatorProbes()...)
	probes = append(probes, gridVariadicProbes()...)
	return probes
}

// gridSortedKeys gives the sorted key list of a string-keyed map. Every
// walk of the enumeration uses it, because Go randomizes map order and a
// randomized order would make the golden file differ between two runs
// against one server.
func gridSortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// gridFunctionProbes enumerates functionRegistry.
//
// For each entry and each wrapper of the closed alphabet, it takes the
// representative base types that gridRepresentativeCells chooses. That
// choice is what keeps the grid at a few thousand cells instead of tens
// of thousands. gridProbeColumnsFor no longer filters candidates by the
// entry domain (the regression): every base of the alphabet reaches the
// chooser. The chooser itself still reads the domain, ORDER only
// (the regression), so a narrow-domain entry still picks its own legal bases
// for its representative slots, while a base its domain refuses can
// still surface elsewhere in the closure through another entry or
// another wrapper.
func gridFunctionProbes(schema *Schema) []gridProbe {
	var probes []gridProbe
	for _, name := range gridSortedKeys(functionRegistry) {
		spec := functionRegistry[name]
		if spec.gen == nil {
			continue
		}
		recipe := *spec.gen
		// A NULLARY entry takes no value argument, thus no wrapper and
		// no base type can reach it: today(), now() and row_number()
		// spell the same call whatever the alphabet says. Such an
		// entry is enumerated exactly ONCE.
		//
		// Enumerating it per wrapper would make 6 times 2 identical
		// cells with 12 different addresses. That is not coverage: it
		// is one measurement counted twelve times, and it would let a
		// reviewer read a large cell count as a large amount of
		// evidence.
		if gridRecipeIsNullary(recipe) {
			sql, ok := gridRenderCall(recipe, name, gridCell{})
			if !ok {
				continue
			}
			probes = append(probes, gridProbe{
				id:        "fn/" + name + "/nullary",
				family:    "fn",
				entry:     name,
				sql:       sql,
				aggregate: recipe.place == placementAggregate,
			})
			continue
		}
		// A window call needs an OVER clause, and a template call has
		// its own syntax. Both are enumerated, but the renderer must
		// know which shape to write.
		for _, wrapper := range gridWrappers {
			candidates := gridProbeColumnsFor(spec.domain, wrapper.key, schema)
			for _, cell := range gridRepresentativeCells(candidates, spec.domain, schema) {
				sql, ok := gridRenderCall(recipe, name, cell)
				if !ok {
					continue
				}
				probes = append(probes, gridProbe{
					id:        "fn/" + name + "/" + wrapper.key + "/" + cell.base,
					family:    "fn",
					entry:     name,
					sql:       sql,
					aggregate: recipe.place == placementAggregate,
				})
			}
		}
	}
	return probes
}

// gridRecipeIsNullary reports whether a recipe puts no fixture column in
// the call. That is true for an arity of zero, and also for a recipe
// whose every position takes a constant, for example a template call that
// spells count(*).
func gridRecipeIsNullary(recipe genSpec) bool {
	if recipe.minArity == 0 {
		return true
	}
	for position := 0; position < recipe.minArity; position++ {
		sort := argSortValue
		if position < len(recipe.argSorts) {
			sort = recipe.argSorts[position]
		} else if len(recipe.argSorts) > 0 {
			sort = recipe.argSorts[len(recipe.argSorts)-1]
		}
		if sort == argSortValue {
			return false
		}
	}
	return true
}

// gridBasesPerCell caps how many representative base types one
// (entry, wrapper) pair takes FROM THE SCALAR PART of its domain.
//
// Two is the measured minimum that still separates a rule which READS the
// argument type from a rule which returns a constant. One representative
// cannot tell those apart, and the whole base list would multiply the
// grid by eight for no new rule.
//
// The cap governs the SCALAR part only. A container representative is
// chosen separately by gridRepresentativeCells; see the comment there for
// the measurement that made the split necessary.
const gridBasesPerCell = 2

// gridBaseKindOf sorts a base key into the KIND that the representative
// chooser separates.
//
// The kinds are not a taxonomy of ClickHouse types. They are the classes
// over which the measured answers DIFFER, and nothing more. Every SCALAR
// base is one kind, because the two-representative rule already separates
// what a scalar can show. Every CONTAINER base is its OWN kind, because
// the server answers each of them differently.
//
// WHY EACH CONTAINER IS ITS OWN KIND. It is tempting to call the five
// container bases one class and take a single representative. That would
// take arr, which is first in gridBases, and it would report the arr
// answer as the container answer. The server does not agree. Measured on
// ClickHouse 25.8.29.51 against real columns of a real table, with a
// VALUE select and not with toTypeName alone:
//
//	                arr             tup             map
//	arraySort   Array(Int32)      Code 43         Code 43
//	length      UInt64            Code 43         UInt64
//	empty       UInt8             Code 43         UInt8
//	max         Array(Int32)  Tuple(Int32,String) Map(String,Int64)
//	hex         Code 43           Code 43         Code 43
//
// The three plain containers split three ways. arraySort accepts arr
// alone; length and empty accept arr and map but REFUSE tup with Code 43,
// which is type evidence; max accepts all three and gives each shape
// back. A single representative would have recorded the arr row and left
// the two Code 43 columns unaddressed, which is the very defect this
// ticket names, one level down.
//
// The two arrays with a WRAPPED ELEMENT split as well:
//
//	                narr                    lcarr
//	arraySort   Array(Nullable(Int32))    Array(String)
//	max         Array(Nullable(Int32))    Array(String)
//	arrayDistinct Array(Int32)            Array(String)
//
// A Nullable element SURVIVES and a LowCardinality element is DROPPED.
// One of the two could never stand for the other.
//
// Five container kinds is therefore the measurement, not a preference. No
// two of the five agree across the function space, thus none of them can
// be a proxy for another.
func gridBaseKindOf(base string) string {
	switch base {
	case "arr", "tup", "map", "narr", "lcarr":
		// Each container base is its own kind, and so takes its own
		// single slot. The key is the kind name, thus a new container
		// base gets its own slot automatically and cannot silently
		// join another one.
		return base
	default:
		// Every scalar, string, temporal and identifier base. These are
		// the bases that the positional cap already reached, thus they
		// stay one kind and keep the two-representative rule.
		return gridScalarKind
	}
}

// gridScalarKind is the kind name of every non-container base. It is a
// named constant because the chooser and its gates both test for it, and
// a bare string in two places is a name that can drift.
const gridScalarKind = "scalar"

// gridRepresentativeCells chooses the base types that one
// (entry, wrapper) pair measures.
//
// WHY THE POSITIONAL CAP HAD TO GO. The old rule took the FIRST
// gridBasesPerCell candidates in gridBases ORDER, and the container bases
// are LAST in that list. Thus for every entry whose domain also accepts a
// scalar, both slots went to scalars and no container was ever measured.
// Measured against the enumeration at the base commit: of the 1696
// (entry, wrapper) pairs of the fn and sized lanes, 216 had a container
// in their accepted domain and only 12 of those reached one. The other
// 204 were STARVED. In the whole 3978-cell census only 6 registry entries
// held a container cell, and each of those has a domain that accepts NO
// scalar, so nothing could crowd it out.
//
// The consequence was that the census reported health for a class it
// could not address. The fn lane reached the narr base but NEVER the
// lcarr base, because arr and narr are taken first. Only the hof lane,
// which names its arrays directly and so escapes the cap, reached lcarr.
//
// THE RULE NOW. Representatives are chosen per KIND, not by list
// position:
//
//   - up to gridBasesPerCell bases of the SCALAR kind, exactly as before;
//   - at most ONE base of EACH container kind.
//
// Each kind takes its members in gridBases order, thus the choice stays
// deterministic and the golden stays byte-reproducible. A container kind
// holds exactly one base today, so the per-kind cap of one costs nothing
// and states the intent for a base that is added later.
//
// WHY ONE PER CONTAINER KIND AND NOT TWO. The scalar kind needs two
// representatives to separate a rule that READS the type from a rule that
// returns a constant. A container kind does not need that second witness,
// because the scalar pair already made that separation for the same
// entry. What a container kind adds is a base the rule has never seen,
// and one base per kind is enough to ask the question.
//
// WHY THE GROWTH IS ACCEPTABLE. The census goes from 3978 cells to 4862,
// which is 884 more and 22 per cent. Every added cell is in the fn
// family, which goes from 2234 to 3118. The sized family does not move:
// no sizedConstructor accepts a container base, thus the chooser has
// nothing new to offer it. That is a large slice and it needs the
// argument below.
//
//   - The alphabet is UNCHANGED. This adds no wrapper letter and no base
//     type. Every added cell was already spelled by the closed alphabet
//     and was thrown away by the cap before it could be measured. The
//     census stays an enumeration of the same alphabet, and it stays a
//     reviewable number.
//   - The growth is bounded by LEGALITY, not by taste. A container base
//     is legal under three wrapper letters only: bare, saf and safarr.
//     The other six letters refuse a container inner, and
//     gridWrapperAccepts already holds those measurements. Nothing here
//     can drift wider.
//   - The added cells are not repetition. gridBaseKindOf records the
//     measurement that no two container bases agree across the function
//     space. A cheaper design that took one container representative was
//     measured at 4378 cells, and it would have recorded the arr answer
//     while leaving the tup and map refusals unaddressed.
//
// The upper bound is five extra cells per (entry, wrapper) pair, over
// three letters, for the entries whose domain accepts a container. That
// bound is what 876 counts.
//
// THE DOMAIN-FIRST ORDER, added by the regression. gridProbeColumnsFor no
// longer filters candidates by the entry domain (the regression), so the
// unfiltered candidate list for a narrow-domain entry, for example
// date_diff with dateArgumentDomain, holds every scalar base of the
// grid and not only d and dt. The per-kind cap below still takes the
// FIRST gridBasesPerCell scalar candidates it sees, so without a
// reorder it would take i32 and u64, which date_diff refuses, and the
// bases the entry actually names, d and dt, would never be picked at
// all: the closure would hold a legal triple that no cell ever spells.
//
// gridOrderDomainFirst puts every candidate the domain accepts ahead of
// every candidate it does not, keeping gridBases order within each
// side, so the cap still picks deterministically and the golden stays
// byte-reproducible. An entry with no domain is unaffected: every
// candidate accepts vacuously and the order does not change.
func gridRepresentativeCells(candidates []gridCell, domain *argumentDomain, schema *Schema) []gridCell {
	ordered := gridOrderDomainFirst(candidates, domain, schema)
	var out []gridCell
	scalars := 0
	takenKinds := map[string]bool{}
	for _, cell := range ordered {
		kind := gridBaseKindOf(cell.base)
		if kind == gridScalarKind {
			if scalars >= gridBasesPerCell {
				continue
			}
			scalars++
		} else {
			if takenKinds[kind] {
				continue
			}
			takenKinds[kind] = true
		}
		out = append(out, cell)
	}
	return out
}

// gridOrderDomainFirst puts every candidate the entry domain accepts
// ahead of every candidate it does not, keeping gridBases order within
// each side. See the regression note on gridRepresentativeCells for why
// the reorder is needed: without it, the per-kind cap can starve a
// narrow-domain entry of the very bases its domain names.
//
// A nil domain, or a candidate whose base type the schema cannot find,
// accepts vacuously, so the order is unchanged for an entry with no
// domain.
func gridOrderDomainFirst(candidates []gridCell, domain *argumentDomain, schema *Schema) []gridCell {
	if domain == nil {
		return candidates
	}
	var accepted, refused []gridCell
	for _, cell := range candidates {
		base, ok := gridBaseTypeOf(cell, schema)
		if !ok || domain.accepts(base) {
			accepted = append(accepted, cell)
			continue
		}
		refused = append(refused, cell)
	}
	return append(accepted, refused...)
}

// gridRenderCall writes one call of a recipe, with the given fixture
// column in every VALUE position and a constant in every other position.
//
// The entry name selects the element-argument rule. An entry named in
// gridElementArgumentEntries gets the ELEMENT of the fixture column in
// every value position after the first one.
//
// It returns false for a recipe that the grid cannot spell, for example a
// template whose verb count does not match the arity. A refusal here is a
// cell that is NOT enumerated, and the no-shrink gate counts those, so a
// recipe that stops being spellable is visible.
func gridRenderCall(recipe genSpec, entry string, cell gridCell) (string, bool) {
	arity := recipe.minArity
	sorts := recipe.argSorts
	args := make([]string, 0, arity)
	for position := 0; position < arity; position++ {
		sort := argSortValue
		if position < len(sorts) {
			sort = sorts[position]
		} else if len(sorts) > 0 {
			sort = sorts[len(sorts)-1]
		}
		if sort == argSortValue {
			// A value position after the first one takes the ELEMENT
			// of the first argument when the entry has that shape.
			// Without this the call would be has(arr, arr), which is
			// Code 386 about the PAIR of arguments and is not
			// evidence about a type rule.
			if position > 0 && gridElementArgumentEntries[entry] {
				if element, ok := gridElementColumnFor(cell); ok {
					args = append(args, element)
					continue
				}
				if gridBaseIsContainer(cell.base) {
					// The entry wants an element and the base is a
					// container whose element the grid cannot name.
					// Repeating the container here would spell
					// has(x, x), which the server answers with Code
					// 386 about the PAIR of arguments. That is not
					// evidence about a type rule, and the grid once
					// recorded exactly that as
					// CHGEN_TYPES_SERVER_REFUSES.
					//
					// A cell that is NOT enumerated is honest, and the
					// no-shrink gate counts it, so the loss is visible.
					// A cell that banks Code 386 as a verdict is not.
					return "", false
				}
			}
			args = append(args, cell.column)
			continue
		}
		text, ok := gridConstantFor(sort, position)
		if !ok {
			return "", false
		}
		args = append(args, text)
	}
	if recipe.template != "" {
		if strings.Count(recipe.template, "%s") != len(args) {
			// A template whose verbs do not match the arity is not
			// spellable. count(*) is the common case: arity zero.
			if strings.Count(recipe.template, "%s") == 0 {
				return recipe.template, true
			}
			return "", false
		}
		values := make([]any, len(args))
		for i, a := range args {
			values[i] = a
		}
		return fmt.Sprintf(recipe.template, values...), true
	}
	call := recipe.spelling + "(" + strings.Join(args, ", ") + ")"
	if recipe.place == placementWindow {
		call += " OVER ()"
	}
	return call, true
}

// gridVariadicProbes enumerates the VARIADIC entries with MIXED argument
// types.
//
// This lane exists because of a measured defect class. A grid that gives
// one type to every position cannot see a rule that reads only the FIRST
// argument: `array(i8, i8)` and a correct `array(i8, i32)` both look
// right when the rule is wrong. The defect the regression was exactly that,
// where arrayFunctionResult read argument one and answered
// Array(Int8) for `array(i8, i32)`.
//
// Thus every variadic entry gets PAIRS of different base types, in both
// orders. Both orders matter: a rule that reads only the first argument
// and a rule that reads only the last are different defects, and a single
// order would catch only one of them.
func gridVariadicProbes() []gridProbe {
	var probes []gridProbe
	for _, name := range gridSortedKeys(functionRegistry) {
		spec := functionRegistry[name]
		if spec.gen == nil || spec.gen.maxArity != -1 {
			continue
		}
		recipe := *spec.gen
		for _, pair := range gridMixedPairs() {
			for _, order := range [][2]gridCell{
				{pair[0], pair[1]}, {pair[1], pair[0]},
			} {
				sql := recipe.spelling + "(" + order[0].column + ", " + order[1].column + ")"
				probes = append(probes, gridProbe{
					id: "var/" + name + "/" + order[0].wrapper + ":" + order[0].base +
						"+" + order[1].wrapper + ":" + order[1].base,
					family:    "var",
					entry:     name,
					sql:       sql,
					aggregate: recipe.place == placementAggregate,
				})
			}
		}
	}
	return probes
}

// gridMixedPairs gives the unordered pairs of fixture columns that the
// variadic lane mixes.
//
// The pairs are chosen, not enumerated in full: a full cross product of
// 78 columns would be 3003 pairs for every variadic entry. Each pair
// below separates one rule that a same-type grid cannot separate.
func gridMixedPairs() [][2]gridCell {
	byKey := map[string]gridCell{}
	for _, cell := range gridColumns() {
		byKey[cell.wrapper+"/"+cell.base] = cell
	}
	want := [][2]string{
		// Integer widening. A rule that reads one position answers
		// Int32; the supertype is Int64 for the unsigned mate.
		{"bare/i32", "bare/u64"},
		// Integer against float. The supertype is Float64.
		{"bare/i32", "bare/f64"},
		// Integer against Decimal.
		{"bare/i32", "bare/dec"},
		// String against FixedString.
		{"bare/s", "bare/fs"},
		// Nullable against bare. A rule that reads one position drops
		// the Nullable of the other.
		{"bare/i32", "n/i32"},
		// LowCardinality against bare. The measured rule differs per
		// family, and only a mixed pair shows which side wins.
		{"bare/s", "lc/s"},
		// LowCardinality(Nullable) against bare.
		{"bare/s", "lcn/s"},
		// Date against DateTime.
		{"bare/d", "bare/dt"},
		// SimpleAggregateFunction against its own bare inner type.
		{"bare/i32", "saf/i32"},
	}
	var pairs [][2]gridCell
	for _, w := range want {
		left, leftOK := byKey[w[0]]
		right, rightOK := byKey[w[1]]
		if leftOK && rightOK {
			pairs = append(pairs, [2]gridCell{left, right})
		}
	}
	return pairs
}

// gridSizedProbes enumerates sizedConstructors.
//
// The size argument is a constant and the data argument is a column. The
// constant stays small, because toDecimal32 refuses a scale above 9 and
// that refusal would be recorded as a type answer for every Decimal
// entry.
func gridSizedProbes(schema *Schema) []gridProbe {
	var probes []gridProbe
	for _, name := range gridSortedKeys(sizedConstructors) {
		constructor := sizedConstructors[name]
		for _, wrapper := range gridWrappers {
			candidates := gridProbeColumnsFor(constructor.domain, wrapper.key, schema)
			for _, cell := range gridRepresentativeCells(candidates, constructor.domain, schema) {
				call := constructor.spelling + "(" + cell.column
				if constructor.kind != sizedConstructorNone {
					call += ", 2"
				}
				call += ")"
				probes = append(probes, gridProbe{
					id:     "sized/" + name + "/" + wrapper.key + "/" + cell.base,
					family: "sized",
					entry:  name,
					sql:    call,
				})
			}
		}
	}
	return probes
}

// gridHigherOrderProbes enumerates higherOrderArrayFunctions.
//
// The data argument must be an Array, thus the wrapper alphabet applies
// to the ELEMENT type and not to the array. The lane varies the lambda
// body: an identity body, a predicate body and a body that changes the
// element type. The three bodies separate the four measured result
// shapes of the family.
//
// The lane also varies the ELEMENT WRAPPER of the array, over the arrays
// that the fixture declares. Until the narr and lcarr bases existed, the
// only array of the grid was Array(Int32), whose element is bare, thus
// no cell of this family could ask what a higher-order call does to an
// element wrapper. That blindness is the subject of the regression.
//
// The measured answers show why the two new arrays are not one rule
// twice. On ClickHouse 25.8.29.51:
//
//	arrayMap(x -> x, Array(LowCardinality(String)))  Array(String)
//	arrayMap(x -> x, Array(Nullable(Int32)))         Array(Nullable(Int32))
//
// The element LowCardinality is dropped and the element Nullable is
// kept. One array alone could not separate "drops every element wrapper"
// from "drops LowCardinality only".
func gridHigherOrderProbes() []gridProbe {
	var probes []gridProbe
	bodies := []struct {
		key  string
		text string
	}{
		{"identity", "x -> x"},
		{"predicate", "x -> x > 1"},
		{"widen", "x -> toFloat64(x)"},
	}
	// Only the arrays that the fixture really declares are used. The
	// lane asks gridHasColumn instead of naming a column blindly,
	// because saf is NOT legal over every array base: the server
	// answers Code 36 for
	// SimpleAggregateFunction(anyLast, Array(LowCardinality(String))).
	var arrays []string
	for _, candidate := range []string{
		gridColumnName("bare", "arr"),
		gridColumnName("saf", "arr"),
		gridColumnName("bare", "narr"),
		gridColumnName("saf", "narr"),
		gridColumnName("bare", "lcarr"),
	} {
		if gridHasColumn(candidate) {
			arrays = append(arrays, candidate)
		}
	}
	for _, name := range gridSortedKeys(higherOrderArrayFunctions) {
		spelling := gridHigherOrderSpelling(name)
		for _, body := range bodies {
			for _, array := range arrays {
				probes = append(probes, gridProbe{
					id:     "hof/" + name + "/" + body.key + "/" + array,
					family: "hof",
					entry:  name,
					sql:    spelling + "(" + body.text + ", " + array + ")",
				})
			}
		}
	}
	return probes
}

// gridHigherOrderSpelling gives the server spelling of a higher-order
// array function. The registry key is lowercased and ClickHouse function
// names are case sensitive, thus the key alone cannot be written into
// SQL. The table is derived from the registry recipe when the entry has
// one, so the two cannot drift.
func gridHigherOrderSpelling(name string) string {
	if recipe, ok := genSpecFor(name); ok && recipe.spelling != "" {
		return recipe.spelling
	}
	return gridHigherOrderSpellings[name]
}

// gridHigherOrderSpellings is the fallback table for the higher-order
// entries that carry no generator recipe.
var gridHigherOrderSpellings = map[string]string{
	"arraymap": "arrayMap", "arrayfilter": "arrayFilter",
	"arraysort": "arraySort", "arraysum": "arraySum",
	"arraymin": "arrayMin", "arraymax": "arrayMax",
	"arraycount": "arrayCount", "arrayexists": "arrayExists",
	"arrayall": "arrayAll",
}

// gridSimpleStateProbes enumerates simpleStateSupportedBases.
//
// Each base aggregate is measured under the -SimpleState combinator over
// the wrapper alphabet. The value column is an integer, because every
// base of this list accepts an integer while only some accept a string,
// and a refusal that comes from the base type would hide the combinator
// rule that this lane measures.
//
// The lane deliberately does NOT build a state and merge it in one
// SELECT. `sumMerge(sumState(i32))` is Code 184, ILLEGAL_AGGREGATION,
// because a state cannot be built in the SELECT that merges it. That
// error is about aggregation context and says nothing about typing, thus
// such a cell would bank noise as coverage.
func gridSimpleStateProbes() []gridProbe {
	var probes []gridProbe
	for _, name := range gridSortedKeys(simpleStateSupportedBases) {
		for _, wrapper := range gridWrappers {
			for _, baseKey := range []string{"i32", "u64"} {
				column := gridColumnName(wrapper.key, baseKey)
				if !gridHasColumn(column) {
					continue
				}
				probes = append(probes, gridProbe{
					id:        "simplestate/" + name + "/" + wrapper.key + "/" + baseKey,
					family:    "simplestate",
					entry:     name,
					sql:       gridSimpleStateSpelling(name) + "SimpleState(" + column + ")",
					aggregate: true,
				})
			}
		}
	}
	return probes
}

// gridSimpleStateSpelling gives the server spelling of a base aggregate.
var gridSimpleStateSpellings = map[string]string{
	"any": "any", "anylast": "anyLast", "min": "min", "max": "max",
	"sum": "sum", "groupbitand": "groupBitAnd",
	"groupbitor": "groupBitOr", "groupbitxor": "groupBitXor",
}

func gridSimpleStateSpelling(name string) string {
	return gridSimpleStateSpellings[name]
}

// gridHasColumn reports whether the fixture holds a column. The wrapper
// alphabet is not legal over every base type, so a lane that names a
// column directly must ask first.
func gridHasColumn(name string) bool {
	for _, cell := range gridColumns() {
		if cell.column == name {
			return true
		}
	}
	return false
}

// gridOperatorProbes enumerates operatorCatalog.
//
// Both operands take the SAME wrapper, and the base types come in pairs
// that suit the family: an arithmetic operator needs numbers, and a LIKE
// operator needs strings. An operator applied to an operand type that the
// server refuses is still enumerated, because the refusal code is the
// evidence that derives the operand domain.
//
// The refusal family (`::` and `->`) is EXCLUDED. The right operand of
// `::` is a type name and the left operand of `->` is a lambda parameter,
// thus neither operand is a value and neither cell measures a type rule.
func gridOperatorProbes() []gridProbe {
	var probes []gridProbe
	for _, spec := range operatorCatalog {
		if spec.family == opRefusal {
			continue
		}
		for _, wrapper := range gridWrappers {
			for _, baseKey := range gridOperatorBases(spec.family) {
				column := gridColumnName(wrapper.key, baseKey)
				if !gridHasColumn(column) {
					continue
				}
				sql, ok := gridRenderOperator(spec.token, column)
				if !ok {
					continue
				}
				probes = append(probes, gridProbe{
					id:     "op/" + gridOperatorKey(spec.token) + "/" + wrapper.key + "/" + baseKey,
					family: "op",
					entry:  spec.token,
					sql:    sql,
				})
			}
		}
	}
	probes = append(probes, gridLogicOperatorConstantOperandProbes()...)
	return probes
}

// gridLogicOperatorConstantOperandProbes is the regression lane: AND and OR
// against a CONSTANT second operand, for example "c_lc_i32 AND 1".
//
// gridRenderOperator always puts the SAME non-constant column on both
// sides ("column OP column"), so a wrapperKeep and a wrapperDrop
// LowCardinality disposition are indistinguishable at every cell
// gridOperatorProbes produced before this lane: inferBinaryOperationType
// precomputes lowCardinalityCount==1 && othersConstant BEFORE it asks the
// transport, and two non-constant LowCardinality operands make that
// candidate false at every such cell, whatever the transport says.
//
// "column AND 1" is the one shape that makes the candidate true: ONE
// LowCardinality operand and a constant on the other side. The server
// accepts it, and it types the same LowCardinality-preserving path that
// the general "one LowCardinality operand, all others constant" rule
// gives a comparison operator (measured: lc = 'a' is
// LowCardinality(UInt8)). AND and OR instead REMOVE the wrapper
// unconditionally, which is exactly the rule this lane makes visible, by
// giving wrapperKeep and wrapperDrop a cell where they answer
// differently:
//
//	c_lc_i32 AND 1   UInt8                    measured on 25.8.29.51
//
// Only AND and OR need the lane: they are the two names whose transport
// (logicOperatorTransport) disagrees with the general rule. IN, the
// comparisons and the LIKE family already have a cell that tells
// wrapperKeep from wrapperDrop, because gridRenderOperator's constant
// right-hand side for LIKE and the subquery form for IN already vary the
// "othersConstant" fact; see gridOperatorBases and gridRenderOperator.
// Adding the lane to every opPredicate member would duplicate coverage
// those members already have and would not close any additional gap.
//
// The base is restricted to the bases that isLogicOperandType accepts
// (i32 and b): a constant "column AND 1" over a String base refuses
// (measured: c_lc_s AND 1 is Code: 43), and a refusal cell measures the
// domain, not the LowCardinality disposition that this lane exists to
// show. "s" stays in gridOperatorBases for the predicate family generally
// (LIKE and the comparisons need it), but it is excluded HERE.
func gridLogicOperatorConstantOperandProbes() []gridProbe {
	var probes []gridProbe
	for _, token := range []string{"AND", "OR"} {
		for _, wrapper := range gridWrappers {
			for _, baseKey := range []string{"i32", "b"} {
				column := gridColumnName(wrapper.key, baseKey)
				if !gridHasColumn(column) {
					continue
				}
				constant, ok := gridBaseSeed(baseKey)
				if !ok {
					continue
				}
				sql := column + " " + token + " " + constant
				probes = append(probes, gridProbe{
					id:     "op/" + gridOperatorKey(token) + "/" + wrapper.key + "-const/" + baseKey,
					family: "op",
					entry:  token,
					sql:    sql,
				})
			}
		}
	}
	return probes
}

// gridBaseSeed reports the constant SQL literal of one base by its key,
// and false when the key is not in the alphabet. It reads gridBases
// directly, so a probe that needs a literal operand cannot drift from the
// seed that the fixture itself uses for the same base.
func gridBaseSeed(baseKey string) (string, bool) {
	for _, base := range gridBases {
		if base.key == baseKey {
			return base.seed, true
		}
	}
	return "", false
}

// gridOperatorBases gives the representative base types of an operator
// family. An arithmetic operator over a string is a refusal that says
// nothing new, while a comparison over both a number and a string is two
// different measured rules.
func gridOperatorBases(family opFamily) []string {
	switch family {
	case opArith:
		return []string{"i32", "f64", "dec"}
	case opConcat:
		return []string{"s", "i32"}
	default:
		return []string{"i32", "s", "b"}
	}
}

// gridRenderOperator writes one operator expression over a fixture
// column. The right operand is the SAME column, never a literal:
// ClickHouse folds a constant and a folded constant reports a different
// LowCardinality wrapper, thus a literal operand would measure the
// folding rule instead of the operator rule.
func gridRenderOperator(token, column string) (string, bool) {
	switch token {
	case "IN", "NOT IN":
		// A subquery keeps both sides real. A literal tuple would be
		// folded.
		return column + " " + token + " (SELECT " + column + " FROM g)", true
	case "LIKE", "NOT LIKE", "ILIKE", "NOT ILIKE", "REGEXP":
		// The pattern of these operators is a constant by
		// construction; the server has no column form. The measured
		// operand is the LEFT one, which stays a column.
		return column + " " + token + " 'a'", true
	}
	return column + " " + token + " " + column, true
}

// gridOperatorKey turns an operator token into a text that is safe inside
// a cell address. The golden file is line oriented, thus a token must not
// bring a separator or a space into the address.
func gridOperatorKey(token string) string {
	replacer := strings.NewReplacer(
		" ", "_", "=", "eq", "<", "lt", ">", "gt", "!", "not",
		"+", "plus", "-", "minus", "*", "star", "/", "slash",
		"%", "pct", "|", "pipe",
	)
	return replacer.Replace(token)
}
