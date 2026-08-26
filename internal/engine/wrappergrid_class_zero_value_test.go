package engine

import (
	"strings"
	"testing"
)

// the regression closed the gap that this file used to only document: the
// zero value of functionWrapperClass (resolver.go) used to be
// wrapperOpaque, so a genuine "this function never carries a wrapper"
// answer and "nobody set this field" were the same bit pattern.
// wrapperClassUnset is now the zero value, and wrapperOpaque moved to a
// distinct, explicit, non-zero constant.
//
// TestWrapperClassZeroValueIsIllegalForAKnownMiss and
// TestEverySpecDeclaresAClass are the shape of
// TestEverySpecDeclaresAStrategy (see wrapper_propagation_test.go),
// applied to the class field instead of the strategy field.

// A lookup miss (a name absent from functionRegistry) must answer
// wrapperClassUnset, never wrapperOpaque. Confusing the two was exactly
// the defect the regression removes: a caller could not tell "nobody answered
// this question" from "the answer is opaque".
func TestWrapperClassZeroValueIsIllegalForAKnownMiss(t *testing.T) {
	unknownName := "chgen_test_name_absent_from_the_registry"
	if _, present := functionRegistry[unknownName]; present {
		t.Fatalf("the probe name %q must be absent from functionRegistry for this test to probe a miss", unknownName)
	}
	missClass := functionClassFor(unknownName)
	if missClass != wrapperClassUnset {
		t.Fatalf("functionClassFor(%q) = %v, want wrapperClassUnset; "+
			"a lookup miss must not read like a real class", unknownName, missClass)
	}
	if missClass == wrapperOpaque {
		t.Fatalf("wrapperClassUnset must not equal wrapperOpaque; the whole point of " +
			"the zero value must have its own identity")
	}
}

// TestEverySpecDeclaresAClass is TestEverySpecDeclaresAStrategy's shape,
// applied to the class field: every registry entry must declare a real
// class, never the illegal zero value.
//
// This is the first of the two directions the guard test must cover: it
// fails if an entry is MISSING a class. See
// TestTransportForClassCoversEveryMember below for the other direction:
// it fails if a new class member is added without a switch case
// somewhere that reads the class.
func TestEverySpecDeclaresAClass(t *testing.T) {
	// The legal range is DERIVED from the enum through
	// wrapperClassLimit, not restated as a case list. A restated list
	// stops being the truth the moment a member is added.
	for name, spec := range functionRegistry {
		switch {
		case spec.class == wrapperClassUnset:
			t.Errorf("function %q has a registry spec with no wrapper class; "+
				"give it a real class from the functionWrapperClass block", name)
		case spec.class < wrapperClassUnset || spec.class >= wrapperClassLimit:
			t.Errorf("function %q has an unknown wrapper class %d; the legal members "+
				"are the values between wrapperClassUnset and wrapperClassLimit",
				name, spec.class)
		}
	}
}

// TestTransportForClassCoversEveryMember is the other direction of the
// guard: it fails if functionWrapperClass gains a new member that
// transportForClass has no case for. transportForClass's switch ends in
// a default that panics (see wrapper_transport.go) precisely so that
// this test can detect a missing case by recovering from that panic,
// rather than by silently returning a guessed transport.
//
// A single-witness test that only checked "no entry is unset" would
// still pass the moment a THIRD person added wrapperClassSomethingNew to
// the enum without teaching transportForClass about it; every call for
// that class would then quietly take the default branch. This test is
// the second witness.
func TestTransportForClassCoversEveryMember(t *testing.T) {
	// The member set is DERIVED from the enum, never restated here. A
	// hand-written list is a second copy of the enum and it does not
	// grow when a member is added, thus it reports success for exactly
	// the member that no switch answers. That was measured: with a
	// hand-written list, a fourth member added to functionWrapperClass
	// with no case in transportForClass left this test passing.
	//
	// wrapperClassLimit (resolver.go) is one past the last legal
	// member, so the walk below covers every member that exists today
	// and every member a later change adds.
	var known []functionWrapperClass
	for class := wrapperClassUnset + 1; class < wrapperClassLimit; class++ {
		known = append(known, class)
	}
	if len(known) == 0 {
		t.Fatal("the derived member set is empty; wrapperClassLimit must stay one past the last member")
	}
	for _, class := range known {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("transportForClass(%v) panicked: %v; every known class must have an explicit case", class, r)
				}
			}()
			_ = transportForClass(class)
		}()
	}

	// wrapperClassUnset must NOT be answered by transportForClass at
	// all: reaching it is a caller bug (an unmigrated registry entry or
	// a genuine lookup miss reaching the transport layer), and
	// transportForClass panics on it by design. This assertion pins
	// that the panic still fires, so a future edit that quietly turns
	// the wrapperClassUnset case into a normal return would be caught
	// here.
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Error("transportForClass(wrapperClassUnset) did not panic; " +
					"it must refuse rather than silently answer like wrapperOpaque")
				return
			}
			msg, ok := r.(string)
			if !ok || !strings.Contains(msg, "wrapperClassUnset") {
				t.Errorf("transportForClass(wrapperClassUnset) panicked with %v, "+
					"want a message naming wrapperClassUnset", r)
			}
		}()
		_ = transportForClass(wrapperClassUnset)
	}()

	// A value past the last legal member exercises the exhaustiveness
	// default branch itself (see the `default:` case in
	// transportForClass). It pins that the default branch panics
	// instead of guessing, so a value the switch was never taught
	// about never reads as a silent wrapperOpaque-shaped answer.
	//
	// The value is taken from wrapperClassLimit, not from len(known).
	// The two agree today, but len(known) would follow a member that
	// somebody adds and would therefore stop probing PAST the enum.
	unknownMember := wrapperClassLimit
	func() {
		defer func() {
			if recover() == nil {
				t.Errorf("transportForClass(%d) did not panic on an unrecognised class value; "+
					"the default branch must refuse, not guess", unknownMember)
			}
		}()
		_ = transportForClass(unknownMember)
	}()
}
