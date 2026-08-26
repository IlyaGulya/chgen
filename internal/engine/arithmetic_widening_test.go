package engine

import (
	"strings"
	"testing"
)

// These tests pin the ClickHouse result types of the binary arithmetic
// operators that the resolver sees as *clickhouse.BinaryOperation:
// "+", "-", "*", "/" and "%". The expected types were measured on
// ClickHouse 25.8.16 with SELECT toTypeName(<expression>) and they agree
// with the documented common-type rules:
// https://clickhouse.com/docs/en/sql-reference/functions/arithmetic-functions
// and src/DataTypes/NumberTraits.h in the ClickHouse source.
func arithmeticTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE nums
(
    u8 UInt8,
    u16 UInt16,
    u32 UInt32,
    u64 UInt64,
    i8 Int8,
    i32 Int32,
    i64 Int64,
    f32 Float32,
    f64 Float64,
    nu64 Nullable(UInt64),
    d92 Decimal(9, 2),
    s String,
    ts DateTime
) ENGINE = MergeTree ORDER BY u8;`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

func TestArithmeticWidening(t *testing.T) {
	schema := arithmeticTestSchema(t)
	cases := []struct {
		name string
		expr string
		want string
	}{
		// Subtraction is always signed. This is the silent-corruption
		// case: a negative difference scanned into uint64 becomes a
		// large positive number without any driver error.
		{"u64_sub_u64", "u64 - u64", "int64"},
		{"u8_sub_u8", "u8 - u8", "int16"},
		// Addition and multiplication double the size below 64 bits
		// and keep the size at 64 bits.
		{"u8_add_u8", "u8 + u8", "uint16"},
		{"u16_add_u16", "u16 + u16", "uint32"},
		{"u32_mul_u32", "u32 * u32", "uint64"},
		{"u64_add_u64", "u64 + u64", "uint64"},
		{"u8_mul_u8", "u8 * u8", "uint16"},
		// One signed operand makes the result signed.
		{"u8_add_i8", "u8 + i8", "int16"},
		{"u64_add_i64", "u64 + i64", "int64"},
		// An integer literal takes the smallest type that holds its
		// value: 2 is UInt8, 300 is UInt16, -1 is Int8.
		{"u64_mul_lit", "u64 * 2", "uint64"},
		{"u8_add_lit1", "u8 + 1", "uint16"},
		{"u8_add_lit300", "u8 + 300", "uint32"},
		{"u8_add_neg1", "u8 + -1", "int16"},
		// Division always returns Float64 for integer operands.
		{"u64_div_lit", "u64 / 2", "float64"},
		{"u64_div_u64", "u64 / u64", "float64"},
		// A float operand makes the result Float64.
		{"f32_add_u8", "f32 + u8", "float64"},
		{"f32_add_f32", "f32 + f32", "float64"},
		{"f64_mul_u64", "f64 * u64", "float64"},
		{"f32_mod_u8", "f32 % u8", "float64"},
		// Modulo takes its sign from the dividend only. An unsigned
		// dividend keeps the divisor size; a signed dividend doubles
		// the divisor size below 64 bits.
		{"u8_mod_u8", "u8 % u8", "uint8"},
		{"u16_mod_u8", "u16 % u8", "uint8"},
		{"u8_mod_u16", "u8 % u16", "uint16"},
		{"u64_mod_i8", "u64 % i8", "uint8"},
		{"i32_mod_i8", "i32 % i8", "int16"},
		{"i8_mod_u64", "i8 % u64", "int64"},
		{"u8_mod_lit", "u8 % 2", "uint8"},
		// A Nullable operand keeps the result Nullable.
		{"null_sub", "nu64 - u64", "*int64"},
		{"null_add_lit", "nu64 + 1", "*uint64"},
		{"null_div", "nu64 / 2", "*float64"},
		// Decimal addition and subtraction with identical types keep
		// the type. Decimal maps to decimal.Decimal in Go.
		{"dec_add_dec", "d92 + d92", "decimal.Decimal"},
		{"dec_sub_dec", "d92 - d92", "decimal.Decimal"},
		// Decimal arithmetic follows the measured 25.8.29.51 rules:
		// an integer operand keeps the Decimal type, another Decimal
		// keeps the storage class, a float gives Float64. This test
		// pinned refusals before; the rules are measured now.
		{"dec_mul_dec", "d92 * d92", "decimal.Decimal"},
		{"dec_div_dec", "d92 / d92", "decimal.Decimal"},
		{"dec_add_int", "d92 + u8", "decimal.Decimal"},
		{"dec_add_float", "d92 + f64", "float64"},
		// DateTime plus or minus an integer stays DateTime.
		{"ts_add_int", "ts + 3600", "time.Time"},
		{"ts_sub_int", "ts - 3600", "time.Time"},
		{"int_add_ts", "3600 + ts", "time.Time"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			query := Query{
				Name:    "Arith",
				Command: CommandMany,
				SQL:     "SELECT " + testCase.expr + " AS v FROM nums",
			}
			if err := resolveQuery(&query, schema); err != nil {
				t.Fatalf("resolveQuery() error = %v", err)
			}
			if len(query.Results) != 1 {
				t.Fatalf("results = %#v, want one result", query.Results)
			}
			if got := query.Results[0].GoType; got != testCase.want {
				t.Fatalf("Go type for %q = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}
}

// Combinations without an established rule must fail loudly. A silent
// wrong number is worse than a generation error.
func TestArithmeticRejectsUnsupportedOperands(t *testing.T) {
	schema := arithmeticTestSchema(t)
	cases := []struct {
		name string
		expr string
	}{
		// A string operand has no arithmetic result type.
		{"string_add", "s + u8"},
		// DateTime multiplication has no result type.
		{"ts_mul_int", "ts * 2"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			query := Query{
				Name:    "Arith",
				Command: CommandMany,
				SQL:     "SELECT " + testCase.expr + " AS v FROM nums",
			}
			err := resolveQuery(&query, schema)
			if err == nil {
				t.Fatalf("resolveQuery() succeeded with type %q, want an explicit error", query.Results[0].GoType)
			}
			if !strings.Contains(err.Error(), "cannot infer result type") {
				t.Fatalf("error = %v, want a cannot-infer error", err)
			}
		})
	}
}
