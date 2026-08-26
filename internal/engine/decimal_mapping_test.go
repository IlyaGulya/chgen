package engine

import (
	"strings"
	"testing"
)

// These tests pin the Go mapping of ClickHouse Decimal columns. The
// clickhouse-go v2.47.0 driver refuses to scan a Decimal column into
// *float64 ("converting Decimal to *float64 is unsupported"), so a
// float mapping makes every Decimal column unreadable. The supported
// scan and bind target is decimal.Decimal from
// github.com/shopspring/decimal, which the driver itself depends on.
// The behavior was measured against ClickHouse 25.8.29.51 with driver
// v2.47.0: decimal.Decimal round-trips bare, Nullable, Array,
// Array(Nullable) and Map(K, Decimal) shapes with exact values for
// Decimal(10,2), Decimal32/64/128/256, negatives, zero and the full
// precision of each storage class.
func decimalTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE money
(
    a Decimal(10, 2),
    b Decimal32(4),
    c Decimal64(10),
    d Decimal128(20),
    e Decimal256(40),
    n Nullable(Decimal(10, 2)),
    arr Array(Decimal(10, 2)),
    arrn Array(Nullable(Decimal64(10))),
    m Map(String, Decimal(10, 2))
) ENGINE = MergeTree ORDER BY a;`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

func TestDecimalColumnsMapToDecimalDecimal(t *testing.T) {
	schema := decimalTestSchema(t)
	query := Query{
		Name:    "ReadMoney",
		Command: CommandMany,
		SQL:     "SELECT a, b, c, d, e, n, arr, arrn, m FROM money",
	}
	if err := resolveQuery(&query, schema); err != nil {
		t.Fatalf("resolveQuery() error = %v", err)
	}
	want := []string{
		"decimal.Decimal",
		"decimal.Decimal",
		"decimal.Decimal",
		"decimal.Decimal",
		"decimal.Decimal",
		"*decimal.Decimal",
		"[]decimal.Decimal",
		"[]*decimal.Decimal",
		"map[string]decimal.Decimal",
	}
	if len(query.Results) != len(want) {
		t.Fatalf("results = %d, want %d", len(query.Results), len(want))
	}
	for index, wantType := range want {
		if got := query.Results[index].GoType; got != wantType {
			t.Errorf("result[%d] GoType = %q, want %q", index, got, wantType)
		}
	}
}

// median and quantile over a Decimal column return the input Decimal
// type unchanged. Measured on 25.8.29.51 with real columns:
// toTypeName(median(Decimal(10,2))) = Decimal(10, 2),
// toTypeName(quantile(0.5)(Decimal64(10))) = Decimal(18, 10),
// toTypeName(quantile(0.9)(Decimal128(20))) = Decimal(38, 20),
// toTypeName(median(Decimal256(40))) = Decimal(76, 40),
// toTypeName(median(Decimal32(4))) = Decimal(9, 4).
func TestMedianOverDecimalKeepsDecimalType(t *testing.T) {
	schema := decimalTestSchema(t)
	cases := []struct {
		name string
		expr string
		want string
	}{
		{"median_decimal", "median(a)", "decimal.Decimal"},
		{"quantile_decimal64", "quantile(0.5)(c)", "decimal.Decimal"},
		{"quantile_decimal128", "quantile(0.9)(d)", "decimal.Decimal"},
		{"median_decimal256", "median(e)", "decimal.Decimal"},
		{"median_nullable_decimal", "median(n)", "*decimal.Decimal"},
		// A float or integer argument still gives Float64.
		{"median_int_stays_float", "median(toInt32(1))", "float64"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			query := Query{
				Name:    "Agg",
				Command: CommandOne,
				SQL:     "SELECT " + testCase.expr + " AS v FROM money",
			}
			if err := resolveQuery(&query, schema); err != nil {
				t.Fatalf("resolveQuery() error = %v", err)
			}
			if got := query.Results[0].GoType; got != testCase.want {
				t.Fatalf("Go type for %q = %q, want %q", testCase.expr, got, testCase.want)
			}
		})
	}
}

func TestGeneratedCodeImportsShopspringDecimal(t *testing.T) {
	schema := decimalTestSchema(t)
	queries, err := parseQueriesWithSchema(t, `-- name: ReadAmounts :many
SELECT a FROM money;`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(string(generated), `"github.com/shopspring/decimal"`) {
		t.Fatalf("generated code does not import shopspring/decimal:\n%s", generated)
	}
}

func TestGeneratedCodeWithoutDecimalOmitsImport(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE plain (v UInt32) ENGINE = MergeTree ORDER BY v;`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadPlain :many
SELECT v FROM plain;`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if strings.Contains(string(generated), "shopspring") {
		t.Fatalf("generated code imports shopspring/decimal without a Decimal column")
	}
}

func TestResultOverrideAcceptsDecimalDecimal(t *testing.T) {
	if err := validateGoType("decimal.Decimal"); err != nil {
		t.Fatalf("validateGoType(decimal.Decimal) error = %v", err)
	}
	if err := validateGoType("*decimal.Decimal"); err != nil {
		t.Fatalf("validateGoType(*decimal.Decimal) error = %v", err)
	}
	if err := validateGoType("[]decimal.Decimal"); err != nil {
		t.Fatalf("validateGoType([]decimal.Decimal) error = %v", err)
	}
}
