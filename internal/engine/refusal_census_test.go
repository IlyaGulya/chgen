package engine

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// The refusal-surface census.
//
// CHGEN_ERROR in the fuzz oracle counts the refusals that the generator
// DRAWS. It is a real instrument, but it is blind by construction: a
// name that the generator cannot spell is never drawn, thus its refusal
// is never counted. The 66 names of the reachability gate are exactly
// that blind spot, and no number in this project said how large the
// blind spot is or whether it grows.
//
// This census is the quantitative sibling of gen_reach_gate_test.go.
// The gate asks "can the generator reach this rule?" and answers name by
// name. The census asks "how much of each inference table is dark?" and
// answers with a number that CI history keeps.
//
// A refusal is the SAFE defect class. That is why refusals accumulate
// with nobody counting them. The census makes the count visible.
//
// WHAT THE CENSUS MEASURES
//
// Three facets of one registry entry, each a different kind of dark:
//
//   - no rule: the entry gives no result type through the generic
//     rule path. This is NOT always a gap. inferFunctionType routes
//     some names to their own inference function before the registry
//     lookup, and such a name legally carries a nil rule.
//   - no domain: no measured argument domain. The resolver then
//     accepts every argument type, and the generator has no column
//     pool to draw from.
//   - no genSpec: no recipe that says how to SPELL the call, thus the
//     generator can never write it and the fuzz oracle can never
//     reach it.
//
// The entry that is dark on BOTH the domain facet and the genSpec facet
// is the one that matters most: nothing measured its arguments and
// nothing can draw it, so no instrument in this project observes it at
// all. The census calls such an entry DARK.
//
// WHY THE CENSUS IS A REPORT PLUS ONE DERIVED CEILING
//
// A FLOOR ("at least N entries must carry a domain") is wrong here. It
// passes for free on the day it is written and it says nothing about
// growth.
//
// A HARDCODED CEILING ("no more than 59 dark entries") is worse. It is a
// number that another commit invalidates: give the sized constructors
// their recipes and the ceiling is stale, so the next author edits the
// constant, and a census that must be edited on every registry change is
// a census that gets deleted.
//
// A PURE REPORT alone is not a gate. Nobody reads a log line that never
// fails.
//
// The census therefore reports every number and enforces ONE invariant
// that needs no constant:
//
//	every DARK registry entry must already be named in
//	genReachExclusions, with its reason.
//
// This bound is derived, not declared. It holds because an entry with no
// genSpec cannot be generator reachable, thus the reachability gate
// already demands a written reason for it. The census adds no second
// list to keep in step; it reuses the list that the gate already keeps
// honest with its own staleness check.
//
// The invariant has the properties that a good gate needs:
//
//   - It does NOT fire on a legitimate registry addition. A new entry
//     with a domain or with a genSpec is not dark and the census is
//     silent.
//   - It DOES fire on a new unmeasured, undrawable entry, which is
//     exactly the silent growth of the refusal surface.
//   - It relaxes by itself. When another author gives the sized
//     constructors their genSpecs, those entries stop being dark, the
//     dark count falls, and nothing here needs an edit. The census
//     READS the tables; it hardcodes no count.
//
// The census runs in the DEFAULT go test run. The fuzz oracle is behind
// a build tag, and a census that only ran under that tag would be
// invisible where the refusal surface actually grows.

// tableCensus holds the measured numbers of one inference table.
type tableCensus struct {
	// table is the Go name of the table.
	table string
	// total is the number of names in the table.
	total int
	// inRegistry is the number of those names that also have a
	// functionRegistry entry. Only such a name can carry a rule, a
	// domain or a genSpec, thus the facet counts below are out of
	// this number and not out of total.
	inRegistry int
	// noRule, noDomain and noGen count the facets that are absent.
	noRule, noDomain, noGen int
	// dark counts the names with neither a domain nor a genSpec.
	dark int
	// darkNames lists them, sorted, for the report.
	darkNames []string
}

// censusOfTable measures one table against functionRegistry.
func censusOfTable(table string, names []string) tableCensus {
	result := tableCensus{table: table, total: len(names)}
	for _, name := range names {
		spec, ok := functionRegistry[name]
		if !ok {
			continue
		}
		result.inRegistry++
		if spec.rule == nil {
			result.noRule++
		}
		if spec.domain == nil {
			result.noDomain++
		}
		if spec.gen == nil {
			result.noGen++
		}
		if spec.domain == nil && spec.gen == nil {
			result.dark++
			result.darkNames = append(result.darkNames, name)
		}
	}
	sort.Strings(result.darkNames)
	return result
}

// refusalCensus measures the same four inference tables that the
// reachability gate and the function-name guard cover. A census over
// fewer tables would report health that it did not measure.
func refusalCensus() []tableCensus {
	measured := make([]tableCensus, 0, 4)
	for _, entry := range genReachTables() {
		names := entry.names
		sort.Strings(names)
		measured = append(measured, censusOfTable(entry.table, names))
	}
	return measured
}

// TestRefusalSurfaceCensus logs the size of the refusal surface of each
// inference table, so CI history holds the trend, and it fails only when
// a DARK entry has no reason in genReachExclusions.
func TestRefusalSurfaceCensus(t *testing.T) {
	measured := refusalCensus()

	var report strings.Builder
	report.WriteString("refusal-surface census over the four inference tables\n")
	report.WriteString(fmt.Sprintf("%-27s %6s %11s %8s %10s %7s %6s\n",
		"table", "names", "inRegistry", "noRule", "noDomain", "noGen", "dark"))

	totals := tableCensus{table: "ALL"}
	for _, one := range measured {
		report.WriteString(fmt.Sprintf("%-27s %6d %11d %8d %10d %7d %6d\n",
			one.table, one.total, one.inRegistry, one.noRule, one.noDomain, one.noGen, one.dark))
		totals.total += one.total
		totals.inRegistry += one.inRegistry
		totals.noRule += one.noRule
		totals.noDomain += one.noDomain
		totals.noGen += one.noGen
		totals.dark += one.dark
	}
	report.WriteString(fmt.Sprintf("%-27s %6d %11d %8d %10d %7d %6d\n",
		totals.table, totals.total, totals.inRegistry, totals.noRule,
		totals.noDomain, totals.noGen, totals.dark))
	report.WriteString("\ndark = no measured argument domain AND no generator recipe: " +
		"no instrument in this project observes such an entry.\n")
	report.WriteString("A name that two tables hold is counted once per table, " +
		"thus ALL counts name USES and not distinct names.\n")
	t.Log("\n" + report.String())

	// The one enforced invariant. It needs no constant, and it falls
	// by itself when an entry gains a domain or a recipe.
	for _, one := range measured {
		for _, name := range one.darkNames {
			reason, excluded := genReachExclusions[name]
			if !excluded {
				t.Errorf("%s: %q has no measured argument domain and no generator recipe, "+
					"thus nothing in this project observes it, and genReachExclusions does not "+
					"name it either; measure a domain, or add a genSpec, or add an exclusion "+
					"with a reason so the gap is recorded instead of silent", one.table, name)
				continue
			}
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s: %q is dark and its exclusion has an empty reason", one.table, name)
			}
		}
	}
}

// TestRefusalCensusReadsTheLiveTables keeps the census itself honest.
//
// A census that measured an empty set would log zeros and pass, and the
// zeros would read as health. This test states the minimum facts that
// make the numbers meaningful: every table has names, and the census
// found registry entries to measure the facets on. It fixes no count,
// because a fixed count is the very thing that makes an instrument rot.
func TestRefusalCensusReadsTheLiveTables(t *testing.T) {
	measured := refusalCensus()

	if len(measured) != 4 {
		t.Fatalf("the census covers %d tables, but the four inference tables are "+
			"functionRegistry, higherOrderArrayFunctions, sizedConstructors and "+
			"simpleStateSupportedBases", len(measured))
	}

	registryEntries := 0
	for _, one := range measured {
		if one.total == 0 {
			t.Errorf("%s: the census read no names from this table; a census over an "+
				"empty set logs zeros that read as health", one.table)
		}
		registryEntries += one.inRegistry
	}
	if registryEntries == 0 {
		t.Error("the census found no functionRegistry entry at all, thus every facet " +
			"count is zero for the wrong reason")
	}
}
