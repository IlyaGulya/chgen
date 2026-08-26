package engine

import (
	"strings"
	"testing"
)

// The expected types in this file were measured on ClickHouse 25.8.29.51
// with real table columns, read through
// DESCRIBE (SELECT <expr> AS x FROM t). Constant folding makes a literal
// measurement lie, thus every case uses a column.
func arrayArithmeticTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE arrs
(
    i16 Int16,
    i32 Int32,
    i64 Int64,
    u64 UInt64,
    f64 Float64,
    dec Decimal(18, 4),
    s String,
    d Date,
    arr_i Array(Int64),
    arr_i32 Array(Int32),
    arr_u Array(UInt8),
    arr_u64 Array(UInt64),
    arr_f Array(Float64),
    arr_f32 Array(Float32),
    arr_s Array(String),
    arr_dec Array(Decimal(18, 4)),
    arr_nu Array(Nullable(Int64)),
    arr_arr Array(Array(Int64)),
    ni32 Nullable(Int32),
    ni64 Nullable(Int64),
    nu64 Nullable(UInt64),
    nf64 Nullable(Float64)
) ENGINE = MergeTree ORDER BY i64;`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

func arrayArithmeticGoType(t *testing.T, schema *Schema, expr string) (string, error) {
	t.Helper()
	query := Query{
		Name:    "ArrArith",
		Command: CommandMany,
		SQL:     "SELECT " + expr + " AS v FROM arrs",
	}
	if err := resolveQuery(&query, schema); err != nil {
		return "", err
	}
	if len(query.Results) != 1 {
		t.Fatalf("results = %#v, want one result", query.Results)
	}
	return query.Results[0].GoType, nil
}

// An Array operand with a scalar operand under "*", "/" and "%" applies
// the ordinary scalar rule to the element type and keeps the Array
// wrapper. Both operand orders behave the same way.
func TestArrayScalarArithmeticSpreadsOverElements(t *testing.T) {
	schema := arrayArithmeticTestSchema(t)
	cases := []struct {
		name string
		expr string
		want string
	}{
		// The five cases that the oracle reported as false refusals.
		{"arr_i_mul_f64", "arr_i * f64", "[]float64"},
		{"f64_div_arr_u", "f64 / arr_u", "[]float64"},
		{"arr_f_mod_f64", "arr_f % f64", "[]float64"},
		{"u64_mul_arr_i", "u64 * arr_i", "[]int64"},
		{"arr_i_mod_i64", "arr_i % i64", "[]int64"},
		// Both operand orders agree for every one of the three
		// operators.
		{"arr_i_mul_i64", "arr_i * i64", "[]int64"},
		{"i64_mul_arr_i", "i64 * arr_i", "[]int64"},
		{"arr_i_div_f64", "arr_i / f64", "[]float64"},
		{"f64_div_arr_i", "f64 / arr_i", "[]float64"},
		{"f64_mod_arr_i", "f64 % arr_i", "[]float64"},
		{"i64_mod_arr_i", "i64 % arr_i", "[]int64"},
		// Division of an integer element always gives Float64, which
		// is the ordinary scalar rule seen through the wrapper.
		{"arr_i_div_i64", "arr_i / i64", "[]float64"},
		{"i64_div_arr_i", "i64 / arr_i", "[]float64"},
		// The element rule covers Decimal exactly as it does for a
		// scalar: an integer keeps the Decimal, a float gives Float64.
		{"arr_i_mul_dec", "arr_i * dec", "[]decimal.Decimal"},
		{"arr_dec_mul_i64", "arr_dec * i64", "[]decimal.Decimal"},
		{"arr_dec_mul_f64", "arr_dec * f64", "[]float64"},
		// A nested Array recurses: the rule unwraps both levels.
		{"arr_arr_mul_i64", "arr_arr * i64", "[][]int64"},
		{"arr_arr_mul_f64", "arr_arr * f64", "[][]float64"},
		{"arr_arr_div_i64", "arr_arr / i64", "[][]float64"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := arrayArithmeticGoType(t, schema, testCase.expr)
			if err != nil {
				t.Fatalf("resolveQuery(%q) error = %v", testCase.expr, err)
			}
			if got != testCase.want {
				t.Fatalf("Go type for %q = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}
}

// Two Array operands are elementwise vector arithmetic for "+" and "-"
// only. The element types take the ordinary common type.
func TestArrayArrayAdditionAndSubtraction(t *testing.T) {
	schema := arrayArithmeticTestSchema(t)
	cases := []struct {
		name string
		expr string
		want string
	}{
		{"arr_i_add_arr_i", "arr_i + arr_i", "[]int64"},
		{"arr_i_sub_arr_i", "arr_i - arr_i", "[]int64"},
		{"arr_i_add_arr_i32", "arr_i + arr_i32", "[]int64"},
		{"arr_i_sub_arr_i32", "arr_i - arr_i32", "[]int64"},
		{"arr_i_add_arr_f", "arr_i + arr_f", "[]float64"},
		{"arr_i_add_arr_f32", "arr_i + arr_f32", "[]float64"},
		// Mixed signedness is allowed here, unlike the branch
		// supertype rule: the server answers Array(Int64).
		{"arr_i_add_arr_u64", "arr_i + arr_u64", "[]int64"},
		{"arr_u64_add_arr_i", "arr_u64 + arr_i", "[]int64"},
		{"arr_i_sub_arr_u64", "arr_i - arr_u64", "[]int64"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := arrayArithmeticGoType(t, schema, testCase.expr)
			if err != nil {
				t.Fatalf("resolveQuery(%q) error = %v", testCase.expr, err)
			}
			if got != testCase.want {
				t.Fatalf("Go type for %q = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}
}

// These combinations are refused by the server with code 43. The
// refusals are correct and must stay refusals: a later change that turns
// one of them into an answer would give a Go type that the server never
// produces.
func TestArrayArithmeticKeepsCorrectRefusals(t *testing.T) {
	schema := arrayArithmeticTestSchema(t)
	cases := []struct {
		name string
		expr string
	}{
		// "+" and "-" have no Array-with-scalar form at all. They are
		// reserved for the elementwise two-Array form, thus a scalar
		// operand is code 43 in both orders.
		{"arr_i_add_i64", "arr_i + i64"},
		{"arr_i_sub_i64", "arr_i - i64"},
		{"arr_i_add_f64", "arr_i + f64"},
		{"i64_sub_arr_i", "i64 - arr_i"},
		{"f64_sub_arr_i", "f64 - arr_i"},
		// "*", "/" and "%" have no two-Array form. Only "+" and "-"
		// are elementwise between two Arrays.
		{"arr_i_mul_arr_i", "arr_i * arr_i"},
		{"arr_i_div_arr_i", "arr_i / arr_i"},
		{"arr_i_mod_arr_i", "arr_i % arr_i"},
		{"arr_i_mul_arr_f", "arr_i * arr_f"},
		{"arr_i_mul_arr_u", "arr_i * arr_u"},
		{"arr_arr_mul_arr_i", "arr_arr * arr_i"},
		// A String element has no arithmetic rule, inside an Array as
		// much as outside one.
		{"arr_i_mul_s", "arr_i * s"},
		{"arr_s_mul_i64", "arr_s * i64"},
		{"arr_s_add_arr_s", "arr_s + arr_s"},
		// A Nullable scalar operand against an Array is code 43, and
		// so is an Array of a Nullable element. The server has no
		// rule that mixes the two wrappers.
		{"arr_i_mul_ni64", "arr_i * ni64"},
		{"arr_i_mul_nf64", "arr_i * nf64"},
		{"arr_nu_mul_i64", "arr_nu * i64"},
		{"arr_nu_mul_f64", "arr_nu * f64"},
		// A date operand has no Array rule.
		{"arr_i_mul_d", "arr_i * d"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := arrayArithmeticGoType(t, schema, testCase.expr)
			if err == nil {
				t.Fatalf("resolveQuery(%q) = %q, want a refusal", testCase.expr, got)
			}
		})
	}
}

// coalesce does not take the common type of every argument. A
// non-Nullable argument can never be NULL, thus every argument after it
// is unreachable. The server truncates the list there and takes the
// ordinary common type of that prefix, stripped of Nullable. The result
// is non-Nullable exactly when such a terminator exists.
func TestCoalesceShortCircuitsOnFirstNonNullableArgument(t *testing.T) {
	schema := arrayArithmeticTestSchema(t)
	cases := []struct {
		name string
		expr string
		want string
	}{
		// A non-Nullable first argument makes the result its own type,
		// however incompatible the later arguments are. Int64 with
		// Float64 and Int32 with UInt64 have no common type at all,
		// and coalesce still answers.
		{"u64_i32", "coalesce(u64, i32)", "uint64"},
		{"i32_u64", "coalesce(i32, u64)", "int32"},
		{"u64_f64", "coalesce(u64, f64)", "uint64"},
		{"i64_f64", "coalesce(i64, f64)", "int64"},
		{"u64_ni32", "coalesce(u64, ni32)", "uint64"},
		{"i64_nf64", "coalesce(i64, nf64)", "int64"},
		{"u64_nu64", "coalesce(u64, nu64)", "uint64"},
		// Three or more arguments truncate at the same place: every
		// argument after the first non-Nullable one is ignored.
		{"u64_i32_f64", "coalesce(u64, i32, f64)", "uint64"},
		{"u64_f64_i64_s", "coalesce(u64, f64, i64, s)", "uint64"},
		// A Nullable prefix is a real common-type search over the
		// prefix and the terminator together. The terminator makes the
		// result non-Nullable.
		{"ni64_i64", "coalesce(ni64, i64)", "int64"},
		{"ni32_i64", "coalesce(ni32, i64)", "int64"},
		{"ni64_i32", "coalesce(ni64, i32)", "int64"},
		{"nu64_u64", "coalesce(nu64, u64)", "uint64"},
		{"ni64_ni32_i64", "coalesce(ni64, ni32, i64)", "int64"},
		{"ni32_ni32_i16", "coalesce(ni32, ni32, i16)", "int32"},
		// Arguments after the terminator stay ignored even when the
		// prefix search was a real one.
		{"ni64_ni32_i64_f64", "coalesce(ni64, ni32, i64, f64)", "int64"},
		{"ni64_ni32_i64_u64", "coalesce(ni64, ni32, i64, u64)", "int64"},
		// With no terminator at all the result keeps Nullable.
		{"ni64_alone", "coalesce(ni64)", "*int64"},
		{"i64_alone", "coalesce(i64)", "int64"},
		{"ni64_ni64", "coalesce(ni64, ni64)", "*int64"},
		{"ni32_ni64", "coalesce(ni32, ni64)", "*int64"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := arrayArithmeticGoType(t, schema, testCase.expr)
			if err != nil {
				t.Fatalf("resolveQuery(%q) error = %v", testCase.expr, err)
			}
			if got != testCase.want {
				t.Fatalf("Go type for %q = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}
}

// When every argument up to and including the terminator is Nullable
// with no terminator present, coalesce runs the honest common-type
// search. It then refuses the same pairs that if() refuses, with code
// 386 NO_COMMON_TYPE. These refusals are correct.
func TestCoalesceKeepsCorrectRefusalsWhenEveryArgumentIsNullable(t *testing.T) {
	schema := arrayArithmeticTestSchema(t)
	cases := []struct {
		name string
		expr string
	}{
		{"nu64_ni32", "coalesce(nu64, ni32)"},
		{"nu64_nf64", "coalesce(nu64, nf64)"},
		// A Nullable first argument does not short-circuit: the
		// terminator is included in the search and can make it fail.
		{"nu64_i32", "coalesce(nu64, i32)"},
		{"ni32_u64", "coalesce(ni32, u64)"},
		{"ni64_f64", "coalesce(ni64, f64)"},
		{"ni64_u64_f64", "coalesce(ni64, u64, f64)"},
		{"ni64_nu64_i32", "coalesce(ni64, nu64, i32)"},
		{"ni64_nu64_nf64", "coalesce(ni64, nu64, nf64)"},
		{"nu64_ni32_ni64", "coalesce(nu64, ni32, ni64)"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := arrayArithmeticGoType(t, schema, testCase.expr)
			if err == nil {
				t.Fatalf("resolveQuery(%q) = %q, want a refusal", testCase.expr, got)
			}
			if !strings.Contains(err.Error(), "common type") {
				t.Logf("refusal for %q: %v", testCase.expr, err)
			}
		})
	}
}
