package engine

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// The parameter verdict table.
//
// THE DEFECT THAT THIS FILE STOPS. A type rule that has no opinion about a
// type PARAMETER gave the parameter back unchanged. The result then held the
// correct base kind and a WRONG parameter: a silently wrong type, which the
// governing rule of this repository names the worst class. Nothing reported
// it, because the shape looked correct. Four measured defects came from this
// one mechanism:
//
//   - groupUniqArray over a bare DateTime leaf kept the timezone that the
//     server drops.
//   - toDate, toDate32 and toDateTime accepted a Decimal that the server
//     refuses with Code: 44.
//   - hex, has and toFixedString had the same class of missing verdict.
//
// THE MECHANISM. A rule that gives a parametric type back must hold a stated
// verdict for the parameter family of that type. The verdict is one of:
//
//	verdictKeep   the parameter survives the call unchanged.
//	verdictDrop   the server builds a fresh type and loses the parameter.
//	verdictRefuse the call has no measured answer, thus chgen refuses.
//
// THE DEFAULT IS REFUSE. A rule and a family with no row in the table refuse.
// A hole is then a refusal that a user reports and that costs one annotation,
// and never a wrong type that nobody sees.
//
// CURATED, NOT DERIVED. Every row below is a measurement over REAL COLUMNS of
// a real table on ClickHouse 25.8.29.51, with BOTH witnesses: DESCRIBE (SELECT
// expr FROM t) over an empty table AND a real SELECT toTypeName(expr) over a
// row. The table is not generated from a probe sweep on purpose: a derived
// table inherits the completeness of its fixture, and fixture incompleteness
// is exactly what hid the timezone defect. A curated table shows a hole as an
// unverified cell that a test can count; a derived table shows a hole as an
// absent row that reads as healthy.
//
// A VERDICT ANSWERS ONE QUESTION ONLY: is the parameter of the rule's result
// COPIED from an argument, unchanged (KEEP), or DROPPED so that the result
// carries no parameter at all (DROP)? A rule whose result parameter is
// neither copied nor dropped, but instead COMPUTED by the supertype lattice
// (commonCHTypes, commonContainerMemberCHType, greatestLeastCommonCHTypes),
// cannot answer that question: the computed value is not present in any one
// argument, so it is not a copy, and it is not absent, so it is not a drop.
// Such a rule needs no verdict cell. See the latticeDerived class of
// unwiredRuleReport below, and isLatticeDerivedRule for how membership is
// derived from the rule's own code path rather than from a name list.

// parameterVerdict is the decision of one rule about one parameter family.
type parameterVerdict int

const (
	// verdictUnstated is the zero value, thus a family that the table
	// does not name reads as unstated and refuses. The zero value is the
	// refusing value on purpose: a forgotten row cannot become a silent
	// copy-through.
	verdictUnstated parameterVerdict = iota
	// verdictKeep says the parameter survives the call unchanged.
	verdictKeep
	// verdictDrop says the server builds a fresh type of the same base
	// kind and loses the parameter.
	verdictDrop
	// verdictRefuse says the pair has no measured answer, thus chgen
	// refuses the call. It differs from verdictUnstated only in that a
	// person wrote it down: an unstated cell is work that remains, and a
	// refusing cell is a decision.
	verdictRefuse
)

func (v parameterVerdict) String() string {
	switch v {
	case verdictKeep:
		return "KEEP"
	case verdictDrop:
		return "DROP"
	case verdictRefuse:
		return "REFUSE"
	default:
		return "UNSTATED"
	}
}

// parameterResultPolicy says why a rule can return a parametric result
// without a row in the parameter verdict table.
type parameterResultPolicy int

const (
	// parameterResultUnknown is the zero value. It always refuses a
	// parametric result that has no curated verdict.
	parameterResultUnknown parameterResultPolicy = iota
	// parameterResultCurated says that every parametric result needs a row in
	// the measured parameter verdict table.
	parameterResultCurated
	// parameterResultLattice says that the result parameters come from
	// the measured common-type lattice. They are computed and are not
	// copied or dropped.
	parameterResultLattice
)

// parameterFamily names a class of type parameter. The family, and not the
// type name, is the key of the table, because one verdict can cover several
// spellings of the same parameter: Decimal32(S) and Decimal(P, S) carry the
// same scale question.
type parameterFamily string

const (
	// familyDateTimeTimezone is the timezone argument of a bare DateTime.
	// It is its OWN family and not a part of the DateTime64 family,
	// because the server treats the two differently: groupUniqArray drops
	// the timezone of a DateTime and keeps every parameter of a
	// DateTime64 (measured, see groupUniqArrayFunctionResult).
	familyDateTimeTimezone parameterFamily = "DateTime.timezone"
	// familyDateTime64Params covers the precision AND the timezone of a
	// DateTime64. The two travel together in LiteralParams and no
	// measurement has yet separated them.
	familyDateTime64Params parameterFamily = "DateTime64.precision+timezone"
	// familyDecimalPrecisionScale covers the precision and the scale of
	// every Decimal spelling.
	familyDecimalPrecisionScale parameterFamily = "Decimal.precision+scale"
	// familyFixedStringLength is the declared width of a FixedString.
	familyFixedStringLength parameterFamily = "FixedString.length"
	// familyEnumTags is the tag list of an Enum8 or an Enum16.
	familyEnumTags parameterFamily = "Enum.tags"
	// familyArrayElement is the element type of an Array.
	familyArrayElement parameterFamily = "Array.element"
	// familyMapKeyValue covers the key type and the value type of a Map.
	familyMapKeyValue parameterFamily = "Map.key+value"
	// familyTupleElements covers the element types of a Tuple and their
	// names.
	familyTupleElements parameterFamily = "Tuple.elements"
	// familyAggregateFunctionInner covers the aggregate name and the
	// argument types inside an AggregateFunction or a
	// SimpleAggregateFunction marker.
	familyAggregateFunctionInner parameterFamily = "AggregateFunction.inner"
	// familyLowCardinalityInner covers the inner type of a
	// LowCardinality wrapper. This family answers differently per rule:
	// assumeNotNull keeps it (measured: assumeNotNull(lc) is
	// LowCardinality(String)), and max drops it (measured: max(lc) is
	// String). A shared valuePreservingKeepFamilies row would therefore
	// be wrong for at least one rule, thus each rule states this family
	// on its own.
	familyLowCardinalityInner parameterFamily = "LowCardinality.inner"
	// familyNullableInner covers the inner type of a Nullable wrapper
	// that a rule returns as its BARE result, distinct from the
	// nullable FLAG that the wrapper transport adds around a result
	// (see nullIfFunctionResult's doc comment on that distinction).
	// arrayElement can return a bare Nullable when it reads a
	// LowCardinality(Nullable(T)) element and strips only the
	// LowCardinality (measured: arrayElement(lcna, 1) is
	// Nullable(String), from lcna Array(LowCardinality(Nullable(String)))).
	familyNullableInner parameterFamily = "Nullable.inner"
)

// parameterFamilyOf reports the parameter family of a type, and false when
// the type carries no parameter and therefore asks no question. A type with
// no parameter can never be copied through wrongly, thus it needs no verdict.
func parameterFamilyOf(value CHType) (parameterFamily, bool) {
	if len(value.Params) == 0 && len(value.LiteralParams) == 0 {
		return "", false
	}
	switch strings.ToLower(value.Name) {
	case "datetime":
		return familyDateTimeTimezone, true
	case "datetime64":
		return familyDateTime64Params, true
	case "decimal", "decimal32", "decimal64", "decimal128", "decimal256":
		return familyDecimalPrecisionScale, true
	case "fixedstring":
		return familyFixedStringLength, true
	case "enum", "enum8", "enum16":
		return familyEnumTags, true
	case "array":
		return familyArrayElement, true
	case "map":
		return familyMapKeyValue, true
	case "tuple":
		return familyTupleElements, true
	case "aggregatefunction", "simpleaggregatefunction":
		return familyAggregateFunctionInner, true
	case "lowcardinality":
		return familyLowCardinalityInner, true
	case "nullable":
		return familyNullableInner, true
	}
	// A parametric type that this list does not name still asks a
	// question, and an unnamed family cannot be answered, thus it must
	// refuse. Returning false here would let an unknown parametric type
	// pass without a verdict, which is the very hole this file closes.
	return parameterFamily("unnamed:" + strings.ToLower(value.Name)), true
}

// parameterVerdictKey names one cell of the table: one rule and one family.
// The rule is named by the REGISTRY NAME of the function and not by the Go
// function that implements the rule, because two names can share one Go rule
// and still differ on the server. groupArray and groupUniqArray build their
// element type through one shared helper and disagree about the DateTime
// timezone, thus a key on the Go rule could not tell them apart.
type parameterVerdictKey struct {
	rule   string
	family parameterFamily
}

// valuePreservingKeepFamilies is the measured accept-set of the functions
// that give their first argument back unchanged.
//
// Measured on ClickHouse 25.8.29.51 over real columns of a real table, with
// BOTH witnesses agreeing on every cell (DESCRIBE over an empty table and
// SELECT toTypeName over a row):
//
//	max(dtz)    DateTime('UTC')        max(dtz64)  DateTime64(3, 'UTC')
//	max(dec)    Decimal(18, 4)         max(en)     Enum8('a' = 1, 'b' = 2)
//	max(fs)     FixedString(8)         max(arrdtz) Array(DateTime('UTC'))
//	max(m)      Map(String, Decimal(18, 4))
//	max(tp)     Tuple(a Int32, b DateTime('UTC'))
//
// min, any, anyLast, argMax, argMin, maxIf, minIf, first_value and last_value
// answer the same type for the same argument on every one of those cells.
// arraySort, arraySlice and arrayResize answer the same over the Array
// spellings of those leaves, and lagInFrame and leadInFrame answer the same
// inside an OVER clause.
//
// The AggregateFunction marker family is in the list, and the measurement is
// its own. Measured on ClickHouse 25.8.29.51 over the columns
// agg AggregateFunction(uniq, UInt64), saf SimpleAggregateFunction(anyLast,
// Int32) and safs SimpleAggregateFunction(min, String), both witnesses:
//
//	any(agg)          AggregateFunction(uniq, UInt64)
//	anyLast(agg)      AggregateFunction(uniq, UInt64)
//	argMax(agg, i32)  AggregateFunction(uniq, UInt64)
//	argMin(agg, i32)  AggregateFunction(uniq, UInt64)
//	max(saf)          SimpleAggregateFunction(anyLast, Int32)
//	min(saf)          SimpleAggregateFunction(anyLast, Int32)
//	any(saf)          SimpleAggregateFunction(anyLast, Int32)
//	max(safs)         SimpleAggregateFunction(min, String)
//	arraySort([saf])  Array(SimpleAggregateFunction(anyLast, Int32))
//
// The marker and its inner types survive every one of those calls, thus the
// verdict is KEEP. This cell was UNSTATED in the first draft of the table and
// the gate then refused any(agg) and argMax(agg, i32), which the server RUNS.
// Two existing tests named that refusal, which is the mechanism working as
// intended: a refusal WIDER than the server's is also a defect, and an
// unstated cell shows it as a failing test rather than as a wrong type.
var valuePreservingKeepFamilies = []parameterFamily{
	familyDateTimeTimezone,
	familyDateTime64Params,
	familyDecimalPrecisionScale,
	familyFixedStringLength,
	familyEnumTags,
	familyArrayElement,
	familyMapKeyValue,
	familyTupleElements,
	familyAggregateFunctionInner,
}

// valuePreservingRuleNames are the registry names whose rule is
// firstFunctionArgument: the rule gives argument zero back unchanged. These
// are exactly the names that the measurement above covers.
var valuePreservingRuleNames = []string{
	"argmax", "argmin", "max", "min", "any", "anylast",
	"maxif", "minif", "argmaxif", "argminif",
	"arraysort", "arrayslice", "arrayresize",
	"first_value", "last_value", "laginframe", "leadinframe", "lag", "lead", "nth_value",
	"first_value_respect_nulls", "firstvaluerespectnulls", "last_value_respect_nulls", "lastvaluerespectnulls",
}

// unwrapKeepRuleNames are registry names whose rule gives an argument's
// type back with an outer wrapper added or removed, and never rebuilds
// the leaf itself. assumeNotNull removes an outer Nullable, nullIf adds
// one, arrayElement and mapValues read one member out of a container, and
// arrayDistinct removes an outer Nullable from an Array element. None of
// them touch a parameter of the LEAF, thus every one of the nine leaf
// families keeps its value. LowCardinality.inner and Nullable.inner are
// NOT in this shared set, because the two wrapper families answer
// differently per rule; see the dedicated rows below.
//
// Measured on ClickHouse 25.8.29.51 over real columns of a real table
// (dtz64 DateTime64(3, 'UTC'), dec Decimal(18, 4) / Decimal(10, 2), en
// Enum8('a' = 1, 'b' = 2), fs FixedString(8), arrdtz Array(DateTime('UTC')),
// m Map(String, Decimal(18, 4)), tp Tuple(a Int32, b DateTime('UTC'))),
// both witnesses (DESCRIBE over an empty table and a real SELECT
// toTypeName over a row):
//
//	assumeNotNull(ndtz)   DateTime('UTC')          (ndtz Nullable(DateTime('UTC')))
//	assumeNotNull(ndec)   Decimal(18, 4)
//	assumeNotNull(en)     Enum8('a' = 1, 'b' = 2)   (en itself, not nullable)
//	nullIf(dtz, dtz2)     Nullable(DateTime('UTC'))
//	nullIf(fs, fs)        Nullable(FixedString(8))
//	arrayElement(arrdtz,1) DateTime('UTC')
//	mapKeys(m)            Array(String)
//	mapValues(m)          Array(Decimal(18, 4))
//	arrayDistinct(adtz)   Array(DateTime('UTC'))   (adtz Array(Nullable(DateTime('UTC'))))
//	tuple(dtz64, dec)     Tuple(DateTime64(3, 'UTC'), Decimal(10, 2))
var unwrapKeepRuleNames = []string{
	"assumenotnull", "nullif", "arrayelement", "mapkeys", "mapvalues",
	"arraydistinct", "tuple",
}

// aggregateStateKeepRuleNames are registry names whose rule wraps the
// first argument, unchanged, inside a fresh AggregateFunction marker
// (aggregateStateFunctionArgument). Only the AggregateFunction.inner
// family applies, because the rule's own result is always an
// AggregateFunction and the caller asks about the RESULT's family.
//
// Measured on ClickHouse 25.8.29.51 over a real column dtz64
// DateTime64(3, 'UTC'), both witnesses:
//
//	quantileState(0.5)(dtz64)  AggregateFunction(quantile(0.5), DateTime64(3, 'UTC'))
var aggregateStateKeepRuleNames = []string{
	"quantilestate", "quantilestateif",
}

// quantileMedianKeepRuleNames are registry names whose rule is
// quantileFunctionArgument. The rule already computes the correct
// Decimal(P, S) and the correct DateTime64(precision, timezone) by
// reading the argument, thus the family verdict here confirms the rule's
// own computation rather than copying an unrelated parameter.
//
// Measured on ClickHouse 25.8.29.51 over real columns dtz64
// DateTime64(3, 'UTC') and dec Decimal(10, 2), both witnesses:
//
//	median(dtz64)          DateTime64(3, 'UTC')
//	quantile(0.5)(dtz64)   DateTime64(3, 'UTC')
//	median(dec)            Decimal(10, 2)
//	medianIf(dec, pred)    Decimal(10, 2)
//	quantileIf(0.5)(dtz64, pred) DateTime64(3, 'UTC')
var quantileMedianKeepRuleNames = []string{
	"quantile", "quantileif", "median", "medianif",
}

// sumKeepRuleNames are registry names whose rule is sumFunctionArgument.
// The rule already widens the Decimal precision itself
// (decimalSumPrecision) and keeps the scale, thus this verdict confirms
// the rule's own computed Decimal.precision+scale rather than copying an
// argument's parameter unchanged.
//
// Measured on ClickHouse 25.8.29.51 over a real column dec Decimal(18, 4),
// both witnesses:
//
//	sum(dec)    Decimal(38, 4)
//	sumIf(dec, pred)  Decimal(38, 4)
var sumKeepRuleNames = []string{
	"sum", "sumif",
}

// absNegateKeepRuleNames are registry names whose rule keeps an
// arithmetic Decimal argument's scale unchanged (absFunctionType and
// negateFunctionType both give a Decimal argument back verbatim; every
// other accepted argument type is a fixed-width integer or float with no
// parameter).
//
// Measured on ClickHouse 25.8.29.51 over a real column dec
// Decimal(18, 4), both witnesses:
//
//	abs(dec)     Decimal(18, 4)
//	negate(dec)  Decimal(18, 4)
var absNegateKeepRuleNames = []string{
	"abs", "negate",
}

// parameterVerdicts is the curated table. Every row carries a measurement in
// the comment of the block that builds it.
var parameterVerdicts = buildParameterVerdicts()

func buildParameterVerdicts() map[parameterVerdictKey]parameterVerdict {
	table := make(map[parameterVerdictKey]parameterVerdict)
	for _, name := range valuePreservingRuleNames {
		for _, family := range valuePreservingKeepFamilies {
			table[parameterVerdictKey{rule: name, family: family}] = verdictKeep
		}
	}
	for _, name := range unwrapKeepRuleNames {
		for _, family := range valuePreservingKeepFamilies {
			table[parameterVerdictKey{rule: name, family: family}] = verdictKeep
		}
	}
	for _, name := range aggregateStateKeepRuleNames {
		table[parameterVerdictKey{rule: name, family: familyAggregateFunctionInner}] = verdictKeep
	}
	for _, name := range quantileMedianKeepRuleNames {
		table[parameterVerdictKey{rule: name, family: familyDecimalPrecisionScale}] = verdictKeep
		table[parameterVerdictKey{rule: name, family: familyDateTime64Params}] = verdictKeep
		table[parameterVerdictKey{rule: name, family: familyDateTimeTimezone}] = verdictKeep
	}
	for _, name := range sumKeepRuleNames {
		table[parameterVerdictKey{rule: name, family: familyDecimalPrecisionScale}] = verdictKeep
	}
	for _, name := range absNegateKeepRuleNames {
		table[parameterVerdictKey{rule: name, family: familyDecimalPrecisionScale}] = verdictKeep
	}

	// These rules keep one measured parameter family. The tests use real
	// fixture columns and include both positive and negative domain cells.
	// The rules cannot reach the other parameter families through their
	// measured domains.
	for _, name := range []string{"lower", "upper"} {
		table[parameterVerdictKey{rule: name, family: familyFixedStringLength}] = verdictKeep
	}
	for _, name := range []string{"round", "roundbankers", "floor", "ceil", "trunc", "sumwithoverflow"} {
		table[parameterVerdictKey{rule: name, family: familyDecimalPrecisionScale}] = verdictKeep
	}

	// reverse keeps a FixedString width, keeps an Array element after
	// its rule removes an inner LowCardinality, and reverses a Tuple
	// while each name stays with its element. Measured on ClickHouse
	// 25.8.29.51 over real columns, with both witnesses:
	//
	//	reverse(fs8)     FixedString(8)
	//	reverse(arr_i)   Array(Int32)
	//	reverse(arr_lc)  Array(String)
	//	reverse(tup)     Tuple(String, Int32)
	//	reverse(ntup)    Tuple(y String, x Int32)
	//	reverse(at)      Array(Tuple(a Int32, b String))
	//
	// These rows confirm the result that reverseFunctionType already
	// builds. They do not copy an argument without that rule.
	table[parameterVerdictKey{rule: "reverse", family: familyFixedStringLength}] = verdictKeep
	table[parameterVerdictKey{rule: "reverse", family: familyArrayElement}] = verdictKeep
	table[parameterVerdictKey{rule: "reverse", family: familyTupleElements}] = verdictKeep

	// LowCardinality.inner and Nullable.inner answer differently per
	// rule (wrapperOpaque rules see the RAW argument, LowCardinality
	// wrapper included, thus the leaf-level measurements above do not
	// cover them). Measured on ClickHouse 25.8.29.51 over real columns,
	// both witnesses:
	//
	//	assumeNotNull(lc)              LowCardinality(String)   KEEP
	//	assumeNotNull(lcn)              LowCardinality(String)   KEEP (Nullable stripped, LC kept)
	//	tuple(lc, i32)                  Tuple(LowCardinality(String), Int32)  KEEP (nested, untouched)
	//	mapKeys(map(toLowCardinality('k'), 1))    Array(String)   DROP
	//	mapValues(map('a', toLowCardinality(5)))  Array(Int32)    DROP
	//	arrayElement(lcna, 1)           Nullable(String)   from
	//	    lcna Array(LowCardinality(Nullable(String))): the rule's own
	//	    stripNestedLowCardinality already removes the LowCardinality
	//	    layer, thus arrayElement's RESULT never carries the
	//	    LowCardinality family and needs no row for it. What remains
	//	    parametric in the result is the Nullable that
	//	    stripNestedLowCardinality left behind, hence the
	//	    Nullable.inner KEEP row below.
	table[parameterVerdictKey{rule: "assumenotnull", family: familyLowCardinalityInner}] = verdictKeep
	table[parameterVerdictKey{rule: "tuple", family: familyLowCardinalityInner}] = verdictKeep
	table[parameterVerdictKey{rule: "mapkeys", family: familyLowCardinalityInner}] = verdictDrop
	table[parameterVerdictKey{rule: "mapvalues", family: familyLowCardinalityInner}] = verdictDrop
	table[parameterVerdictKey{rule: "arrayelement", family: familyNullableInner}] = verdictKeep

	// The groupArray family propagates the argument type, thus every
	// parameter of the leaf survives. Measured on ClickHouse 25.8.29.51,
	// both witnesses:
	//
	//	groupArray(dtz)   Array(DateTime('UTC'))
	//	groupArray(en)    Array(Enum8('a' = 1, 'b' = 2))
	//
	// The result is always an Array, thus the family that the caller asks
	// about is familyArrayElement. The element carries its own parameters
	// and groupArrayElementType builds it, so one KEEP row covers the
	// shape.
	for _, name := range []string{"grouparray", "grouparrayif"} {
		table[parameterVerdictKey{rule: name, family: familyArrayElement}] = verdictKeep
	}

	// The groupUniqArray family builds a FRESH element type for a bare
	// DateTime leaf and loses the timezone, and keeps every parameter of
	// every other leaf. Measured on ClickHouse 25.8.29.51, both witnesses,
	// and reproduced on a second server instance:
	//
	//	groupUniqArray(dtz)    Array(DateTime)              timezone LOST
	//	groupUniqArray(dtz64)  Array(DateTime64(3, 'UTC'))  kept
	//	groupUniqArray(dec)    Array(Decimal(18, 4))        kept
	//	groupUniqArray(en)     Array(Enum8('a' = 1, 'b' = 2)) kept
	//	groupUniqArray(fs)     Array(FixedString(8))        kept
	//
	// The cause is in the server (AggregateFunctionGroupUniqArray.cpp
	// builds a fresh DataTypeDateTime) and not in the If combinator.
	// This is the row that shows why the table needs three verdicts and
	// not two: the correct answer here is neither KEEP nor REFUSE.
	for _, name := range []string{"groupuniqarray", "groupuniqarrayif"} {
		table[parameterVerdictKey{rule: name, family: familyArrayElement}] = verdictKeep
		table[parameterVerdictKey{rule: name, family: familyDateTimeTimezone}] = verdictDrop
		// The other measured leaves keep every parameter.
		table[parameterVerdictKey{rule: name, family: familyDateTime64Params}] = verdictKeep
		table[parameterVerdictKey{rule: name, family: familyDecimalPrecisionScale}] = verdictKeep
		table[parameterVerdictKey{rule: name, family: familyEnumTags}] = verdictKeep
		table[parameterVerdictKey{rule: name, family: familyFixedStringLength}] = verdictKeep
		// Measured with both witnesses:
		// groupUniqArray(saf) is
		// Array(SimpleAggregateFunction(anyLast, Int32)), thus the
		// marker and its inner types survive.
		table[parameterVerdictKey{rule: name, family: familyAggregateFunctionInner}] = verdictKeep
		// Measured with both witnesses:
		// groupUniqArray(m)  Array(Map(String, Decimal(18, 4)))
		// groupUniqArray(tp) Array(Tuple(a Int32, b DateTime('UTC')))
		// The container parameters survive, and the timezone INSIDE a
		// Tuple element survives as well: only a bare DateTime at the
		// top of the leaf loses it.
		table[parameterVerdictKey{rule: name, family: familyMapKeyValue}] = verdictKeep
		table[parameterVerdictKey{rule: name, family: familyTupleElements}] = verdictKeep
	}

	return table
}

// parameterVerdictFor reports the verdict of one rule about one parameter
// family. A pair with no row gives verdictUnstated, thus the caller refuses.
func parameterVerdictFor(rule string, family parameterFamily) parameterVerdict {
	return parameterVerdicts[parameterVerdictKey{rule: strings.ToLower(rule), family: family}]
}

// ruleHasAnyVerdict reports whether the table names the rule at all.
func ruleHasAnyVerdict(rule string) bool {
	lowered := strings.ToLower(rule)
	for key := range parameterVerdicts {
		if key.rule == lowered {
			return true
		}
	}
	return false
}

// applyParameterVerdict is the gate. It runs after a rule has produced a
// result and before the result leaves the generic function path. It asks the
// table what the rule decided about the parameter family of the result, and:
//
//	KEEP     the result goes back unchanged.
//	DROP     the parameter is removed from the result.
//	UNSTATED and REFUSE both refuse.
//
// The gate reads the family of the RESULT and not of the argument, because
// the result is what the caller will use. A result with no parameter asks no
// question and passes through.
func applyParameterVerdict(rule string, result CHType) (CHType, error) {
	if _, parametric := parameterFamilyOf(result); !parametric {
		return result, nil
	}
	if ruleHasAnyVerdict(rule) {
		return applyParameterVerdictStrict(rule, result)
	}
	spec, ok := functionRegistry[strings.ToLower(rule)]
	if ok && spec.parameterPolicy == parameterResultLattice {
		return result, nil
	}
	return CHType{}, fmt.Errorf(
		"function %s gives a parametric result, but its parameter policy is unknown; %s",
		rule, pinTypeHint,
	)
}

// applyParameterVerdictStrict applies only the curated verdict table. Every
// parametric result must hold a verdict. It does not accept a lattice policy.
func applyParameterVerdictStrict(rule string, result CHType) (CHType, error) {
	family, parametric := parameterFamilyOf(result)
	if !parametric {
		return result, nil
	}
	switch parameterVerdictFor(rule, family) {
	case verdictKeep:
		return result, nil
	case verdictDrop:
		return CHType{Name: result.Name}, nil
	default:
		return CHType{}, fmt.Errorf(
			"function %s gives a %s result, and no measured verdict says what happens to its %s parameter; %s",
			rule, result.Name, family, pinTypeHint,
		)
	}
}

// allParameterFamilies lists every family that unverifiedParameterCells and
// reachableUnverifiedParameterCells check. It is the same 10-family list the
// curated table itself measures against.
var allParameterFamilies = []parameterFamily{
	familyDateTimeTimezone,
	familyDateTime64Params,
	familyDecimalPrecisionScale,
	familyFixedStringLength,
	familyEnumTags,
	familyArrayElement,
	familyMapKeyValue,
	familyTupleElements,
	familyAggregateFunctionInner,
	familyLowCardinalityInner,
}

// unverifiedParameterCells lists every rule and family pair that the table
// covers by rule but leaves unstated for that family. It states the remaining
// work as a concrete list, and TestUnverifiedParameterCells
// prints it. This is bookkeeping and never a behaviour: an unstated cell
// refuses, whether or not this function names it.
//
// THE COUNT HERE IS MOSTLY NOISE. It is the Cartesian product of every rule
// that the table names and all 10 families. It never asks
// whether the rule can actually PRODUCE a result of that family. A rule that
// the server refuses for 8 of the 9 argument shapes still shows 8 unstated
// cells here, and the count then RISES when the one reachable cell gets
// wired. reachableUnverifiedParameterCells is the number to read for real
// remaining work; this function stays for the raw total and for callers that
// already filter it themselves.
func unverifiedParameterCells() []string {
	rules := make(map[string]struct{})
	for key := range parameterVerdicts {
		rules[key.rule] = struct{}{}
	}
	var cells []string
	for rule := range rules {
		for _, family := range allParameterFamilies {
			if parameterVerdictFor(rule, family) == verdictUnstated {
				cells = append(cells, rule+" / "+string(family))
			}
		}
	}
	sort.Strings(cells)
	return cells
}

// familyProbeArguments gives one representative argument CHType per family,
// used ONLY to ask "can this rule's OWN Go function ever return a result of
// this family", never to guess a server accept-set from a fixture. Each
// probe is the same shape unverifiedParameterCells and the curated table
// already reason about elsewhere in this file (a bare DateTime with a
// timezone, a DateTime64 with precision and timezone, a Decimal with
// precision and scale, and so on), so a probe here is not a new measurement
// surface: it is the argument side of a question the table already answers
// on the result side.
var familyProbeArguments = map[parameterFamily]CHType{
	familyDateTimeTimezone:       {Name: "DateTime", LiteralParams: []string{"'UTC'"}},
	familyDateTime64Params:       {Name: "DateTime64", LiteralParams: []string{"3", "'UTC'"}},
	familyDecimalPrecisionScale:  {Name: "Decimal", LiteralParams: []string{"18", "4"}},
	familyFixedStringLength:      {Name: "FixedString", LiteralParams: []string{"8"}},
	familyEnumTags:               {Name: "Enum8", LiteralParams: []string{"'a' = 1", "'b' = 2"}},
	familyArrayElement:           {Name: "Array", Params: []CHType{{Name: "Int32"}}},
	familyMapKeyValue:            {Name: "Map", Params: []CHType{{Name: "String"}, {Name: "Int32"}}},
	familyTupleElements:          {Name: "Tuple", Params: []CHType{{Name: "Int32"}, {Name: "String"}}},
	familyAggregateFunctionInner: {Name: "AggregateFunction", Params: []CHType{{Name: "uniq"}, {Name: "UInt64"}}},
	familyLowCardinalityInner:    {Name: "LowCardinality", Params: []CHType{{Name: "String"}}},
}

// probeDomainInput reports the type that the registry's argument domain
// should see for a given probe. checkArgumentDomain's own doc comment
// says the caller passes "the base type... without its Nullable and
// LowCardinality wrappers", so a LowCardinality.inner or Nullable.inner
// probe (which is itself a wrapper around a plain base type) is unwrapped
// one layer before the domain check, exactly as the real resolver path
// unwraps an argument before calling checkArgumentDomain. Every other
// probe already names a bare base type and needs no unwrapping.
func probeDomainInput(probe CHType) CHType {
	if strings.EqualFold(probe.Name, "LowCardinality") && len(probe.Params) == 1 {
		return probe.Params[0]
	}
	if strings.EqualFold(probe.Name, "Nullable") && len(probe.Params) == 1 {
		return probe.Params[0]
	}
	return probe
}

// ruleCanReachFamily reports whether calling the registry rule for name with
// the family's representative argument produces a RESULT of that same
// family, without error. This is a fact about the rule's own Go code, called
// in-process: it is not a probe against the server and it is not a table
// that survives past one call, so it does not become the kind of derived
// table the CURATED, NOT DERIVED note above rejects. It answers a narrower
// question than "does the server accept this call": a rule that erred here
// could still be reachable through a DIFFERENT argument of the same family
// (case in point: abs accepts a Decimal argument and refuses every other
// family's representative, which is exactly the measurement this function
// reproduces for abs / familyDecimalPrecisionScale).
//
// The check has two gates, matching the two steps of the real resolver
// path (inferFunctionType checks spec.domain before it ever calls
// spec.rule):
//
//  1. The registered domain, if any, must accept the probe (unwrapped one
//     LowCardinality or Nullable layer first; see probeDomainInput). A rule
//     like sumFunctionArgument has a "default: result = first" branch that
//     would otherwise echo an unsupported argument straight back, which is
//     not what the server does: the domain is what actually refuses
//     sum(dateTimeColumn) with Code 43. Skipping this gate would report
//     every one of sum's 8 unreachable families as reachable, which is the
//     exact false positive this function must not produce.
//  2. The rule itself must return a result whose family matches the one
//     asked about. This gate catches the OTHER direction of the same
//     mistake: groupArray has no domain (it accepts every type), but
//     groupArrayElementType always strips a LowCardinality wrapper before
//     it builds the Array element (measured groupArray(lc) is
//     Array(String), never Array(LowCardinality(String))), so
//     LowCardinality.inner is unreachable for groupArray even though the
//     call itself succeeds.
//
// A rule missing from functionRegistry, or with a nil rule, reports
// unreachable: absence of a positive answer is the safe default, in the
// same spirit as verdictUnstated refusing by default.
func ruleCanReachFamily(rule string, family parameterFamily) bool {
	spec, ok := functionRegistry[strings.ToLower(rule)]
	if !ok || spec.rule == nil {
		return false
	}
	probe, ok := familyProbeArguments[family]
	if !ok {
		return false
	}
	if spec.domain != nil && !spec.domain.accepts(probeDomainInput(probe)) {
		return false
	}
	// wrapperAggregate ALWAYS removes the LowCardinality wrapper in the
	// wrapper-transport step that runs AFTER the rule (resolver.go's own
	// doc comment on the enum member: "The LowCardinality wrapper is
	// always removed"). A raw-rule probe cannot see that step, and a
	// wrapperAggregate rule such as firstFunctionArgument echoes the
	// wrapped probe straight back, so probing the rule alone would
	// report a false positive here (measured: max(lc) is String, never
	// LowCardinality(String), although firstFunctionArgument's own
	// return value still says LowCardinality(String)). This is the
	// single point where a fact about spec.class, and not about
	// spec.rule, decides the family.
	if family == familyLowCardinalityInner && spec.class == wrapperAggregate {
		return false
	}
	result, err := spec.rule([]CHType{probe})
	if err != nil {
		return false
	}
	gotFamily, parametric := parameterFamilyOf(result)
	return parametric && gotFamily == family
}

// reachableUnverifiedParameterCells is unverifiedParameterCells filtered to
// the cells that a rule can produce, as measured by ruleCanReachFamily. This
// is the count of the real remaining work, with the
// structurally impossible pairs (a rule and a family the rule's own code can
// never return) removed. It moves the RIGHT way when a rule is wired: wiring
// a reachable cell removes exactly one entry here, and wiring is impossible
// for an unreachable cell because no call ever produces that family, so this
// count can only fall or stay flat as work proceeds.
func reachableUnverifiedParameterCells() []string {
	var cells []string
	for _, cell := range unverifiedParameterCells() {
		rule, family, ok := strings.Cut(cell, " / ")
		if !ok {
			// unverifiedParameterCells always joins with " / "; a cell
			// that does not split is a bug in that function and not a
			// family this one should silently drop.
			cells = append(cells, cell)
			continue
		}
		if ruleCanReachFamily(rule, parameterFamily(family)) {
			cells = append(cells, cell)
		}
	}
	return cells
}

// unwiredRuleReport is the count and the risk class of every registry rule
// that ruleHasAnyVerdict does not name. THE DEFECT THAT THIS FUNCTION MAKES
// VISIBLE: applyParameterVerdict skips the gate entirely for a rule with no
// row in the table, thus refuse-by-default is in force only INSIDE the
// wired rules. A rule outside that set can still copy an unknown parameter
// through in silence, and before this function no test named that count.
//
// The rules split into five classes:
//
//	nilRule         the registry gives no rule function at all, thus the
//	                name never reaches inferFunctionType's generic path
//	                and the gate can never see its result. No risk.
//	sizedMarker     the name is a sized-constructor marker
//	                (addSizedConstructorSemanticSpecs): inferFunctionType
//	                routes it to inferSizedConstructorType BEFORE the
//	                registry rule ever runs, and the registry rule is a
//	                refusal stub that exists only so other lookups (the
//	                wrapper class, the generator spec) have one place to
//	                read. The stub always errors, so fixedResultFunctionType
//	                cannot classify it; it needs its own check. No risk
//	                through this gate: the size comes from a constant
//	                argument of the call, not from an existing typed
//	                argument, and sizedConstructorResult builds it
//	                directly (sized_constructor.go). That path is
//	                its own family, not this one.
//	fixed           the rule answers the SAME result type for two
//	                unrelated argument types (checked with
//	                fixedResultFunctionType). Such a rule COMPUTES a type
//	                and cannot copy an argument's parameter through. No
//	                risk.
//	latticeDerived  the rule builds its result with the supertype lattice
//	                (commonCHTypes, commonContainerMemberCHType or
//	                greatestLeastCommonCHTypes), checked by
//	                isLatticeDerivedRule. A verdict answers "copied or
//	                dropped", and a lattice-built parameter is neither: it
//	                is COMPUTED from every argument together, so it asks a
//	                question a verdict cell cannot answer. No risk through
//	                THIS gate; the lattice function itself is the witness,
//	                and its own tests are where a defect must show up.
//	parametric      the rule's result type depends on its argument type,
//	                it is not a sized-constructor marker, and it is not
//	                lattice-derived. This is the class that COULD copy an
//	                unknown parameter through, thus it is the real
//	                remaining risk and the next work item.
type unwiredRuleReport struct {
	nilRule        []string
	sizedMarker    []string
	fixed          []string
	latticeDerived []string
	parametric     []string
}

// total reports how many registry rules ruleHasAnyVerdict does not name.
func (r unwiredRuleReport) total() int {
	return len(r.nilRule) + len(r.sizedMarker) + len(r.fixed) + len(r.latticeDerived) + len(r.parametric)
}

// buildUnwiredRuleReport walks functionRegistry and classifies every rule
// that ruleHasAnyVerdict does not cover. It is bookkeeping and it changes
// its answer only when the registry or the curated table changes.
func buildUnwiredRuleReport() unwiredRuleReport {
	var report unwiredRuleReport
	for name, spec := range functionRegistry {
		if ruleHasAnyVerdict(name) {
			continue
		}
		if _, sized := sizedConstructors[name]; sized {
			report.sizedMarker = append(report.sizedMarker, name)
			continue
		}
		switch {
		case spec.rule == nil:
			report.nilRule = append(report.nilRule, name)
		case fixedResultFunctionType(spec.rule):
			report.fixed = append(report.fixed, name)
		case isLatticeDerivedRule(name, spec.rule):
			report.latticeDerived = append(report.latticeDerived, name)
		default:
			report.parametric = append(report.parametric, name)
		}
	}
	sort.Strings(report.nilRule)
	sort.Strings(report.sizedMarker)
	sort.Strings(report.fixed)
	sort.Strings(report.latticeDerived)
	sort.Strings(report.parametric)
	return report
}

// latticeProbeDecimalNarrow and latticeProbeDecimalWide are the two
// arguments that isLatticeDerivedRule feeds to a candidate rule. Their
// shapes are decisive: a rule that builds its result by copying one
// argument or by a fixed computation cannot answer Decimal(38, 4), while
// every lattice function used in this repository does, because a wider
// Decimal absorbs a narrower one (commonCHTypes; measured in
// supertype.go and greatest_least_common_type_test.go).
//
// latticeProbeSigned and latticeProbeUnsigned64 are the second probe
// pair. A Decimal pair alone cannot tell "greatest" from "least" apart,
// because commonCHTypes's Decimal branch does not read the name.
// greatestLeastCommonCHTypes's OWN signed/UInt64 special case does read
// the name (greatest(Int64, UInt64) is UInt64, least(Int64, UInt64) is
// Int64; see greatestLeastSignedUInt64Pair), so this pair is the one
// that catches a rule closed over the WRONG name.
var (
	latticeProbeDecimalNarrow = CHType{Name: "Decimal", LiteralParams: []string{"9", "2"}}
	latticeProbeDecimalWide   = CHType{Name: "Decimal", LiteralParams: []string{"38", "4"}}
	latticeProbeSigned        = CHType{Name: "Int64"}
	latticeProbeUnsigned64    = CHType{Name: "UInt64"}
)

// isLatticeDerivedRule reports whether the registry rule at name builds
// its result with the supertype lattice: commonCHTypes,
// commonContainerMemberCHType or greatestLeastCommonCHTypes. Membership
// must come from the rule's own behaviour and never from a hand-written
// name list. A list becomes stale when a new lattice-built rule is registered
// without a matching list update. A previous four-name list had this defect.
//
// The check has two parts, one per calling shape:
//
//  1. arrayFunctionResult and mapFunctionResult are top-level functions
//     called DIRECTLY as spec.rule (see registry.go), so a rule value
//     that points at either one is identified by comparing function
//     pointers with reflect. This is exact: it can never mistake an
//     unrelated rule for these two, and it can never miss them.
//
//  2. greatestLeastFunctionType is a FACTORY: registry.go calls it once
//     per name ("greatest", "least") and stores the returned CLOSURE,
//     so no fixed function pointer identifies it. Instead the rule is
//     probed with two argument pairs whose lattice answer is well known
//     (a Decimal pair, where the wider one absorbs the narrower one, and
//     a signed/UInt64 pair, where the answer depends on whether the
//     rule was closed over "greatest" or "least"; see
//     greatestLeastSignedUInt64Pair) and the result is compared, in both
//     argument orders, against calling greatestLeastCommonCHTypes
//     directly under the same name. Only a rule whose BODY dispatches to
//     that lattice function for THIS name can match every probe.
func isLatticeDerivedRule(name string, rule functionTypeRule) bool {
	if rule == nil {
		return false
	}
	if functionPointerEquals(rule, arrayFunctionResult) || functionPointerEquals(rule, mapFunctionResult) {
		return true
	}
	return matchesGreatestLeastLattice(name, rule)
}

// functionPointerEquals reports whether two functionTypeRule values point
// at the same top-level function. It is only meaningful for a rule that
// registry.go stores directly (not a closure returned by a factory),
// because two closures from the same factory share one code pointer even
// when they close over different state.
func functionPointerEquals(a, b functionTypeRule) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// matchesGreatestLeastLattice reports whether rule reproduces
// greatestLeastCommonCHTypes's answer, in both argument orders, for both
// decisive probe pairs. name is passed to greatestLeastCommonCHTypes
// exactly as greatestLeastFunctionType's closure would pass its own
// factory argument, so the probe only passes for a rule that actually
// dispatches to the lattice under THIS name.
func matchesGreatestLeastLattice(name string, rule functionTypeRule) bool {
	return matchesLatticePairBothOrders(name, rule, latticeProbeDecimalNarrow, latticeProbeDecimalWide) &&
		matchesLatticePairBothOrders(name, rule, latticeProbeSigned, latticeProbeUnsigned64)
}

// matchesLatticePairBothOrders reports whether rule gives the same answer
// as greatestLeastCommonCHTypes(name, ...) for the pair (left, right) and
// for the swapped pair (right, left). A mismatch in either direction, or
// an error where the lattice function gives an answer, fails the probe.
func matchesLatticePairBothOrders(name string, rule functionTypeRule, left, right CHType) bool {
	if !matchesLatticePair(name, rule, left, right) {
		return false
	}
	return matchesLatticePair(name, rule, right, left)
}

// matchesLatticePair reports whether rule([]CHType{left, right}) equals
// greatestLeastCommonCHTypes(name, []CHType{left, right}).
func matchesLatticePair(name string, rule functionTypeRule, left, right CHType) bool {
	want, wantErr := greatestLeastCommonCHTypes(name, []CHType{left, right})
	if wantErr != nil {
		return false
	}
	got, gotErr := rule([]CHType{left, right})
	if gotErr != nil {
		return false
	}
	return got.String() == want.String()
}
