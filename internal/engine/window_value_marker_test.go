package engine

import "testing"

// TestWindowValueFunctionsKeepNullableInnerMarker pins the cells that the
// expanded wrapper grid found. These functions give a source value back.
// They keep SimpleAggregateFunction around a Nullable scalar inner type.
// The wrapper-grid analysis and execution lanes measured every case on
// ClickHouse 25.8.29.51.
func TestWindowValueFunctionsKeepNullableInnerMarker(t *testing.T) {
	schema, err := schemaFromDDLErr(t, valuePreservingScalarMarkerSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{
		"lag(saggn) OVER ()",
		"lead(saggn) OVER ()",
		"nth_value(saggn, 2) OVER ()",
		"first_value_respect_nulls(saggn) OVER ()",
		"firstValueRespectNulls(saggn) OVER ()",
		"last_value_respect_nulls(saggn) OVER ()",
		"lastValueRespectNulls(saggn) OVER ()",
	} {
		got, inferErr := inferSelectItemCHType(t, schema, "SELECT "+expression+" AS a FROM t")
		if inferErr != nil {
			t.Errorf("%s: %v", expression, inferErr)
			continue
		}
		if want := "SimpleAggregateFunction(sum, Nullable(Int64))"; got != want {
			t.Errorf("%s = %s, want %s", expression, got, want)
		}
	}
}

// TestPlainValueWindowsStillReadThroughNullableInnerMarker pins the close
// neighbours. first_value and last_value do not use the value-preserving
// transport and must not gain it with the RESPECT NULLS names.
func TestPlainValueWindowsStillReadThroughNullableInnerMarker(t *testing.T) {
	schema, err := schemaFromDDLErr(t, valuePreservingScalarMarkerSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{
		"first_value(saggn) OVER ()",
		"last_value(saggn) OVER ()",
	} {
		got, inferErr := inferSelectItemCHType(t, schema, "SELECT "+expression+" AS a FROM t")
		if inferErr != nil {
			t.Errorf("%s: %v", expression, inferErr)
			continue
		}
		if want := "Nullable(Int64)"; got != want {
			t.Errorf("%s = %s, want %s", expression, got, want)
		}
	}
}
