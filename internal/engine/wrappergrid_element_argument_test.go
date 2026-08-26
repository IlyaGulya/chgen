package engine

// This file guards the ELEMENT-ARGUMENT rule of the grid enumerator.
//
// It has NO build tag, because the rule is a property of the enumeration
// and not of a measurement. A server is not needed to see it break.
//
// The defect that the rule cures: the enumerator put the SAME fixture
// column in every value position, thus it spelled has(arr, arr). The
// server answers Code 386, NO_COMMON_TYPE, which is a refusal about the
// PAIR of arguments. The grid read that refusal as
// CHGEN_TYPES_SERVER_REFUSES, which said that chgen typed something the
// server refuses. That was false: chgen answers Bool for has(arr, 1) and
// that answer is right.

import (
	"strings"
	"testing"
)

// An entry that takes an element must never receive the container twice.
// This is the exact shape that made Code 386, thus the test names the
// shape and not only the four addresses.
func TestGridElementArgumentNeverRepeatsTheContainer(t *testing.T) {
	schema := gridTestSchema(t)
	for _, probe := range gridBuildProbes(schema) {
		if !gridElementArgumentEntries[probe.entry] {
			continue
		}
		// The check counts WHOLE COLUMN NAMES, not base tokens.
		//
		// A base token is not safe any more. The arrays with a wrapped
		// element are named narr and lcarr, and the text "_arr" is
		// inside "c_bare_narr". A substring count over base tokens
		// would therefore read one narr column as an arr column, and
		// the rule would be enforced against the wrong address.
		//
		// The whole-name form has no such overlap, because every
		// fixture column name is unique by construction.
		// Every CONTAINER base is guarded, whether or not it has an
		// element base. tup has no element base, thus the enumerator
		// can never rewrite it, and that is exactly why it must stay
		// under the check: a cell that repeated a Tuple column would
		// be the same Code 386 shape with no cure applied.
		for _, cell := range gridColumns() {
			if !gridBaseIsContainer(cell.base) {
				continue
			}
			if strings.Count(probe.sql, cell.column) > 1 {
				t.Errorf("cell %s repeats a container argument: %s",
					probe.id, probe.sql)
			}
		}
	}
}

// The four cells that carried the wrong verdict must keep their
// addresses and must now spell the element call.
//
// The addresses are asserted because the golden is keyed by address. A
// cure that renamed a cell would drop the old address from the golden,
// and the coverage that the old address carried would die quietly.
func TestGridHasCellsSendTheElement(t *testing.T) {
	want := map[string]string{
		"fn/has/bare/arr": "has(c_bare_arr, c_bare_i32)",
		"fn/has/bare/map": "has(c_bare_map, c_bare_s)",
		"fn/has/saf/arr":  "has(c_saf_arr, c_bare_i32)",
		"fn/has/saf/map":  "has(c_saf_map, c_bare_s)",
		// The safarr letter wraps the base in ONE Array level, thus the
		// element of the column is the BASE itself and not the element
		// of the base. Measured on 25.8.29.51 against real columns:
		//
		//	has(saf_arr_of_array_col, bare_array_col)  UInt8
		//	has(saf_arr_of_array_col, bare_int_col)    Code 386
		//	has(saf_arr_of_int_col,   bare_int_col)    UInt8
		//
		// These addresses pin the rule for a scalar base and for a
		// container base alike. Without them the enumerator could go
		// back to sending the element of the BASE, which banks Code 386
		// as a verdict for four cells.
		"fn/has/safarr/i32":  "has(c_safarr_i32, c_bare_i32)",
		"fn/has/safarr/arr":  "has(c_safarr_arr, c_bare_arr)",
		"fn/has/safarr/map":  "has(c_safarr_map, c_bare_map)",
		"fn/has/safarr/tup":  "has(c_safarr_tup, c_bare_tup)",
		"fn/has/safarr/narr": "has(c_safarr_narr, c_bare_narr)",
	}
	schema := gridTestSchema(t)
	got := map[string]string{}
	for _, probe := range gridBuildProbes(schema) {
		if _, wanted := want[probe.id]; wanted {
			got[probe.id] = probe.sql
		}
	}
	for id, expected := range want {
		actual, present := got[id]
		if !present {
			t.Errorf("cell %s is gone from the enumeration", id)
			continue
		}
		if actual != expected {
			t.Errorf("cell %s spells %q, want %q", id, actual, expected)
		}
	}
}

// The element base table must agree with the fixture declaration. A
// change to the element type of a container base would otherwise leave
// the table stale, and the enumerator would send a column of the wrong
// type without any test noticing.
func TestGridElementBaseMatchesTheFixture(t *testing.T) {
	want := map[string]string{
		// Array(Int32) has element Int32.
		"arr": "Int32",
		// has(mp, k) tests the KEY of Map(String, Int64), thus String.
		"map": "String",
		// The two arrays with a WRAPPED element take the BARE mate.
		// Such a cell measures what the function does to the element
		// wrapper of the ARRAY, thus the mate must carry no wrapper of
		// its own. Measured on 25.8.29.51 against real columns:
		// has(Array(Nullable(Int32)), Int32) is UInt8, and
		// has(Array(LowCardinality(String)), String) is UInt8.
		//
		// The wanted type is therefore the BARE element type, not the
		// wrapped one. A test that wanted Nullable(Int32) here would
		// lock in the two-things-at-once shape that the rule forbids.
		"narr":  "Int32",
		"lcarr": "String",
		// A Tuple base reaches an element-argument entry only through
		// the safarr letter, where the column is
		// SimpleAggregateFunction(anyLast, Array(Tuple(Int32, String)))
		// and the ELEMENT is the Tuple itself. The mate is thus the
		// BARE tup column, by the same rule as every line above.
		//
		// This line was added by the regression. Until that ticket the
		// representative chooser took bases by LIST POSITION, no Tuple
		// ever reached such an entry, and the absence of an element
		// base was therefore invisible. Measured on 25.8.29.51 against
		// real columns of a real AggregatingMergeTree table:
		//
		//	has(saf_arr_tuple_col, bare_tuple_col)  UInt8
		//	has(saf_arr_tuple_col, saf_arr_tuple_col)
		//	                     Code 386, no supertype
		"tup": "Tuple(Int32, String)",
	}
	byKey := map[string]string{}
	for _, base := range gridBases {
		byKey[base.key] = base.chType
	}
	for container, elementType := range want {
		// A container that the fixture does not declare yet is
		// skipped, not failed. The element rule and the base list live
		// in different files, and this test guards the AGREEMENT of
		// the two. It must not demand that a base exist, or it would
		// report the absence of a base as a broken element rule.
		if _, declared := byKey[container]; !declared {
			continue
		}
		elementKey, ok := gridElementBaseOf(container)
		if !ok {
			t.Errorf("base %q has no element base", container)
			continue
		}
		if byKey[elementKey] != elementType {
			t.Errorf("element of %q is base %q of type %q, want %q",
				container, elementKey, byKey[elementKey], elementType)
		}
	}
	// A base that is not a container must have NO element, so that the
	// default same-column rendering stays in force for it.
	//
	// tup was in this list until the regression. It was a container all
	// along, and it belonged here only because the cap made the
	// question unreachable. A container with no element base is not a
	// safe default: it makes the enumerator repeat the container, which
	// is the Code 386 shape. Every CONTAINER base must now name an
	// element base, and the loop below asserts the complement.
	for _, base := range []string{"i32", "s", "d"} {
		if _, ok := gridElementBaseOf(base); ok {
			t.Errorf("base %q must have no element", base)
		}
	}
	// Every container base MUST name an element base. Without one the
	// enumerator has no mate to send, and the only alternatives are to
	// repeat the container, which banks Code 386 as a verdict, or to
	// drop the cell, which loses the coverage that the regression restored.
	for _, base := range gridBases {
		if !gridBaseIsContainer(base.key) {
			continue
		}
		if _, ok := gridElementBaseOf(base.key); !ok {
			t.Errorf("container base %q names no element base; an "+
				"element-argument entry over it can only repeat the "+
				"container, which is Code 386 and not type evidence",
				base.key)
		}
	}
}

// The element column is taken BARE for every wrapper. A cell that varied
// the wrapper of the container AND of the element would measure two
// changes at once and could not say which one moved the answer.
func TestGridElementColumnIsAlwaysBare(t *testing.T) {
	for _, cell := range gridColumns() {
		column, ok := gridElementColumnFor(cell)
		if !ok {
			continue
		}
		if !strings.HasPrefix(column, "c_bare_") {
			t.Errorf("cell %s/%s takes element column %s, which is not bare",
				cell.wrapper, cell.base, column)
		}
	}
}
