package engine

import (
	"testing"
)

// ClickHouse removes the dead branch of a two-branch `if` when the
// condition folds to a constant, and it removes the branch BEFORE it
// gives the branch a type. Thus a dead branch that has no type at all
// does not stop the expression.
//
// Measured on ClickHouse 25.8.29.51 with the real columns of the oracle
// fixture table t, never with bare literals, and each expression was
// also executed with SELECT <expression> FROM t LIMIT 1:
//
//	if(false, if(b, i64, f64), u8)   UInt8
//	if(true,  u8, if(b, i64, f64))   UInt8
//	if(b,     if(b, i64, f64), u8)   Code: 386
//
// This is NOT an n-ary join rule. A branch function does join all of its
// branches at one time, but that is not what these expressions need: the
// server gives the dead branch no type at all, thus no join over it
// happens. Measured: multiIf(true, i8, b, u64, u32) is itself Code: 386
// on the server, and the expression that holds it still answers UInt16,
// because a constant condition removed the branch that holds it.
//
// The rule is narrow, and the measurements below fix each of its edges:
//
//   - Only `if` does this. `multiIf` and `CASE` give a type to EVERY
//     branch, thus a constant condition does not save them.
//   - Only a dead branch that has NO type is removed. When both branches
//     have a type, the normal join runs, and the join can still refuse:
//     if(false, f64, i64) is Code: 386, although the live branch alone
//     is Int64.
//   - The join result is not the live branch: if(false, nf64, u8) is
//     Nullable(Float64), not UInt8.
//   - A live branch with no type is still a refusal.
func TestConstantConditionIfDropsUntypedDeadBranch(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []struct{ expr, want string }{
		// The dead branch has no type. The live branch gives the type.
		{"if(false, if(b, i64, f64), u8)", "UInt8"},
		{"if(true, u8, if(b, i64, f64))", "UInt8"},
		{"if(NOT true, if(b, i64, f64), u8)", "UInt8"},
		{"if(1, u8, if(b, i64, f64))", "UInt8"},

		// The condition folds to a constant through an operator, thus
		// the same rule applies.
		{"if(b AND false, if(b, i64, f64), u8)", "UInt8"},
		{"if(1 > 2, if(b, i64, f64), u8)", "UInt8"},
		{"if(toString(0.5) < '', if(b, i64, f64), u8)", "UInt8"},

		// Both branches have a type, thus the normal join runs and the
		// live branch does NOT win.
		{"if(false, nf64, u8)", "Nullable(Float64)"},
		{"if(true, u8, i16)", "Int16"},
		{"if(false, i16, u8)", "Int16"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// The dead-branch rule must not open a refusal that the server keeps.
// Every expression here is Code: 386 NO_COMMON_TYPE on 25.8.29.51.
func TestConstantConditionIfKeepsRefusals(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []string{
		// The condition is not constant, thus every branch needs a
		// type.
		"if(b, if(b, i64, f64), u8)",
		"if(i8 > 0, if(b, i64, f64), u8)",

		// The LIVE branch has no type. A constant condition does not
		// help.
		"if(false, u8, if(b, i64, f64))",
		"if(true, if(b, i64, f64), u8)",

		// Both branches have a type, thus the normal join runs and
		// still refuses.
		"if(false, f64, i64)",
		"if(true, i64, f64)",
		"if(true, u64, i64)",

		// multiIf gives a type to every branch, thus a constant
		// condition does not remove a branch.
		"multiIf(true, u8, b, if(b, i64, f64), u16)",
		"multiIf(false, if(b, i64, f64), b, u8, u16)",
		"multiIf(b, u8, false, if(b, i64, f64), u16)",

		// CASE behaves like multiIf, not like if.
		"CASE WHEN false THEN if(b, i64, f64) ELSE u8 END",
	}
	for _, expr := range cases {
		if got, err := inferCHTypeStringErr(t, schema, expr); err == nil {
			t.Errorf("type of %q = %s, want a refusal", expr, got)
		}
	}
}

// The five expressions that the oracle reported as false refusals, with
// the type that ClickHouse 25.8.29.51 gives each one. Each was measured
// with toTypeName AND executed with SELECT <expression> FROM t LIMIT 1.
// Every one holds a constant condition on an `if`, and the branch that
// the condition drops is the branch that has no type.
func TestOracleFalseRefusalsNowType(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []struct{ expr, want string }{
		// The earlier grammar. The dead branch holds multiIf(true, i8, b, u64,
		// u32), which the server itself refuses when it is alone.
		{"if((toString(0.5) < ''), multiIf((1e10 >= i64), (i32 * -129), (fs ILIKE '%a%'), multiIf(true, i8, b, u64, u32), f32), 256)", "UInt16"},
		{"if(true, -(u32), multiIf((ns LIKE '%a%'), CAST(9223372036854775807 AS Int64), (NOT false), f64, CAST(-129 AS Float32)))", "Int64"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// oracleSubsetSchema holds the columns that the curated findings below use.
// The column types match the current fixture.
func oracleSubsetSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    i64 Int64, u8 UInt8, u32 UInt32, u64 UInt64,
    f64 Float64, b Bool, nf64 Nullable(Float64),
    arr_i Array(Int32),
    e16 Enum16('x' = 1, 'y' = 2),
    i128 Int128, i256 Int256
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

// These three false refusals carry their measured server types.
func TestCuratedOracleFalseRefusalsNowType(t *testing.T) {
	schema := oracleSubsetSchema(t)
	cases := []struct{ expr, want string }{
		{"if((NOT true), (CASE i64 WHEN 1 THEN (10000000000 * 10000000000) ELSE (9223372036854775807 * -129) END), (toInt256(u64) / 1))", "Float64"},
		{"if((false AND (e16 = 'x')), if((NOT false), -(nf64), i256), (if(false, u32, nf64) * u8))", "Nullable(Float64)"},
		{"if(false, coalesce(nullIf(multiIf(true, 18446744073709551615, b, nf64, 1.5), toDecimal128(18446744073709551615, 2)), toDecimal128(18446744073709551615, 2)), (toInt128(f64) - arr_i[1]))", "Int128"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// A constant condition that is a CALL folds too. The earlier code
// folded only a plain literal, or a comparison of two plain literals,
// which made chgen refuse expressions that the server types. That was
// the cause of three false refusals of the fuzz oracle.
//
// The governing rule of constant_condition.go still holds: fold only a
// form that was MEASURED. Each expression below was measured on
// ClickHouse 25.8.29.51 against the real fixture columns with
// toTypeName, and the whole expression was also executed with
// SELECT <expression> FROM t LIMIT 1, because toTypeName is an analysis
// witness only.
//
// The condition need NOT be Bool. Measured: the condition of the seed-7
// finding below has the type Nullable(UInt8) and the value 0, and the
// server still drops the dead branch.
func TestConstantConditionFoldsConstantCall(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []struct{ expr, want string }{
		// empty and notEmpty of a constant string, and of a concat of
		// constant strings. concat('abc', '') is 'abc', thus empty is
		// 0 and the SECOND branch is the dead one.
		{"if(empty(concat('abc', '')), if(b, i64, f64), u8)", "UInt8"},
		{"if(empty('a'), if(b, i64, f64), u8)", "UInt8"},
		{"if(empty(''), u8, if(b, i64, f64))", "UInt8"},
		{"if(notEmpty('a'), u8, if(b, i64, f64))", "UInt8"},

		// The conversions whose result type is UInt8 or Bool.
		{"if(toUInt8(0), if(b, i64, f64), u8)", "UInt8"},
		{"if(toUInt8(1), u8, if(b, i64, f64))", "UInt8"},
		{"if(toBool(0), if(b, i64, f64), u8)", "UInt8"},
		{"if(CAST(0 AS UInt8), if(b, i64, f64), u8)", "UInt8"},

		// nullIf keeps the type of its first argument, thus
		// nullIf(0, 255) is Nullable(UInt8), which folds.
		{"if(nullIf(0, 255), if(b, i64, f64), u8)", "UInt8"},
		{"if(nullIf(1, 255), u8, if(b, i64, f64))", "UInt8"},

		// isNull and isNotNull of a constant.
		{"if(isNull(1), if(b, i64, f64), u8)", "UInt8"},
		{"if(isNotNull(1), u8, if(b, i64, f64))", "UInt8"},

		// NOT of a constant call.
		{"if(NOT toUInt8(1), if(b, i64, f64), u8)", "UInt8"},

		// A comparison whose OPERANDS are calls. This is the condition
		// of the seed-7 finding: its type is Nullable(UInt8) and its
		// value is 0.
		{"if((CAST(10000000000 AS Int64) <= nullIf(4000000000, 255)), if(b, i64, f64), u8)", "UInt8"},
		{"if((toInt64(5) > 10), if(b, i64, f64), u8)", "UInt8"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// The call fold must NOT widen past what was measured. Every expression
// here must still refuse.
//
// The first group is the type boundary. isConstant is NOT the rule: each
// condition below has isConstant = 1 and a non-zero value, yet the
// server does NOT drop the dead branch, because the condition needs a
// conversion first. Measured on 25.8.29.51, all Code: 386:
//
//	if(toUInt16(2),   u8, if(b, i64, f64))
//	if(toInt64(2),    u8, if(b, i64, f64))
//	if(toFloat64(2),  u8, if(b, i64, f64))
//	if(length('abc'), u8, if(b, i64, f64))
//
// The second group is the non-constant boundary: an argument that is a
// COLUMN makes the call non-constant, thus nothing folds.
//
// The third group is determinism, where chgen is deliberately STRICTER
// than the server. Measured: isConstant(now()) is 1, and
// if(now() > toDateTime(0), u8, if(b, i64, f64)) is UInt8 on the server,
// thus the server folds it. chgen refuses, because the value of now()
// depends on the moment the query runs: a condition such as
// `now() > '2030-01-01'` takes one branch today and the other branch
// later, and a wrong guess would drop a branch that the server types,
// which turns a refusal into a WRONG TYPE. A refusal is the cheaper
// failure, thus the refusal is intended here.
func TestConstantConditionCallFoldKeepsRefusals(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []string{
		// The type of the constant is not UInt8 or Bool.
		"if(toUInt16(2), u8, if(b, i64, f64))",
		"if(toInt64(2), u8, if(b, i64, f64))",
		"if(toFloat64(2), u8, if(b, i64, f64))",
		"if(length('abc'), u8, if(b, i64, f64))",

		// An argument is a column, thus the call is not constant.
		"if(toUInt8(u8), if(b, i64, f64), u8)",
		"if(nullIf(i64, 255), if(b, i64, f64), u8)",
		"if(empty(s), if(b, i64, f64), u8)",
		"if(CAST(i64 AS UInt8), if(b, i64, f64), u8)",
		"if(empty(concat(s, '')), if(b, i64, f64), u8)",
		"if((CAST(i64 AS Int64) <= nullIf(4000000000, 255)), if(b, i64, f64), u8)",

		// Non-deterministic. rand() is not constant for the server
		// either; now() IS constant for the server, and chgen still
		// refuses it on purpose.
		"if(rand() > 0, u8, if(b, i64, f64))",
		"if(now() > toDateTime(0), u8, if(b, i64, f64))",

		// A function that is not in the measured list does not fold,
		// although its arguments are constants.
		"if(sipHash64('abc') > 0, u8, if(b, i64, f64))",
	}
	for _, expr := range cases {
		if got, err := inferCHTypeStringErr(t, schema, expr); err == nil {
			t.Errorf("type of %q = %s, want a refusal", expr, got)
		}
	}
}

// The two boundaries that the call fold must keep. Only `if` drops a
// branch: multiIf gives a type to EVERY branch, thus a constant
// condition does not save it. Measured on 25.8.29.51:
//
//	multiIf(true, i16, false, i64, toUInt64(dec))   Code: 386
//	multiIf(false, i64, true, i16, toUInt64(dec))   Code: 386
//	if(true, i16, if(false, i64, toUInt64(dec)))    Int16
func TestConstantConditionCallFoldKeepsMultiIfBoundary(t *testing.T) {
	schema := supertypeTestSchema(t)
	for _, expr := range []string{
		"multiIf(true, i16, false, i64, toUInt64(dec))",
		"multiIf(false, i64, true, i16, toUInt64(dec))",
	} {
		if got, err := inferCHTypeStringErr(t, schema, expr); err == nil {
			t.Errorf("type of %q = %s, want a refusal", expr, got)
		}
	}
	// A nested `if` DOES fold, and the dead arm is never typed.
	const nested = "if(true, i16, if(false, i64, toUInt64(dec)))"
	if got := inferCHTypeString(t, schema, nested); got != "Int16" {
		t.Errorf("type of %q = %s, want Int16", nested, got)
	}
}

// The fuzz-oracle false refusals that the call fold removes. Each was
// measured with toTypeName AND executed with
// SELECT <expression> FROM t LIMIT 1 on 25.8.29.51.
//
// The seed-1234 finding is NOT here, because it is not a
// constant-condition defect. Its condition is the plain literal
// `false`, which the earlier code already folded. Measured
// decomposition of that expression:
//
//	if(false, 10000000000, 18446744073709551615)   UInt64
//	ifNull(nullIf(-129, -129), -129)               Int16
//	nullIf(<the UInt64>, <the Int16>)              Nullable(UInt64)
//
// chgen refuses that nullIf with "no common ClickHouse type for UInt64
// and Int16", thus the cause is the join of UInt64 with a negative
// Int16 inside nullIf, and it needs its own ticket.
func TestOracleCallConditionFalseRefusalsNowType(t *testing.T) {
	schema := supertypeTestSchema(t)
	cases := []struct{ expr, want string }{
		// seed 7, earlier grammar. analysis Int16, execution 5. The dead arm
		// holds if(false, i64, toUInt64(dec)), which is Code: 386 on its
		// own, and the outer condition folds to false, thus the server
		// never types it.
		{"if((CAST(10000000000 AS Int64) <= nullIf(4000000000, 255)), multiIf((-2.5 = 10000000000), i64, (9223372036854775807 = -129), (1.5 - f32), toUInt64(dec)), (u8 * -(-1)))", "Int16"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// The grammar-v2 false refusal of seed 7. Its condition is
// empty(concat('abc', ”)), whose type is UInt8 and whose value is 0,
// thus the server drops the SECOND branch and never types it.
func TestCuratedOracleCallConditionFalseRefusalNowTypes(t *testing.T) {
	// oracleSubsetSchema does not hold i16 or lcn, thus this test
	// carries its own subset. The column types are copied from
	// oracleSchemaDDL, so the measured types stay valid.
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    i16 Int16, u8 UInt8, u32 UInt32, f64 Float64,
    lcn LowCardinality(Nullable(String))
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	const expr = "if(empty(concat('abc', '')), multiIf(empty(lcn), toUInt64(i16), (u8 > 3), CAST(u32 AS Int64), f64), toInt64(u8))"
	if got := inferCHTypeString(t, schema, expr); got != "Int64" {
		t.Errorf("type of %q = %s, want Int64", expr, got)
	}
}
