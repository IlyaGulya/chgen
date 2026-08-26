package engine

import (
	"strings"
	"testing"
)

// This file guards the genSpec data. Step 4 of the migration adds the
// index builder that reads the data. Until then nothing consumes it, and
// a data table with no check rots.
//
// The checks below are internal. They compare a genSpec against itself
// and against its registry key. They do not compare against a second
// list, because a second list is the defect this epic removes.

// TestGenSpecEntriesAreConsistent checks every genSpec in the registry.
func TestGenSpecEntriesAreConsistent(t *testing.T) {
	for name, spec := range functionRegistry {
		if spec.gen == nil {
			continue
		}
		gen := *spec.gen

		if gen.spelling == "" {
			t.Errorf("%s: the spelling is empty", name)
		}
		// The registry key is the lowercased spelling. This is what
		// lets the generator write the server spelling while the
		// resolver keeps a case-insensitive lookup.
		if got := strings.ToLower(gen.spelling); got != name {
			t.Errorf("%s: the lowercased spelling is %q, and it must equal the registry key", name, got)
		}

		if gen.minArity < 0 {
			t.Errorf("%s: minArity is %d, and it must not be negative", name, gen.minArity)
		}
		// maxArity is -1 for a variadic function. Every other value
		// must be at least minArity.
		if gen.maxArity != -1 && gen.maxArity < gen.minArity {
			t.Errorf("%s: maxArity %d is less than minArity %d", name, gen.maxArity, gen.minArity)
		}

		if gen.place == placementUnset {
			t.Errorf("%s: the placement is unset", name)
		}

		// argSorts describes the leading argument positions. It must
		// not be longer than the largest legal arity, and for a
		// fixed-arity function it must describe every position.
		if len(gen.argSorts) > gen.minArity && gen.maxArity != -1 && len(gen.argSorts) > gen.maxArity {
			t.Errorf("%s: argSorts has %d entries, and the largest arity is %d", name, len(gen.argSorts), gen.maxArity)
		}
		if gen.minArity == gen.maxArity && len(gen.argSorts) != gen.minArity {
			t.Errorf("%s: the arity is %d, and argSorts has %d entries", name, gen.minArity, len(gen.argSorts))
		}
		if gen.maxArity == -1 && len(gen.argSorts) == 0 {
			t.Errorf("%s: a variadic recipe must describe at least the repeated argument position", name)
		}
		for i, sort := range gen.argSorts {
			if sort == argSortUnset {
				t.Errorf("%s: argument %d has an unset sort", name, i)
			}
		}

		// A template is a printf format. It must hold one verb for
		// each argument that it renders. The count of the verbs must
		// not be larger than the largest legal arity.
		if gen.template != "" {
			verbs := strings.Count(gen.template, "%s")
			if gen.maxArity != -1 && verbs > gen.maxArity {
				t.Errorf("%s: the template has %d verbs, and the largest arity is %d", name, verbs, gen.maxArity)
			}
		}
	}
}

// TestEveryFunctionHasACallShape checks the production view. The semantic
// builder also enforces this rule before it creates the view.
func TestEveryFunctionHasACallShape(t *testing.T) {
	for name, spec := range functionRegistry {
		if spec.gen == nil {
			t.Errorf("function %s has no call shape", name)
		}
	}
}

// TestGenSpecForReadsTheRegistry checks the accessor. An unknown name reports
// false.
func TestGenSpecForReadsTheRegistry(t *testing.T) {
	if _, ok := genSpecFor("no_such_function_name"); ok {
		t.Error("an unknown name must not report a recipe")
	}
	gen, ok := genSpecFor("touint64")
	if !ok {
		t.Fatal("toUInt64 must have a recipe")
	}
	if gen.spelling != "toUInt64" {
		t.Errorf("the spelling is %q, want toUInt64", gen.spelling)
	}
	if gen.place != placementScalar {
		t.Error("toUInt64 must be scalar")
	}
}

// TestWindowOnlyFunctionsAreDeclaredWindow pins the fact that the
// placement cannot be derived. row_number has the same rule, class and
// strategy as now(), yet it is legal only with an OVER clause.
func TestWindowOnlyFunctionsAreDeclaredWindow(t *testing.T) {
	windowOnly := []string{"row_number", "rank", "dense_rank", "first_value", "last_value", "laginframe", "leadinframe"}
	for _, name := range windowOnly {
		gen, ok := genSpecFor(name)
		if !ok {
			t.Errorf("%s must have a recipe", name)
			continue
		}
		if gen.place != placementWindow {
			t.Errorf("%s must have the window placement", name)
		}
	}
}
