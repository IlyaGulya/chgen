package queries

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func TestNativeParametersAndUnsupportedGrammar(t *testing.T) {
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{os.Getenv("CHGEN_CONTRACT_NATIVE")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := New(conn)
	setRows, err := q.ParenthesizedSet(t.Context(), ParenthesizedSetParams{Start: 0})
	if err != nil {
		t.Fatal(err)
	}
	var setValues []uint32
	for _, row := range setRows {
		setValues = append(setValues, row.Value)
	}
	slices.Sort(setValues)
	if !slices.Equal(setValues, []uint32{0, 2}) {
		t.Fatalf("native parameter in parenthesized set: %v", setValues)
	}
	for _, example := range []struct {
		left, right string
		want        float64
	}{{"abc", "abc", 1}, {"abc", "xyz", 0}} {
		row, err := q.Comparison(t.Context(), ComparisonParams{Left: example.left, Right: example.right})
		if err != nil || row.Similarity != example.want {
			t.Fatalf("server-owned function inference: %+v, %v", row, err)
		}
	}
	formatted, err := q.Formatting(t.Context(), FormattingParams{})
	if err != nil || formatted.Label != "value:0" {
		t.Fatalf("keyword-shaped function and schema: %+v, %v", formatted, err)
	}
	scalars, err := q.Scalars(t.Context(), ScalarsParams{
		B: true, I8: -128, I16: -32768, I32: -2147483648, I64: -9223372036854775808,
		U8: 255, U16: 65535, U32: 4294967295, U64: 18446744073709551615, F32: 1.25, F64: -2.5,
	})
	if err != nil || scalars != (ScalarsRow{
		B: true, I8: -128, I16: -32768, I32: -2147483648, I64: -9223372036854775808,
		U8: 255, U16: 65535, U32: 4294967295, U64: 18446744073709551615, F32: 1.25, F64: -2.5,
	}) {
		t.Fatalf("native scalar bounds: %+v, %v", scalars, err)
	}
	const text = "quote '\" slash \\ tab\t newline\n null\x00 backspace\b formfeed\f carriage\r ; DROP TABLE never"
	ctx := clickhouse.Context(t.Context(), clickhouse.WithParameters(clickhouse.Parameters{"Limit": "1", "Text": "stale"}))
	rows, err := q.Read(ctx, ReadParams{Limit: 4, Text: text})
	if err != nil || len(rows) != 3 {
		t.Fatalf("native read: %+v, %v", rows, err)
	}
	for i, row := range rows {
		if row.Number != uint64(i+1) || row.Text != text {
			t.Fatalf("parameter value changed: %+v", rows)
		}
	}
	if _, err := q.Empty(t.Context(), EmptyParams{}); !errors.Is(err, ErrNoRows) {
		t.Fatalf("empty result: %v", err)
	}
	row, err := q.Sum(t.Context(), SumParams{})
	if err != nil || row.SumNumber != 3 {
		t.Fatalf("unaliased aggregate: %+v, %v", row, err)
	}
	recursive, err := q.Recursive(t.Context(), RecursiveParams{})
	if err != nil || len(recursive) != 3 || recursive[0].N != 1 || recursive[2].N != 3 {
		t.Fatalf("recursive CTE: %+v, %v", recursive, err)
	}
	if _, err := q.Drift(t.Context(), DriftParams{Width: 3}); err == nil || !strings.Contains(err.Error(), "result contract") {
		t.Fatalf("parameter-dependent type drift escaped: %v", err)
	}
	if _, err := q.EmptyDrift(t.Context(), EmptyDriftParams{Width: 3}); err == nil || !strings.Contains(err.Error(), "result contract") {
		t.Fatalf("empty type drift escaped: %v", err)
	}
}
