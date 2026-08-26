package engine

import (
	"strings"
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// These tests pin the ClickHouse type that chgen reports for an Enum column,
// and the Go type it maps to.
//
// Type-oracle finding N5: the value set was lost during inference. The
// schema parser kept only the constructor name, so the column
// Enum8('a' = 1, 'zz' = 2) rendered as the bare word "Enum8". ClickHouse
// itself reports the full value set, measured on 25.8.29.51:
//
//	system.columns.type   Enum8('a' = 1, 'zz' = 2)
//	toTypeName(e8)        Enum8('a' = 1, 'zz' = 2)
//
// A consumer that reads the bare name cannot map the enum at all, thus the
// loss is not cosmetic.
//
// ClickHouse renders the set in one canonical form, 'name' = number, and it
// fills in an omitted number. Measured on 25.8.29.51:
//
//	Enum8('a', 'b')          becomes Enum8('a' = 1, 'b' = 2)
//	Enum8('a' = -3, 'b')     becomes Enum8('a' = -3, 'b' = -2)
//	Enum8('a' = 5, 'b')      is rejected by the server (code 223); a number
//	                         is either given for every element or for none
//
// The implicit number therefore continues from the previous element, and a
// set with no number at all starts at 1.
//
// The mixed form Enum8('a' = -3, 'b') is not in these tests. ClickHouse
// rejects it with code 223, and clickhouse-sql-parser also refuses to parse
// it, thus no such column can exist to be mapped.
func enumTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events
(
    e8 Enum8('a' = 1, 'zz' = 2),
    e16 Enum16('x' = 1, 'y' = 2),
    ne8 Nullable(Enum8('a' = 1, 'zz' = 2)),
    ae8 Array(Enum8('a' = 1, 'zz' = 2)),
    implicit Enum8('a', 'b', 'c'),
    negative Enum8('a' = -3, 'b' = -2)
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

// The rendered type must equal what ClickHouse reports for the same column.
func TestEnumColumnKeepsItsValueSet(t *testing.T) {
	schema := enumTestSchema(t)
	table := schema.Tables["events"]
	want := map[string]string{
		"e8":       "Enum8('a' = 1, 'zz' = 2)",
		"e16":      "Enum16('x' = 1, 'y' = 2)",
		"ne8":      "Nullable(Enum8('a' = 1, 'zz' = 2))",
		"ae8":      "Array(Enum8('a' = 1, 'zz' = 2))",
		"implicit": "Enum8('a' = 1, 'b' = 2, 'c' = 3)",
		"negative": "Enum8('a' = -3, 'b' = -2)",
	}
	for _, column := range table.Columns {
		wantType, ok := want[column.Name]
		if !ok {
			continue
		}
		if got := column.Type.String(); got != wantType {
			t.Errorf("column %s type = %q, want %q", column.Name, got, wantType)
		}
	}
}

// An Enum column carries a string name over the wire. clickhouse-go scans it
// into a Go string in every wrapper shape, thus the Go mapping is string.
func TestEnumColumnsMapToString(t *testing.T) {
	schema := enumTestSchema(t)
	query := Query{
		Name:    "ReadEvents",
		Command: CommandMany,
		SQL:     "SELECT e8, e16, ne8, ae8 FROM events",
	}
	if err := resolveQuery(&query, schema); err != nil {
		t.Fatalf("resolveQuery() error = %v", err)
	}
	want := []string{"string", "string", "*string", "[]string"}
	if len(query.Results) != len(want) {
		t.Fatalf("results = %d, want %d", len(query.Results), len(want))
	}
	for index, wantType := range want {
		if got := query.Results[index].GoType; got != wantType {
			t.Errorf("result[%d] GoType = %q, want %q", index, got, wantType)
		}
	}
}

// The generated code must compile and name the Go type, which proves that the
// value set in the ClickHouse type does not leak into the Go type name.
func TestEnumGeneratesStringField(t *testing.T) {
	schema := enumTestSchema(t)
	queries, err := parseQueriesWithSchema(t, `-- name: ReadEvents :many
SELECT e8, ae8 FROM events;`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	// gofmt aligns the struct fields, thus the gap between the name and the
	// type is not one space. Collapse the runs of spaces before the match.
	text := strings.Join(strings.Fields(string(generated)), " ")
	if !strings.Contains(text, "E8 string") {
		t.Errorf("generated code has no string field for the Enum8 column:\n%s", text)
	}
	if !strings.Contains(text, "Ae8 []string") {
		t.Errorf("generated code has no []string field for the Array(Enum8) column:\n%s", text)
	}
}

// enumSupertypeSchema holds the column pairs that the Enum supertype rule
// needs. The value sets of e8 and e8b are the same; e8c and e16c have a
// different set, which changes the result.
func enumSupertypeSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE et
(
    e8 Enum8('a' = 1, 'zz' = 2),
    e8b Enum8('a' = 1, 'zz' = 2),
    e8c Enum8('q' = 7, 'w' = 9),
    e16 Enum16('x' = 1, 'y' = 2),
    e16b Enum16('x' = 1, 'y' = 2),
    e16c Enum16('p' = 5),
    ne8 Nullable(Enum8('a' = 1, 'zz' = 2)),
    ae8 Array(Enum8('a' = 1, 'zz' = 2)),
    c UInt8,
    s String,
    fs8 FixedString(8),
    ns Nullable(String),
    lc LowCardinality(String),
    i8 Int8,
    i32 Int32,
    u8 UInt8,
    u16 UInt16,
    f64 Float64,
    dec Decimal(18, 4),
    d Date
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

func inferEnumSupertype(t *testing.T, schema *Schema, exprSQL string) (string, error) {
	t.Helper()
	statements, err := clickhouse.NewParser("SELECT " + exprSQL + " FROM et").ParseStmts()
	if err != nil {
		t.Fatalf("parse %q: %v", exprSQL, err)
	}
	selectQuery, ok := statements[0].(*clickhouse.SelectQuery)
	if !ok {
		t.Fatalf("parse %q: not a SELECT", exprSQL)
	}
	scope, _, err := resolveScope(selectQuery, schema)
	if err != nil {
		t.Fatalf("resolveScope %q: %v", exprSQL, err)
	}
	inferred, err := inferExprType(selectQuery.SelectItems[0].Expr, scope)
	if err != nil {
		return "", err
	}
	return inferred.String(), nil
}

// An Enum joins the ClickHouse supertype lattice through its storage
// integer, and a string-like peer wins over that integer. Before this
// test, chgen refused every mixed pair with "no common ClickHouse type",
// so a CASE that mixed an Enum column with a String column could not be
// typed at all, although ClickHouse types it as String (type-oracle
// finding N5, second part).
//
// Every expected value below was measured on ClickHouse 25.8.29.51
// against real table columns, because ClickHouse folds constants and a
// literal peer therefore reports a different type than a column does.
func TestEnumSupertypeMatchesClickHouse(t *testing.T) {
	schema := enumSupertypeSchema(t)
	cases := []struct {
		expr string
		want string
	}{
		// A string-like peer wins, in every spelling.
		{"if(c = 1, e8, s)", "String"},
		{"if(c = 1, e8, fs8)", "String"},
		{"if(c = 1, e8, lc)", "String"},
		{"if(c = 1, lc, e8)", "String"},
		{"if(c = 1, e8, ns)", "Nullable(String)"},
		{"if(c = 1, ne8, s)", "Nullable(String)"},
		{"if(c = 1, ae8, [s])", "Array(String)"},
		{"multiIf(c = 1, e8, c = 2, s, e8)", "String"},
		// The same value set keeps the Enum, and keeps the set.
		{"if(c = 1, e8, e8b)", "Enum8('a' = 1, 'zz' = 2)"},
		{"if(c = 1, e16, e16b)", "Enum16('x' = 1, 'y' = 2)"},
		{"if(c = 1, ne8, e8b)", "Nullable(Enum8('a' = 1, 'zz' = 2))"},
		// A different value set falls back to the storage integer.
		{"if(c = 1, e8, e8c)", "Int8"},
		{"if(c = 1, e16, e16c)", "Int16"},
		{"if(c = 1, e8, e16)", "Int16"},
		// An integer or a float peer joins the storage integer.
		{"if(c = 1, e8, i8)", "Int8"},
		{"if(c = 1, e8, i32)", "Int32"},
		{"if(c = 1, e8, u8)", "Int16"},
		{"if(c = 1, e8, u16)", "Int32"},
		{"if(c = 1, e16, i8)", "Int16"},
		{"if(c = 1, e16, i32)", "Int32"},
		{"if(c = 1, e16, u8)", "Int16"},
		{"if(c = 1, e8, f64)", "Float64"},
		{"if(c = 1, ae8, [i8])", "Array(Int8)"},
	}
	for _, testCase := range cases {
		got, err := inferEnumSupertype(t, schema, testCase.expr)
		if err != nil {
			t.Errorf("%s: error = %v, want %s", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// ClickHouse has no supertype for an Enum with a Decimal or with a
// temporal type (code 386, measured on 25.8.29.51). chgen must refuse
// these as well: the storage integer alone would give Decimal(18, 4) and
// a wrong type is worse than a refusal.
func TestEnumSupertypeRefusesWhereClickHouseRefuses(t *testing.T) {
	schema := enumSupertypeSchema(t)
	for _, expr := range []string{"if(c = 1, e8, dec)", "if(c = 1, e8, d)"} {
		got, err := inferEnumSupertype(t, schema, expr)
		if err == nil {
			t.Errorf("%s = %q, want a refusal", expr, got)
		}
	}
}
