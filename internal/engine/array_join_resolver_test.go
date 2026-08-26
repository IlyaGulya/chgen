package engine

import (
	"strings"
	"testing"
)

const arrayJoinSchema = `
CREATE TABLE array_join_rows (
    id UInt8,
    scalar Int64,
    values Array(Int32),
    peers Array(UInt16),
    nullable_values Array(Nullable(Int16)),
    low_values Array(LowCardinality(String)),
    tuple_values Array(Tuple(x UInt8, y Nullable(String))),
    nested_values Nested(k UInt16, v String),
    empty_values Array(UInt32),
    mapped Map(String, UInt16)
) ENGINE = Memory;
`

func inferArrayJoinResults(sql string) ([]Result, error) {
	return inferQueryResults(arrayJoinSchema, sql)
}

func TestArrayJoinAddsExactElementAliasesToTheQueryScope(t *testing.T) {
	results, err := inferArrayJoinResults("SELECT item AS value FROM array_join_rows ARRAY JOIN values AS item")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].CHType.String() != "Int32" {
		t.Fatalf("results = %#v", results)
	}
	if _, err := inferArrayJoinResults("SELECT Item AS value FROM array_join_rows ARRAY JOIN values AS item"); err == nil || !strings.Contains(err.Error(), "Item") {
		t.Fatalf("exact alias error = %v", err)
	}
}

func TestArrayJoinPreservesOrRebindsTheSourceNameByAlias(t *testing.T) {
	tests := []struct {
		sql  string
		want []string
	}{
		{sql: "SELECT values AS value FROM array_join_rows ARRAY JOIN values", want: []string{"Int32"}},
		{sql: "SELECT values AS original, item AS value FROM array_join_rows ARRAY JOIN values AS item", want: []string{"Array(Int32)", "Int32"}},
		{sql: "SELECT r.values AS value FROM array_join_rows AS r ARRAY JOIN values", want: []string{"Int32"}},
		{sql: "SELECT r.values AS original, item AS value FROM array_join_rows AS r ARRAY JOIN values AS item", want: []string{"Array(Int32)", "Int32"}},
	}
	for _, test := range tests {
		results, err := inferArrayJoinResults(test.sql)
		if err != nil {
			t.Errorf("%s: %v", test.sql, err)
			continue
		}
		if len(results) != len(test.want) {
			t.Errorf("%s: results = %#v", test.sql, results)
			continue
		}
		for index, want := range test.want {
			if got := results[index].CHType.String(); got != want {
				t.Errorf("%s: result %d = %s, want %s", test.sql, index, got, want)
			}
		}
	}
}

func TestArrayJoinElementFamiliesAndClauses(t *testing.T) {
	tests := []struct {
		sql  string
		want []string
	}{
		{sql: "SELECT a, b FROM array_join_rows ARRAY JOIN values AS a, peers AS b", want: []string{"Int32", "UInt16"}},
		{sql: "SELECT item FROM array_join_rows LEFT ARRAY JOIN empty_values AS item", want: []string{"UInt32"}},
		{sql: "SELECT item FROM array_join_rows ARRAY JOIN nullable_values AS item", want: []string{"Nullable(Int16)"}},
		{sql: "SELECT item FROM array_join_rows ARRAY JOIN low_values AS item", want: []string{"LowCardinality(String)"}},
		{sql: "SELECT item.x AS x, item.y AS y FROM array_join_rows ARRAY JOIN tuple_values AS item", want: []string{"UInt8", "Nullable(String)"}},
		{sql: "SELECT nested_values.k AS k, nested_values.v AS v FROM array_join_rows ARRAY JOIN nested_values", want: []string{"UInt16", "String"}},
		{sql: "SELECT item.1 AS key, item.2 AS value FROM array_join_rows ARRAY JOIN mapped AS item", want: []string{"String", "UInt16"}},
		{sql: "SELECT item, count() AS n FROM array_join_rows ARRAY JOIN values AS item WHERE item > 0 GROUP BY item HAVING item > 0 ORDER BY item", want: []string{"Int32", "UInt64"}},
	}
	for _, test := range tests {
		results, err := inferArrayJoinResults(test.sql)
		if err != nil {
			t.Errorf("%s: %v", test.sql, err)
			continue
		}
		for index, want := range test.want {
			if index >= len(results) || results[index].CHType.String() != want {
				t.Errorf("%s: results = %#v, want type %d = %s", test.sql, results, index, want)
			}
		}
	}
}

func TestArrayJoinRefusesUnsupportedScopeBoundaries(t *testing.T) {
	tests := []struct {
		sql  string
		want string
	}{
		{sql: "SELECT item FROM array_join_rows ARRAY JOIN missing AS item", want: "missing"},
		{sql: "SELECT item FROM array_join_rows ARRAY JOIN scalar AS item", want: "requires an Array or Map"},
		{sql: "SELECT item FROM array_join_rows ARRAY JOIN values AS item, peers AS item", want: "defined more than once"},
		{sql: "SELECT item FROM array_join_rows ARRAY JOIN values AS item PREWHERE item > 0", want: "PREWHERE cannot use"},
	}
	for _, test := range tests {
		if _, err := inferArrayJoinResults(test.sql); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v, want %q", test.sql, err, test.want)
		}
	}
}

func TestArrayJoinParametersUseTheElementType(t *testing.T) {
	query := Query{Name: "array_join_param", Command: CommandMany, SQL: "SELECT item AS value FROM array_join_rows ARRAY JOIN values AS item WHERE item > ?"}
	schema, err := conformanceSchema(arrayJoinSchema)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolveQuery(&query, schema); err != nil {
		t.Fatal(err)
	}
	if len(query.Params) != 1 || query.Params[0].CHType.String() != "Int32" {
		t.Fatalf("params = %#v", query.Params)
	}
}

func TestArrayJoinSameNodeUsesTheScopeBeforeTheNode(t *testing.T) {
	for _, query := range []string{
		"SELECT a, b FROM array_join_rows ARRAY JOIN values AS a, [a] AS b",
		"SELECT a, b FROM array_join_rows ARRAY JOIN values AS a, arrayMap(x -> x + a, peers) AS b",
	} {
		if _, err := inferArrayJoinResults(query); err == nil || !strings.Contains(err.Error(), "column \"a\"") {
			t.Errorf("%s: error = %v, want the same-node alias refusal", query, err)
		}
	}
	results, err := inferArrayJoinResults("SELECT values, b FROM array_join_rows ARRAY JOIN values, [values] AS b")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].CHType.String() != "Int32" || results[1].CHType.String() != "Array(Int32)" {
		t.Fatalf("results = %#v, want Int32 and Array(Int32)", results)
	}
}

func TestSequentialArrayJoinNodesPublishThePreviousNode(t *testing.T) {
	results, err := inferArrayJoinResults("SELECT a, b FROM array_join_rows ARRAY JOIN values AS a ARRAY JOIN [a] AS b")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].CHType.String() != "Int32" || results[1].CHType.String() != "Int32" {
		t.Fatalf("results = %#v, want two Int32 results", results)
	}
}
