package engine

import (
	"strings"
	"testing"
)

// The function-name guard.
//
// A type rule can only give a type to a call that ClickHouse accepts. A
// rule that names a function which does not exist on the server is dead
// code at best: every argument type answers Code: 46 (UNKNOWN_FUNCTION),
// thus the rule can only type a call that always fails. The bug that
// started this guard was the rule "tohex": ClickHouse has no toHex
// function, the real name is hex. The same audit found "greaterorequal"
// and "lessorequal"; the real names carry a final s.
//
// The lists below were measured against a real ClickHouse server, version
// 25.8.29.51, with:
//
//	SELECT lower(name) FROM system.functions
//
// The unit test suite must run without a server, thus the measurement is
// frozen here as a static list instead of being queried at test time.
//
// Case handling. ClickHouse function names are case-sensitive and
// system.functions lists the canonical spelling (hex, toTimeZone,
// greaterOrEquals). system.functions also carries a case_insensitive
// flag, but the resolver looks every rule up by the lowercased call name
// (see inferFunctionType), thus the guard compares lowercased names on
// both sides. That is the same comparison the production lookup makes,
// so a name that passes the guard is a name the lookup can reach.
//
// One measured caveat that this comparison hides: countDistinct is
// accepted by the server only in that exact spelling, and lowercase
// countdistinct answers Code: 46. Such a name is still listed as a known
// combinator below, because the resolver's lowercasing is a separate
// concern from whether the rule names a real function.
var serverFunctionNamesLowered = []string{
	"bitshiftright", "fromunixtimestamp64milli",
	"abs", "adddays", "addhours", "addminutes", "addmonths",
	"addquarters", "addseconds", "addweeks", "addyears", "and",
	"any", "anylast", "argmax", "argmin",
	"array", "arrayall", "arrayavg", "arraycount", "arraycumsum", "arraycumsumnonnegative", "arraydistinct",
	"arrayelement", "arrayexists", "arrayfill", "arrayfilter", "arrayfirst", "arrayfirstindex", "arrayfirstornull", "arrayfold", "arraymap",
	"arraylast", "arraylastindex", "arraylastornull", "arraymax", "arraymin", "arraypartialreversesort", "arraypartialsort", "arrayproduct", "arrayresize", "arrayreversefill",
	"arrayreversesort", "arrayreversesplit", "arrayslice", "arraysort", "arraysplit", "arraystringconcat", "arraysum", "assumenotnull",
	"avg", "ceil", "cityhash64", "concat", "count",
	"date_diff", "datediff", "dense_rank", "denserank", "empty",
	"equals", "first_value", "first_value_respect_nulls", "firstvaluerespectnulls", "floor", "greater", "greaterorequals",
	"greatest", "grouparray", "groupbitand", "groupbitor",
	"groupbitxor", "groupuniqarray", "has", "hex",
	"isnotnull", "isnull", "lag", "laginframe", "last_value", "last_value_respect_nulls", "lastvaluerespectnulls",
	"lead", "leadinframe", "least", "length", "less",
	"lessorequals", "lower", "map", "mapkeys", "mapvalues",
	"max", "median", "min", "negate", "notempty",
	"notequals", "now", "now64", "nth_value", "ntile", "nullif", "or", "percent_rank", "percentrank",
	"quantile", "rank", "reverse", "round", "roundbankers",
	"row_number", "substring", "subtractdays", "subtracthours",
	"subtractminutes", "subtractmonths", "subtractquarters",
	"subtractseconds", "subtractweeks", "subtractyears",
	"sum", "sumwithoverflow", "todate", "todate32", "todate32ornull",
	"todate32orzero", "todateornull", "todateorzero", "todatetime",
	"todatetime64", "todatetime64ornull", "todatetime64orzero", "todatetimeornull",
	"todatetimeorzero", "today", "todecimal128", "todecimal128ornull",
	"todecimal128orzero", "todecimal256", "todecimal256ornull", "todecimal256orzero",
	"todecimal32", "todecimal32ornull", "todecimal32orzero", "todecimal64",
	"todecimal64ornull", "todecimal64orzero", "tofixedstring", "tofloat32",
	"tofloat32ornull", "tofloat32orzero", "tofloat64", "tofloat64ornull",
	"tofloat64orzero", "toint128", "toint128ornull", "toint128orzero",
	"toint16", "toint16ornull", "toint16orzero", "toint256",
	"toint256ornull", "toint256orzero", "toint32", "toint32ornull",
	"toint32orzero", "toint64", "toint64ornull", "toint64orzero",
	"toint8", "toint8ornull", "toint8orzero", "toipv4ornull",
	"toipv4orzero", "toipv6ornull", "toipv6orzero", "tostartofday",
	"tostartofhour", "tostartofminute",
	"tostartofinterval", "tostring", "totimezone", "touint128",
	"touint128ornull", "touint128orzero", "touint16", "touint16ornull",
	"touint16orzero", "touint256", "touint256ornull", "touint256orzero",
	"touint32", "touint32ornull", "touint32orzero", "touint64",
	"touint64ornull", "touint64orzero", "touint8", "touint8ornull",
	"touint8orzero", "touuidornull", "touuidorzero", "toyyyymm", "toyyyymmdd", "toyyyymmddhhmmss", "trim",
	"trimleft", "trimright", "trunc", "truncate", "tuple", "tupleelement",
	"uniq", "uniqcombined", "uniqcombined64", "uniqexact",
	"uniqhll12", "uniqtheta", "upper", "xor",
}

// Aggregate combinator forms do not appear in system.functions as their
// own rows: ClickHouse builds them from a base aggregate plus a suffix.
// Each name here was called against the same server and answered a type
// instead of Code: 46, for example countIf(1, 1=1) is UInt64 and
// quantileState(1) is AggregateFunction(quantile, UInt8).
var serverCombinatorNamesLowered = []string{
	"argmaxif", "argminif", "avgif", "countdistinct", "countif",
	"grouparrayif", "groupuniqarrayif", "maxif", "medianif", "minif",
	"quantileif", "quantilestate", "quantilestateif", "sumif",
	"uniqexactif",
}

func knownServerFunctions() map[string]bool {
	known := make(map[string]bool, len(serverFunctionNamesLowered)+len(serverCombinatorNamesLowered))
	for _, name := range serverFunctionNamesLowered {
		known[name] = true
	}
	for _, name := range serverCombinatorNamesLowered {
		known[name] = true
	}
	return known
}

// TestEveryRuleNamesARealFunction fails when a table names a function
// that the measured server does not have. A new rule for a genuinely new
// function must add the name to the list above, and the name must first
// be measured against a server.
func TestEveryRuleNamesARealFunction(t *testing.T) {
	known := knownServerFunctions()

	tables := []struct {
		table string
		names []string
	}{
		{"functionRegistry", keysOfRegistry()},
		{"higherOrderArrayFunctions", keysOfHigherOrderArray()},
		{"simpleStateSupportedBases", keysOfSimpleStateBases()},
	}

	for _, entry := range tables {
		for _, name := range entry.names {
			if name != strings.ToLower(name) {
				t.Errorf("%s: key %q is not lowercased; the resolver looks rules up by the lowercased call name", entry.table, name)
				continue
			}
			if !known[name] {
				t.Errorf("%s: rule names function %q, which ClickHouse 25.8.29.51 does not have; "+
					"remove the rule, or correct the spelling, or measure the name and add it to serverFunctionNamesLowered",
					entry.table, name)
			}
		}
	}
}

// TestGuardListItselfIsUsed keeps the guard honest. A list entry that no
// table names is dead weight, and it would hide the removal of the rule
// that justified it.
func TestGuardListItselfIsUsed(t *testing.T) {
	used := make(map[string]bool)
	for _, names := range [][]string{
		keysOfRegistry(), keysOfHigherOrderArray(), keysOfSimpleStateBases(),
	} {
		for _, name := range names {
			used[name] = true
		}
	}
	for _, name := range serverCombinatorNamesLowered {
		if !used[name] {
			t.Errorf("serverCombinatorNamesLowered has %q, which no rule table names any more", name)
		}
	}
}

func keysOfRegistry() []string {
	names := make([]string, 0, len(functionRegistry))
	for name := range functionRegistry {
		names = append(names, name)
	}
	return names
}

func keysOfHigherOrderArray() []string {
	names := make([]string, 0, len(higherOrderArrayFunctions))
	for name := range higherOrderArrayFunctions {
		names = append(names, name)
	}
	return names
}

func keysOfSimpleStateBases() []string {
	names := make([]string, 0, len(simpleStateSupportedBases))
	for name := range simpleStateSupportedBases {
		names = append(names, name)
	}
	return names
}
