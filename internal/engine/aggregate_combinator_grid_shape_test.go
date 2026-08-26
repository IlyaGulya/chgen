package engine

// The shape gates of the combinator grid.
//
// These tests need NO server. They run in the default `go test ./...`
// run and they guard the properties that make the combinator golden
// trustworthy: the cell addresses are unique and stable, the enumeration
// matches the sampled fuzz lane it promotes, and the closed
// suffix-by-base product cannot shrink without a reviewer seeing it.
//
// The MEASUREMENT itself is in aggregate_combinator_grid_test.go, behind
// the fuzzoracle tag.

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestCombGridBasesMatchTheFuzzLane holds combGridBases against
// combinatorBaseSpellings, the list the tagged fuzz oracle already
// samples (typeoracle_fuzz_test.go via gen_draw_test.go). This grid
// promotes that SAME lane to a committed product; a base added to one
// list and not the other would mean the grid measures a different
// alphabet than the lane it claims to promote.
func TestCombGridBasesMatchTheFuzzLane(t *testing.T) {
	want := combinatorBaseSpellings()
	got := combGridBases()
	if len(want) != len(got) {
		t.Fatalf("combGridBases has %d entries, combinatorBaseSpellings has %d; "+
			"the grid must promote the exact lane the fuzz oracle already samples",
			len(got), len(want))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Errorf("position %d: combGridBases has %q, combinatorBaseSpellings has %q",
				i, got[i], want[i])
		}
	}
}

func TestCombGridCoversAllMeasuredBasesWithoutStateSkips(t *testing.T) {
	if got := len(combGridBases()); got != 19 {
		t.Fatalf("combinator base count = %d, want 19", got)
	}
	if skipped := combGridSkippedProductCells(); len(skipped) != 0 {
		t.Fatalf("combinator grid still skips %d state cells: %v", len(skipped), skipped)
	}
}

// TestCombGridSuffixesMatchTheFuzzLane holds combGridSuffixes against the
// spelled values of combinatorSuffixSpellings, so a suffix the dispatch
// gains is fuzzed by the sampled lane AND measured exhaustively by this
// grid at the same time, never one without the other.
func TestCombGridSuffixesMatchTheFuzzLane(t *testing.T) {
	spellings := combinatorSuffixSpellings()
	want := map[string]bool{}
	for _, spelling := range spellings {
		want[spelling] = true
	}
	got := map[string]bool{}
	for _, spelling := range combGridSuffixes() {
		got[spelling] = true
	}
	for spelling := range want {
		if !got[spelling] {
			t.Errorf("combinatorSuffixSpellings spells %q and combGridSuffixes does not; "+
				"the closed product would not measure this suffix", spelling)
		}
	}
	for spelling := range got {
		if !want[spelling] {
			t.Errorf("combGridSuffixes spells %q and combinatorSuffixSpellings does not know it; "+
				"the grid would measure a suffix the dispatch table does not name", spelling)
		}
	}
}

// TestCombGridCellAddressesAreUnique guards the golden's key space: two
// cells sharing an address would silently overwrite one another.
func TestCombGridCellAddressesAreUnique(t *testing.T) {
	seen := map[string]string{}
	for _, probe := range combGridBuildProbes() {
		if previous, clash := seen[probe.id]; clash {
			t.Errorf("cell address %q is used twice:\n  %s\n  %s", probe.id, previous, probe.sql)
			continue
		}
		seen[probe.id] = probe.sql
	}
}

// TestCombGridEnumerationIsDeterministic guards byte reproducibility: Go
// randomizes map iteration order, and this grid's skip-reason table is a
// map, so a missing sort could give a different cell ORDER across runs.
func TestCombGridEnumerationIsDeterministic(t *testing.T) {
	first := combGridBuildProbes()
	for attempt := 0; attempt < 4; attempt++ {
		again := combGridBuildProbes()
		if len(again) != len(first) {
			t.Fatalf("attempt %d made %d cells, the first build made %d", attempt, len(again), len(first))
		}
		for i := range first {
			if first[i].id != again[i].id || first[i].sql != again[i].sql {
				t.Fatalf("attempt %d differs at position %d:\n  first: %s %s\n  again: %s %s",
					attempt, i, first[i].id, first[i].sql, again[i].id, again[i].sql)
			}
		}
	}
}

// TestCombGridProductAccountsForEveryCell is the alphabet-closure gate.
// It asserts over the CLOSED suffix-by-base product directly, not over
// the golden header: every (base, suffix) pair must either be spelled by
// combGridRenderProduct or be named, with a live reason, in
// combGridSkippedProductCells. A regeneration of the golden cannot make
// an unaddressed pair disappear from this walk, because the walk never
// reads the golden.
func TestCombGridProductAccountsForEveryCell(t *testing.T) {
	skipped := combGridSkippedProductCells()
	var unaccounted []string
	for _, base := range combGridBases() {
		for _, suffix := range combGridSuffixes() {
			pair := base + "/" + suffix
			if _, ok := combGridRenderProduct(base, suffix); ok {
				continue
			}
			if _, named := skipped[pair]; named {
				continue
			}
			unaccounted = append(unaccounted, pair)
		}
	}
	if len(unaccounted) > 0 {
		sort.Strings(unaccounted)
		t.Errorf("%d (base, suffix) pairs of the closed product are neither spelled nor named "+
			"in combGridSkippedProductCells: %s", len(unaccounted), strings.Join(unaccounted, " "))
	}
}

// TestCombGridSortedSkippedCellsIsDeterministic guards the golden
// header's byte reproducibility: Go randomizes map iteration order, so
// the "# skipped" lines must be read through combGridSortedSkippedCells,
// never through a raw range over combGridSkippedProductCells.
func TestCombGridSortedSkippedCellsIsDeterministic(t *testing.T) {
	first := combGridSortedSkippedCells()
	for attempt := 0; attempt < 4; attempt++ {
		again := combGridSortedSkippedCells()
		if len(again) != len(first) {
			t.Fatalf("attempt %d gave %d entries, the first call gave %d", attempt, len(again), len(first))
		}
		for i := range first {
			if first[i] != again[i] {
				t.Fatalf("attempt %d differs at position %d: first=%+v again=%+v",
					attempt, i, first[i], again[i])
			}
		}
	}
}

// TestCombGridSkipListIsLive keeps combGridSkippedProductCells from
// growing stale: an entry that the product now spells is dead weight
// that hides real coverage.
func TestCombGridSkipListIsLive(t *testing.T) {
	for pair := range combGridSkippedProductCells() {
		parts := strings.SplitN(pair, "/", 2)
		if len(parts) != 2 {
			t.Errorf("malformed skip-list key %q", pair)
			continue
		}
		if _, ok := combGridRenderProduct(parts[0], parts[1]); ok {
			t.Errorf("combGridSkippedProductCells names %q as unspellable, "+
				"but combGridRenderProduct now spells it; delete the stale entry", pair)
		}
	}
}

// TestCombGridEveryMergeBaseHasAStateColumn holds
// combGridMergeStateColumn against the fixture DDL: a base this function
// claims to reach must name a column that combGridSchemaDDL actually
// declares, or a -Merge cell would read a column that does not exist.
func TestCombGridEveryMergeBaseHasAStateColumn(t *testing.T) {
	ddl := combGridSchemaDDL()
	for _, base := range combGridBases() {
		column, ok := combGridMergeStateColumn(base)
		if !ok {
			continue
		}
		if !strings.Contains(ddl, column+" AggregateFunction") {
			t.Errorf("combGridMergeStateColumn(%q) names column %q, "+
				"which the fixture DDL does not declare as an AggregateFunction column", base, column)
		}
	}
}

// TestCombGridGoldenDoesNotShrink reads the per-family counts from the
// golden header and fails the DEFAULT test run, without a server, when
// the live enumeration makes fewer cells than the golden recorded for
// any family. A count that only ever falls without a reason is how
// coverage dies.
func TestCombGridGoldenDoesNotShrink(t *testing.T) {
	recorded, total, ok := combGridGoldenFamilyCounts(t)
	if !ok {
		t.Skip("the combinator golden is absent; generate it with -chgen-grid-regenerate")
	}
	live := map[string]int{}
	for _, probe := range combGridBuildProbes() {
		live[probe.family]++
	}
	liveTotal := 0
	for _, count := range live {
		liveTotal += count
	}
	for family, want := range recorded {
		got := live[family]
		if got < want {
			t.Errorf("family %q shrank: the golden holds %d cells, the enumeration now makes %d",
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
		t.Errorf("the combinator grid shrank in total: the golden holds %d cells, "+
			"the enumeration now makes %d", total, liveTotal)
	}
}

// combGridGoldenFamilyCounts reads the per-family counts and the total
// from the golden header.
func combGridGoldenFamilyCounts(t *testing.T) (map[string]int, int, bool) {
	t.Helper()
	file, err := os.Open(filepath.FromSlash(combGridGoldenPath))
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
				t.Fatalf("malformed family count in the combinator golden: %q", line)
			}
			counts[fields[1]] = value
		case len(fields) == 2 && fields[0] == "cells":
			value, err := strconv.Atoi(fields[1])
			if err != nil {
				t.Fatalf("malformed cell count in the combinator golden: %q", line)
			}
			total = value
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read the combinator golden header: %v", err)
	}
	return counts, total, len(counts) > 0
}

// TestCombGridGoldenCarriesTheRegenerationBanner keeps the golden's
// header honest: a rewrite that dropped the warning could not pass.
func TestCombGridGoldenCarriesTheRegenerationBanner(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(combGridGoldenPath))
	if err != nil {
		t.Skip("the combinator golden is absent; generate it with -chgen-grid-regenerate")
	}
	for _, want := range []string{
		"DO NOT EDIT BY HAND",
		"-chgen-grid-regenerate",
		"clickhouse_version",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the combinator golden header lost %q", want)
		}
	}
}

// TestCombGridNegativeProbesCoverTheTicketedDefects asserts that the
// negative-dispatch lane names the exact cells the regression requires: a
// mismatched -Merge name, an ALIAS-matched -Merge name, a container
// -OrNull refusal and both orders of the quantile State/If combinator
// chain. This test needs no server: it is a property of the probe list
// itself, not of a measured answer.
func TestCombGridNegativeProbesCoverTheTicketedDefects(t *testing.T) {
	ids := map[string]bool{}
	for _, probe := range combGridNegativeProbes() {
		ids[probe.id] = true
	}
	for _, want := range []string{
		"negative/merge-mismatch/sum-over-quantile",
		"negative/merge-mismatch/uniq-over-sum",
		"negative/merge-alias/median-over-quantile",
		"negative/ornull-container/groupuniqarray",
		"negative/ornull-container/grouparray",
		"negative/ornull-scalar-control/sum",
		"negative/combinator-order/quantile-stateif",
		"negative/combinator-order/quantile-ifstate",
	} {
		if !ids[want] {
			t.Errorf("the negative-dispatch lane is missing the required cell %q", want)
		}
	}
}
