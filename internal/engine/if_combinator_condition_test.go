package engine

import "testing"

// ifConditionSchema holds the columns that the -If condition grid needs.
// The table is named "probe" because inferTestExprType puts the expression
// in "SELECT <expr> FROM probe". A different name makes every case fail
// with "table probe is not present", thus every refusal would look correct
// for the wrong reason. TestIfConditionControlAccepted guards against that.
const ifConditionSchema = `
CREATE TABLE probe (
	u64 UInt64,
	u8 UInt8,
	b Bool,
	nu8 Nullable(UInt8),
	i32 Int32,
	ni32 Nullable(Int32),
	f64 Float64,
	s String,
	lcs LowCardinality(String),
	e8 Enum8('a' = 1, 'b' = 2),
	d Date,
	dt DateTime,
	dec Decimal(18, 4),
	fs FixedString(8),
	uid UUID,
	arr Array(Int32)
) ENGINE = Memory
`

func ifConditionTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, ifConditionSchema)
	if err != nil {
		t.Fatalf("parse the -If condition schema as probe: %v", err)
	}
	return schema
}

// TestIfConditionControlAccepted is the harness control. A legal UInt8
// condition must give a type. If this case fails, the harness is broken
// and every refusal in this file is meaningless.
func TestIfConditionControlAccepted(t *testing.T) {
	schema := ifConditionTestSchema(t)
	got, err := inferTestExprType(t, schema, "sumIf(u64, u8)")
	if err != nil {
		t.Fatalf("control sumIf(u64, u8): %v", err)
	}
	if got == "" {
		t.Fatal("control sumIf(u64, u8) gave an empty type")
	}
}

// TestIfConditionMustBeUInt8 pins the measured boundary of the trailing
// condition argument of the -If combinator.
//
// Measured on ClickHouse 25.8.29.51 over the REAL COLUMNS of a table, and
// not over literals, because ClickHouse folds constants. Both witnesses
// agree on every cell, the analysis witness toTypeName and a real SELECT:
//
//	sumIf(u64, u8)    UInt64          sumIf(u64, i32)   Code 43
//	sumIf(u64, b)     UInt64          sumIf(u64, u64)   Code 43
//	sumIf(u64, nu8)   UInt64          sumIf(u64, f64)   Code 43
//	                                  sumIf(u64, s)     Code 43
//	                                  sumIf(u64, lcs)   Code 43
//	                                  sumIf(u64, e8)    Code 43
//	                                  sumIf(u64, d)     Code 43
//	                                  sumIf(u64, dt)    Code 43
//	                                  sumIf(u64, dec)   Code 43
//	                                  sumIf(u64, fs)    Code 43
//	                                  sumIf(u64, uid)   Code 43
//	                                  sumIf(u64, arr)   Code 43
//	                                  sumIf(u64, ni32)  Code 43
//
// The error is "Illegal type <T> of last argument for aggregate function
// with If suffix".
//
// The rule is therefore: the BASE of the condition must be UInt8. Bool is
// an alias of UInt8 and passes. The Nullable and the LowCardinality
// wrappers are transparent: LowCardinality(UInt8),
// LowCardinality(Nullable(UInt8)) and Nullable(Bool) all pass, measured
// with allow_suspicious_low_cardinality_types.
//
// Note that Int32 is refused although it converts to UInt8 elsewhere. The
// rule is the base type and never an implicit conversion.
func TestIfConditionMustBeUInt8(t *testing.T) {
	schema := ifConditionTestSchema(t)
	accepted := []string{"u8", "b", "nu8"}
	refused := []string{
		"i32", "ni32", "u64", "f64", "s", "lcs",
		"e8", "d", "dt", "dec", "fs", "uid", "arr",
	}
	for _, condition := range accepted {
		expr := "sumIf(u64, " + condition + ")"
		if _, err := inferTestExprType(t, schema, expr); err != nil {
			t.Errorf("%s: %v, want a type because the server accepts it", expr, err)
		}
	}
	for _, condition := range refused {
		expr := "sumIf(u64, " + condition + ")"
		got, err := inferTestExprType(t, schema, expr)
		if err == nil {
			t.Errorf("%s: gave %s, want a refusal because the server gives Code 43", expr, got)
		}
	}
}

// TestIfConditionRuleCoversEveryIfForm shows that the check belongs to the
// combinator and not to one aggregate name.
//
// The condition is the trailing argument of EIGHT measured forms. Every
// one of them gives Code 43 for an Int32 condition and a type for a UInt8
// condition, measured on ClickHouse 25.8.29.51:
//
//	sumIf, sumStateIf, sumIfState, sumIfOrNull, sumIfOrDefault,
//	sumResampleIf, sumArrayIf, quantileStateIf
//
// A fix for one name only would leave the same hole in the other seven,
// which is how the second tracking item of this family came to exist. This test
// fails if a later change narrows the rule back to one name.
func TestIfConditionRuleCoversEveryIfForm(t *testing.T) {
	schema := ifConditionTestSchema(t)
	cases := []struct{ legal, illegal string }{
		{"sumIf(u64, u8)", "sumIf(u64, i32)"},
		{"sumStateIf(u64, u8)", "sumStateIf(u64, i32)"},
		{"sumIfState(u64, u8)", "sumIfState(u64, i32)"},
		{"sumIfOrNull(u64, u8)", "sumIfOrNull(u64, i32)"},
		{"sumIfOrDefault(u64, u8)", "sumIfOrDefault(u64, i32)"},
		{"sumArrayIf(arr, u8)", "sumArrayIf(arr, i32)"},
		{"quantileStateIf(0.5)(u64, u8)", "quantileStateIf(0.5)(u64, i32)"},
	}
	for _, testCase := range cases {
		if _, err := inferTestExprType(t, schema, testCase.legal); err != nil {
			t.Errorf("%s: %v, want a type because the server accepts it", testCase.legal, err)
		}
		got, err := inferTestExprType(t, schema, testCase.illegal)
		if err == nil {
			t.Errorf("%s: gave %s, want a refusal because the server gives Code 43",
				testCase.illegal, got)
		}
	}
}
