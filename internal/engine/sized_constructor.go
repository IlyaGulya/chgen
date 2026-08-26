package engine

import (
	"fmt"
	"strconv"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// This file holds the type rules of the wide and the sized type
// constructors, together with the *OrNull and the *OrZero variants of the
// cast family.
//
// Every rule below was measured on ClickHouse 25.8.29.51 (image
// clickhouse/clickhouse-server:25.8, disposable container) with
// DESCRIBE (SELECT <expression> FROM t) over real table columns. A
// measurement over literals only is not usable, because ClickHouse folds
// constants and a folded constant reports a different LowCardinality
// wrapper.
//
// The measured rules:
//
//	toInt128(i32)             -> Int128
//	toUInt256(u32)            -> UInt256
//	toFixedString(s, 5)       -> FixedString(5)
//	toDecimal32(f64, 2)       -> Decimal(9, 2)
//	toDecimal64(f64, 2)       -> Decimal(18, 2)
//	toDecimal128(f64, 10)     -> Decimal(38, 10)
//	toDecimal256(f64, 5)      -> Decimal(76, 5)
//	toDecimal64(d128, 7)      -> Decimal(18, 7)   (the argument scale
//	                                               does not reach the
//	                                               result)
//	toInt32OrNull(s)          -> Nullable(Int32)
//	toInt32OrZero(s)          -> Int32
//	toInt32OrZero(ns)         -> Nullable(Int32)  (OrZero does NOT
//	                                               remove Nullable)
//	toInt32OrNull(lc)         -> LowCardinality(Nullable(Int32))
//	toDecimal64(lc, 2)        -> Decimal(18, 2)   (a Decimal result
//	                                               drops LowCardinality)

// sizedConstructorKind says where a constructor takes its result size.
type sizedConstructorKind int

const (
	// sizedConstructorNone: the result type is complete without a size
	// argument (toInt128, toUInt256, toInt32OrZero).
	sizedConstructorNone sizedConstructorKind = iota
	// sizedConstructorDecimal: the precision comes from the name and the
	// scale from a constant argument (toDecimal64(x, S)).
	sizedConstructorDecimal
	// sizedConstructorLength: a constant argument gives the one type
	// parameter (toFixedString(x, N), toDateTime64(x, P)).
	sizedConstructorLength
)

// sizedConstructor describes one constructor of this family.
type sizedConstructor struct {
	// base is the result type name without a wrapper and without a size.
	base string
	// kind says how the size argument reaches the result.
	kind sizedConstructorKind
	// decimalPrecision is the digit count that the name fixes, used by
	// sizedConstructorDecimal only.
	decimalPrecision string
	// sizeArgIndex is the position of the constant size argument.
	sizeArgIndex int
	// orNull adds Nullable to the base result, for the *OrNull variants.
	orNull bool
	// spelling is the name as the server accepts it. The map key is
	// lowercased and ClickHouse function names are case sensitive, thus
	// the key alone cannot be written into SQL. The field is set where
	// the table is built, from the very string that makes the key, so
	// the two can never disagree.
	spelling string
	// domain is the measured argument domain of the data argument. It
	// is the SAME value that the registry spec carries, thus one
	// measurement both tightens the inference refusal and splits the
	// generator column pool. It is never nil for a member of this
	// family: every one of the three families was measured.
	domain *argumentDomain
}

// sizedConstructors holds every constructor of this family. The map keys
// are lowercase, like every other key of the function registry.
var sizedConstructors = buildSizedConstructors()

// castTargets lists the plain cast targets that get an OrNull and an
// OrZero variant. A target appears here only when the measurement showed
// that ClickHouse has both variants.
var castTargets = []struct {
	suffix string // the name part after "to"
	base   string // the resulting ClickHouse type name
}{
	{"Int8", "Int8"}, {"Int16", "Int16"}, {"Int32", "Int32"}, {"Int64", "Int64"},
	{"Int128", "Int128"}, {"Int256", "Int256"},
	{"UInt8", "UInt8"}, {"UInt16", "UInt16"}, {"UInt32", "UInt32"}, {"UInt64", "UInt64"},
	{"UInt128", "UInt128"}, {"UInt256", "UInt256"},
	{"Float32", "Float32"}, {"Float64", "Float64"},
	{"Date", "Date"}, {"Date32", "Date32"}, {"DateTime", "DateTime"},
	{"UUID", "UUID"}, {"IPv4", "IPv4"}, {"IPv6", "IPv6"},
}

// decimalTargets lists the Decimal constructors with the digit count that
// each name fixes: Decimal32 holds 9 digits, Decimal64 18, Decimal128 38
// and Decimal256 76. This is the same table that canonicalDecimalType
// uses, in the constructor direction.
var decimalTargets = []struct {
	suffix    string
	precision string
}{
	{"Decimal32", "9"}, {"Decimal64", "18"},
	{"Decimal128", "38"}, {"Decimal256", "76"},
}

func buildSizedConstructors() map[string]sizedConstructor {
	table := make(map[string]sizedConstructor)

	// put adds one constructor under the lowercased key and records the
	// spelling that made the key. The spelling is therefore never a
	// second list: it is the same string, kept instead of thrown away.
	put := func(spelling string, entry sizedConstructor, domain *argumentDomain) {
		entry.spelling = spelling
		entry.domain = domain
		table[strings.ToLower(spelling)] = entry
	}

	// The wide integers have no plain rule yet. The narrow ones already
	// have one in the function registry and must not get a second, thus only
	// the OrNull and the OrZero variants are added for them below.
	//
	// These four CONVERT a number, thus they take the wide integer
	// domain and not the text domain of the OrZero and OrNull variants.
	for _, target := range []string{"Int128", "Int256", "UInt128", "UInt256"} {
		put("to"+target, sizedConstructor{base: target}, &wideIntegerArgumentDomain)
	}

	// toFixedString(x, N) gives FixedString(N). It accepts String and
	// FixedString, that is the same measured set as the OrZero and the
	// OrNull variants.
	put("toFixedString", sizedConstructor{
		base: "FixedString", kind: sizedConstructorLength, sizeArgIndex: 1,
	}, &castTextArgumentDomain)

	// The Decimal constructors, plain and both variants.
	//
	// The plain form and the two variants do NOT share a domain, although
	// they share the result rule. toDecimal64(x, 2) converts a NUMBER and
	// refuses a text with Code: 6, while toDecimal64OrZero(x, 2) parses a
	// TEXT and refuses every number with Code: 43. Measured both ways.
	for _, target := range decimalTargets {
		plain := sizedConstructor{
			base:             "Decimal",
			kind:             sizedConstructorDecimal,
			decimalPrecision: target.precision,
			sizeArgIndex:     1,
		}
		put("to"+target.suffix, plain, &decimalConstructorArgumentDomain)
		put("to"+target.suffix+"OrZero", plain, &castTextArgumentDomain)
		orNull := plain
		orNull.orNull = true
		put("to"+target.suffix+"OrNull", orNull, &castTextArgumentDomain)
	}

	// The plain cast targets get their OrNull and OrZero variants. Every
	// one of them parses a text.
	for _, target := range castTargets {
		put("to"+target.suffix+"OrZero",
			sizedConstructor{base: target.base}, &castTextArgumentDomain)
		put("to"+target.suffix+"OrNull",
			sizedConstructor{base: target.base, orNull: true}, &castTextArgumentDomain)
	}

	// toDateTime64OrNull/OrZero(x, P) keep the precision argument.
	put("toDateTime64OrZero", sizedConstructor{
		base: "DateTime64", kind: sizedConstructorLength, sizeArgIndex: 1,
	}, &castTextArgumentDomain)
	put("toDateTime64OrNull", sizedConstructor{
		base: "DateTime64", kind: sizedConstructorLength, sizeArgIndex: 1, orNull: true,
	}, &castTextArgumentDomain)

	return table
}

// sizedConstructorResult builds the bare result type of a constructor
// from its argument expressions. It refuses when a needed size argument
// is absent or is not a constant integer, because ClickHouse itself
// refuses a non-constant size and a guess there would be silently wrong.
func sizedConstructorResult(spec sizedConstructor, displayName string, args []clickhouse.Expr) (CHType, error) {
	result := CHType{Name: spec.base}
	switch spec.kind {
	case sizedConstructorNone:
	case sizedConstructorDecimal:
		scale, err := constantIntegerArgument(displayName, args, spec.sizeArgIndex, "scale")
		if err != nil {
			return CHType{}, err
		}
		result.LiteralParams = []string{spec.decimalPrecision, scale}
	case sizedConstructorLength:
		size := "3"
		if spec.sizeArgIndex < len(args) {
			var err error
			size, err = constantIntegerArgument(displayName, args, spec.sizeArgIndex, "size")
			if err != nil {
				return CHType{}, err
			}
		} else if !strings.EqualFold(displayName, "toDateTime64OrNull") && !strings.EqualFold(displayName, "toDateTime64OrZero") {
			return CHType{}, fmt.Errorf("function %s needs a constant size argument at position %d; %s",
				displayName, spec.sizeArgIndex+1, pinTypeHint)
		}
		result.LiteralParams = []string{size}
		if strings.EqualFold(spec.base, "DateTime64") && len(args) > spec.sizeArgIndex+1 {
			zoneExpr := unwrapColumnExpr(args[spec.sizeArgIndex+1])
			zone, ok := zoneExpr.(*clickhouse.StringLiteral)
			if !ok {
				return CHType{}, fmt.Errorf("function %s needs a constant timezone; %s", displayName, pinTypeHint)
			}
			result.LiteralParams = append(result.LiteralParams, clickhouse.Format(zone))
		}
	}
	if spec.orNull {
		result = CHType{Name: "Nullable", Params: []CHType{result}}
	}
	return result, nil
}

// constantIntegerArgument reads a constant non-negative integer argument.
// Anything else is a refusal.
func constantIntegerArgument(displayName string, args []clickhouse.Expr, index int, role string) (string, error) {
	if index >= len(args) {
		return "", fmt.Errorf("function %s needs a constant %s argument at position %d; %s",
			displayName, role, index+1, pinTypeHint)
	}
	literal := unwrapColumnExpr(args[index])
	number, ok := literal.(*clickhouse.NumberLiteral)
	if !ok {
		return "", fmt.Errorf("function %s needs a constant %s argument, got %T; %s",
			displayName, role, literal, pinTypeHint)
	}
	text := strings.TrimSpace(clickhouse.Format(number))
	if _, err := strconv.ParseUint(text, 10, 16); err != nil {
		return "", fmt.Errorf("function %s needs a constant non-negative integer %s argument, got %q; %s",
			displayName, role, text, pinTypeHint)
	}
	return text, nil
}

// unwrapColumnExpr removes the ColumnExpr shell that the parser puts
// around a select-list item.
func unwrapColumnExpr(expression clickhouse.Expr) clickhouse.Expr {
	if column, ok := expression.(*clickhouse.ColumnExpr); ok {
		return column.Expr
	}
	return expression
}

// inferSizedConstructorType types one constructor of this family. The
// wrappers move like a plain cast (the wrapperTransparent class): the
// result is Nullable when an argument is Nullable, and LowCardinality
// when exactly one argument is LowCardinality and every other argument is
// a constant. The exception is a result type that cannot go inside
// LowCardinality, for example Decimal or DateTime64, which drops the
// wrapper. See resultRejectsLowCardinality.
func inferSizedConstructorType(spec sizedConstructor, name, displayName string, args []clickhouse.Expr, scope queryScope) (CHType, error) {
	result, err := sizedConstructorResult(spec, displayName, args)
	if err != nil {
		return CHType{}, err
	}
	// Only the first argument carries data. The size argument is a
	// constant by the check above, so it adds no wrapper, but it still
	// takes part in the LowCardinality "every other argument is a
	// constant" test through transparentWrapperFlags.
	argTypes := make([]CHType, 0, len(args))
	for _, arg := range args {
		inferred, argErr := inferExprType(arg, scope)
		if argErr != nil {
			// A size argument is a constant literal, thus it always
			// types. Any failure here is a real one and must refuse:
			// a bare result can silently drop a Nullable wrapper
			// that ClickHouse keeps.
			return CHType{}, fmt.Errorf("function %s argument: %w", displayName, argErr)
		}
		argTypes = append(argTypes, inferred)
	}
	if len(argTypes) == 0 {
		return CHType{}, fmt.Errorf("function %s has no arguments", displayName)
	}
	// The measured domain of the DATA argument. This route answers
	// before the generic rule lookup, thus it has to apply the domain
	// itself: without this check the family gives a type to a call that
	// the server refuses, for example toInt32OrZero(i32) or
	// toDecimal64(d, 2), which are both Code: 43.
	//
	// The check runs on the base type, that is after the Nullable and
	// the LowCardinality wrappers come off, because the server decides
	// on the inner type. Only the first argument carries data; the size
	// argument is a constant that constantIntegerArgument has already
	// checked.
	if spec.domain != nil {
		base, _, _ := splitCHWrappers(argTypes[0])
		if domainErr := checkArgumentDomain(displayName, *spec.domain, base); domainErr != nil {
			return CHType{}, domainErr
		}
	}
	if narrowErr := checkFixedStringNarrowing(displayName, argTypes[0], result); narrowErr != nil {
		return CHType{}, narrowErr
	}
	// This family is the CONVERSIONS (toFixedString, toDecimal64 and
	// their neighbours). None of them carries dynamicForcesNullable: a
	// conversion names its target type and consumes a Dynamic
	// argument, thus it answers that exact type with no Nullable
	// wrapper. Measured on ClickHouse 25.8.29.51, paired with
	// ignore(...): toDecimal64(dyn, 2) is Decimal(18, 2), and
	// toInt64(dyn) is Int64. The name is passed so that the rule stays
	// with the spec and not with the call site.
	nullable, lowCardinality := transparentWrapperFlags(name, args, argTypes, scope)
	if resultRejectsLowCardinality(result) {
		lowCardinality = false
	}
	return applyCHWrappers(result, nullable, lowCardinality), nil
}

// checkFixedStringNarrowing refuses a toFixedString call whose constant
// target width is BELOW the width of a FixedString source. It does
// nothing for every other constructor, and it does nothing for a String
// source, so the one caller needs no name test beyond the base name.
//
// Measured on ClickHouse 25.8.29.51 with a VALUE select, over an empty
// table and again over a table holding the short value 'ab' (fs is
// FixedString(8), fs16 is FixedString(16)):
//
//	toFixedString(fs, 3)     Code: 131   String too long for type
//	                                     FixedString(3)
//	toFixedString(fs16, 3)   Code: 131   same code, source is wider
//	toFixedString(fs16, 15)  Code: 131   'ab' WOULD fit in
//	                                     FixedString(15), and the server
//	                                     STILL refuses
//	toFixedString(fs, 8)     FixedString(8)     equal width, RUNS
//	toFixedString(fs, 16)    FixedString(16)    widening, RUNS
//	toFixedString(s, 3)      FixedString(3)     String source, RUNS
//
// The empty-table result and the short-value result agree, thus the
// server compares the DECLARED widths of the two types and never reads
// the value. A rule keyed on the declared widths therefore cannot cost a
// false refusal.
//
// the regression: the REFUSAL below is correct, but an earlier version of the
// message gave the WRONG reason. It said "ClickHouse refuses a target
// width below the source width" as if this were an ANALYSIS-time rule.
// It is not. Measured on 25.8.29.51:
//
//	SELECT toTypeName(toFixedString(fs, 3))   ->  FixedString(3)   analysis SUCCEEDS
//	SELECT toFixedString(fs, 3) FROM t        ->  Code: 131, at EXECUTION
//
// toTypeName is an ANALYSIS-ONLY witness here: it reports the type the
// server WOULD give the expression, and it says nothing about whether the
// query can actually RUN. chgen's own execution oracle uses toTypeName as
// its primary witness elsewhere in this codebase, so a reader who checks
// this message against toTypeName alone will find the message "wrong" and
// may be tempted to relax the refusal into an accepted call that then
// dies in production. Do not use toTypeName to re-litigate this cell;
// use a VALUE select, the way this comment's measurement did.
//
// The real mechanism is a TYPE property of FixedString, not a value
// property: a FixedString(N) column is ALWAYS exactly N bytes, because
// ClickHouse pads every shorter value with NUL bytes up to N. A value
// that reads as 'ab' in FixedString(8) is actually 8 bytes long on disk,
// so it can never fit in FixedString(2), and every other value of that
// same source type is in the identical position. The refusal is
// therefore safe to raise at chgen's ANALYSIS time, before the query
// ever reaches the server, because there is no source value of
// FixedString(N) that could make target width M < N succeed.
//
// A String source is NOT refused here, even for a short target, because
// for a String the SAME Code: 131 depends on the VALUE and not on the
// type (measured: toFixedString(String 'ab', 2) RUNS and gives 'ab', but
// toFixedString(String 'abcdefgh', 2) is Code: 131 at execution over the
// SAME target width). Whether a given String value fits is undecidable
// from the type alone, so the domain that already covers toFixedString's
// source type (castTextArgumentDomain, String or FixedString only) must
// stay as wide as the server for a String source: refusing here would be
// a refusal WIDER than the server's, which breaks a query that runs.
func checkFixedStringNarrowing(displayName string, source, result CHType) error {
	if result.Name != "FixedString" {
		return nil
	}
	sourceBase, _, _ := splitCHWrappers(source)
	if sourceBase.normalizedName() != "fixedstring" {
		return nil
	}
	if len(sourceBase.LiteralParams) != 1 || len(result.LiteralParams) != 1 {
		return nil
	}
	sourceWidth, err := strconv.Atoi(sourceBase.LiteralParams[0])
	if err != nil {
		return nil
	}
	targetWidth, err := strconv.Atoi(result.LiteralParams[0])
	if err != nil {
		return nil
	}
	if targetWidth >= sourceWidth {
		return nil
	}
	return fmt.Errorf(
		"function %s narrows a FixedString(%d) source to FixedString(%d); a FixedString(%d) value is always %d bytes long because ClickHouse NUL-pads it to that width, so no value of the source type can fit in the narrower target, and the server reports Code: 131 (String too long) when the query RUNS, not when it is analyzed; %s",
		displayName, sourceWidth, targetWidth, sourceWidth, sourceWidth, pinTypeHint,
	)
}

// addSizedConstructorSemanticSpecs adds every constructor of this family to
// the typed semantic source before the production registry validates it. The
// rule and the wrapper class come from the one sizedConstructors table and
// land in one spec, so they cannot drift apart.
//
// The rule in the spec is a marker: inferFunctionType routes
// these names to inferSizedConstructorType before the generic rule
// lookup, because the rule needs the argument expressions and a
// functionTypeRule sees only the argument types. The marker still refuses
// rather than guessing, so a caller that reaches it never gets a wrong
// type.
func addSizedConstructorSemanticSpecs(source map[string]functionSpec) map[string]functionSpec {
	for name, constructor := range sizedConstructors {
		markerName := name
		sourceSpec := functionSpec{
			family:     semanticFamilyConversion,
			resultMode: resultRuleGeneric,
			rule: func([]CHType) (CHType, error) {
				return CHType{}, fmt.Errorf("function %s needs its argument expressions to be typed; %s",
					markerName, pinTypeHint)
			},
			// The wrappers of this family move like a plain
			// cast. A result type that cannot go inside
			// LowCardinality, for example Decimal or
			// DateTime64, drops the wrapper, which
			// inferSizedConstructorType applies on top of
			// this class.
			class: wrapperTransparent,
			// These names never reach the generic rule path:
			// inferFunctionType routes them to
			// inferSizedConstructorType first. The strategy is
			// therefore the generic one, exactly as the
			// absence from the two strategy sets meant before.
			strategy: argsGeneric,
			// The measured domain of the data argument. It is the
			// SAME value that inferSizedConstructorType applies,
			// because both read it from the one sizedConstructors
			// table. One measurement therefore tightens the
			// inference refusal and splits the generator column
			// pool, and no second copy exists to drift.
			domain:     constructor.domain,
			domainMode: argumentDomainRestricted,
			domainArgs: []int{0},
			// The generator recipe. It is built from the
			// constructor itself, not written out per name: the
			// arity and the position of the constant argument are
			// already stated by the kind and by sizeArgIndex, thus
			// a hand written recipe would be a second copy of
			// facts that this table holds.
			gen:             sizedConstructorGenSpec(constructor),
			parameterPolicy: parameterResultCurated,
			evidence:        registryMeasurementEvidence,
		}
		source[markerName] = sourceSpec
	}
	return source
}

// sizedConstructorGenSpec builds the generator recipe of one member of
// this family.
//
// The recipe declares NEITHER the domain NOR the result type, exactly
// like every other genSpec. The column pool still comes from
// spec.domain.accepts, and the result still comes from inference over the
// written expression. What the recipe adds is the SHAPE of the call: how
// many arguments there are, and which position holds the constant that
// the result type reads.
//
// The constant position is what made this family unreachable. A value
// argument can be drawn from the fixture, but toDecimal64(x, 2) is
// Decimal(18, 2) and toDecimal64(x, 3) is Decimal(18, 3): the SIZE
// argument decides the result type. The recipe therefore marks that
// position argSortConstInt, and the generator writes an integer literal
// there. The expected type is not computed from the recipe; it is
// computed by inference over the same text that the generator wrote,
// thus the constant reaches the rule through the expression and the two
// descriptions of the type cannot disagree.
func sizedConstructorGenSpec(constructor sizedConstructor) *genSpec {
	// A constructor with no size argument takes the data argument only.
	if constructor.kind == sizedConstructorNone {
		return scalarCall(constructor.spelling, 1)
	}
	// A sized constructor takes the data argument and the constant.
	// sizeArgIndex says where the constant sits, thus the arity is one
	// more than that index and the sorts follow the same statement.
	arity := constructor.sizeArgIndex + 1
	sorts := valueSorts(arity)
	sorts[constructor.sizeArgIndex] = argSortConstInt
	return &genSpec{
		spelling: constructor.spelling,
		minArity: arity,
		maxArity: arity,
		argSorts: sorts,
		place:    placementScalar,
	}
}
