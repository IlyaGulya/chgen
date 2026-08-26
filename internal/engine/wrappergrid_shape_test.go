package engine

// The shape gates of the wrapper grid.
//
// These tests need NO server. They run in the default `go test ./...`
// run and they guard the properties that make the golden trustworthy:
// the cell addresses are unique and stable, the enumeration is
// deterministic, and the grid cannot shrink without a reviewer seeing it.
//
// The MEASUREMENT itself is in wrappergrid_test.go behind the fuzzoracle
// tag. Splitting the two is deliberate: a property that can be checked
// without a server must be checked on every commit, not only when
// somebody starts a ClickHouse.

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// gridTestSchema parses the grid fixture DDL into a chgen catalog.
func gridTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, gridSchemaDDL())
	if err != nil {
		t.Fatalf("the grid fixture DDL does not parse: %v", err)
	}
	return schema
}

// The fixture DDL must be derived from the cell list, so that a column
// cannot exist without a cell and a cell cannot name a missing column. A
// hand-written DDL beside a hand-written cell list is the duplication
// that this grid exists to remove.
func TestGridFixtureHasExactlyTheEnumeratedColumns(t *testing.T) {
	schema := gridTestSchema(t)
	table, ok := schema.Tables["g"]
	if !ok {
		t.Fatal("the grid fixture has no table `g`")
	}
	cells := gridColumns()
	if len(table.Columns) != len(cells) {
		t.Fatalf("the fixture has %d columns but the enumeration makes %d cells",
			len(table.Columns), len(cells))
	}
	declared := map[string]bool{}
	for _, column := range table.Columns {
		declared[column.Name] = true
	}
	for _, cell := range cells {
		if !declared[cell.column] {
			t.Errorf("cell %s/%s names column %s, which the fixture does not declare",
				cell.wrapper, cell.base, cell.column)
		}
	}
}

// A cell address is the key of the golden file. Two cells that share an
// address would silently overwrite one another, and the grid would lose a
// measurement without any count changing.
func TestGridCellAddressesAreUnique(t *testing.T) {
	schema := gridTestSchema(t)
	seen := map[string]string{}
	for _, probe := range gridBuildProbes(schema) {
		if previous, clash := seen[probe.id]; clash {
			t.Errorf("cell address %q is used twice:\n  %s\n  %s",
				probe.id, previous, probe.sql)
			continue
		}
		seen[probe.id] = probe.sql
	}
}

// The enumeration must be deterministic. Go randomizes map iteration
// order, thus a walk that forgot to sort its keys would give a different
// cell ORDER on every run, and the golden file could never be
// byte-reproducible.
//
// The test builds the grid twice in one process and compares the ordered
// address list. Two builds in one process is the strongest available
// check: Go re-randomizes map order per range statement, so a missing
// sort shows here without needing a second process.
func TestGridEnumerationIsDeterministic(t *testing.T) {
	schema := gridTestSchema(t)
	first := gridBuildProbes(schema)
	for attempt := 0; attempt < 4; attempt++ {
		again := gridBuildProbes(schema)
		if len(again) != len(first) {
			t.Fatalf("attempt %d made %d cells, the first build made %d",
				attempt, len(again), len(first))
		}
		for i := range first {
			if first[i].id != again[i].id || first[i].sql != again[i].sql {
				t.Fatalf("attempt %d differs at position %d:\n  first: %s %s\n  again: %s %s",
					attempt, i, first[i].id, first[i].sql, again[i].id, again[i].sql)
			}
		}
	}
}

// Every probe must put a real COLUMN in the measured position. ClickHouse
// folds constants, and a folded constant reports a different
// LowCardinality wrapper, thus a cell built only from literals would
// measure the folding rule instead of the rule under test.
// The one exemption is a NULLARY entry. today() and row_number() take no
// value argument, thus there is no measured position to put a column in.
// Such a cell is addressed "…/nullary", and the address is what the test
// accepts: a cell that claims a wrapper and a base type must read a
// column, because otherwise the address would promise evidence that the
// SQL does not hold.
func TestGridProbesReadAFixtureColumn(t *testing.T) {
	schema := gridTestSchema(t)
	for _, probe := range gridBuildProbes(schema) {
		if strings.HasSuffix(probe.id, "/nullary") {
			// A nullary cell may still MENTION a column inside a
			// constant argument: countIf takes a predicate and no
			// value, thus its one argument is built over a column
			// although no wrapper of the alphabet reaches the result.
			// The address is honest either way, because it promises
			// no wrapper and no base type.
			continue
		}
		if !strings.Contains(probe.sql, "c_") {
			t.Errorf("cell %s has no fixture column: %s", probe.id, probe.sql)
		}
	}
}

// The no-shrink gate.
//
// The golden header records the cell count per family. This test reads
// those counts and compares them with the live enumeration. A grid that
// loses cells therefore fails the DEFAULT test run, without a server,
// and the failure names the family that shrank.
//
// A count that only ever goes down without a reason is how coverage dies.
// The gate makes such a fall a build failure instead of a silent loss.
func TestGridDoesNotShrink(t *testing.T) {
	recorded, total, ok := gridGoldenFamilyCounts(t)
	if !ok {
		t.Skip("the golden file is absent; generate it with -chgen-grid-regenerate")
	}
	schema := gridTestSchema(t)
	live := map[string]int{}
	for _, probe := range gridBuildProbes(schema) {
		live[probe.family]++
	}
	liveTotal := 0
	for _, count := range live {
		liveTotal += count
	}
	for family, want := range recorded {
		got := live[family]
		if got < want {
			t.Errorf("family %q shrank: the golden holds %d cells, the enumeration now makes %d.\n"+
				"A grid that loses cells loses coverage. If the loss is intended, say why "+
				"and regenerate the golden with -chgen-grid-regenerate.",
				family, want, got)
		}
	}
	for family, got := range live {
		if _, present := recorded[family]; !present {
			t.Errorf("family %q is new with %d cells and the golden does not hold it. "+
				"Regenerate the golden so the new cells are measured.", family, got)
		}
	}
	if liveTotal < total {
		t.Errorf("the grid shrank in total: the golden holds %d cells, the enumeration now makes %d",
			total, liveTotal)
	}
}

// gridGoldenFamilyCounts reads the per-family counts and the total from
// the golden header.
func gridGoldenFamilyCounts(t *testing.T) (map[string]int, int, bool) {
	t.Helper()
	file, err := os.Open(filepath.FromSlash(gridGoldenPath))
	if err != nil {
		return nil, 0, false
	}
	defer file.Close()
	counts := map[string]int{}
	total := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "#") {
			break
		}
		fields := strings.Split(strings.TrimPrefix(line, "# "), "\t")
		switch {
		case len(fields) == 3 && fields[0] == "family":
			value, err := strconv.Atoi(fields[2])
			if err != nil {
				t.Fatalf("malformed family count in the golden: %q", line)
			}
			counts[fields[1]] = value
		case len(fields) == 2 && fields[0] == "cells":
			value, err := strconv.Atoi(fields[1])
			if err != nil {
				t.Fatalf("malformed cell count in the golden: %q", line)
			}
			total = value
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read the golden header: %v", err)
	}
	return counts, total, len(counts) > 0
}

// The golden must never be edited by hand. The header says so, and this
// test keeps the header honest: it fails if the banner is missing, thus a
// rewrite that drops the warning cannot pass.
func TestGridGoldenCarriesTheRegenerationBanner(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(gridGoldenPath))
	if err != nil {
		t.Skip("the golden file is absent; generate it with -chgen-grid-regenerate")
	}
	for _, want := range []string{
		"DO NOT EDIT BY HAND",
		"-chgen-grid-regenerate",
		"clickhouse_version",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the golden header lost %q", want)
		}
	}
}

// The variadic lane must exist and must mix DIFFERENT base types.
//
// This lane is the one that catches the defect class where a rule reads
// only the first argument: `array(i8, i32)` gave Array(Int8) because
// arrayFunctionResult never looked past position one. A grid that gave
// one type to every position could not see that, thus the lane is a
// required property and not an optional extra.
func TestGridVariadicLaneMixesTypes(t *testing.T) {
	probes := gridVariadicProbes()
	if len(probes) == 0 {
		t.Fatal("the variadic lane is empty; the first-argument defect class is unreachable")
	}
	mixed := 0
	for _, probe := range probes {
		address := strings.TrimPrefix(probe.id, "var/")
		parts := strings.SplitN(address, "/", 2)
		if len(parts) != 2 {
			t.Errorf("malformed variadic address %q", probe.id)
			continue
		}
		operands := strings.Split(parts[1], "+")
		if len(operands) == 2 && operands[0] != operands[1] {
			mixed++
		}
	}
	if mixed != len(probes) {
		t.Errorf("only %d of %d variadic cells mix two different operands; "+
			"a same-type pair cannot show a rule that reads one position",
			mixed, len(probes))
	}
	// Both orders must be present. A rule that reads only the first
	// argument and a rule that reads only the last are different
	// defects, and one order would catch only one of them.
	seen := map[string]bool{}
	for _, probe := range probes {
		seen[probe.id] = true
	}
	reversed := 0
	for id := range seen {
		address := strings.TrimPrefix(id, "var/")
		parts := strings.SplitN(address, "/", 2)
		if len(parts) != 2 {
			continue
		}
		operands := strings.Split(parts[1], "+")
		if len(operands) != 2 {
			continue
		}
		if seen["var/"+parts[0]+"/"+operands[1]+"+"+operands[0]] {
			reversed++
		}
	}
	if reversed != len(probes) {
		t.Errorf("only %d of %d variadic cells have their mirror order; "+
			"without both orders the grid sees a first-argument rule but not a last-argument one",
			reversed, len(probes))
	}
}

// gridClosureExclusions names every (entry, wrapper, base kind) triple
// that the closed alphabet spells but that TestGridAddressesTheAlphabet
// finds absent from the enumeration ON PURPOSE, together with the reason.
//
// A triple belongs here only when the enumerator has a measured reason to
// refuse it, not because the reason is convenient. An entry added to this
// table without a real reason hides a shrink instead of explaining one.
//
// The table is checked for staleness by TestGridClosureExclusionsAreLive:
// every entry must still be absent from the live enumeration, or the
// exclusion is stale and must be deleted.
var gridClosureExclusions = map[string]string{
	// has takes the ELEMENT of a container in its second position
	// (gridElementArgumentEntries). A Tuple has no separate element
	// column: gridElementBaseOf("tup") names tup itself, which is the
	// SAME column as a bare Tuple argument. Sending it as the mate
	// would spell has(c_bare_tup, c_bare_tup), the Code 386 pair
	// refusal that gridElementColumnFor exists to prevent, so the
	// enumerator refuses the mate instead of repeating the container.
	// The tup kind IS reached for has under safarr, where the wrapper
	// adds an Array level and the mate is a genuinely different
	// column; see TestGridHasCellsSendTheElement.
	"has/bare/tup": "has has no distinct element column for a bare Tuple; sending the same column twice would spell the Code 386 repeat that gridElementColumnFor refuses",
	"has/saf/tup":  "same Code 386 repeat as has/bare/tup: the mate column for a bare Tuple element is the container column itself",
}

// gridAlphabetClosure walks the closure of the closed wrapper alphabet
// and reports every (lane, entry, wrapper, base kind) triple that the
// alphabet spells but that no probe in the given enumeration addresses
// and that gridClosureExclusions does not name.
//
// This is the ONE walk behind two call sites: TestGridAddressesTheAlphabet
// (below), which fails the default test run when the returned list is
// non-empty, and gridLegalButUnenumeratedCount in wrappergrid_test.go
// (fuzzoracle-tagged), which uses only the count for the golden header.
// Before this function existed, both call sites ran the same loop over
// the same sources (gridColumns, functionRegistry, sizedConstructors,
// gridWrappers, gridBaseKindOf, gridClosureExclusions) as two separate
// copies. One copy is the rule; both callers read it.
//
// TestGridDoesNotShrink compares the live enumeration against the counts
// recorded in the golden, so a blessed regeneration resets the baseline
// it checks against. The regression is the proof: the domain filter of
// gridProbeColumnsFor removed 240 cells, TestGridDoesNotShrink fired, and
// a regeneration satisfied it, because that gate never asked whether the
// alphabet still had unaddressed triples.
//
// This walk asks that question directly. For every entry of the fn and
// sized lanes (the two lanes that gridProbeColumnsFor and
// gridRepresentativeCells govern; the hof, op, simplestate and var lanes
// name their bases directly and no cap can starve them), and for every
// wrapper letter and base KIND that the closed alphabet spells for that
// wrapper, the triple must be enumerated at least once, or it must be
// named in gridClosureExclusions with a reason. A regeneration of the
// golden cannot make an unaddressed triple disappear from this walk,
// because the walk never reads the golden.
//
// The base KIND, not the base itself, is what closure asks for. Two
// bases of the same scalar kind are interchangeable for this question:
// the representative cap keeps exactly gridBasesPerCell of them and the
// alphabet does not promise that every individual scalar base reaches
// every entry, only that the SCALAR CLASS does. A container kind holds
// one base today, so kind and base agree there.
func gridAlphabetClosure(probes []gridProbe) []string {
	reached := map[string]bool{}
	for _, probe := range probes {
		if probe.family != "fn" && probe.family != "sized" {
			continue
		}
		parts := strings.Split(probe.id, "/")
		if len(parts) < 4 {
			// A nullary cell, for example "fn/now/nullary", spells no
			// base and cannot address a (entry, wrapper, kind) triple.
			continue
		}
		entry, wrapper, base := parts[1], parts[2], parts[3]
		reached[entry+"/"+wrapper+"/"+gridBaseKindOf(base)] = true
	}

	// legalKinds[wrapper] names every base kind the alphabet spells for
	// that wrapper, from gridColumns, which is the closed alphabet
	// itself and asks no server.
	legalKinds := map[string]map[string]bool{}
	for _, cell := range gridColumns() {
		if legalKinds[cell.wrapper] == nil {
			legalKinds[cell.wrapper] = map[string]bool{}
		}
		legalKinds[cell.wrapper][gridBaseKindOf(cell.base)] = true
	}

	var missing []string
	check := func(lane, name string) {
		for _, wrapper := range gridWrappers {
			for kind := range legalKinds[wrapper.key] {
				triple := name + "/" + wrapper.key + "/" + kind
				if reached[triple] {
					continue
				}
				if _, excluded := gridClosureExclusions[triple]; excluded {
					continue
				}
				missing = append(missing, lane+"/"+triple)
			}
		}
	}
	for _, name := range gridSortedKeys(functionRegistry) {
		spec := functionRegistry[name]
		if spec.gen == nil || gridRecipeIsNullary(*spec.gen) {
			continue
		}
		if _, higherOrder := higherOrderArrayFunctions[name]; higherOrder && spec.resultMode == resultRuleSpecialRoute {
			continue
		}
		check("fn", name)
	}
	for _, name := range gridSortedKeys(sizedConstructors) {
		check("sized", name)
	}

	sort.Strings(missing)
	return missing
}

// TestGridAddressesTheAlphabet asserts over the CLOSURE of the closed
// alphabet, not over the golden header. See gridAlphabetClosure for the
// walk itself; this test only turns a non-empty result into a failure.
func TestGridAddressesTheAlphabet(t *testing.T) {
	schema := gridTestSchema(t)
	missing := gridAlphabetClosure(gridBuildProbes(schema))
	if len(missing) == 0 {
		return
	}
	shown := missing
	if len(shown) > 20 {
		shown = shown[:20]
	}
	t.Errorf("%d (entry, wrapper, base kind) triples are legal in the closed alphabet "+
		"but no cell addresses them, and none is named in gridClosureExclusions.\n"+
		"A regeneration of the golden cannot fix this: either the enumerator must "+
		"reach the triple, or gridClosureExclusions must name it with a reason.\n"+
		"First offenders: %s", len(missing), strings.Join(shown, " "))
}

// TestGridClosureExclusionsAreLive keeps gridClosureExclusions from
// silently growing stale. An entry that the enumeration now reaches is
// dead weight: it hides that the triple is fine and it is one more line
// a reader must check by hand.
func TestGridClosureExclusionsAreLive(t *testing.T) {
	schema := gridTestSchema(t)
	reached := map[string]bool{}
	for _, probe := range gridBuildProbes(schema) {
		if probe.family != "fn" && probe.family != "sized" {
			continue
		}
		parts := strings.Split(probe.id, "/")
		if len(parts) < 4 {
			continue
		}
		entry, wrapper, base := parts[1], parts[2], parts[3]
		reached[entry+"/"+wrapper+"/"+gridBaseKindOf(base)] = true
	}
	for triple := range gridClosureExclusions {
		if reached[triple] {
			t.Errorf("gridClosureExclusions names %q as unenumerated, "+
				"but the enumeration now reaches it; delete the stale entry", triple)
		}
	}
}
