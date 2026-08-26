package engine

import "testing"

// decimalDateRefusalSchema holds one Decimal column of each measured
// width, plus the neighbour columns that the boundary check needs: an
// integer, a float, a String, a FixedString and a DateTime, so the test
// can confirm the rule stayed narrow.
const decimalDateRefusalSchema = `
CREATE TABLE probe (
    k UInt8,
    dec Decimal(18, 4),
    d32 Decimal32(4),
    d64 Decimal64(4),
    d128 Decimal128(4),
    d256 Decimal256(4),
    i32 Int32,
    f64 Float64,
    s String,
    fs FixedString(8),
    dt DateTime
) ENGINE = MergeTree ORDER BY k
`

func decimalDateRefusalTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, decimalDateRefusalSchema)
	if err != nil {
		t.Fatalf("parse probe schema: %v", err)
	}
	return schema
}

// TestDateFromValueRefusesADecimalArgument covers the regression.
//
// Measured on ClickHouse 25.8.29.51 with DESCRIBE over an empty table
// and confirmed by a VALUE select over a one-row table (the failure is
// ILLEGAL_COLUMN, not a value-parse error, so DESCRIBE is already a
// valid witness):
//
//	toDate(dec)        Code: 44   Illegal column ... of first
//	                              argument of function toDate
//	toDate32(dec)      Code: 44
//	toDateTime(dec)    Code: 44
//
// All five Decimal widths give the same refusal, thus this is one fact
// about the Decimal family and every width appears here as its own
// case, not as a single representative.
func TestDateFromValueRefusesADecimalArgument(t *testing.T) {
	schema := decimalDateRefusalTestSchema(t)
	refused := []string{
		"toDate(dec)",
		"toDate(d32)",
		"toDate(d64)",
		"toDate(d128)",
		"toDate(d256)",
		"toDate32(dec)",
		"toDate32(d32)",
		"toDate32(d64)",
		"toDate32(d128)",
		"toDate32(d256)",
		"toDateTime(dec)",
		"toDateTime(d32)",
		"toDateTime(d64)",
		"toDateTime(d128)",
		"toDateTime(d256)",
	}
	for _, exprSQL := range refused {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err == nil {
			t.Errorf("%s: chgen answered %s, but the server refuses the call with Code: 44", exprSQL, inferred)
		}
	}
}

// TestDateFromValueAcceptsTheNeighboursOfDecimal guards the boundary
// from the other side. A refusal wider than the server's would break a
// query that runs, and this is what proves the rule did not become too
// wide.
//
// toDateTime64 is the sharpest neighbour: toDateTime64(dec, 3) RUNS and
// returns a value on ClickHouse 25.8.29.51, although it shares the
// scalar-argument family with toDate, toDate32 and toDateTime. It must
// therefore keep its type for a Decimal argument.
func TestDateFromValueAcceptsTheNeighboursOfDecimal(t *testing.T) {
	schema := decimalDateRefusalTestSchema(t)
	accepted := map[string]string{
		"toDate(i32)":           "Date",
		"toDate(f64)":           "Date",
		"toDate(s)":             "Date",
		"toDate(fs)":            "Date",
		"toDate32(i32)":         "Date32",
		"toDateTime(i32)":       "DateTime",
		"toDateTime64(dec, 3)":  "DateTime64(3)",
		"toDateTime64(d32, 3)":  "DateTime64(3)",
		"toDateTime64(d256, 3)": "DateTime64(3)",
		"toDateOrZero(s)":       "Date",
	}
	for exprSQL, want := range accepted {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err != nil {
			t.Errorf("%s: chgen refused a call that the server runs: %v", exprSQL, err)
			continue
		}
		if inferred != want {
			t.Errorf("%s: chgen answered %s, want %s", exprSQL, inferred, want)
		}
	}
}

// TestOrZeroAndOrNullAlreadyRefuseADecimalArgument confirms that the
// *OrZero and *OrNull spellings of toDate, toDate32 and toDateTime need
// no change for this ticket. They already refuse every Decimal width
// through castTextArgumentDomain, which accepts String and FixedString
// only.
//
// Measured on ClickHouse 25.8.29.51 with a VALUE select:
// toDateOrZero(dec) is Code: 43, "Conversion functions with postfix
// 'OrZero' or 'OrNull' should take String argument".
func TestOrZeroAndOrNullAlreadyRefuseADecimalArgument(t *testing.T) {
	schema := decimalDateRefusalTestSchema(t)
	refused := []string{
		"toDateOrZero(dec)",
		"toDateOrNull(dec)",
		"toDate32OrZero(dec)",
		"toDate32OrNull(dec)",
		"toDateTimeOrZero(dec)",
		"toDateTimeOrNull(dec)",
	}
	for _, exprSQL := range refused {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err == nil {
			t.Errorf("%s: chgen answered %s, but the server refuses the call with Code: 43", exprSQL, inferred)
		}
	}
}
