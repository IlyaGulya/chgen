package engine

import (
	"fmt"
	"strings"
	"testing"
)

// This file holds the guard that keeps wrapper transport a first-class
// concept.
//
// The rule of the design is: a functionTypeRule reasons about BARE base
// types. It never receives a transport wrapper and never returns one.
// Every decision about which wrappers a result carries belongs to the
// transport spec and the ONE applier in wrapper_transport.go.
//
// Without this guard the old shape comes back one site at a time: a rule
// starts to look at a wrapper, another site removes the wrapper by hand,
// and the two disagree. Seven past defects in this package have that
// shape, and each of them was a SILENTLY WRONG TYPE, which is the worst
// defect class.

// transportWrapperNames are the three transport wrappers. A type whose
// outermost name is one of these is a wrapped type.
var transportWrapperNames = []string{"LowCardinality", "Nullable", "SimpleAggregateFunction"}

// wrappedTransportName reports the transport wrapper at the top level of
// a type, and "" when the type is bare.
func wrappedTransportName(value CHType) string {
	for _, name := range transportWrapperNames {
		if strings.EqualFold(value.Name, name) {
			return name
		}
	}
	return ""
}

// guardQueries are real queries that drive the registry rules through
// the inference pipeline with WRAPPED column types in every argument
// position. A rule that peeks at a wrapper is caught here.
//
// The schema deliberately offers each wrapper alone and in the nested
// forms that the server produces, so the guard sees the same shapes that
// production queries carry.
const wrapperTransportGuardSchema = `
CREATE TABLE t (
    lc     LowCardinality(String),
    lcn    LowCardinality(Nullable(String)),
    lci    LowCardinality(Int64),
    lcni   LowCardinality(Nullable(Int64)),
    ns     Nullable(String),
    ni64   Nullable(Int64),
    sagg   SimpleAggregateFunction(sum, Int64),
    saggn  SimpleAggregateFunction(sum, Nullable(Int64)),
    saggs  SimpleAggregateFunction(min, String),
    s      String,
    i64    Int64,
    arrlc  Array(LowCardinality(String))
);
`

// TestFunctionTypeRulesNeverSeeWrappedTypes is the guard the transport
// design needs.
//
// It replaces every rule in the function registry with an instrumented
// rule that records its arguments and its result, runs a body of real
// queries over wrapped columns, and then fails when any rule received a
// wrapped argument or returned a wrapped result.
//
// The invariant holds for every rule that goes THROUGH the transport,
// which is every rule whose class is not wrapperOpaque. Those rules see
// bare base types, and the ONE applier decides the wrappers afterwards.
//
// A wrapperOpaque rule is exempt BY DEFINITION, not by accident. Its
// class says "this function's own rule already handles the wrappers",
// and the two inference paths hand it the unstripped argument types on
// purpose. There are exactly four such rules, and each of them must see
// the wrapper to be correct:
//
//	array, tuple    constructors that KEEP the wrapper inside the
//	                container (measured: array(lc) is
//	                Array(LowCardinality(String)))
//	assumeNotNull   exists precisely to REMOVE a Nullable wrapper
//	groupArray      an aggregate that strips the wrapper itself, at
//	                every depth (measured: groupArray(lc) is
//	                Array(String))
//
// The exemption is closed: the guard lists those four names and fails
// when a FIFTH opaque rule starts to read a wrapper. That is the
// property that matters, because a new wrapper-aware rule outside this
// list is the defect shape returning.
//
// A failure here is not a test that needs relaxing. It means a rule has
// started to reason about transport, which is exactly the defect this
// design removes. Move the decision into the transport spec instead.
func TestFunctionTypeRulesNeverSeeWrappedTypes(t *testing.T) {
	schema, err := schemaFromDDLErr(t, wrapperTransportGuardSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}

	type violation struct {
		function string
		position string
		typeName string
		wrapper  string
	}
	var violations []violation

	// Instrument every rule in the registry, then restore the registry
	// when the test ends. The registry is package state, thus the
	// restore must happen even when the test fails.
	original := make(map[string]functionTypeRule, len(functionRegistry))
	for name, spec := range functionRegistry {
		if spec.rule == nil {
			continue
		}
		original[name] = spec.rule
	}
	t.Cleanup(func() {
		for name, rule := range original {
			spec := functionRegistry[name]
			spec.rule = rule
			functionRegistry[name] = spec
		}
	})
	for name, rule := range original {
		observedName, observedRule := name, rule
		spec := functionRegistry[observedName]
		spec.rule = func(args []CHType) (CHType, error) {
			for index, arg := range args {
				if wrapper := wrappedTransportName(arg); wrapper != "" {
					violations = append(violations, violation{
						function: observedName,
						position: fmt.Sprintf("argument %d", index),
						typeName: arg.String(),
						wrapper:  wrapper,
					})
				}
			}
			result, ruleErr := observedRule(args)
			if ruleErr == nil {
				if wrapper := wrappedTransportName(result); wrapper != "" {
					violations = append(violations, violation{
						function: observedName,
						position: "result",
						typeName: result.String(),
						wrapper:  wrapper,
					})
				}
			}
			return result, ruleErr
		}
		functionRegistry[observedName] = spec
	}

	// Drive the rules. An expression that refuses is fine: the guard is
	// about what the rules SEE, not about which calls are legal.
	for _, expr := range wrapperTransportGuardExprs() {
		_, _ = inferSelectItemCHType(t, schema, "SELECT "+expr+" AS a FROM t")
	}

	// wrapperAwareOpaqueRules is the CLOSED list of wrapperOpaque rules
	// that own their wrappers. A name outside this list that reads a
	// wrapper is a defect. See the comment on this test.
	wrapperAwareOpaqueRules := map[string]bool{
		"array":         true,
		"tuple":         true,
		"assumenotnull": true,
		"grouparray":    true,
	}
	// A value-preserving rule owns its wrappers for the same reason: the
	// server nests the SimpleAggregateFunction wrapper UNDER the
	// Nullable that the rule itself adds, and the applier cannot build
	// that nesting from outside. See wrapperValuePreservingRules.
	for name := range wrapperValuePreservingRules {
		wrapperAwareOpaqueRules[name] = true
	}
	// A rule on the exemption list must still BE opaque. If its class
	// ever changes, it starts going through the transport and the
	// exemption becomes a hole.
	for name := range wrapperAwareOpaqueRules {
		if wrapperValuePreservingRules[name] {
			// A value-preserving rule is exempt for a different, also
			// measured reason, and its class is not wrapperOpaque.
			continue
		}
		if functionClassFor(name) != wrapperOpaque {
			t.Errorf(
				"%s is on the wrapper-aware exemption list but its class is no longer "+
					"wrapperOpaque; it now goes through the transport, thus it must "+
					"reason about bare base types",
				name,
			)
		}
	}

	// The exemption list must stay tight as well as closed: a name that
	// no longer reads a wrapper does not need the exemption, and leaving
	// it there would hide a later regression on that name.
	exercised := make(map[string]bool, len(violations))
	for _, found := range violations {
		exercised[found.function] = true
	}
	for name := range wrapperAwareOpaqueRules {
		if !exercised[name] {
			t.Errorf(
				"%s is on the wrapper-aware exemption list but no longer reads a "+
					"wrapper; remove it from the list so the guard covers it",
				name,
			)
		}
	}

	seen := make(map[string]bool, len(violations))
	for _, found := range violations {
		if wrapperAwareOpaqueRules[found.function] {
			continue
		}
		key := found.function + "|" + found.position + "|" + found.typeName
		if seen[key] {
			continue
		}
		seen[key] = true
		t.Errorf(
			"rule %s got a wrapped type at %s: %s carries a %s wrapper; "+
				"a functionTypeRule must reason about bare base types only, "+
				"and the wrapper decision belongs in the transport spec "+
				"(see wrapper_transport.go)",
			found.function, found.position, found.typeName, found.wrapper,
		)
	}
}

// wrapperTransportGuardExprs lists the expressions the guard drives. Each
// one puts a wrapped column into an argument position of a registry
// function, across the argsIndependent, argsFirstOnly and argsGeneric
// strategies and across all three wrapper classes.
func wrapperTransportGuardExprs() []string {
	var exprs []string
	// One wrapped argument, for every strategy and class.
	for _, arg := range []string{"lc", "lcn", "lci", "lcni", "ns", "ni64", "sagg", "saggn", "saggs"} {
		exprs = append(exprs,
			"max("+arg+")",
			"min("+arg+")",
			"any("+arg+")",
			"greatest("+arg+")",
			"least("+arg+")",
			"toString("+arg+")",
			"lower("+arg+")",
			"upper("+arg+")",
			"length("+arg+")",
			"empty("+arg+")",
			"groupArray("+arg+")",
			"array("+arg+")",
			"tuple("+arg+", i64)",
			"isNull("+arg+")",
			"assumeNotNull("+arg+")",
			"first_value("+arg+") OVER ()",
		)
	}
	// Two or more arguments, which is where the asymmetric greatest and
	// least grid lives.
	for _, pair := range [][2]string{
		{"lc", "lc"}, {"lc", "s"}, {"lcn", "lcn"}, {"lcn", "ns"},
		{"lci", "i64"}, {"lcni", "ni64"}, {"sagg", "i64"}, {"sagg", "sagg"},
		{"saggs", "s"}, {"saggn", "ni64"}, {"ns", "s"}, {"ni64", "i64"},
	} {
		exprs = append(exprs,
			"greatest("+pair[0]+", "+pair[1]+")",
			"least("+pair[0]+", "+pair[1]+")",
			"argMax("+pair[0]+", "+pair[1]+")",
			"argMin("+pair[0]+", "+pair[1]+")",
			"nullIf("+pair[0]+", "+pair[1]+")",
			"concat("+pair[0]+", "+pair[1]+")",
			"equals("+pair[0]+", "+pair[1]+")",
		)
	}
	// A wrapper INSIDE a container member, which the top-level flags
	// cannot express.
	exprs = append(exprs,
		"groupArray(arrlc)",
		"argMin(arrlc, ni64)",
		"any(tuple(i64, lc))",
		"arrayDistinct(arrlc)",
		"arraySort(arrlc)",
	)
	return exprs
}

// TestWrapperStackRoundTrip checks that splitWrapperStack and
// applyWrapperStack are inverses for the shapes the server produces. A
// stack that does not round-trip would silently change a type.
func TestWrapperStackRoundTrip(t *testing.T) {
	cases := []string{
		"String",
		"Int64",
		"Nullable(Int64)",
		"LowCardinality(String)",
		"LowCardinality(Nullable(String))",
		"SimpleAggregateFunction(sum, Int64)",
		"SimpleAggregateFunction(sum, Nullable(Int64))",
		"Array(Int64)",
		"Tuple(Int64, String)",
	}
	for _, spelling := range cases {
		value, err := parseCHTypeName(spelling)
		if err != nil {
			t.Fatalf("parseCHTypeName(%q) error = %v", spelling, err)
		}
		base, stack := splitWrapperStack(value)
		if wrapper := wrappedTransportName(base); wrapper != "" {
			t.Errorf("%s: base %s still carries a %s wrapper", spelling, base.String(), wrapper)
		}
		got := applyWrapperStack(base, stack).String()
		if got != spelling {
			t.Errorf("%s: round trip = %s, want %s", spelling, got, spelling)
		}
	}
}

// TestWrapperTransportHasNoNameSpecialCase pins the property that made
// the old applier fragile: the applier must not branch on a function
// name. A function whose measured transport differs from its class
// declares that in wrapperTransportOverrides instead.
func TestWrapperTransportHasNoNameSpecialCase(t *testing.T) {
	// greatest and least are the asymmetric pair that the class enum
	// could not express. Both axes must come from the override, not from
	// a name test inside the applier.
	for _, name := range []string{"greatest", "least"} {
		transport, ok := wrapperTransportOverrides[name]
		if !ok {
			t.Fatalf("%s must carry its own transport", name)
		}
		if transport.lowCardinality != wrapperConditional || transport.lowCardinalityWhen == nil {
			t.Errorf("%s must keep LowCardinality under a condition", name)
		}
		if transport.simpleAggregate != wrapperConditional || transport.simpleAggregateWhen == nil {
			t.Errorf("%s must keep SimpleAggregateFunction under a condition", name)
		}
		// The measured grid: LowCardinality survives at arity 1 only.
		if !transport.lowCardinalityWhen(wrapperCall{argCount: 1}) {
			t.Errorf("%s must keep LowCardinality at arity 1", name)
		}
		if transport.lowCardinalityWhen(wrapperCall{argCount: 2}) {
			t.Errorf("%s must drop LowCardinality at arity 2", name)
		}
		// SimpleAggregateFunction survives at arity 1, and at arity 2
		// only when the inner type is not numeric.
		numeric := CHType{Name: "Int64"}
		text := CHType{Name: "String"}
		if !transport.simpleAggregateWhen(wrapperCall{argCount: 1, base: numeric}) {
			t.Errorf("%s must keep SimpleAggregateFunction at arity 1", name)
		}
		if transport.simpleAggregateWhen(wrapperCall{argCount: 2, base: numeric}) {
			t.Errorf("%s must drop SimpleAggregateFunction over a numeric inner type at arity 2", name)
		}
		if !transport.simpleAggregateWhen(wrapperCall{argCount: 2, base: text}) {
			t.Errorf("%s must keep SimpleAggregateFunction over a non-numeric inner type", name)
		}
	}
}
