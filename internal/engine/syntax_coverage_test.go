package engine

import "testing"

// syntaxTestSchema adds the array element types that the lambda rules
// need, on top of the columns of wrapperTestSchema.
func syntaxTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    i8   Int8, i16 Int16, i32 Int32, i64 Int64,
    u8   UInt8, u16 UInt16, u32 UInt32, u64 UInt64,
    f32  Float32, f64 Float64, dec Decimal(18, 4), b Bool,
    s    String, fs FixedString(8),
    d    Date, dt DateTime, dt64 DateTime64(3),
    ni32 Nullable(Int32), nf64 Nullable(Float64), ns Nullable(String),
    nb   Nullable(Bool),
    arr_i Array(Int32), arr_s Array(String),
    arr_n Array(Nullable(Int32)), arr_lc Array(LowCardinality(String)),
    arr_u8 Array(UInt8), arr_f Array(Float32), arr_d Array(Decimal(18, 4)),
    m    Map(String, Int64),
    lc   LowCardinality(String), lcn LowCardinality(Nullable(String))
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

// TestCaseWhenType checks the CASE WHEN rule. Every expectation below was
// measured on ClickHouse 25.8.29.51 with real table columns, because
// ClickHouse folds constants and a literal-only measurement gives a
// different LowCardinality answer.
//
// The measured rule: the result is the common supertype of the THEN
// branches and the ELSE branch. The conditions never reach the result,
// not even a Nullable condition. A CASE without ELSE adds Nullable.
// LowCardinality is always dropped.
func TestCaseWhenType(t *testing.T) {
	schema := syntaxTestSchema(t)
	cases := []struct{ expr, want string }{
		// Searched form with ELSE: the common supertype of the branches.
		{"CASE WHEN b THEN i32 ELSE i16 END", "Int32"},
		{"CASE WHEN b THEN i32 ELSE u32 END", "Int64"},
		{"CASE WHEN b THEN dec ELSE i64 END", "Decimal(38, 4)"},
		{"CASE WHEN b THEN s ELSE fs END", "String"},
		{"CASE WHEN b THEN d ELSE dt END", "DateTime"},
		{"CASE WHEN b THEN f32 ELSE i16 END", "Float32"},
		{"CASE WHEN b THEN i32 ELSE f64 END", "Float64"},
		{"CASE WHEN b THEN arr_i ELSE arr_i END", "Array(Int32)"},
		{"CASE WHEN b THEN b ELSE u8 END", "Bool"},
		// More than one WHEN: the supertype over all branches at once.
		{"CASE WHEN b THEN i8 WHEN b THEN u32 ELSE dec END", "Decimal(18, 4)"},
		// A Nullable branch makes the result Nullable.
		{"CASE WHEN b THEN ni32 ELSE i16 END", "Nullable(Int32)"},
		// A Nullable condition does NOT reach the result.
		{"CASE WHEN ni32 = 1 THEN i32 ELSE i16 END", "Int32"},
		// No ELSE: the result adds Nullable.
		{"CASE WHEN b THEN i32 END", "Nullable(Int32)"},
		{"CASE WHEN b THEN s END", "Nullable(String)"},
		{"CASE WHEN b THEN ni32 END", "Nullable(Int32)"},
		// LowCardinality is dropped, with and without ELSE.
		{"CASE WHEN b THEN lc ELSE lc END", "String"},
		{"CASE WHEN b THEN lc ELSE s END", "String"},
		{"CASE WHEN b THEN lcn ELSE lc END", "Nullable(String)"},
		{"CASE WHEN b THEN lc END", "Nullable(String)"},
		{"CASE WHEN b THEN lcn END", "Nullable(String)"},
		// Operand form: the operand is a condition, not a value.
		{"CASE i32 WHEN 1 THEN s ELSE fs END", "String"},
		{"CASE i32 WHEN 1 THEN u8 END", "Nullable(UInt8)"},
		{"CASE s WHEN 'a' THEN i32 ELSE i16 END", "Int32"},
		// An unsigned literal narrows to Int64 exactly as multiIf does.
		{"CASE WHEN b THEN i8 ELSE 10000000000 END", "Int64"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// TestCaseWhenRefusesWithoutCommonType keeps the honest refusal when the
// branches have no common ClickHouse type. ClickHouse rejects such a
// query, so a guess here would be worse than a refusal.
func TestCaseWhenRefusesWithoutCommonType(t *testing.T) {
	schema := syntaxTestSchema(t)
	for _, expr := range []string{
		"CASE WHEN b THEN dec ELSE f64 END",
		"CASE WHEN b THEN i32 ELSE arr_i END",
	} {
		inferCHTypeError(t, schema, expr)
	}
}

// TestLambdaHigherOrderType checks the higher-order array functions.
// Measured on ClickHouse 25.8.29.51 with real array columns: the lambda
// parameter binds to the array element type with LowCardinality removed
// and Nullable kept (arrayMap(x -> toTypeName(x), arr_lc) gives
// ['String'], arrayMap(x -> toTypeName(x), arr_n) gives
// ['Nullable(Int32)']).
func TestLambdaHigherOrderType(t *testing.T) {
	schema := syntaxTestSchema(t)
	cases := []struct{ expr, want string }{
		// arrayMap gives Array(<lambda body type>).
		{"arrayMap(x -> x * 2, arr_i)", "Array(Int64)"},
		{"arrayMap(x -> toString(x), arr_i)", "Array(String)"},
		{"arrayMap(x -> x, arr_s)", "Array(String)"},
		{"arrayMap(x -> x, arr_n)", "Array(Nullable(Int32))"},
		{"arrayMap(x -> x, arr_lc)", "Array(String)"},
		{"arrayMap(x -> upper(x), arr_lc)", "Array(String)"},
		{"arrayMap(x -> x + i32, arr_i)", "Array(Int64)"},
		// A binary lambda binds each parameter to its own array.
		{"arrayMap((x, y) -> x, arr_i, arr_s)", "Array(Int32)"},
		{"arrayMap((x, y) -> y, arr_i, arr_s)", "Array(String)"},
		// arrayFilter and arraySort give the input array type back.
		{"arrayFilter(x -> x > 1, arr_i)", "Array(Int32)"},
		{"arrayFilter(x -> x > 1, arr_n)", "Array(Nullable(Int32))"},
		{"arrayFilter(x -> x != '', arr_lc)", "Array(String)"},
		{"arraySort(x -> -x, arr_i)", "Array(Int32)"},
		{"arraySort(x -> x, arr_n)", "Array(Nullable(Int32))"},
		{"arraySort(x -> x, arr_lc)", "Array(String)"},
		// The array aggregates.
		{"arraySum(x -> x * 2, arr_i)", "Int64"},
		{"arraySum(x -> x, arr_u8)", "UInt64"},
		{"arraySum(x -> x, arr_f)", "Float64"},
		{"arraySum(x -> x, arr_d)", "Decimal(38, 4)"},
		{"arrayMin(x -> x, arr_i)", "Int32"},
		{"arrayMin(x -> x, arr_u8)", "UInt8"},
		{"arrayMin(x -> x, arr_n)", "Nullable(Int32)"},
		{"arrayMax(x -> x, arr_i)", "Int32"},
		{"arrayCount(x -> x > 1, arr_i)", "UInt32"},
		{"arrayCount(x -> x > 1, arr_n)", "UInt32"},
		// ClickHouse gives UInt8 here, and so does chgen: arrayExists
		// and arrayAll are the same predicate family as the comparison
		// operators, and the family always answers UInt8.
		{"arrayExists(x -> x > 1, arr_i)", "UInt8"},
		{"arrayExists(x -> x > 1, arr_n)", "UInt8"},
		{"arrayAll(x -> x > 1, arr_i)", "UInt8"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// TestLambdaParameterStaysScoped makes sure the lambda parameter does not
// leak out of the lambda body. A leak would let a later expression read a
// name that ClickHouse does not have.
//
// The probe puts the leaked name in a second lambda and in a plain
// function argument. It does not use a comparison operator, because the
// comparison rule absorbs an untypeable operand into a bare result by
// design, which would hide the leak instead of showing it.
func TestLambdaParameterStaysScoped(t *testing.T) {
	schema := syntaxTestSchema(t)
	// "x" is bound only inside the first lambda, so the second lambda
	// must not see it.
	inferCHTypeError(t, schema, "arrayMap(y -> x, arr_i)")
	// The binding must not survive the lambda either.
	inferCHTypeError(t, schema, "toString(arrayMap(y -> x, arr_i))")
}

// TestLambdaRefusesUnsupportedShapes keeps the refusal for the shapes
// that have no measured rule.
func TestLambdaRefusesUnsupportedShapes(t *testing.T) {
	schema := syntaxTestSchema(t)
	for _, expr := range []string{
		// arraySum over a Nullable element type is an error on
		// ClickHouse itself (ILLEGAL_TYPE_OF_ARGUMENT).
		"arraySum(x -> x, arr_n)",
		// A non-array data argument has no element type.
		"arrayMap(x -> x, i32)",
		// A higher-order function with no lambda has no rule here.
		"arrayMap(arr_i)",
	} {
		inferCHTypeError(t, schema, expr)
	}
}

// TestBetweenType checks BETWEEN. Measured on ClickHouse 25.8.29.51:
// the result is UInt8, Nullable when any of the three operands is
// Nullable, and LowCardinality is dropped. The drop is the difference
// from a plain comparison, where lc = 'a' keeps LowCardinality(UInt8).
func TestBetweenType(t *testing.T) {
	schema := syntaxTestSchema(t)
	cases := []struct{ expr, want string }{
		{"i32 BETWEEN 1 AND 5", "UInt8"},
		{"i32 NOT BETWEEN 1 AND 5", "UInt8"},
		{"ni32 BETWEEN 1 AND 5", "Nullable(UInt8)"},
		{"i32 BETWEEN ni32 AND 5", "Nullable(UInt8)"},
		{"lc BETWEEN 'a' AND 'z'", "UInt8"},
		{"lcn BETWEEN 'a' AND 'z'", "Nullable(UInt8)"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// TestDateDiffType checks dateDiff. Measured on ClickHouse 25.8.29.51:
// the result is Int64 for every unit and every temporal argument pair.
func TestDateDiffType(t *testing.T) {
	schema := syntaxTestSchema(t)
	cases := []struct{ expr, want string }{
		{"dateDiff('day', d, dt)", "Int64"},
		{"dateDiff('day', d, dt64)", "Int64"},
		{"dateDiff('second', dt, dt)", "Int64"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// TestFixedStringResultType covers two rules that the CASE WHEN work
// made reachable. Both were wrong before, but no generated expression
// could show it, because the CASE around them refused first.
//
// Measured on ClickHouse 25.8.29.51 with real table columns:
//   - Concatenation of a FixedString gives String, not FixedString
//     (fs || fs and concat(fs, fs) are both String).
//   - upper and lower keep FixedString(N) (upper(fs) is FixedString(8)),
//     while trim gives String.
func TestFixedStringResultType(t *testing.T) {
	schema := syntaxTestSchema(t)
	cases := []struct{ expr, want string }{
		{"fs || fs", "String"},
		{"fs || s", "String"},
		{"fs || 'x'", "String"},
		{"concat(fs, fs)", "String"},
		{"upper(fs)", "FixedString(8)"},
		{"lower(fs)", "FixedString(8)"},
		{"upper(s)", "String"},
		{"upper(lc)", "LowCardinality(String)"},
		{"upper(ns)", "Nullable(String)"},
		{"trim(fs)", "String"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}
