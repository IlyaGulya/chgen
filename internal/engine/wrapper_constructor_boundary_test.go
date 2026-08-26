package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This is the boundary guard of the wrapper constructors.
//
// The transport design of wrapper_transport.go rests on one property: a
// transport wrapper goes on a type in ONE place, thus the measured
// rejection rule applies to every path at once. wrapNullable holds the
// canBeInsideNullable rule and wrapLowCardinality holds the
// resultRejectsLowCardinality rule. A site that builds the CHType node
// by hand skips whichever rule applies to it, and the result is a
// silently wrong type, which is the worst defect class in this package.
//
// That is not a hypothetical. The regression was a wrapLowCardinality call
// site that never asked resultRejectsLowCardinality, and the regression was a
// binary-operator path that never asked the transport table at all. The
// A1 refactor gave the APPLIER one owner; without this guard the
// CONSTRUCTORS keep more than one, and the escape happens one site at a
// time.
//
// The guard reads the package with go/parser and fails when a file
// outside wrapper_transport.go builds a composite CHType literal whose
// Name field is LowCardinality, Nullable or SimpleAggregateFunction. It
// is a syntactic check on purpose: it can be read without running the
// inference, and it cannot be satisfied by a path that the test queries
// happen to miss.
//
// The allowlist below is CLOSED and each entry carries a reason. It also
// carries a staleness check: an entry that no longer names a real
// construction site fails the test, so the list shrinks as the paths are
// corrected and it cannot rot into a blanket permission.

// wrapperConstructorOwner is the one file that may build a transport
// wrapper node. Both constructors live there.
const wrapperConstructorOwner = "wrapper_transport.go"

// wrapperConstructorAllowance is one permitted hand-built wrapper node.
type wrapperConstructorAllowance struct {
	// file is the file that holds the site.
	file string
	// wrapper is the wrapper name that the site builds.
	wrapper string
	// count is how many such sites the file holds. An exact count is
	// part of the guard: a NEW site in an already listed file must fail,
	// otherwise one allowed exception would open the whole file.
	count int
	// reason states why the site cannot go through the constructor. A
	// reason is required, and an empty one fails the test.
	reason string
}

// wrapperConstructorAllowlist is the CLOSED list of hand-built wrapper
// nodes outside wrapper_transport.go.
//
// Do NOT widen this list to make a change easier. Each entry names a
// path that still places a wrapper by hand, thus each entry is a place
// where a measured rejection rule is not applied. The list is a record
// of remaining work, not a permission.
var wrapperConstructorAllowlist = []wrapperConstructorAllowance{
	{
		file:    "sized_constructor.go",
		wrapper: "Nullable",
		count:   1,
		reason: "sizedConstructorResult builds the DECLARED result type of a " +
			"*OrNull constructor, for example toInt32OrNull -> Nullable(Int32). " +
			"That Nullable is part of the result type the name promises, not a " +
			"transport wrapper carried over from an argument, thus the transport " +
			"applier must not be the one to add it. The base is always a scalar " +
			"cast target, so canBeInsideNullable holds for it by construction.",
	},
	{
		file:    "aggregate_combinator.go",
		wrapper: "SimpleAggregateFunction",
		count:   1,
		reason: "the -SimpleState combinator CREATES the marker rather than " +
			"transporting one: its result type IS " +
			"SimpleAggregateFunction(base, T). The applier can only put back a " +
			"wrapper that an argument carried, thus it cannot build this one. " +
			"The aggregate-function parameter comes from the combinator name.",
	},
	{
		file:    "infer_function.go",
		wrapper: "Nullable",
		count:   2,
		reason: "the sum and the quantile rules rebuild the Nullable that their " +
			"own measured result type carries, and quantileFunctionResultType " +
			"rebuilds it RECURSIVELY around a changed inner type. The applier " +
			"puts a wrapper around a finished result only, thus it cannot " +
			"reproduce that nesting from outside. Same shape as the " +
			"wrapperValuePreservingRules exemption of the transport guard.",
	},
	{
		file:    "supertype.go",
		wrapper: "Nullable",
		count:   3,
		reason: "the measurement proves that canBeInsideNullable can never refuse at " +
			"any of the three sites. Site 1, commonSimpleAggregateCHType, wraps " +
			"'left' whole, which is always literally a SimpleAggregateFunction(...) " +
			"node; that name is accepted inside Nullable, per the same measured " +
			"table canBeInsideNullable already encodes. Site 2, the Enum-vs-string " +
			"branch, always wraps the hardcoded CHType{Name: \"String\"}, never a " +
			"container. Site 3, the final wrap in commonCHType, wraps 'result', " +
			"which can be Array, Map or Tuple only when BOTH operands already had " +
			"that same container name at entry; leftNullable and rightNullable " +
			"there require the RAW operand itself to carry top-level name " +
			"'Nullable', and on ClickHouse 25.8.29.51 the server refuses to " +
			"construct Nullable(Array), Nullable(Map), Nullable(Tuple) or " +
			"Nullable(AggregateFunction) AT ALL, at any depth (Code 43, 'Nested " +
			"type ... cannot be inside Nullable type'), confirmed by CREATE " +
			"TABLE for all four bare shapes and for the same four nested inside " +
			"a Tuple element, a Map value and an Array element. Since chgen only " +
			"ever infers a type that a real ClickHouse column or expression can " +
			"hold, left and right can never carry a Nullable wrapper over one of " +
			"those four names, thus the three sites cannot reach a base that " +
			"canBeInsideNullable would refuse. Also confirmed directly against " +
			"the branch family: if(b, arr, NULL), multiIf(b, arr, NULL) and CASE " +
			"WHEN b THEN arr END all answer Code 386 NO_COMMON_TYPE before any " +
			"Nullable wrap is reachable, for Array, Tuple and Map alike, and the " +
			"same CASE form over an AggregateFunction column answers Code 43. " +
			"The separate arithmetic.go site does reach " +
			"an Array result through a different path and needs its own fix. " +
			"The supertype path still decides the Nullable of a COMMON type " +
			"over two operands, which is not a transport decision over one " +
			"argument, so the entry stays although the base can never be " +
			"refused.",
	},
}

// the regression removed the supertype.go LowCardinality entry that stood
// here. The two sites, commonContainerMemberCHType for the outer node
// and restoreContainerMemberLowCardinality for the nested ones, now both
// call wrapLowCardinality. The keep-if-all rule still decides WHEN to
// keep the wrapper; the constructor decides WHETHER the common base can
// hold one. Measured on ClickHouse 25.8.29.51: a rejected base makes the
// server refuse the whole expression with Code: 43, thus the wrapper
// must not go on the type. See supertype_lowcardinality_reject_test.go.

// wrapperConstructorSite is one hand-built wrapper node that the scan
// found.
type wrapperConstructorSite struct {
	file    string
	line    int
	wrapper string
}

// scanWrapperConstructorSites reads every non-test Go file of the
// package and reports each composite CHType literal whose Name field is
// a transport wrapper.
//
// It looks for the shape
//
//	CHType{Name: "Nullable", ...}
//
// which is how every wrapper node in this package is built. A site that
// used a computed name would not be found, and that is acceptable: such
// a site cannot be written by accident, while the literal form is
// exactly the one that keeps reappearing.
func scanWrapperConstructorSites(t *testing.T) []wrapperConstructorSite {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("no Go file was read; the scan is broken, thus the guard " +
			"would pass without measuring anything")
	}

	fileSet := token.NewFileSet()
	var sites []wrapperConstructorSite
	for _, name := range names {
		file, parseErr := parser.ParseFile(fileSet, filepath.Clean(name), nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if identifier, isIdent := literal.Type.(*ast.Ident); !isIdent || identifier.Name != "CHType" {
				return true
			}
			for _, element := range literal.Elts {
				pair, isPair := element.(*ast.KeyValueExpr)
				if !isPair {
					continue
				}
				key, isIdent := pair.Key.(*ast.Ident)
				if !isIdent || key.Name != "Name" {
					continue
				}
				value, isLit := pair.Value.(*ast.BasicLit)
				if !isLit || value.Kind != token.STRING {
					continue
				}
				text, unquoteErr := strconv.Unquote(value.Value)
				if unquoteErr != nil {
					continue
				}
				for _, wrapper := range transportWrapperNames {
					if strings.EqualFold(text, wrapper) {
						sites = append(sites, wrapperConstructorSite{
							file:    name,
							line:    fileSet.Position(value.Pos()).Line,
							wrapper: wrapper,
						})
					}
				}
			}
			return true
		})
	}
	return sites
}

// TestOnlyWrapperTransportBuildsWrapperTypes is the boundary guard.
//
// It fails when a file other than wrapper_transport.go builds a
// LowCardinality, a Nullable or a SimpleAggregateFunction CHType node
// and the site is not on the allowlist with a reason.
//
// A failure here is not a test to relax. It means a new path places a
// wrapper by hand, thus that path does not apply the measured rejection
// rule that the constructor holds. Route the site through wrapNullable
// or wrapLowCardinality. If it truly cannot go through them, that is a
// FINDING worth reporting before it becomes an allowlist entry.
func TestOnlyWrapperTransportBuildsWrapperTypes(t *testing.T) {
	sites := scanWrapperConstructorSites(t)

	// The owner must actually hold both constructors. If it stops
	// building wrapper nodes, the scan is looking at the wrong shape and
	// every other check below would pass for the wrong reason.
	ownerWrappers := make(map[string]bool)
	for _, site := range sites {
		if site.file == wrapperConstructorOwner {
			ownerWrappers[site.wrapper] = true
		}
	}
	for _, wrapper := range []string{"Nullable", "LowCardinality"} {
		if !ownerWrappers[wrapper] {
			t.Errorf("%s builds no %s node; both wrapper constructors must live "+
				"there, thus the scan is not seeing what the guard assumes",
				wrapperConstructorOwner, wrapper)
		}
	}

	// Count the sites outside the owner, per file and per wrapper.
	type key struct{ file, wrapper string }
	found := make(map[key]int)
	lines := make(map[key][]int)
	for _, site := range sites {
		if site.file == wrapperConstructorOwner {
			continue
		}
		identity := key{site.file, site.wrapper}
		found[identity]++
		lines[identity] = append(lines[identity], site.line)
	}

	allowed := make(map[key]wrapperConstructorAllowance, len(wrapperConstructorAllowlist))
	for _, allowance := range wrapperConstructorAllowlist {
		identity := key{allowance.file, allowance.wrapper}
		if _, duplicate := allowed[identity]; duplicate {
			t.Errorf("wrapperConstructorAllowlist has two entries for %s/%s; "+
				"one entry must carry the whole count for a file and a wrapper",
				allowance.file, allowance.wrapper)
		}
		if strings.TrimSpace(allowance.reason) == "" {
			t.Errorf("the allowlist entry for %s/%s has an empty reason; an "+
				"exception without a reason hides the gap it names",
				allowance.file, allowance.wrapper)
		}
		allowed[identity] = allowance
	}

	// A site that is not allowed, or a file whose count grew, is a
	// defect.
	for identity, count := range found {
		allowance, listed := allowed[identity]
		if !listed {
			t.Errorf(
				"%s builds a %s CHType node by hand at line(s) %v; only %s may "+
					"build a transport wrapper, because wrapNullable and "+
					"wrapLowCardinality hold the measured rejection rules that a "+
					"hand-built node skips. Route the site through the constructor, "+
					"or add an allowlist entry with a measured reason",
				identity.file, identity.wrapper, lines[identity], wrapperConstructorOwner,
			)
			continue
		}
		if count > allowance.count {
			t.Errorf(
				"%s now builds %d %s nodes by hand, and the allowlist permits %d "+
					"(line(s) %v); a NEW hand-built wrapper is the defect this guard "+
					"exists to catch, thus the extra site must go through the "+
					"constructor",
				identity.file, count, identity.wrapper, allowance.count, lines[identity],
			)
		}
	}

	// The staleness check. An allowlist entry that names fewer sites
	// than it permits is out of date: the path was corrected, and
	// leaving the entry would silently permit the defect to come back.
	for identity, allowance := range allowed {
		count := found[identity]
		if count == allowance.count {
			continue
		}
		if count == 0 {
			t.Errorf(
				"the allowlist entry for %s/%s is stale: that file builds no %s "+
					"node any more. Remove the entry, so the guard covers the file "+
					"again",
				identity.file, identity.wrapper, identity.wrapper,
			)
			continue
		}
		t.Errorf(
			"the allowlist entry for %s/%s permits %d sites and only %d remain; "+
				"lower the count, so a new hand-built wrapper cannot hide in the "+
				"headroom that the entry leaves",
			identity.file, identity.wrapper, allowance.count, count,
		)
	}
}
