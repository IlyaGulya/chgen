package engine

import (
	"sort"
	"strings"
	"testing"
)

// The generator reachability gate.
//
// A type rule can only be tested by a fuzz case that reaches it. A rule
// that the expression generator can never write a call to is NEVER
// fuzzed, thus a defect in that rule stays silent. The gate names every
// such rule, so the gap is a number instead of a surprise.
//
// The gate covers the same four inference tables that the function-name
// guard covers: functionRegistry, higherOrderArrayFunctions,
// sizedConstructors and simpleStateSupportedBases. A gate over fewer
// tables would report health that it did not measure.
//
// The gate is STRUCTURAL. It asks whether the generator COULD build a
// call, not whether some run DID emit one. An emission count over N runs
// would be flaky, and it would trust luck.
//
// A name is reachable when the index holds a candidate for it and every
// value position of that candidate has a non-empty fixture column pool.
// Both facts are computed: the pool comes from the measured domain
// predicate, and the index never declares a copy of it.
//
// The gate runs in the DEFAULT go test run. The fuzzer is behind the
// fuzzoracle build tag, thus a gate inside the tagged file would be
// invisible where it matters.

// genReachExclusions names the rules that the generator cannot reach
// yet. Every entry needs a reason. An entry with an empty reason fails
// the gate, and so does an entry that IS reachable. The stale check is
// what makes the list shrink instead of rot.
//
// The list is honest on purpose. Step 4 of the migration adds the gate;
// step 7 shrinks the list one family at a time. A small list won by
// hand-waving would report coverage that does not exist.
//
// Some excluded names DO appear in a generated expression, for example
// arrayMap. They are written by the curated v2 lanes, which spell the
// call by hand. That is emission, not reachability: the gate asks
// whether the generator can build the call FROM THE INDEX, and none of
// these names carries a genSpec. The two questions must stay apart,
// because a hand written production covers one shape while a genSpec
// covers the whole domain.
//
// The list therefore shrinks by giving the entries a genSpec, and not by
// adding another hand written lane.
//
// STEP 7 REMOVED FAMILY 1, the 59 sized and cast constructors.
//
// Each of them now carries a genSpec that sizedConstructorGenSpec builds
// from the sizedConstructors table, and each carries a measured domain.
// The recipe marks the size position argSortConstInt, thus the generator
// writes an integer literal there and the constant reaches the result
// through the WRITTEN EXPRESSION: inference reads the same text that the
// generator produced. The result type is still never declared beside the
// rule, thus the seam that keeps the two descriptions of a type from
// disagreeing is unchanged.
//
// The three domains were measured by EXECUTION, not by toTypeName, which
// reports a type for calls the server then refuses while it runs them.
// See castTextArgumentDomain, wideIntegerArgumentDomain and
// decimalConstructorArgumentDomain in argument_domain.go.
//
// One family is left.
//
// Family 2: the higher-order array functions, 7 names. Each takes a
// lambda first, and the result depends on the TYPE OF THE LAMBDA BODY,
// not on the argument types alone. inferFunctionType routes these names
// to their own inference function before the registry lookup, thus they
// carry no functionRegistry entry and no rule that the index could call.
// Reaching them needs a lambda body generator.
//
// The list is written out name by name. It is NOT built from the tables
// in an init function. A list derived from the tables could never report
// a missing entry, thus the gate would pass for every new rule and the
// gap would be silent again. That is the very defect the gate removes.
var genReachExclusions = map[string]string{}

// genReachTables gives the four inference tables that the gate covers.
func genReachTables() []struct {
	table string
	names []string
} {
	return []struct {
		table string
		names []string
	}{
		{"functionRegistry", keysOfRegistry()},
		{"higherOrderArrayFunctions", keysOfHigherOrderArray()},
		{"sizedConstructors", keysOfSizedConstructorRules()},
		{"simpleStateSupportedBases", keysOfSimpleStateBases()},
	}
}

// keysOfSizedConstructorRules gives the names of the sized constructor
// table. The name guard does not cover this table today; the gate does,
// because a constructor rule is as untestable when it is unreachable.
func keysOfSizedConstructorRules() []string {
	names := make([]string, 0, len(sizedConstructors))
	for name := range sizedConstructors {
		names = append(names, name)
	}
	return names
}

// genReachIsReachable reports whether the generator can build a call to
// the name. It is the one definition of reachable that both the gate and
// the stale check use, so the two can never disagree.
func genReachIsReachable(index map[string]genCandidate, name string) bool {
	candidate, ok := index[name]
	if !ok {
		return false
	}
	return candidate.buildable()
}

// TestEveryRuleIsGeneratorReachable fails when a rule in one of the four
// inference tables has no generator candidate and no exclusion with a
// reason.
func TestEveryRuleIsGeneratorReachable(t *testing.T) {
	index := buildGenIndex(t)

	reachable := 0
	excluded := 0
	for _, entry := range genReachTables() {
		names := entry.names
		sort.Strings(names)
		for _, name := range names {
			if genReachIsReachable(index, name) {
				reachable++
				continue
			}
			reason, isExcluded := genReachExclusions[name]
			if !isExcluded {
				t.Errorf("%s: the generator cannot reach %q, and no exclusion names it; "+
					"add a genSpec so the generator can write the call, or add an entry to "+
					"genReachExclusions with a reason", entry.table, name)
				continue
			}
			if strings.TrimSpace(reason) == "" {
				t.Errorf("genReachExclusions[%q] has an empty reason; an exclusion without a "+
					"reason hides the gap it names", name)
				continue
			}
			excluded++
		}
	}
	t.Logf("reachability over the four tables: %d name uses reachable, %d excluded with a reason",
		reachable, excluded)
}

// TestGenReachExclusionsAreStillNeeded keeps the exclusion list honest.
// An entry that IS reachable, or that names no rule in any of the four
// tables, is dead weight and would hide the gap it claims to record.
func TestGenReachExclusionsAreStillNeeded(t *testing.T) {
	index := buildGenIndex(t)

	known := make(map[string]bool)
	for _, entry := range genReachTables() {
		for _, name := range entry.names {
			known[name] = true
		}
	}

	if len(genReachExclusions) == 0 {
		for name := range known {
			if !genReachIsReachable(index, name) {
				t.Errorf("the exclusion list is empty, but function %s is not reachable", name)
			}
		}
		return
	}

	for name, reason := range genReachExclusions {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("genReachExclusions[%q] has an empty reason", name)
		}
		if !known[name] {
			t.Errorf("genReachExclusions has %q, which no inference table names any more; "+
				"remove the entry", name)
			continue
		}
		if genReachIsReachable(index, name) {
			t.Errorf("genReachExclusions has %q, but the generator CAN build a call to it "+
				"now (%s); remove the exclusion, because the gap it records is closed",
				name, candidateSummary(index[name]))
		}
	}
}
