package engine

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// supertypeLattice names WHICH of the three lattice entry points a caller
// needs. The three entry points share plumbing (commonCHType,
// commonNumericCHType) but answer DIFFERENT questions for the same input
// pair, and until now the difference lived only in a comment, so a new
// rule could reach for the nearest-sounding helper and get a wrong answer
// that looks right. This type turns the choice into something a compiler
// or a test can check, instead of something only a comment states.
//
// Measured on ClickHouse 25.8.29.51 with real table columns, never over
// literals, because the server folds a constant:
//
//	if(b, lc, lc)        String                     (LowCardinality GONE)
//	array(lc, lc)         Array(LowCardinality(String)) (LowCardinality KEPT)
//	if(c, i32, u64)       Code: 386, NO_COMMON_TYPE
//	greatest(i32, u64)    Int128                     (branch family refuses this)
//	greatest(i64, u64)    UInt64      least(i64, u64) Int64  (same pair, opposite function)
//
// There is no default member and no zero value that means "pick one for
// me": resolveSupertypeLattice panics on an unnamed value, because a
// silent fallback would recreate the exact defect this type exists to
// close. See resolveSupertypeLattice for the one dispatch point that
// reads this enum.
type supertypeLattice int

const (
	// branchLattice is the branch-transport entry point: if, multiIf,
	// CASE and coalesce. It strips LowCardinality at every depth
	// (commonCHType calls stripLowCardinalityDeep on both operands of
	// every fold step) and has no function-name-specific exception.
	// Implemented by commonCHTypes.
	branchLattice supertypeLattice = iota + 1
	// containerMemberLattice is the container-constructor entry point:
	// array and map. It KEEPS LowCardinality, per member position, when
	// EVERY member at that position carries the wrapper. Implemented by
	// commonContainerMemberCHType.
	containerMemberLattice
	// greatestLeastLattice is the entry point for greatest and least
	// ONLY. At arity two it carries a signed-integer/UInt64 exception
	// that the branch family does not have (see
	// greatestLeastSignedUInt64Pair); every other cell defers to the
	// same computation as branchLattice. Implemented by
	// greatestLeastCommonCHTypes, which needs the function name to
	// decide the exception.
	greatestLeastLattice
)

// String names a supertypeLattice member for a diagnostic or a test
// failure message. An unnamed value has no case here on purpose: printing
// a plausible-looking name for garbage input would hide the same defect
// class that this type exists to catch.
func (lattice supertypeLattice) String() string {
	switch lattice {
	case branchLattice:
		return "branchLattice"
	case containerMemberLattice:
		return "containerMemberLattice"
	case greatestLeastLattice:
		return "greatestLeastLattice"
	default:
		return fmt.Sprintf("supertypeLattice(%d)", int(lattice))
	}
}

// resolveSupertypeLattice is the ONE dispatch point that turns a NAMED
// choice of lattice entry point into a call to the matching
// implementation. A caller states which of the three measured behaviours
// it needs; there is no path through this function that guesses.
//
// functionName is only read by greatestLeastLattice, which needs it to
// decide the signed/UInt64 exception (see greatestLeastCommonCHTypes). The
// other two entry points ignore it, because neither commonCHTypes nor
// commonContainerMemberCHType has a function-name-specific rule.
//
// A caller that names the WRONG entry point for its construct gets the
// OTHER measured answer, not a compile error: Go has no closed-union
// argument type that could refuse "containerMemberLattice for an if
// branch" at compile time without one wrapper type per entry point, which
// would just move the same naming decision one level up. The type-checked
// part of this fix is that resolveSupertypeLattice has EXACTLY three
// named, documented members and no default behaviour; the runtime part is
// that TestResolveSupertypeLatticeCatchesWrongEntryPoint below shows a
// call sent to the wrong member producing the OTHER function's measured
// answer, wrong for its construct, so a caller that used this dispatcher
// and asserted its own measured cell would fail exactly the way a caller
// of the bare function name could fail today, and pins the reason: the
// three implementations are NOT interchangeable, which is the fact this
// enum exists to keep visible.
//
// An unnamed (zero or out-of-range) lattice value panics rather than
// silently choosing one implementation, because a silent default is
// itself the defect class this type was built to close.
func resolveSupertypeLattice(lattice supertypeLattice, functionName string, types []CHType) (CHType, error) {
	switch lattice {
	case branchLattice:
		return commonCHTypes(types)
	case containerMemberLattice:
		return commonContainerMemberCHType(types)
	case greatestLeastLattice:
		return greatestLeastCommonCHTypes(functionName, types)
	default:
		panic(fmt.Sprintf("resolveSupertypeLattice: caller did not name a supertype lattice entry point (got %s)", lattice))
	}
}

// commonBranchCHType gives the common supertype of the branch types of a
// conditional construct. branchExprs holds the expression of each branch
// in the same order as branchTypes, so that a literal branch can narrow.
//
// ClickHouse narrows an unsigned integer literal to Int64 when the plain
// lattice fails and the value fits (measured on 25.8.29.51:
// if(b, i8, 10000000000) is Int64, while if(b, i8, CAST(1 AS UInt64))
// stays NO_COMMON_TYPE). The retry happens once, with every literal
// branch narrowed.
func commonBranchCHType(branchTypes []CHType, branchExprs []clickhouse.Expr) (CHType, error) {
	result, err := commonCHTypes(branchTypes)
	if err == nil {
		return result, nil
	}
	narrowed := false
	retryTypes := make([]CHType, len(branchTypes))
	copy(retryTypes, branchTypes)
	for position, expression := range branchExprs {
		if !isNarrowableUnsignedIntegerLiteral(expression) {
			continue
		}
		base, nullable, lowCardinality := splitCHWrappers(retryTypes[position])
		if strings.HasPrefix(strings.ToLower(base.Name), "uint") && len(base.Params) == 0 {
			retryTypes[position] = applyCHWrappers(CHType{Name: "Int64"}, nullable, lowCardinality)
			narrowed = true
		}
	}
	if !narrowed {
		return CHType{}, err
	}
	return commonCHTypes(retryTypes)
}

// commonNullEliminatingCHType removes an untyped NULL before ifNull or
// coalesce joins the remaining values. This rule is not valid for if,
// multiIf, CASE, or a container constructor.
func commonNullEliminatingCHType(types []CHType, expressions []clickhouse.Expr) (CHType, error) {
	if !untypedNullNeutralInNullEliminators {
		return commonBranchCHType(types, expressions)
	}
	keptTypes := make([]CHType, 0, len(types))
	keptExpressions := make([]clickhouse.Expr, 0, len(expressions))
	for index, value := range types {
		if isUntypedNullType(value) {
			continue
		}
		keptTypes = append(keptTypes, value)
		keptExpressions = append(keptExpressions, expressions[index])
	}
	if len(keptTypes) == 0 {
		return commonBranchCHType(types, expressions)
	}
	return commonBranchCHType(keptTypes, keptExpressions)
}

var untypedNullNeutralInNullEliminators = true

// inferCaseExprType types CASE WHEN, in the searched form
// (CASE WHEN cond THEN value ... END) and in the operand form
// (CASE operand WHEN match THEN value ... END).
//
// Measured on ClickHouse 25.8.29.51 with real table columns: the result
// is the common supertype of the THEN branches together with the ELSE
// branch, exactly as multiIf computes it (CASE WHEN b THEN i8 ELSE
// 10000000000 END and multiIf(b, i8, 10000000000) are both Int64). The
// conditions never reach the result, not even a Nullable condition
// (CASE WHEN ni32 = 1 THEN i32 ELSE i16 END is Int32). A CASE without
// ELSE adds Nullable (CASE WHEN b THEN i32 END is Nullable(Int32)).
// LowCardinality is always dropped (CASE WHEN b THEN lc ELSE lc END is
// String, not LowCardinality(String)); commonCHType drops it already.
//
// The operand of the operand form is part of the condition, not a value,
// so it does not reach the result either (CASE i32 WHEN 1 THEN s ELSE fs
// END is String).
//
// When the branches have no common ClickHouse type, this refuses. That
// keeps the honest refusal: ClickHouse rejects such a query as well.
func inferCaseExprType(expression *clickhouse.CaseExpr, scope queryScope) (CHType, error) {
	if len(expression.Whens) == 0 {
		return CHType{}, fmt.Errorf("CASE expression has no WHEN branch")
	}
	// The conditions never reach the result type, but they must still be
	// legal. A condition that the server refuses makes the whole CASE
	// fail (measured on ClickHouse 25.8.29.51 with real table columns:
	// "CASE WHEN empty(e8) THEN 1 ELSE 2 END" and
	// "CASE trim(e8) WHEN 'a' THEN 1 ELSE 2 END" are both Code: 43).
	// A bare positional placeholder is not a refusal: it has no result
	// type yet, thus a condition such as "CASE WHEN i32 = ? THEN ..."
	// keeps working.
	if expression.Expr != nil {
		if _, err := inferExprType(expression.Expr, scope); err != nil && !errors.Is(err, errPlaceholderResultType) {
			return CHType{}, fmt.Errorf("CASE operand: %w", err)
		}
	}
	branchTypes := make([]CHType, 0, len(expression.Whens)+1)
	branchExprs := make([]clickhouse.Expr, 0, len(expression.Whens)+1)
	for _, when := range expression.Whens {
		if when.Then == nil {
			return CHType{}, fmt.Errorf("CASE branch has no THEN value")
		}
		if when.When != nil {
			if _, err := inferExprType(when.When, scope); err != nil && !errors.Is(err, errPlaceholderResultType) {
				return CHType{}, fmt.Errorf("CASE WHEN condition: %w", err)
			}
		}
		branchType, err := inferExprType(when.Then, scope)
		if err != nil {
			return CHType{}, fmt.Errorf("CASE THEN branch: %w", err)
		}
		branchTypes = append(branchTypes, branchType)
		branchExprs = append(branchExprs, when.Then)
	}
	hasElse := expression.Else != nil
	if hasElse {
		elseType, err := inferExprType(expression.Else, scope)
		if err != nil {
			return CHType{}, fmt.Errorf("CASE ELSE branch: %w", err)
		}
		branchTypes = append(branchTypes, elseType)
		branchExprs = append(branchExprs, expression.Else)
	}
	result, err := commonBranchCHType(branchTypes, branchExprs)
	if err != nil {
		return CHType{}, err
	}
	base, nullable, _ := splitCHWrappers(result)
	if !hasElse {
		// Without ELSE a row that matches no branch gives NULL.
		nullable = true
	}
	return applyCHWrappers(base, nullable, false), nil
}

// inferBetweenType types BETWEEN and NOT BETWEEN.
//
// Measured on ClickHouse 25.8.29.51 with real table columns: the result
// is UInt8, and Nullable when any of the three operands is Nullable
// (i32 BETWEEN 1 AND 5 is UInt8, ni32 BETWEEN 1 AND 5 and
// i32 BETWEEN ni32 AND 5 are both Nullable(UInt8)). LowCardinality is
// dropped, because BETWEEN expands to a pair of comparisons joined by
// AND (lc BETWEEN 'a' AND 'z' is UInt8, while the plain comparison
// lc = 'a' keeps LowCardinality(UInt8)).
func inferBetweenType(expression *clickhouse.BetweenClause, scope queryScope) (CHType, error) {
	nullable := false
	for _, operand := range []clickhouse.Expr{expression.Expr, expression.Between, expression.And} {
		if operand == nil {
			return CHType{}, fmt.Errorf("BETWEEN is missing an operand")
		}
		operandType, err := inferExprType(operand, scope)
		if err != nil {
			return CHType{}, fmt.Errorf("BETWEEN operand: %w", err)
		}
		_, operandNullable, _ := splitCHWrappers(operandType)
		nullable = nullable || operandNullable
	}
	return applyCHWrappers(CHType{Name: "UInt8"}, nullable, false), nil
}

// isNarrowableUnsignedIntegerLiteral reports an expression whose value is
// a non-negative integer literal that needs more than 32 bits, that fits
// Int64, and that has not yet been merged into a supertype with anything
// else. Only such a constant narrows in the ClickHouse supertype
// computation; a column, a cast or a smaller literal does not.
//
// Measured on ClickHouse 25.8.29.51 against real columns. Each of these
// sub-expressions is UInt64 when it is typed alone, yet the literal inside
// still narrows to Int64 when the whole expression meets an Int64 branch:
//
//	if(b, i64, nullIf(10000000000, i32))          -> Nullable(Int64)
//	if(b, i64, ifNull(10000000000, u8))           -> Int64
//	if(b, i64, coalesce(10000000000))             -> Int64
//	if(b, i64, if(b, 10000000000, 10000000001))   -> Int64
//
// The freedom ends as soon as the literal is combined with a peer value or
// is given a type outright. All of these are Code: 386 (NO_COMMON_TYPE):
//
//	if(b, i64, toUInt64(10000000000))     -- a cast pins the type
//	if(b, i64, 10000000000 + 0)           -- arithmetic makes a new value
//	if(b, i64, greatest(10000000000, 5))  -- merged with a peer
//	if(b, i64, multiIf(b, 10000000000, u8))
//
// Thus the recursion below follows only the argument positions whose type
// passes through to the result unchanged, and a branch construct only when
// EVERY one of its value branches is itself narrowable.
func isNarrowableUnsignedIntegerLiteral(expression clickhouse.Expr) bool {
	switch expr := expression.(type) {
	case *clickhouse.ColumnExpr:
		return isNarrowableUnsignedIntegerLiteral(expr.Expr)
	case *clickhouse.ParamExprList:
		if expr.Items != nil && len(expr.Items.Items) == 1 {
			return isNarrowableUnsignedIntegerLiteral(expr.Items.Items[0])
		}
	case *clickhouse.NumberLiteral:
		if strings.ContainsAny(expr.Literal, ".eE") || strings.HasPrefix(expr.Literal, "-") {
			return false
		}
		value, err := strconv.ParseInt(expr.Literal, 10, 64)
		if err != nil {
			return false
		}
		// ClickHouse gives a literal the SMALLEST type that holds it. A
		// literal that fits UInt32 or less therefore arrives already
		// pinned to that unsigned type and takes the peer with it, so it
		// does not narrow. Only a literal that needs more than 32 bits
		// keeps the freedom to become Int64.
		//
		// Measured on ClickHouse 25.8.29.51 by moving the peer literal in
		// "if(b, i64, if(b, 10000000000, PEER))": PEER of 1, 255, 256,
		// 65535 and 4294967295 are all Code: 386 (NO_COMMON_TYPE), while
		// 4294967296 and above give Int64.
		return value > math.MaxUint32
	case *clickhouse.FunctionExpr:
		return isNarrowableThroughFunction(expr)
	case *clickhouse.CaseExpr:
		return isNarrowableThroughCase(expr)
	}
	return false
}

// isNarrowableThroughCase applies the branch-function rule to a CASE
// expression. A CASE is the syntax form of multiIf: its result is the
// supertype of its value branches, thus the literal inside it keeps the
// freedom to narrow only when EVERY value branch is itself narrowable.
//
// The value branches are each THEN value and the ELSE value. The CASE
// operand and the WHEN conditions never reach the result, exactly as the
// conditions of if and multiIf do not, so they are skipped here. This is
// the same branch set that inferCaseExprType merges.
//
// Measured on ClickHouse 25.8.29.51 against real columns:
//
//	if(b, i64, CASE WHEN b THEN 10000000000 ELSE 10000000001 END)   -> Int64
//	if(b, i64, CASE s WHEN 'abc' THEN 10000000000 ELSE 1e10+1 END)  -> Int64
//	if(b, i64, CASE WHEN b THEN 10000000000 END)          -> Nullable(Int64)
//
// and a pinned branch is Code: 386 (NO_COMMON_TYPE), the same as multiIf:
//
//	if(b, i64, CASE WHEN b THEN 10000000000 ELSE u8 END)
//	if(b, i64, CASE WHEN b THEN toUInt64(10000000000) ELSE 1 END)
//
// A CASE with no WHEN branch is not narrowable. inferCaseExprType refuses
// that shape anyway, and a false here keeps the refusal.
func isNarrowableThroughCase(expr *clickhouse.CaseExpr) bool {
	if len(expr.Whens) == 0 {
		return false
	}
	for _, when := range expr.Whens {
		if when.Then == nil || !isNarrowableUnsignedIntegerLiteral(when.Then) {
			return false
		}
	}
	// A missing ELSE contributes an implicit NULL, not a typed peer, so it
	// does not pin the literal: the result only gains Nullable.
	if expr.Else != nil && !isNarrowableUnsignedIntegerLiteral(expr.Else) {
		return false
	}
	return true
}

// narrowingPassThroughFunctions names the functions whose result type is
// the type of one nominated argument, passed through without a supertype
// computation. The value is the index of that argument.
var narrowingPassThroughFunctions = map[string]int{
	"nullif":        0,
	"ifnull":        0,
	"tonullable":    0,
	"assumenotnull": 0,
	"identity":      0,
	"materialize":   0,
}

// narrowingBranchFunctions names the branch functions whose result is the
// supertype of their value branches. Such a function stays narrowable only
// when every value branch is narrowable, because a single non-narrowable
// branch forces the supertype computation that pins the literal.
var narrowingBranchFunctions = map[string]bool{
	"if":       true,
	"multiif":  true,
	"coalesce": true,
}

func isNarrowableThroughFunction(expr *clickhouse.FunctionExpr) bool {
	if expr.Params == nil || expr.Params.Items == nil {
		return false
	}
	arguments := expr.Params.Items.Items
	name := strings.ToLower(expr.Name.Name)

	if index, ok := narrowingPassThroughFunctions[name]; ok {
		return index < len(arguments) && isNarrowableUnsignedIntegerLiteral(arguments[index])
	}

	if !narrowingBranchFunctions[name] {
		return false
	}
	branches := branchValueArguments(name, arguments)
	if len(branches) == 0 {
		return false
	}
	for _, branch := range branches {
		if !isNarrowableUnsignedIntegerLiteral(branch) {
			return false
		}
	}
	return true
}

// branchValueArguments returns the arguments of a branch function that
// carry a value into the result. The conditions of if and multiIf never
// reach the result, so they are skipped.
func branchValueArguments(name string, arguments []clickhouse.Expr) []clickhouse.Expr {
	switch name {
	case "if":
		if len(arguments) != 3 {
			return nil
		}
		return []clickhouse.Expr{arguments[1], arguments[2]}
	case "multiif":
		// multiIf(cond, value, cond, value, ..., else): every odd
		// position is a value, and the last argument is the else.
		if len(arguments) < 3 || len(arguments)%2 == 0 {
			return nil
		}
		values := make([]clickhouse.Expr, 0, len(arguments)/2+1)
		for index := 1; index < len(arguments); index += 2 {
			values = append(values, arguments[index])
		}
		return append(values, arguments[len(arguments)-1])
	case "coalesce":
		return arguments
	}
	return nil
}

func commonCHTypes(types []CHType) (CHType, error) {
	if len(types) == 0 {
		return CHType{}, fmt.Errorf("cannot infer common type from no expressions")
	}
	// the regression: a signed integer, UInt64 and a wide integer (Int128,
	// UInt128, Int256, UInt256) is an N-ARY interaction that a pairwise
	// fold cannot see in every order. The pairwise pair rule
	// (commonNumericCHType) correctly refuses Int64 with UInt64 alone,
	// because that pair truly has no supertype; but when a wide branch
	// is present ANYWHERE in the list, the same signed integer and
	// UInt64 both join it, and the whole-list result exists. If the
	// sort below (which ranks Decimal and Float above everything else,
	// including wide integers) ever lets the signed branch meet UInt64
	// BEFORE the wide branch, the fold stops at that refusal and never
	// reaches the wide branch that would have saved it.
	//
	// mixedSignIntegerCHTypes computes the answer over ALL branches at
	// once, the same way the server does, so this shortcut has no order
	// dependence to begin with. It applies ONLY to a multiset of bare
	// integers (Bool counts as UInt8) with at least one signed and one
	// unsigned member; any Decimal, Float, Enum or other type in the
	// list falls through to the ordinary pairwise fold below unchanged.
	if result, nullable, handled := mixedSignIntegerCHTypes(types); handled {
		if result.err != nil {
			return CHType{}, result.err
		}
		return applyCHWrappers(result.value, nullable, false), nil
	}
	// the regression: a DateTime/DateTime64 multiset with more than one
	// distinct timezone is ORDER DEPENDENT on the server itself, thus a
	// pairwise fold in ANY order, sorted or not, is the wrong shape: the
	// sort below would scramble the very order the rule needs to read.
	// dateTimeTimezoneCHTypes reads the branch list in the caller's
	// original order, computes the rule directly, and returns before the
	// sort ever runs. It applies only when every branch, after
	// unwrapping Nullable and LowCardinality, is a bare DateTime or a
	// DateTime64; any Date, Date32 or non-temporal branch in the list
	// falls through unchanged to the ordinary pairwise fold below.
	if result, nullable, handled := dateTimeTimezoneCHTypes(types); handled {
		return applyCHWrappers(result, nullable, false), nil
	}
	// ClickHouse computes the supertype over all branches at once. A
	// pairwise fold in source order can widen two integers into a type
	// that no longer joins a Decimal branch (multiIf(b, i32, b, u32,
	// dec) is Decimal(18, 4) on 25.8.29.51, but Int32 with UInt32 folds
	// to Int64 first, which needs Decimal(38, 4)). Folding the Decimal
	// branches first keeps the fold order-independent, because the
	// Decimal-integer join looks at each integer alone.
	//
	// A float branch needs the same treatment, and for the same reason.
	// Measured on 25.8.29.51: multiIf(b, u32, false, i16, 1e10) is
	// Float64, but the source order folds UInt32 with Int16 into Int64
	// first, and Int64 with Float64 has no supertype. When the float
	// comes first, each integer joins the float alone (UInt32 with
	// Float64 is Float64, then Int16 with Float64 is Float64), which is
	// what the server computes. This never widens the refusal boundary:
	// a 64-bit integer still fails against a float in the pair rule, so
	// multiIf(b, i64, false, i16, 1e10) stays refused, as the server
	// refuses it.
	priority := func(value CHType) int {
		base, _, _ := splitCHWrappers(value)
		switch {
		case isDecimalWithParams(base):
			return 0
		case isFloatCHTypeName(base.Name):
			return 1
		default:
			return 2
		}
	}
	ordered := make([]CHType, 0, len(types))
	for rank := 0; rank <= 2; rank++ {
		for _, value := range types {
			if priority(value) == rank {
				ordered = append(ordered, value)
			}
		}
	}
	// Only the FIRST branch can carry a SimpleAggregateFunction wrapper
	// into the result, thus the fold must start at the first branch as
	// the caller wrote it. The reorder above exists for the Decimal and
	// Float supertype rules, which need the widest type first, and it
	// would otherwise move a later branch into the deciding position
	// (measured: if(c, saggd, dec) is
	// SimpleAggregateFunction(sum, Decimal(38, 4)), and the reorder alone
	// gave the bare Decimal(38, 4)).
	//
	// The fix keeps the reorder for the supertype and folds the first
	// branch in first, so its wrapper meets every peer.
	if _, wrapped := simpleAggregateWrapperInner(types[0]); wrapped {
		result := types[0]
		for _, next := range ordered {
			if next.String() == types[0].String() {
				continue
			}
			var err error
			result, err = commonCHType(result, next)
			if err != nil {
				return CHType{}, err
			}
		}
		return result, nil
	}
	// The SEED goes through the same deep LowCardinality strip as every
	// folded pair. A one-element fold never enters the loop below, thus
	// without this the branch rule would depend on how many branches the
	// caller had: coalesce truncates its argument list after the first
	// non-Nullable argument, and a single remaining Array(LowCardinality
	// (String)) argument kept the wrapper that the server removes
	// (measured: coalesce(alc, as_) is Array(String)).
	result := stripLowCardinalityDeep(ordered[0])
	for _, next := range ordered[1:] {
		var err error
		result, err = commonCHType(result, next)
		if err != nil {
			return CHType{}, err
		}
	}
	return result, nil
}

// mixedSignIntegerResult holds the outcome of mixedSignIntegerCHTypes: a
// type or a refusal, kept apart from "not handled at all" so the caller
// can tell the three outcomes apart.
type mixedSignIntegerResult struct {
	value CHType
	err   error
}

// mixedSignIntegerCHTypes computes the ClickHouse mixed-sign integer
// supertype over a WHOLE branch list at once. handled reports whether
// every branch, after stripping Nullable and LowCardinality, is a bare
// integer type (Bool counts as UInt8) with at least one signed and one
// unsigned member present; when handled is false the caller must fall
// back to the ordinary pairwise fold. nullable reports whether any
// branch was Nullable, for the caller to reapply after unwrapping.
//
// THE RULE, measured on ClickHouse 25.8.29.51 with SELECT
// toTypeName(multiIf(...)) FROM t on real table columns, never on
// constant literals (the server folds a constant and the rule would
// lie): a mixed-sign multiset has a supertype exactly when some signed
// integer width W, drawn from {8, 16, 32, 64, 128, 256}, is at least
// twice every DISTINCT unsigned operand's width and at least every
// signed operand's own width. The result is the smallest such W. This
// is the SAME formula that commonNumericCHType already applies to a
// PAIR; the fix is running it over every branch at once instead of
// folding pairs in a sorted order, because the pairwise fold can retire
// a signed-with-UInt64 pair as a refusal before a wide branch, present
// elsewhere in the list, ever gets the chance to join both.
//
//	multiIf(b, i8|i16|i32|i64, u64, i128)   -> Int128  (every order)
//	multiIf(b, i8|i16|i32|i64, u64, u128)   -> Int256  (every order)
//	multiIf(b, i32|i64,        u64, i256)   -> Int256  (every order)
//
// An all-unsigned multiset (no signed member at all) is not this rule's
// concern; handled is false and the caller's ordinary same-kind fold,
// which already answers the widest unsigned type, applies unchanged.
//
// A width that needs more than 256 bits does not exist as a ClickHouse
// type, thus the multiset is refused, exactly as the pairwise rule
// already refuses UInt256 with any signed peer (Int512 does not exist).
// This refusal is unchanged by the regression, and it does not depend on
// order either (measured: multiIf(b, i32, u64, u256) is NO_COMMON_TYPE
// in all six orders).
//
// This function does not widen the refusal boundary that
// commonNumericCHType already draws for a PLAIN PAIR: a signed integer
// with UInt64 and NO wide branch anywhere in the list is not "handled"
// here in a way that could ever answer a type for that pair alone,
// because the candidate search below is the identical formula and a
// plain pair with no wide member never reaches a candidate width either
// (measured: if(b, i64, u64) stays NO_COMMON_TYPE, TestSignedUInt64PairAloneStaysRefused
// pins this).
func mixedSignIntegerCHTypes(types []CHType) (result mixedSignIntegerResult, nullable bool, handled bool) {
	infos := make([]numericInfo, 0, len(types))
	for _, value := range types {
		base, valueNullable, _ := splitCHWrappers(stripLowCardinalityDeep(value))
		nullable = nullable || valueNullable
		if strings.EqualFold(base.Name, "Bool") && len(base.Params) == 0 {
			base = CHType{Name: "UInt8"}
		}
		info, ok := numericInfoForType(base)
		if !ok || info.kind == "float" {
			return mixedSignIntegerResult{}, false, false
		}
		infos = append(infos, info)
	}
	hasSigned, hasUnsigned := false, false
	maxSignedBits := 0
	maxUnsignedBits := 0
	seenUnsignedBits := make(map[int]bool)
	for _, info := range infos {
		if info.kind == "signed" {
			hasSigned = true
			if info.bits > maxSignedBits {
				maxSignedBits = info.bits
			}
		} else {
			hasUnsigned = true
			seenUnsignedBits[info.bits] = true
			if info.bits > maxUnsignedBits {
				maxUnsignedBits = info.bits
			}
		}
	}
	if !hasSigned || !hasUnsigned {
		// No sign mix at all: this rule does not apply, and the caller's
		// ordinary same-kind fold already gives the widest member.
		return mixedSignIntegerResult{}, false, false
	}
	// The candidate widths follow commonNumericCHType exactly: the 128-
	// and 256-bit signed sizes only enter the search when a 128-bit or
	// wider operand (of either sign) is already present, because
	// ClickHouse only reaches Int128/Int256 through that door (measured:
	// UInt64 with any signed peer has no supertype, although Int128
	// exists).
	candidates := []int{8, 16, 32, 64}
	if maxSignedBits >= 128 || maxUnsignedBits >= 128 {
		candidates = append(candidates, 128, 256)
	}
	for _, width := range candidates {
		if width < maxSignedBits {
			continue
		}
		fits := true
		for unsignedBits := range seenUnsignedBits {
			if width < unsignedBits*2 {
				fits = false
				break
			}
		}
		if fits {
			return mixedSignIntegerResult{value: CHType{Name: numericTypeName("signed", width)}}, nullable, true
		}
	}
	return mixedSignIntegerResult{err: fmt.Errorf("no common ClickHouse type for mixed-sign integer branches")}, nullable, true
}

// dateTimeTimezoneCHTypes computes the ClickHouse DateTime/DateTime64
// supertype over a WHOLE branch list, in the caller's ORIGINAL order.
// handled reports whether every branch, after stripping Nullable and
// LowCardinality, is a bare DateTime or a DateTime64; when handled is
// false the caller must fall back to the ordinary pairwise fold, because
// a Date, a Date32 or any non-temporal branch needs the join rules that
// commonCHType already has. nullable reports whether any branch was
// Nullable, for the caller to reapply after unwrapping.
//
// THE RULE, measured on ClickHouse 25.8.29.51 with SELECT
// toTypeName(greatest(...)) FROM t on real table columns, never on
// constant literals (the server folds a constant and the rule would
// lie), over 3096 machine-compared cells at arity 2, 3 and 4:
//
//	precision = the MAXIMUM precision over all branches. A bare
//	            DateTime counts as precision 0.
//	timezone  = if EVERY branch has the maximum precision, the
//	            timezone of the FIRST branch. Otherwise the timezone
//	            of the LAST branch that attains the maximum precision.
//
// This rule is ORDER DEPENDENT by design: the server itself answers a
// different timezone for a different argument order (measured:
// greatest(dt('UTC'), dt('Europe/Berlin')) is DateTime('UTC'), while
// the reversed call is DateTime('Europe/Berlin')). commonCHTypes sorts
// its branches into Decimal, Float and everything-else ranks before
// the pairwise fold, which would scramble this order; thus this
// function reads types in the untouched caller order and must run
// BEFORE that sort, exactly as mixedSignIntegerCHTypes does for the
// mixed-sign integer rule above.
//
// A DateTime64 branch is read from LiteralParams: the first element is
// the precision, and a second element, when present, is the timezone.
// A bare DateTime reads its own LiteralParams element, when present,
// as the timezone (measured: DateTime('UTC') carries LiteralParams
// []string{"'UTC'"}); a DateTime with no LiteralParams has no
// timezone, which the result then also omits.
func dateTimeTimezoneCHTypes(types []CHType) (result CHType, nullable bool, handled bool) {
	type dateTimeBranch struct {
		precision int
		timezone  string
	}
	branches := make([]dateTimeBranch, 0, len(types))
	maxPrecision := -1
	for _, value := range types {
		base, valueNullable, _ := splitCHWrappers(stripLowCardinalityDeep(value))
		nullable = nullable || valueNullable
		var branch dateTimeBranch
		switch {
		case strings.EqualFold(base.Name, "DateTime64"):
			if len(base.LiteralParams) == 0 {
				return CHType{}, false, false
			}
			precision, err := strconv.Atoi(strings.TrimSpace(base.LiteralParams[0]))
			if err != nil {
				return CHType{}, false, false
			}
			branch.precision = precision
			if len(base.LiteralParams) >= 2 {
				branch.timezone = base.LiteralParams[1]
			}
		case strings.EqualFold(base.Name, "DateTime"):
			branch.precision = 0
			if len(base.LiteralParams) >= 1 {
				branch.timezone = base.LiteralParams[0]
			}
		default:
			return CHType{}, false, false
		}
		if branch.precision > maxPrecision {
			maxPrecision = branch.precision
		}
		branches = append(branches, branch)
	}
	everyBranchAtMax := true
	lastAtMaxTimezone := ""
	for _, branch := range branches {
		if branch.precision != maxPrecision {
			everyBranchAtMax = false
			continue
		}
		lastAtMaxTimezone = branch.timezone
	}
	timezone := lastAtMaxTimezone
	if everyBranchAtMax {
		timezone = branches[0].timezone
	}
	if maxPrecision == 0 {
		if timezone == "" {
			return CHType{Name: "DateTime"}, nullable, true
		}
		return CHType{Name: "DateTime", LiteralParams: []string{timezone}}, nullable, true
	}
	literalParams := []string{strconv.Itoa(maxPrecision)}
	if timezone != "" {
		literalParams = append(literalParams, timezone)
	}
	return CHType{Name: "DateTime64", LiteralParams: literalParams}, nullable, true
}

// greatestLeastCommonCHTypes is the common-type rule for greatest and
// least ONLY. name must be "greatest" or "least" (case-insensitive).
//
// The related regressions cover the branch family (if, multiIf, CASE) and
// greatest/least DISAGREE on a signed integer mixed with UInt64.
// commonCHTypes (through commonNumericCHType) refuses every such pair,
// which is the CORRECT and measured answer for the branch family, but
// greatest and least accept it at arity two. Measured on ClickHouse
// 25.8.29.51 with real table columns, never over literals:
//
//	if(c, i32, u64)      Code: 386 (NO_COMMON_TYPE)
//	greatest(i32, u64)   Int128        least(i32, u64)    Int128
//	greatest(i8, u64)    Int128        least(i8, u64)     Int128
//	greatest(u64, i32)   Int128        least(u64, i32)    Int128
//	greatest(i64, u64)   UInt64        least(i64, u64)    Int64
//	greatest(u64, i64)   UInt64        least(u64, i64)    Int64
//
// The rule needs the FUNCTION NAME, which a common-type helper
// structurally cannot see, thus it cannot live inside commonCHTypes or
// commonNumericCHType: those two are the ONE entry point of the branch
// family (if, multiIf, ifNull, coalesce), and widening them would wrongly
// make the branch family accept the mix too. This helper stays a
// SEPARATE, narrow entry point that only greatest and least may call.
//
// The special pair answer applies ONLY at arity two. Measured: a third
// operand of ANY type, including a duplicate of an already-accepted
// operand, turns the answer back into the branch-family refusal
// (greatest(i64, u64, u64), greatest(i64, u64, i64) and
// greatest(i32, u64, i32) are all Code: 386). Thus this is not a
// widening of the numeric lattice; it is a fact about the TWO-ARGUMENT
// form of these two functions alone, and arity three or more, and every
// other type combination, defers to commonCHTypes unchanged.
//
// A signed type narrower than Int64 gives Int128 for both functions;
// only the Int64-with-UInt64 pair splits by function, and it does not
// widen at all (UInt64 for greatest, Int64 for least). Argument order
// does not matter for either row.
func greatestLeastCommonCHTypes(name string, types []CHType) (CHType, error) {
	name = strings.ToLower(name)
	if len(types) == 2 && (name == "greatest" || name == "least") {
		if result, ok := greatestLeastSignedUInt64Pair(name, types[0], types[1]); ok {
			return result, nil
		}
	}
	return commonCHTypes(types)
}

// greatestLeastSignedUInt64Pair reports the measured greatest/least
// result for a two-argument (signed, UInt64) pair, in either order, and
// whether the pair matches that shape at all. See
// greatestLeastCommonCHTypes for the measurement.
func greatestLeastSignedUInt64Pair(name string, left, right CHType) (CHType, bool) {
	signed, unsigned := left, right
	if !isBareSignedInteger(signed) || !isBareUnsignedCHType64(unsigned) {
		signed, unsigned = right, left
		if !isBareSignedInteger(signed) || !isBareUnsignedCHType64(unsigned) {
			return CHType{}, false
		}
	}
	if strings.EqualFold(signed.Name, "Int64") || strings.EqualFold(signed.Name, "Int") {
		if name == "least" {
			return CHType{Name: "Int64"}, true
		}
		return CHType{Name: "UInt64"}, true
	}
	return CHType{Name: "Int128"}, true
}

// isBareSignedInteger reports a signed integer type with no parameters,
// narrower than Int128: Int8, Int16, Int32 or Int64 (and its alias Int).
func isBareSignedInteger(value CHType) bool {
	if len(value.Params) != 0 {
		return false
	}
	switch strings.ToLower(value.Name) {
	case "int8", "int16", "int32", "int64", "int":
		return true
	default:
		return false
	}
}

// isBareUnsignedCHType64 reports a bare UInt64 type with no parameters.
func isBareUnsignedCHType64(value CHType) bool {
	return strings.EqualFold(value.Name, "UInt64") && len(value.Params) == 0
}

// commonSimpleAggregateCHType applies the branch-supertype rule for a
// SimpleAggregateFunction(f, T) operand. It reports handled = false when
// no operand is such a type, and the caller then continues as before.
//
// This is the BRANCH family (if, multiIf, ifNull, coalesce). It KEEPS the
// wrapper. Do not join it with the unwrap rules in arithmetic.go: an
// operand of a binary arithmetic operator, and an argument of a unary
// operator or of an aggregate, UNWRAP to the inner type. The two groups
// need opposite answers, thus each rule stays local to its own position.
//
// The condition to keep the wrapper is EXACT equality of the peer with
// the inner type, not "the supertype fits". A peer that only widens to
// the inner type drops the wrapper. Measured on ClickHouse 25.8.29.51
// through the HTTP interface, against real columns of a real table, never
// over literals, because the server folds constants. The columns are sagg
// SimpleAggregateFunction(sum, Int64), saggmax SimpleAggregateFunction(max,
// Int64), saggu SimpleAggregateFunction(max, UInt8), saggn
// SimpleAggregateFunction(sum, Nullable(Int64)), saggs
// SimpleAggregateFunction(min, String), saggf SimpleAggregateFunction(sum,
// Float64), i64 Int64, u8 UInt8, s String, ni64 Nullable(Int64) and nu8
// Nullable(UInt8).
//
// The peer EQUALS the inner type, thus the wrapper stays:
//
//	if(c, sagg, i64)         SimpleAggregateFunction(sum, Int64)
//	if(c, sagg, sagg)        SimpleAggregateFunction(sum, Int64)
//	if(c, saggu, u8)         SimpleAggregateFunction(max, UInt8)
//	if(c, saggs, s)          SimpleAggregateFunction(min, String)
//	if(c, saggn, ni64)       SimpleAggregateFunction(sum, Nullable(Int64))
//	multiIf(c, sagg, i64)    SimpleAggregateFunction(sum, Int64)
//	ifNull(sagg, i64)        SimpleAggregateFunction(sum, Int64)
//	coalesce(sagg, i64)      SimpleAggregateFunction(sum, Int64)
//
// The peer only WIDENS to the inner type, thus the wrapper goes. This is
// the reason the test is equality and not a supertype fit:
//
//	if(c, sagg, u8)          Int64
//	if(c, saggu, i64)        Int64
//	if(c, saggn, i64)        Nullable(Int64)
//	if(c, saggn, nu8)        Nullable(Int64)
//	if(c, sagg, nu8)         Nullable(Int64)
//	if(c, sagg, saggu)       Int64
//
// A Nullable peer of an equal inner type keeps the wrapper and puts the
// Nullable OUTSIDE it. SimpleAggregateFunction accepts a Nullable, unlike
// AggregateFunction, thus a HasPrefix guard on the name would be wrong:
//
//	if(c, sagg, ni64)        Nullable(SimpleAggregateFunction(sum, Int64))
//	if(c, saggu, nu8)        Nullable(SimpleAggregateFunction(max, UInt8))
//
// The FIRST wrapper is the only candidate. The fold in the caller is
// left to right, thus a peer that is a different SimpleAggregateFunction
// is compared with the inner type of the first one:
//
//	if(c, i64, sagg)                        Int64
//	if(c, sagg, saggmax)                    SimpleAggregateFunction(sum, Int64)
//	if(c, saggmax, sagg)                    SimpleAggregateFunction(max, Int64)
//	multiIf(c, sagg, c2, i64, saggmax)      SimpleAggregateFunction(sum, Int64)
//
// The rule does not widen the refusal boundary. A pair that the inner
// rule refuses stays refused, and the server refuses it too: if(c, sagg,
// s) and if(c, sagg, saggf) both answer Code: 386 (NO_COMMON_TYPE).
func commonSimpleAggregateCHType(left, right CHType) (result CHType, handled bool, err error) {
	inner, ok := simpleAggregateWrapperInner(left)
	if !ok {
		// Only the left operand can carry the wrapper into the result.
		// A wrapper on the right alone always drops, thus that case
		// continues on the normal path with the inner type.
		if innerRight, okRight := simpleAggregateWrapperInner(right); okRight {
			common, commonErr := commonCHType(left, innerRight)
			return common, true, commonErr
		}
		return CHType{}, false, nil
	}

	// A peer that is itself a SimpleAggregateFunction is compared through
	// its inner type, because the peer wrapper never reaches the result.
	peer := right
	if peerInner, okPeer := simpleAggregateWrapperInner(peer); okPeer {
		peer = peerInner
	}

	// The test is EXACT structural equality, thus it is not
	// compatibleNamedArgTypes: that helper looks through a Nullable on
	// purpose, and here a Nullable difference must drop the wrapper
	// (measured: ifNull(saggn, i64) is Int64, not the wrapper).
	//
	// The peer is compared WHOLE first. That is what keeps a Nullable
	// inner type together with a Nullable peer:
	// if(c, saggn, ni64) is SimpleAggregateFunction(sum, Nullable(Int64)).
	if inner.String() == peer.String() {
		return left, true, nil
	}
	// A Nullable peer of a NON-Nullable equal inner type keeps the
	// wrapper and puts the Nullable outside it (measured:
	// if(c, sagg, ni64) is Nullable(SimpleAggregateFunction(sum, Int64))).
	if strings.EqualFold(peer.Name, "Nullable") && len(peer.Params) == 1 &&
		inner.String() == peer.Params[0].String() {
		// This hand-builds Nullable instead of calling wrapNullable, and
		// the regression measured that canBeInsideNullable can never refuse
		// here: "left" is wrapped WHOLE, and left is always literally a
		// SimpleAggregateFunction(...) node at this point (the guard
		// above already established that inner came from
		// simpleAggregateWrapperInner(left)). canBeInsideNullable
		// accepts that name explicitly. See
		// wrapperConstructorAllowlist in
		// wrapper_constructor_boundary_test.go for the full measurement.
		return CHType{Name: "Nullable", Params: []CHType{left}}, true, nil
	}

	// The peer differs from the inner type. The wrapper drops and the
	// normal supertype rules decide the answer.
	common, commonErr := commonCHType(inner, right)
	return common, true, commonErr
}

// simpleAggregateWrapperInner reports the inner type T of a
// SimpleAggregateFunction(f, T) and whether the type is one.
func simpleAggregateWrapperInner(value CHType) (CHType, bool) {
	if strings.EqualFold(value.Name, "SimpleAggregateFunction") && len(value.Params) == 2 {
		return value.Params[1], true
	}
	return CHType{}, false
}

// simpleAggregateMarkerSurvives reports whether a
// SimpleAggregateFunction(f, T) marker survives a function that READS
// its argument as a value.
//
// The marker survives only when T is a BARE SCALAR. When T is Nullable,
// LowCardinality, Array, Map or Tuple, the server answers T itself with
// no marker at all.
//
// The rule is NOT about nullability, and it is NOT an unconditional
// look-through. Two cells decide that, and both have a NON-Nullable
// inner type:
//
//	identity(saf)      SimpleAggregateFunction(anyLast, Int32)
//	identity(safarrb)  Array(Int32)
//
// where saf is SimpleAggregateFunction(anyLast, Int32) and safarrb is
// SimpleAggregateFunction(anyLast, Array(Int32)). A nullability test
// predicts that both keep the marker and is wrong on the second. An
// unconditional look-through predicts that both drop it and is wrong on
// the first.
//
// identity applies no type rule of its own, thus the split happens when
// the argument is read, before any rule runs. The same split was
// measured through four independent witnesses: identity, any,
// assumeNotNull and groupArray.
//
// Measured on ClickHouse 25.8.29.51 with real columns of a real
// AggregatingMergeTree table, never over literals:
//
//	inner Int32                   marker kept
//	inner String                  marker kept
//	inner Decimal(18, 4)          marker kept
//	inner DateTime                marker kept
//	inner FixedString(4)          marker kept
//	inner UUID                    marker kept
//	inner Nullable(Int32)         marker dropped
//	inner Array(Int32)            marker dropped
//	inner Array(String)           marker dropped
//	inner Map(String, Int32)      marker dropped
//	inner Tuple(Int32, Int32)     marker dropped
//	inner LowCardinality(String)  marker dropped
//
// The container CONSTRUCTORS array, tuple and map are a DIFFERENT class
// and keep the marker for every inner shape, thus this rule must stay
// out of splitCHWrappers and out of the shared wrapper split.
func simpleAggregateMarkerSurvives(inner CHType) bool {
	switch {
	case strings.EqualFold(inner.Name, "Nullable"),
		strings.EqualFold(inner.Name, "LowCardinality"),
		strings.EqualFold(inner.Name, "Array"),
		strings.EqualFold(inner.Name, "Map"),
		strings.EqualFold(inner.Name, "Tuple"):
		return false
	}
	return true
}

// simpleAggregateMarkerSurvivesValuePreserving reports whether a
// SimpleAggregateFunction(f, T) marker survives a VALUE-PRESERVING
// SCALAR or window function. The exact family is greatest, least, nullIf,
// lagInFrame, leadInFrame, lag, lead, nth_value,
// first_value_respect_nulls, firstValueRespectNulls,
// last_value_respect_nulls, and lastValueRespectNulls.
//
// The marker survives when T is a bare scalar, and it also survives when
// T is a Nullable of a bare scalar. It DROPS when T is LowCardinality,
// Array, Map or Tuple.
//
// This is NOT the rule of simpleAggregateMarkerSurvives. The two rules
// give a DIFFERENT answer in one cell, the Nullable inner, thus they
// must stay separate. A function that READS the argument as a value
// unwraps a Nullable inner; a value-preserving scalar function gives the
// argument back and keeps it.
//
// Measured on ClickHouse 25.8.29.51 through the HTTP interface, against
// real columns of a real AggregatingMergeTree table with one row, never
// over literals, because the server folds constants:
//
//	inner type              identity           greatest / lagInFrame
//	Int64                   marker kept        marker kept
//	Nullable(Int64)         marker DROPPED     marker KEPT
//	Array(Int32)            marker dropped     marker dropped
//	Tuple(Int32, Int32)     marker dropped     marker dropped
//	LowCardinality(String)  marker dropped     marker dropped
//
// The raw cells that decide the two rows where the rules disagree:
//
//	identity(saggn)                 Nullable(Int64)
//	greatest(saggn)                 SimpleAggregateFunction(sum, Nullable(Int64))
//	lagInFrame(saggn, 1) OVER ()    SimpleAggregateFunction(sum, Nullable(Int64))
//	leadInFrame(saggn, 1) OVER ()   SimpleAggregateFunction(sum, Nullable(Int64))
//
// And the cells that make this family drop the marker:
//
//	greatest(safarr)                Array(Int32)
//	greatest(saftup)                Tuple(Int32, Int32)
//	greatest(saflcs)                LowCardinality(String)
//	lagInFrame(safarr, 1) OVER ()   Array(Int32)
//	lagInFrame(saftup, 1) OVER ()   Tuple(Int32, Int32)
//	lagInFrame(saflcs, 1) OVER ()   String
//
// The LowCardinality row is the reason the two families do NOT agree on
// the final type although they agree on the marker. Both drop the
// marker; then each transport applies its OWN LowCardinality
// disposition, which was measured before and is already correct.
// greatest and least keep the wrapper at arity 1, and the frame-shift
// functions always drop it:
//
//	greatest(saflc)                LowCardinality(Int32)
//	least(saflc)                   LowCardinality(Int32)
//	lagInFrame(saflc, 2) OVER ()   Int32
//	leadInFrame(saflc, 2) OVER ()  Int32
//
// Thus this helper states the ONE fact that the two families share, and
// it does not repeat the LowCardinality rule of either transport.
//
// IMPLEMENTATION. This function is the ONE STEP delegation
// simpleAggregateMarkerSurvives(the inner with its outer Nullable, if
// any, peeled off). The regression checked the delegation before writing it
// this way, because both functions are MEASURED rules and a clean
// refactor proves nothing about a measured rule on its own.
//
// The two rules disagree only on a Nullable inner: this function keeps
// the marker there, and simpleAggregateMarkerSurvives drops it. Peeling
// the Nullable off before the call is exact ONLY because a Nullable
// cannot hold a second Nullable, a LowCardinality, an Array, a Map or a
// Tuple, thus "the inner with a Nullable peeled off" can never itself BE
// one of those wrapped types, and the two rules cannot diverge through
// that path. Confirmed on ClickHouse 25.8.29.51 by CREATE TABLE, Code
// 43, ILLEGAL_TYPE_OF_ARGUMENT, on all four:
//
//	Nullable(Array(Int32))            Code 43
//	Nullable(Tuple(Int32, String))    Code 43
//	Nullable(Map(String, Int64))      Code 43
//	Nullable(LowCardinality(Int32))   Code 43
//	Nullable(Nullable(Int32))         Code 43
//
// The full alphabet of reachable inner shapes was enumerated in Go and
// compared cell by cell (bare scalar, Nullable(scalar),
// LowCardinality(scalar), LowCardinality(Nullable(scalar)), Array, Tuple,
// Map): zero cells disagree. See the guard test that pins this
// equivalence over the same alphabet.
func simpleAggregateMarkerSurvivesValuePreserving(inner CHType) bool {
	if strings.EqualFold(inner.Name, "Nullable") && len(inner.Params) == 1 {
		inner = inner.Params[0]
	}
	return simpleAggregateMarkerSurvives(inner)
}

// readSimpleAggregateValue gives the type that a function which READS
// its argument as a value sees in place of a SimpleAggregateFunction
// marker. A marker that does not survive is replaced by its inner type;
// every other type passes through unchanged.
//
// See simpleAggregateMarkerSurvives for the measured rule.
func readSimpleAggregateValue(value CHType) CHType {
	inner, ok := simpleAggregateWrapperInner(value)
	if !ok || simpleAggregateMarkerSurvives(inner) {
		return value
	}
	return inner
}

// commonCHType gives the ClickHouse supertype of two types. Together with
// commonCHTypes, which folds a whole branch list over it, it is the ONE
// entry point of the branch-family supertype, thus if, multiIf, ifNull and
// coalesce all get the same answer.
//
// The LowCardinality removal is deliberately NOT splitCHWrappers, and it
// is deliberately DEEP. stripLowCardinalityDeep removes the wrapper at
// every depth, because the branch family removes it at every depth on the
// server. A Nullable with a wrong argument count stays an ERROR here, not
// a type that passes through unchanged.
//
// The strip belongs on BOTH operands, although only the SEED strip in
// commonCHTypes is observable today. The shortcut below returns the LEFT
// operand when the two sides are structurally compatible, and
// compatibleNamedArgTypes looks through LowCardinality at every depth.
// A nested wrapper can therefore only reach a result through the left
// operand, and every branch fold seeds its left operand from
// commonCHTypes, which strips it. The strip here keeps the invariant true
// at the function boundary, so a future caller that folds in another
// order cannot reintroduce the defect that this pair closed:
// if(b, array(lc), array(s)) answered Array(LowCardinality(String)) while
// the swapped if(b, array(s), array(lc)) answered Array(String), and the
// server answers Array(String) for both.
//
// The container CONSTRUCTOR needs the OPPOSITE rule and must not call this
// function for the wrapper decision: [lc, lc] keeps the wrapper as
// Array(LowCardinality(String)). commonContainerMemberCHType owns that
// rule; it strips the members itself and puts one wrapper back when EVERY
// member had one.
func commonCHType(left, right CHType) (CHType, error) {
	left = stripLowCardinalityDeep(left)
	right = stripLowCardinalityDeep(right)
	if result, handled := commonWithNullLiteral(left, right); handled {
		return result, nil
	}

	if result, handled, err := commonSimpleAggregateCHType(left, right); handled {
		return result, err
	}

	leftNullable := strings.EqualFold(left.Name, "Nullable")
	rightNullable := strings.EqualFold(right.Name, "Nullable")
	if leftNullable {
		if len(left.Params) != 1 {
			return CHType{}, fmt.Errorf("Nullable expects one type argument")
		}
		left = left.Params[0]
	}
	if rightNullable {
		if len(right.Params) != 1 {
			return CHType{}, fmt.Errorf("Nullable expects one type argument")
		}
		right = right.Params[0]
	}

	// Bool joins the lattice as UInt8. When the computed supertype is
	// UInt8 and one side was Bool, the result is Bool (measured on
	// ClickHouse 25.8.29.51: if(b, b, u8) is Bool, if(b, b, u16) is
	// UInt16, if(b, b, i32) is Int32, if(b, dec, b) is Decimal(18, 4)).
	leftBool := strings.EqualFold(left.Name, "Bool")
	rightBool := strings.EqualFold(right.Name, "Bool")
	if leftBool && len(left.Params) == 0 {
		left = CHType{Name: "UInt8"}
	}
	if rightBool && len(right.Params) == 0 {
		right = CHType{Name: "UInt8"}
	}

	// An Enum joins the lattice through its storage integer, and a
	// string-like peer wins over that integer. Two Enums with the same
	// value set keep the Enum, which compatibleNamedArgTypes decides.
	// Measured on ClickHouse 25.8.29.51 with real table columns:
	//
	//	if(b, e8, s)       String        if(b, e8, lc)      String
	//	if(b, e8, fs)      String        if(b, e8, ns)      Nullable(String)
	//	if(b, e8, e8same)  Enum8(...)    if(b, e8, e8other) Int8
	//	if(b, e8, e16)     Int16         if(b, e8, u8)      Int16
	//	if(b, e8, u16)     Int32         if(b, e8, i64)     Int64
	//	if(b, e8, f32)     Float32       if(b, e8, f64)     Float64
	//	if(b, e8, u64)     no supertype  if(b, e8, dec)     no supertype
	//	if(b, e8, d)       no supertype  if(b, e8, uid)     no supertype
	//
	// The Enum thus becomes its integer only against an integer or a
	// float peer. Against a Decimal, a temporal type or any other type
	// ClickHouse has no supertype and chgen must refuse as well, thus
	// the substitution is not unconditional.
	leftEnum := isEnumCHType(left)
	rightEnum := isEnumCHType(right)
	if (leftEnum || rightEnum) && !compatibleNamedArgTypes(left, right) {
		if isStringSupertypeFamily(left) || isStringSupertypeFamily(right) {
			stringResult := CHType{Name: "String"}
			if leftNullable || rightNullable {
				// Hand-built Nullable: the regression measured that this
				// site cannot be refused, because stringResult is
				// always the hardcoded bare String above, never a
				// container. canBeInsideNullable accepts String
				// unconditionally.
				return CHType{Name: "Nullable", Params: []CHType{stringResult}}, nil
			}
			return stringResult, nil
		}
		if leftEnum {
			left = enumStorageInteger(left)
		}
		if rightEnum {
			right = enumStorageInteger(right)
		}
		// An Enum never joins a Decimal on ClickHouse, although its
		// storage integer would.
		if isDecimalWithParams(left) || isDecimalWithParams(right) {
			return CHType{}, fmt.Errorf("no common ClickHouse type for %s and %s", left.String(), right.String())
		}
	}

	var result CHType
	switch {
	// The test is EXACT structural equality, not compatibleNamedArgTypes.
	// That helper looks through a Nullable and a LowCardinality on
	// purpose (it answers "the same named-arg type", used for Enum
	// value-set matching), and using it here as an "already equal, keep
	// left" shortcut silently drops a Nullable element inside a
	// container.
	//
	// Measured on ClickHouse 25.8.29.51 with real columns, a is
	// Array(Int32) and an is Array(Nullable(Int32)):
	//
	//	concat(a, an)   Array(Nullable(Int32))   [1,2,1,NULL]
	//
	// compatibleNamedArgTypes(a, an) reports true, because it looks
	// through the element Nullable, so the old shortcut answered plain
	// left = Array(Int32) here and lost the element wrapper although the
	// server value carries a real NULL. The same loss reproduces through
	// Map values, Tuple elements and nested Array levels; see the Array,
	// Map and Tuple cases below, which recurse through commonCHType on
	// each element instead of trusting the loose compatibility check.
	//
	// Two IDENTICAL types (String() equal, wrapper depth and all) still
	// take this fast path: there is no element mismatch to lose.
	case left.String() == right.String():
		result = left
	case commonDecimalSupertype(left, right) != nil:
		result = *commonDecimalSupertype(left, right)
	case isDecimalWithParams(left) || isDecimalWithParams(right):
		// A Decimal with a Float peer, or with a peer outside the
		// measured table, has no supertype on ClickHouse either
		// (if(b, dec, f64) is NO_COMMON_TYPE on 25.8.29.51).
		return CHType{}, fmt.Errorf("no common ClickHouse type for %s and %s", left.String(), right.String())
	case numericCHType(left) && numericCHType(right):
		var err error
		result, err = commonNumericCHType(left, right)
		if err != nil {
			return CHType{}, err
		}
	case isStringSupertypeFamily(left) && isStringSupertypeFamily(right):
		// String and FixedString join as String (measured on
		// ClickHouse 25.8.29.51: if(b, s, fs) is String).
		result = CHType{Name: "String"}
	case strings.EqualFold(left.Name, "Array") && strings.EqualFold(right.Name, "Array") && len(left.Params) == 1 && len(right.Params) == 1:
		inner, err := commonCHType(left.Params[0], right.Params[0])
		if err != nil {
			return CHType{}, err
		}
		result = CHType{Name: "Array", Params: []CHType{inner}}
	case strings.EqualFold(left.Name, "Map") && strings.EqualFold(right.Name, "Map") && len(left.Params) == 2 && len(right.Params) == 2:
		key, err := commonCHType(left.Params[0], right.Params[0])
		if err != nil {
			return CHType{}, err
		}
		value, err := commonCHType(left.Params[1], right.Params[1])
		if err != nil {
			return CHType{}, err
		}
		result = CHType{Name: "Map", Params: []CHType{key, value}}
	// Tuple joins ELEMENTWISE, like Array and Map. A size mismatch has no
	// supertype on the server (measured on ClickHouse 25.8.29.51: if(b,
	// Tuple(i32, s), Tuple(1, 2, 3)) is Code 386, NO_COMMON_TYPE, "Tuples
	// have different sizes"), thus this case requires equal arity and
	// falls through to the refusal default otherwise.
	//
	// Element names survive the join only when both sides name the
	// SAME element the SAME way; any other pairing, including one named
	// side and one bare side, drops every name (measured: a Tuple(x
	// Int32, y String) peer with a Tuple(z Int32, w String) peer is
	// Tuple(Int32, String), unnamed).
	case strings.EqualFold(left.Name, "Tuple") && strings.EqualFold(right.Name, "Tuple") && len(left.Params) == len(right.Params) && len(left.Params) > 0:
		elements := make([]CHType, len(left.Params))
		namesMatch := len(left.ParamNames) == len(left.Params) && len(right.ParamNames) == len(right.Params)
		names := make([]string, len(left.Params))
		for index := range left.Params {
			elementType, err := commonCHType(left.Params[index], right.Params[index])
			if err != nil {
				return CHType{}, err
			}
			elements[index] = elementType
			if namesMatch && left.ParamNames[index] != "" && left.ParamNames[index] == right.ParamNames[index] {
				names[index] = left.ParamNames[index]
			} else {
				namesMatch = false
			}
		}
		result = CHType{Name: "Tuple", Params: elements}
		if namesMatch {
			result.ParamNames = names
		}
	case strings.EqualFold(left.Name, "DateTime") && strings.EqualFold(right.Name, "DateTime64"):
		result = right
	case strings.EqualFold(left.Name, "DateTime64") && strings.EqualFold(right.Name, "DateTime"):
		result = left
	case strings.EqualFold(left.Name, "Date") && strings.EqualFold(right.Name, "Date32"):
		result = right
	case strings.EqualFold(left.Name, "Date32") && strings.EqualFold(right.Name, "Date"):
		result = left
	// Date joins DateTime and DateTime64 as the peer type (measured on
	// ClickHouse 25.8.29.51: if(b, d, dt) is DateTime, if(b, d, dt64)
	// is DateTime64(3)).
	case strings.EqualFold(left.Name, "Date") && (strings.EqualFold(right.Name, "DateTime") || strings.EqualFold(right.Name, "DateTime64")):
		result = right
	case (strings.EqualFold(left.Name, "DateTime") || strings.EqualFold(left.Name, "DateTime64")) && strings.EqualFold(right.Name, "Date"):
		result = left
	// Date32 with DateTime gives DateTime64(0), not DateTime (measured
	// on ClickHouse 25.8.29.51: if(b, dd32, dt) is DateTime64(0)).
	// Date32 with DateTime64 keeps the DateTime64 precision.
	case strings.EqualFold(left.Name, "Date32") && strings.EqualFold(right.Name, "DateTime"):
		result = CHType{Name: "DateTime64", LiteralParams: []string{"0"}}
	case strings.EqualFold(left.Name, "DateTime") && strings.EqualFold(right.Name, "Date32"):
		result = CHType{Name: "DateTime64", LiteralParams: []string{"0"}}
	case strings.EqualFold(left.Name, "Date32") && strings.EqualFold(right.Name, "DateTime64"):
		result = right
	case strings.EqualFold(left.Name, "DateTime64") && strings.EqualFold(right.Name, "Date32"):
		result = left
	default:
		return CHType{}, fmt.Errorf("no common ClickHouse type for %s and %s", left.String(), right.String())
	}
	if (leftBool || rightBool) && strings.EqualFold(result.Name, "UInt8") && len(result.Params) == 0 {
		result = CHType{Name: "Bool"}
	}
	if leftNullable || rightNullable {
		// Hand-built Nullable: the regression measured that "result" can
		// never be a base that canBeInsideNullable would refuse
		// (Array, Map, Tuple or AggregateFunction) while
		// leftNullable/rightNullable is true. The Array, Map and Tuple
		// switch cases above only fire when BOTH left and right
		// already carry that same container name at entry, and
		// leftNullable/rightNullable require the RAW left/right value
		// to have TOP-LEVEL name "Nullable" before the unwrap a few
		// lines up. On ClickHouse 25.8.29.51 the server refuses to
		// construct Nullable(Array(...)), Nullable(Map(...)),
		// Nullable(Tuple(...)) or Nullable(AggregateFunction(...)) AT
		// ALL, at any nesting depth (CREATE TABLE gives Code 43,
		// "Nested type ... cannot be inside Nullable type", confirmed
		// bare and nested inside a Tuple element, a Map value and an
		// Array element). Since chgen only infers types that a real
		// ClickHouse column or expression can hold, left/right can
		// never carry such a Nullable, so this branch can never reach
		// a refused base. AggregateFunction never appears as "result"
		// here at all (no switch case builds it). See
		// wrapperConstructorAllowlist in
		// wrapper_constructor_boundary_test.go for the full grid.
		return CHType{Name: "Nullable", Params: []CHType{result}}, nil
	}
	return result, nil
}

func commonWithNullLiteral(left, right CHType) (CHType, bool) {
	if !untypedNullNeutralInBranches {
		return CHType{}, false
	}
	leftNull := isUntypedNullType(left)
	rightNull := isUntypedNullType(right)
	switch {
	case leftNull && rightNull:
		return left, true
	case leftNull:
		if untypedNullRequiresNullablePeer && !canBeInsideNullable(right) {
			return CHType{}, false
		}
		return wrapNullable(right), true
	case rightNull:
		if untypedNullRequiresNullablePeer && !canBeInsideNullable(left) {
			return CHType{}, false
		}
		return wrapNullable(left), true
	default:
		return CHType{}, false
	}
}

var untypedNullNeutralInBranches = true
var untypedNullRequiresNullablePeer = true

func isUntypedNullType(value CHType) bool {
	return strings.EqualFold(value.Name, "Nullable") && len(value.Params) == 1 &&
		strings.EqualFold(value.Params[0].Name, "Nothing") && len(value.Params[0].Params) == 0
}

// isDecimalWithParams reports a Decimal(P, S) type with known precision
// and scale.
func isDecimalWithParams(value CHType) bool {
	_, _, ok := decimalPrecisionScale(value)
	return ok
}

// isFloatCHTypeName reports the ClickHouse float type names.
func isFloatCHTypeName(name string) bool {
	return strings.EqualFold(name, "Float32") || strings.EqualFold(name, "Float64")
}

// isStringSupertypeFamily reports the types that join the ClickHouse
// String supertype rule: String and FixedString(N) only. An Enum, a UUID
// or an IP address is NOT a member, because those types have no String
// supertype in the lattice.
//
// Use this ONLY for the supertype rules. For insert compatibility, which
// accepts a wider family, use insertAcceptsAsString.
func isStringSupertypeFamily(value CHType) bool {
	return strings.EqualFold(value.Name, "String") || strings.EqualFold(value.Name, "FixedString")
}

// isEnumCHType reports an Enum constructor with a known value set. A bare
// Enum name with no value set is not one: chgen keeps the value set from
// the schema, thus a set-less Enum can only come from a type that chgen
// does not know, and it must not silently take the Enum supertype rule.
func isEnumCHType(value CHType) bool {
	return isEnumTypeName(value.Name) && len(value.LiteralParams) > 0
}

// enumStorageInteger gives the integer type that holds an Enum value.
// Enum8 and its alias Enum store Int8, and Enum16 stores Int16. This is
// the type through which an Enum joins the ClickHouse supertype lattice
// against an integer or a float peer.
func enumStorageInteger(value CHType) CHType {
	if strings.EqualFold(value.Name, "Enum16") {
		return CHType{Name: "Int16"}
	}
	return CHType{Name: "Int8"}
}

// commonDecimalSupertype gives the common supertype when at least one
// side is a Decimal and the other side is a Decimal or an integer.
// Measured on ClickHouse 25.8.29.51 with real columns: the precision is
// the full precision of the widest storage class among the operands,
// where an integer type counts as the class that holds its digits
// (if(b, dec, u16) is Decimal(18, 4), if(b, dec, i64) is Decimal(38, 4),
// if(b, dec2, u8) is Decimal(18, 2), if(b, dec, dec2) is Decimal(18, 4)).
// The scale is the largest operand scale. It returns nil when the rule
// does not apply.
func commonDecimalSupertype(left, right CHType) *CHType {
	leftPrecision, leftScale, leftDecimal := decimalPrecisionScale(left)
	rightPrecision, rightScale, rightDecimal := decimalPrecisionScale(right)
	classOf := func(value CHType, precision int, decimal bool) (int, bool) {
		if decimal {
			return decimalClassPrecision(precision), true
		}
		digits, ok := decimalDigitsForInteger(value.Name)
		if !ok || len(value.Params) != 0 {
			return 0, false
		}
		return decimalClassPrecision(digits), true
	}
	if !leftDecimal && !rightDecimal {
		return nil
	}
	leftClass, leftOK := classOf(left, leftPrecision, leftDecimal)
	rightClass, rightOK := classOf(right, rightPrecision, rightDecimal)
	if !leftOK || !rightOK {
		return nil
	}
	precision := maxInt(leftClass, rightClass)
	scale := maxInt(leftScale, rightScale)
	return &CHType{Name: "Decimal", LiteralParams: []string{strconv.Itoa(precision), strconv.Itoa(scale)}}
}

func commonNumericCHType(left, right CHType) (CHType, error) {
	leftInfo, leftOK := numericInfoForType(left)
	rightInfo, rightOK := numericInfoForType(right)
	if !leftOK || !rightOK {
		return CHType{}, fmt.Errorf("%s and %s are not numeric", left.String(), right.String())
	}
	if leftInfo.kind == "float" || rightInfo.kind == "float" {
		if leftInfo.kind == "float" && rightInfo.kind == "float" {
			if leftInfo.bits == 32 && rightInfo.bits == 32 {
				return CHType{Name: "Float32"}, nil
			}
			return CHType{Name: "Float64"}, nil
		}
		// A float with an integer keeps the float that represents
		// every integer value exactly (measured on ClickHouse
		// 25.8.29.51: if(b, f32, i16) is Float32, if(b, f32, i32) is
		// Float64, and a 64-bit integer with any float is
		// NO_COMMON_TYPE).
		float, integer := leftInfo, rightInfo
		if integer.kind == "float" {
			float, integer = integer, float
		}
		switch {
		case integer.bits <= 16 && float.bits == 32:
			return CHType{Name: "Float32"}, nil
		case integer.bits <= 32:
			return CHType{Name: "Float64"}, nil
		default:
			return CHType{}, fmt.Errorf("no common ClickHouse type for %s and %s", left.String(), right.String())
		}
	}
	if leftInfo.kind == rightInfo.kind {
		bits := leftInfo.bits
		if rightInfo.bits > bits {
			bits = rightInfo.bits
		}
		return CHType{Name: numericTypeName(leftInfo.kind, bits)}, nil
	}
	unsigned := leftInfo
	signed := rightInfo
	if signed.kind == "unsigned" {
		unsigned, signed = signed, unsigned
	}
	// A mixed sign pair needs a signed type that holds the whole unsigned
	// range, thus at least twice the unsigned width, and at least the width of
	// the signed operand. Measured on ClickHouse 25.8.29.51 with real columns:
	// the server reaches Int128 and Int256 only when the pair already holds a
	// 128 bit or 256 bit operand. Thus UInt64 with a signed peer has no
	// supertype, although Int128 exists, and UInt256 has none either, because
	// Int512 does not exist.
	candidates := []int{8, 16, 32, 64}
	if unsigned.bits >= 128 || signed.bits >= 128 {
		candidates = append(candidates, 128, 256)
	}
	for _, bits := range candidates {
		if bits >= unsigned.bits*2 && bits >= signed.bits {
			return CHType{Name: numericTypeName("signed", bits)}, nil
		}
	}
	return CHType{}, fmt.Errorf("no common ClickHouse type for %s and %s", left.String(), right.String())
}

type numericInfo struct {
	kind string
	bits int
}

func numericInfoForType(value CHType) (numericInfo, bool) {
	switch strings.ToLower(value.Name) {
	case "uint8":
		return numericInfo{kind: "unsigned", bits: 8}, true
	case "uint16":
		return numericInfo{kind: "unsigned", bits: 16}, true
	case "uint32":
		return numericInfo{kind: "unsigned", bits: 32}, true
	case "uint64":
		return numericInfo{kind: "unsigned", bits: 64}, true
	case "int8":
		return numericInfo{kind: "signed", bits: 8}, true
	case "int16":
		return numericInfo{kind: "signed", bits: 16}, true
	case "int32":
		return numericInfo{kind: "signed", bits: 32}, true
	case "int64", "int":
		return numericInfo{kind: "signed", bits: 64}, true
	case "uint128":
		return numericInfo{kind: "unsigned", bits: 128}, true
	case "uint256":
		return numericInfo{kind: "unsigned", bits: 256}, true
	case "int128":
		return numericInfo{kind: "signed", bits: 128}, true
	case "int256":
		return numericInfo{kind: "signed", bits: 256}, true
	case "float32":
		return numericInfo{kind: "float", bits: 32}, true
	case "float64":
		return numericInfo{kind: "float", bits: 64}, true
	default:
		return numericInfo{}, false
	}
}

func numericTypeName(kind string, bits int) string {
	if kind == "unsigned" {
		return fmt.Sprintf("UInt%d", bits)
	}
	return fmt.Sprintf("Int%d", bits)
}

func unwrapLowCardinality(value CHType) CHType {
	for strings.EqualFold(value.Name, "LowCardinality") && len(value.Params) == 1 {
		value = value.Params[0]
	}
	return value
}

// stripLowCardinalityDeep removes the LowCardinality wrapper from a type
// at EVERY depth, not only at the top level. It is the member-transport
// rule of the BRANCH family (if, multiIf, ifNull, coalesce), which is the
// opposite of the container CONSTRUCTOR rule in
// commonContainerMemberCHType. greatest and least call commonCHTypes too,
// through greatestLeastCommonCHTypes, and therefore this deep-strip rule
// DOES apply to them as well (see TestGreatestLeastDropsNestedLowCardinality
// for the measured grid over Array, Tuple and Map).
//
// The membership this comment used to claim was TOO WIDE on a different
// axis: the regression measured that the SIGNED-WITH-UInt64 numeric rule is
// NOT shared. if(c, i32, u64) is Code: 386 (NO_COMMON_TYPE), while
// greatest(i32, u64) is Int128, and greatest(i64, u64) and
// least(i64, u64) even disagree with EACH OTHER (UInt64 vs Int64) on the
// exact same operand pair. commonNumericCHType, which this file also
// owns, keeps the branch-family refusal; greatestLeastCommonCHTypes holds
// the separate, function-specific pair rule. Do not read this comment as
// license to carry a branch rule into greatest and least without
// checking the specific rule against a measurement: the LowCardinality
// depth rule is shared, the signed/unsigned numeric rule is not, and the
// two must not be conflated again.
//
// Measured on ClickHouse 25.8.29.51 with real table columns, never over
// literals, because the server folds a constant branch. The columns are
// s String, lc LowCardinality(String), lcn LowCardinality(Nullable(String)),
// as_ Array(String), alc Array(LowCardinality(String)),
// alcn Array(LowCardinality(Nullable(String))),
// ms Map(String, String), mlc Map(String, LowCardinality(String)),
// ts Tuple(Int32, String) and tlc Tuple(Int32, LowCardinality(String)).
//
// The branch removes the wrapper at every depth and in every position,
// and the answer does NOT depend on the order of the operands:
//
//	if(b, alc, as_)     Array(String)     if(b, as_, alc)     Array(String)
//	if(b, alc, alc)     Array(String)
//	if(b, tlc, ts)      Tuple(Int32, String)
//	if(b, ts, tlc)      Tuple(Int32, String)
//	if(b, tlc, tlc)     Tuple(Int32, String)
//	if(b, mlc, ms)      Map(String, String)
//	if(b, ms, mlc)      Map(String, String)
//	if(b, mlc, mlc)     Map(String, String)
//	if(b, alcn, alc)    Array(Nullable(String))
//	if(b, alcn, alcn)   Array(Nullable(String))
//	if(b, array(array(lc)), array(array(lc)))   Array(Array(String))
//
// An all-LowCardinality operand pair still loses the wrapper here. That
// is what separates this rule from the constructor rule, where
// [alc, alc] keeps it as Array(Array(LowCardinality(String))).
//
// The same removal applies to a LowCardinality INSIDE a
// SimpleAggregateFunction, thus the strip runs before the branch wrapper
// rule (measured with sagglc
// SimpleAggregateFunction(anyLast, LowCardinality(String)):
// if(k, sagglc, lc) and if(k, sagglc, sagglc) are both String).
func stripLowCardinalityDeep(value CHType) CHType {
	value = unwrapLowCardinality(value)
	if len(value.Params) == 0 {
		return value
	}
	params := make([]CHType, len(value.Params))
	for index, param := range value.Params {
		params[index] = stripLowCardinalityDeep(param)
	}
	stripped := value
	stripped.Params = params
	return stripped
}

// commonContainerMemberCHType gives the element type that a container
// CONSTRUCTOR builds from its members. It is the common supertype of the
// members, with one addition that the plain lattice does not have: when
// EVERY member is LowCardinality, the result keeps a LowCardinality
// wrapper.
//
// Measured on ClickHouse 25.8.29.51 with real table columns, because the
// server folds a literal array:
//
//	SELECT toTypeName([lc, lc])   Array(LowCardinality(String))
//	SELECT toTypeName([lc, s])    Array(String)
//	SELECT toTypeName([lcn, lc])  Array(LowCardinality(Nullable(String)))
//	SELECT toTypeName([lci, lci64])  Array(LowCardinality(Int64))
//
// Thus one bare member removes the wrapper from all of them, while an
// all-LowCardinality member set keeps it and joins the inner types. This
// is why commonCHTypes alone is not enough here: that helper always
// unwraps LowCardinality, which is right for if and multiIf
// (if(b, lc, lc) is String) but wrong for a constructor.
//
// The Nullable position is not special. LowCardinality(Nullable(String))
// with LowCardinality(String) keeps both wrappers, because the inner
// join of Nullable(String) with String is Nullable(String).
func commonContainerMemberCHType(members []CHType) (CHType, error) {
	if len(members) == 0 {
		return CHType{}, fmt.Errorf("cannot infer the member type from no expressions")
	}
	inner := make([]CHType, len(members))
	allLowCardinality := true
	for index, member := range members {
		if !strings.EqualFold(member.Name, "LowCardinality") || len(member.Params) != 1 {
			allLowCardinality = false
		}
		inner[index] = unwrapLowCardinality(member)
	}
	result, err := commonCHTypes(inner)
	if err != nil {
		return CHType{}, err
	}
	// commonCHTypes is the BRANCH entry point, thus it removed the
	// LowCardinality wrapper at EVERY depth. That is right for the top
	// level, which the allLowCardinality test above decides on its own,
	// but it also removed the wrappers of the NESTED positions, where the
	// constructor rule applies again and separately.
	//
	// The rule is recursive and order-independent at every depth
	// (measured on ClickHouse 25.8.29.51 with real columns):
	//
	//	[alc, alc]    Array(Array(LowCardinality(String)))
	//	[alc, as_]    Array(Array(String))
	//	[as_, alc]    Array(Array(String))
	//	[tlc, tlc]    Array(Tuple(Int32, LowCardinality(String)))
	//	[tlc, ts]     Array(Tuple(Int32, String))
	//	[mlc, mlc]    Array(Map(String, LowCardinality(String)))
	//	[mlc, ms]     Array(Map(String, String))
	//	[array(array(lc)), array(array(lc))]  Array(Array(Array(LowCardinality(String))))
	//	[array(array(lc)), array(array(s))]   Array(Array(Array(String)))
	//
	// Thus the nested wrappers are restored here by asking the same rule
	// again for each parameter position, over the matching parameters of
	// the members. A member whose shape does not match the result shape
	// contributes nothing, which correctly makes the position lose the
	// wrapper.
	result = restoreContainerMemberLowCardinality(result, inner)
	if allLowCardinality {
		// wrapLowCardinality, and not a hand-built node: the keep-if-all
		// rule decides WHEN to keep the wrapper, but only the constructor
		// knows WHETHER the common base can hold one. A base in the
		// measured rejection table makes the server refuse the WHOLE
		// expression with Code: 43, thus the bare type is the honest
		// answer. See resultRejectsLowCardinality. This was the regression.
		return wrapLowCardinality(result), nil
	}
	return result, nil
}

// restoreContainerMemberLowCardinality puts the nested LowCardinality
// wrappers back on a container-constructor supertype. commonCHTypes
// removes them at every depth, because it is the branch rule; the
// constructor keeps a wrapper at a nested position when EVERY member had
// one there. See commonContainerMemberCHType for the measured grid.
//
// The result shape decides which positions exist. Each member must have
// the same type name and the same number of parameters to contribute; a
// member of a different shape leaves the position bare, which is the
// conservative answer.
func restoreContainerMemberLowCardinality(result CHType, members []CHType) CHType {
	if len(result.Params) == 0 {
		return result
	}
	restored := result
	restored.Params = make([]CHType, len(result.Params))
	copy(restored.Params, result.Params)
	for index := range restored.Params {
		position := make([]CHType, 0, len(members))
		for _, member := range members {
			bare := unwrapLowCardinality(member)
			if !strings.EqualFold(bare.Name, result.Name) || len(bare.Params) != len(result.Params) {
				position = nil
				break
			}
			position = append(position, bare.Params[index])
		}
		if len(position) == 0 {
			continue
		}
		allLowCardinality := true
		inner := make([]CHType, len(position))
		for innerIndex, value := range position {
			if !strings.EqualFold(value.Name, "LowCardinality") || len(value.Params) != 1 {
				allLowCardinality = false
			}
			inner[innerIndex] = unwrapLowCardinality(value)
		}
		nested := restoreContainerMemberLowCardinality(restored.Params[index], inner)
		if allLowCardinality {
			// The same obligation as the outer node, at a parameter
			// position: ask the constructor, because a rejected base
			// cannot hold the wrapper here either. See the regression.
			nested = wrapLowCardinality(nested)
		}
		restored.Params[index] = nested
	}
	return restored
}

// arrayFunctionResult is the rule for the array constructor and for the
// array literal, which are the same function. The element type is the
// common supertype of the arguments; see commonContainerMemberCHType.
//
// Measured on 25.8.29.51 with real columns:
//
//	SELECT toTypeName(array(lc, s))   Array(String)
//	SELECT toTypeName([i8, i32])      Array(Int32)
//	SELECT toTypeName([i32, u32])     Array(Int64)
//	SELECT toTypeName([dec, i32])     Array(Decimal(18, 4))
//	SELECT toTypeName([s, fs])        Array(String)
//	SELECT toTypeName([d, dt])        Array(DateTime)
//
// A member set with no supertype is a refusal on the server too, thus
// the refusal here is correct: [s, i32] and [dec, f64] are both
// NO_COMMON_TYPE (code 386).
func arrayFunctionResult(args []CHType) (CHType, error) {
	if len(args) == 0 {
		return CHType{}, fmt.Errorf("array function has no arguments")
	}
	element, err := commonContainerMemberCHType(args)
	if err != nil {
		return CHType{}, fmt.Errorf("array constructor: %w", err)
	}
	return CHType{Name: "Array", Params: []CHType{element}}, nil
}

// mapFunctionResult is the rule for the map constructor. The arguments
// alternate key and value. The key type is the common supertype of the
// odd positions and the value type is the common supertype of the even
// positions, each computed like an array element.
//
// Measured on 25.8.29.51 with real columns:
//
//	SELECT toTypeName(map('k', lc))            Map(String, LowCardinality(String))
//	SELECT toTypeName(map(lc, s))              Map(LowCardinality(String), String)
//	SELECT toTypeName(map(lc, s, lc, s))       Map(LowCardinality(String), String)
//	SELECT toTypeName(map(lc, s, s, s))        Map(String, String)
//	SELECT toTypeName(map('a', i32, 'b', i64)) Map(String, Int64)
//	SELECT toTypeName(map(i32, s, i64, s))     Map(Int64, String)
//	SELECT toTypeName(map('k', (i32, lc)))     Map(String, Tuple(Int32, LowCardinality(String)))
//
// Two argument sets are a refusal on the server as well:
//
//	map('a', s, 'b')     code 42, NUMBER_OF_ARGUMENTS_DOESNT_MATCH
//	map('a', i32, 'b', s) code 386, NO_COMMON_TYPE
//
// A Nullable key is refused with code 36 (BAD_ARGUMENTS), and so is a
// LowCardinality(Nullable(...)) key, thus the test looks through the
// LowCardinality wrapper before it examines the Nullable.
//
// map() with no arguments gives Map(Nothing, Nothing) on the server.
// chgen refuses that instead, because Nothing has no Go type to
// generate. A refusal is the mild failure; a wrong type is not.
func mapFunctionResult(args []CHType) (CHType, error) {
	if len(args) == 0 {
		return CHType{}, fmt.Errorf("map constructor with no arguments has the element type Nothing, which chgen cannot map to a Go type")
	}
	if len(args)%2 != 0 {
		return CHType{}, fmt.Errorf("map constructor requires an even number of arguments, but %d were given", len(args))
	}
	keys := make([]CHType, 0, len(args)/2)
	values := make([]CHType, 0, len(args)/2)
	for index, arg := range args {
		if index%2 == 0 {
			keys = append(keys, arg)
			continue
		}
		values = append(values, arg)
	}
	key, err := commonContainerMemberCHType(keys)
	if err != nil {
		return CHType{}, fmt.Errorf("map constructor key: %w", err)
	}
	if strings.EqualFold(unwrapLowCardinality(key).Name, "Nullable") {
		return CHType{}, fmt.Errorf("map cannot have a key of type %s", key.String())
	}
	value, err := commonContainerMemberCHType(values)
	if err != nil {
		return CHType{}, fmt.Errorf("map constructor value: %w", err)
	}
	return CHType{Name: "Map", Params: []CHType{key, value}}, nil
}

// groupArrayElementType is the element-type step shared by the whole
// groupArray family (groupArray, groupArrayIf, groupUniqArray,
// groupUniqArrayIf). ClickHouse skips NULL values in these aggregates, so
// the element type drops the Nullable and LowCardinality wrappers
// (measured on ClickHouse 25.8.29.51: groupArray(Nullable(String)) is
// Array(String)). The plain array constructor keeps the wrappers instead.
func groupArrayElementType(args []CHType) (CHType, error) {
	if len(args) == 0 {
		return CHType{}, fmt.Errorf("array function has no arguments")
	}
	// groupArray is an aggregate, thus the element loses every
	// LowCardinality wrapper, at the top level and inside a container
	// member. The class of this rule is wrapperOpaque, thus the shared
	// aggregate path does not do it here. Measured:
	//
	//	SELECT toTypeName(groupArray(lc))          Array(String)
	//	SELECT toTypeName(groupArray((i32, lc)))   Array(Tuple(Int32, String))
	//
	// See stripNestedLowCardinality.
	//
	// groupArray READS its argument as a value, thus a
	// SimpleAggregateFunction marker that does not survive that read is
	// replaced by its inner type BEFORE the wrapper split. The marker
	// survives a bare scalar inner type only. Measured:
	//
	//	SELECT toTypeName(groupArray(saf))
	//	                    Array(SimpleAggregateFunction(anyLast, Int32))
	//	SELECT toTypeName(groupArray(safn))     Array(Int32)
	//	SELECT toTypeName(groupArray(safarrb))  Array(Array(Int32))
	//
	// The two non-Nullable inners saf and safarrb answer differently,
	// thus the test is not nullability. See
	// simpleAggregateMarkerSurvives.
	element, _, _ := splitCHWrappers(readSimpleAggregateValue(args[0]))
	return stripNestedLowCardinality(element), nil
}

// groupArrayFunctionResult is the rule for groupArray and groupArrayIf.
// The element type keeps every parameter of its leaf type, including a
// DateTime timezone (measured on ClickHouse 25.8.29.51:
// groupArray(DateTime('UTC')) is Array(DateTime('UTC'))).
func groupArrayFunctionResult(args []CHType) (CHType, error) {
	element, err := groupArrayElementType(args)
	if err != nil {
		return CHType{}, err
	}
	return CHType{Name: "Array", Params: []CHType{element}}, nil
}

// groupUniqArrayFunctionResult is the rule for groupUniqArray and
// groupUniqArrayIf. These two functions build the element type the same
// way groupArray does, with one divergence: on a bare DateTime leaf (a
// DateTime that carries a timezone but NOT the DateTime64 precision
// argument), the server drops the timezone. A DateTime64 leaf keeps
// every parameter, and so does every other parametric leaf (Decimal,
// FixedString, Enum).
//
// Measured on ClickHouse 25.8.29.51 over real table columns:
//
//	SELECT toTypeName(groupUniqArray(dtz))    -- dtz DateTime('UTC')
//	                                           Array(DateTime)
//	SELECT toTypeName(groupUniqArray(dtz64))  -- dtz64 DateTime64(3,'UTC')
//	                                           Array(DateTime64(3, 'UTC'))
//
// The cause is groupUniqArray itself, confirmed in the ClickHouse source
// (AggregateFunctionGroupUniqArray.cpp, createResultType): it builds a
// fresh DataTypeDateTime instead of propagating the argument type, so the
// timezone is lost. AggregateFunctionGroupArray.cpp propagates the
// argument type unchanged, which is why groupArray keeps the timezone.
//
// The defect survives a wrapper: groupUniqArray(nullIf(dtz, dtz)) also
// answers Array(DateTime) on the server (measured the same way). The
// check below therefore reads the leaf AFTER groupArrayElementType has
// already stripped the Nullable and LowCardinality wrappers.
func groupUniqArrayFunctionResult(args []CHType) (CHType, error) {
	element, err := groupArrayElementType(args)
	if err != nil {
		return CHType{}, err
	}
	// The timezone drop is a VERDICT and no longer a name-shaped special
	// case in this function: the pair (groupuniqarray,
	// DateTime.timezone) carries verdictDrop in the curated table, and
	// applyParameterVerdictStrict performs the drop. A parametric leaf
	// family with no verdict now refuses here instead of copying its
	// parameter through. See parameter_verdict.go.
	element, err = applyParameterVerdictStrict("groupuniqarray", element)
	if err != nil {
		return CHType{}, err
	}
	return CHType{Name: "Array", Params: []CHType{element}}, nil
}

func tupleFunctionResult(args []CHType) (CHType, error) {
	if len(args) == 0 {
		return CHType{}, fmt.Errorf("tuple function has no arguments")
	}
	return CHType{Name: "Tuple", Params: args}, nil
}

// arrayElementFunctionResult types arrayElement(array, index).
//
// A read through arrayElement drops a LowCardinality wrapper from the
// element it returns, at every depth. A Nullable wrapper survives the
// same read untouched. This is the same wrapperOpaque LowCardinality
// rule that groupArrayFunctionResult already applies through
// stripNestedLowCardinality, so this rule reuses that helper rather
// than repeating it.
//
// Measured on ClickHouse 25.8.29.51 with real columns and a VALUE
// witness, not toTypeName alone: lca is Array(LowCardinality(String))
// with value ['a','b'], lcaa is
// Array(Array(LowCardinality(String))) with value [['a','b'],['c']],
// and lcna is Array(LowCardinality(Nullable(String))) with value
// ['a', NULL].
//
//	arrayElement(lca, 2)                    String          'b'
//	arrayElement(lcaa, 1)                   Array(String)   ['a','b']
//	arrayElement(arrayElement(lcaa, 1), 1)   String          'a'
//	arrayElement(lcna, 1)                   Nullable(String) 'a'
func arrayElementFunctionResult(args []CHType) (CHType, error) {
	if len(args) == 0 {
		return CHType{}, fmt.Errorf("arrayElement has no arguments")
	}
	if strings.EqualFold(args[0].Name, "Array") && len(args[0].Params) == 1 {
		result := stripNestedLowCardinality(args[0].Params[0])
		// A NULL index produces NULL, independently of the element's
		// nullability (measured with real Nullable(UInt64) columns).
		if len(args) > 1 {
			_, nullable, _ := splitCHWrappers(args[1])
			if nullable {
				if !canBeInsideNullable(result) {
					return CHType{}, fmt.Errorf("arrayElement cannot put %s inside Nullable for a nullable index", result.String())
				}
				return wrapNullable(result), nil
			}
		}
		return result, nil
	}
	return CHType{}, fmt.Errorf("arrayElement expects an Array argument")
}

func mapKeysFunctionResult(args []CHType) (CHType, error) {
	if len(args) == 0 {
		return CHType{}, fmt.Errorf("mapKeys has no arguments")
	}
	if strings.EqualFold(args[0].Name, "Map") && len(args[0].Params) == 2 {
		return CHType{Name: "Array", Params: []CHType{args[0].Params[0]}}, nil
	}
	return CHType{}, fmt.Errorf("mapKeys expects a Map argument")
}

func mapValuesFunctionResult(args []CHType) (CHType, error) {
	if len(args) == 0 {
		return CHType{}, fmt.Errorf("mapValues has no arguments")
	}
	if strings.EqualFold(args[0].Name, "Map") && len(args[0].Params) == 2 {
		return CHType{Name: "Array", Params: []CHType{args[0].Params[1]}}, nil
	}
	return CHType{}, fmt.Errorf("mapValues expects a Map argument")
}

func (scopes *scopeIndex) lookup(node clickhouse.Expr, fallback queryScope) queryScope {
	if scope, ok := scopes.byNode[node]; ok {
		return scope
	}
	return fallback
}

func (scope queryScope) lookupTable(name string) (Table, bool) {
	for index := len(scope.tables) - 1; index >= 0; index-- {
		scoped := scope.tables[index]
		if name == scoped.alias || name == scoped.table.Name {
			return scoped.table, true
		}
	}
	if table, ok := scope.relations[name]; ok {
		return table, true
	}
	if scope.reservedRelations[name] {
		return Table{}, false
	}
	if scope.parent != nil {
		return scope.parent.lookupTable(name)
	}
	return Table{}, false
}

func (scope queryScope) lookupLocalTable(name string) (Table, bool) {
	for index := len(scope.tables) - 1; index >= 0; index-- {
		scoped := scope.tables[index]
		if name == scoped.alias || name == scoped.table.Name {
			return scoped.table, true
		}
	}
	return Table{}, false
}

func (scope queryScope) hasReservedRelation(name string) bool {
	if scope.reservedRelations[name] {
		return true
	}
	return scope.parent != nil && scope.parent.hasReservedRelation(name)
}

func (scope queryScope) hasFromAlias(name string) bool {
	for index := scope.fromBindingStart; index < len(scope.tables); index++ {
		if scope.tables[index].alias == name {
			return true
		}
	}
	return false
}

func (scope queryScope) lookupScalar(name string) (CHType, bool) {
	if scalar, ok := scope.projectionAliases[name]; ok {
		return scalar, true
	}
	if scalar, ok := scope.scalars[name]; ok {
		return scalar, true
	}
	if !scope.exactScalarNames {
		for scalarName, scalar := range scope.scalars {
			if strings.EqualFold(name, scalarName) {
				return scalar, true
			}
		}
	}
	if scope.reservedScalars[name] {
		return CHType{}, false
	}
	if scope.parent != nil {
		return scope.parent.lookupScalar(name)
	}
	return CHType{}, false
}

func (scope queryScope) lookupLocalScalar(name string) (CHType, bool) {
	if scalar, ok := scope.projectionAliases[name]; ok {
		return scalar, true
	}
	if scalar, ok := scope.scalars[name]; ok {
		return scalar, true
	}
	if !scope.exactScalarNames {
		for scalarName, scalar := range scope.scalars {
			if strings.EqualFold(name, scalarName) {
				return scalar, true
			}
		}
	}
	return CHType{}, false
}

func (scope queryScope) lookupProjectionExpr(name string) (clickhouse.Expr, bool) {
	if expression, ok := scope.projectionExprs[name]; ok && !scope.aliasExpansion[name] {
		return expression, true
	}
	if scope.parent != nil {
		return scope.parent.lookupProjectionExpr(name)
	}
	return nil, false
}

func (scope queryScope) lookupLocalColumn(qualifier, name string) (CHType, error) {
	local := scope
	local.parent = nil
	return local.lookupColumn(qualifier, name)
}

func (scope queryScope) lookupColumn(qualifier, name string) (CHType, error) {
	if qualifier == "" {
		if joined, ok := scope.arrayJoinTypes[name]; ok {
			return joined, nil
		}
		if merged, ok := scope.usingTypes[name]; ok {
			return merged, nil
		}
	} else if joined, ok := scope.arrayJoinQualified[qualifier+"."+name]; ok {
		return joined, nil
	} else if merged, ok := scope.usingQualified[qualifier+"."+name]; ok {
		return merged, nil
	}
	var found []Column
	localQualifier := false
	for _, scoped := range scope.tables {
		if qualifier != "" {
			if qualifier != scoped.alias && qualifier != scoped.table.Name {
				continue
			}
			localQualifier = true
		}
		if column, ok := scoped.table.Columns[name]; ok {
			found = append(found, column)
		}
	}
	if len(found) == 0 {
		if scope.parent != nil && !localQualifier {
			return scope.parent.lookupColumn(qualifier, name)
		}
		message := fmt.Sprintf("column %q is not present in FROM tables", name)
		if suggestion := suggestName(name, scope.columnCandidates(qualifier)); suggestion != "" {
			message += fmt.Sprintf("; did you mean %q?", suggestion)
		}
		return CHType{}, fmt.Errorf("%s", message)
	}
	if len(found) > 1 && qualifier == "" {
		if len(scope.tables)-scope.fromBindingStart != 2 {
			return CHType{}, fmt.Errorf("column %q is ambiguous", name)
		}
	}
	return found[0].Type, nil
}
