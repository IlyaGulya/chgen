package engine

import "testing"

// TestGroupUniqArrayDropsDateTimeTimezone pins the regression grid: the
// groupArray family and the groupUniqArray family agree on every leaf
// except a bare DateTime with a timezone.
//
// Measured on ClickHouse 25.8.29.51 with DESCRIBE (SELECT expr FROM t)
// over an empty table (dtz DateTime('UTC'), dtz64 DateTime64(3,'UTC'),
// dec Decimal(18, 4)):
//
//	groupArray(dtz)                    Array(DateTime('UTC'))
//	groupArrayIf(dtz, u8 = 5)          Array(DateTime('UTC'))
//	groupUniqArray(dtz)                Array(DateTime)          -- timezone LOST
//	groupUniqArrayIf(dtz, u8 = 5)      Array(DateTime)          -- timezone LOST
//	groupArray(dtz64)                  Array(DateTime64(3, 'UTC'))
//	groupArrayIf(dtz64, u8 = 5)        Array(DateTime64(3, 'UTC'))
//	groupUniqArray(dtz64)              Array(DateTime64(3, 'UTC'))
//	groupUniqArrayIf(dtz64, u8 = 5)    Array(DateTime64(3, 'UTC'))
//	groupUniqArray(nullIf(dtz, dtz))   Array(DateTime)          -- wrapper does not hide the defect
//	groupUniqArray(dec)                Array(Decimal(18, 4))    -- parametric non-temporal leaf keeps params
//
// The cause is groupUniqArray itself (AggregateFunctionGroupUniqArray.cpp
// builds a fresh DataTypeDateTime instead of propagating the argument
// type), and NOT the If combinator, and NOT a name-shaped special case in
// chgen: see groupUniqArrayFunctionResult in supertype.go.
func TestGroupUniqArrayDropsDateTimeTimezone(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    u8    UInt8,
    dtz   DateTime('UTC'),
    dtz64 DateTime64(3, 'UTC'),
    dec   Decimal(18, 4)
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}

	cases := []struct{ expr, want string }{
		// groupArray family: every parameter of the leaf survives.
		{"groupArray(dtz)", "Array(DateTime('UTC'))"},
		{"groupArrayIf(dtz, u8 = 5)", "Array(DateTime('UTC'))"},
		{"groupArray(dtz64)", "Array(DateTime64(3, 'UTC'))"},
		{"groupArrayIf(dtz64, u8 = 5)", "Array(DateTime64(3, 'UTC'))"},

		// groupUniqArray family: a bare DateTime leaf loses the
		// timezone; a DateTime64 leaf keeps every parameter.
		{"groupUniqArray(dtz)", "Array(DateTime)"},
		{"groupUniqArrayIf(dtz, u8 = 5)", "Array(DateTime)"},
		{"groupUniqArray(dtz64)", "Array(DateTime64(3, 'UTC'))"},
		{"groupUniqArrayIf(dtz64, u8 = 5)", "Array(DateTime64(3, 'UTC'))"},

		// The defect survives a wrapper: the leaf check must run
		// after Nullable/LowCardinality come off, not on the top
		// node.
		{"groupUniqArray(nullIf(dtz, dtz))", "Array(DateTime)"},

		// A parametric non-temporal leaf is unaffected and keeps its
		// parameters.
		{"groupUniqArray(dec)", "Array(Decimal(18, 4))"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}
