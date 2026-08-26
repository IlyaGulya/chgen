package engine

import (
	"strings"
	"testing"
)

const setOperationSchema = `
CREATE TABLE set_left (id UInt8, value Int16, num Int16, label String, lc LowCardinality(String)) ENGINE = Memory;
CREATE TABLE set_right (id UInt16, value UInt32, num UInt32, label FixedString(8), lc LowCardinality(String)) ENGINE = Memory;
CREATE TABLE set_wide (id UInt64, value Int64, num Int64, label String) ENGINE = Memory;
CREATE TABLE q (v UInt64) ENGINE = Memory;
CREATE TABLE set_sink (v UInt16) ENGINE = Memory;
CREATE TABLE set_bad_sink (v Array(Int16)) ENGINE = Memory;
`

func inferSetOperationResults(sql string) ([]Result, error) {
	return inferQueryResults(setOperationSchema, sql)
}

func TestSetOperationsUseFirstNamesAndNaryPositionalTypes(t *testing.T) {
	results, err := inferSetOperationResults("SELECT id AS first_id, value AS first_value FROM set_left UNION ALL SELECT id AS ignored_id, value AS ignored_value FROM set_right UNION DISTINCT SELECT id, value FROM set_wide")
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"first_id", "first_value"}
	wantTypes := []string{"UInt64", "Int64"}
	if len(results) != len(wantNames) {
		t.Fatalf("results = %#v", results)
	}
	for index := range results {
		if results[index].SQLName != wantNames[index] || results[index].CHType.String() != wantTypes[index] {
			t.Errorf("result %d = %s %s, want %s %s", index, results[index].SQLName, results[index].CHType.String(), wantNames[index], wantTypes[index])
		}
	}
}

func TestEverySetOperationSpellingResolves(t *testing.T) {
	for _, operation := range []string{"UNION ALL", "UNION DISTINCT", "INTERSECT", "EXCEPT"} {
		t.Run(strings.ReplaceAll(operation, " ", "_"), func(t *testing.T) {
			results, err := inferSetOperationResults("SELECT id AS result FROM set_left " + operation + " SELECT id AS other_name FROM set_right")
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].SQLName != "result" || results[0].CHType.String() != "UInt16" {
				t.Fatalf("results = %#v", results)
			}
		})
	}
}

func TestSetOperationBranchesHaveIndependentScopes(t *testing.T) {
	results, err := inferSetOperationResults("SELECT value AS result FROM set_left UNION ALL SELECT id FROM set_right")
	if err != nil {
		t.Fatal(err)
	}
	if got := results[0].CHType.String(); got != "Int32" {
		t.Fatalf("result = %s, want Int32", got)
	}
	if _, err := inferSetOperationResults("SELECT value FROM set_left UNION ALL SELECT missing FROM set_right"); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing branch column error = %v", err)
	}
}

func TestSetOperationWidthAndTypeMismatchRefuse(t *testing.T) {
	for _, test := range []struct {
		sql  string
		want string
	}{
		{sql: "SELECT id FROM set_left UNION ALL SELECT id, value FROM set_right", want: "column count"},
		{sql: "SELECT label FROM set_left UNION ALL SELECT id FROM set_right", want: "common type"},
	} {
		if _, err := inferSetOperationResults(test.sql); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("error = %v, want %q", err, test.want)
		}
	}
}

func TestSetOperationKeepsLowCardinalityOnlyWhenEveryBranchHasIt(t *testing.T) {
	tests := []struct {
		sql  string
		want string
	}{
		{sql: "SELECT lc AS result FROM set_left UNION ALL SELECT lc FROM set_right", want: "LowCardinality(String)"},
		{sql: "SELECT lc AS result FROM set_left UNION ALL SELECT label FROM set_right", want: "String"},
	}
	for _, test := range tests {
		results, err := inferSetOperationResults(test.sql)
		if err != nil {
			t.Errorf("%s: %v", test.sql, err)
			continue
		}
		if got := results[0].CHType.String(); got != test.want {
			t.Errorf("%s: result = %s, want %s", test.sql, got, test.want)
		}
	}
}

func TestForwardCTEsAndExactDependencyNamespaces(t *testing.T) {
	for _, sql := range []string{
		"WITH later AS q, toInt32(7) AS later SELECT q AS result",
		"WITH first AS (SELECT value FROM second), second AS (SELECT value FROM set_left) SELECT value AS result FROM first",
		"WITH scalar_value AS (SELECT q AS value), toInt32(7) AS q SELECT value AS result FROM scalar_value",
		"WITH relation_value AS (SELECT toInt32(7) AS value), (SELECT value FROM relation_value LIMIT 1) AS scalar_value SELECT scalar_value AS result",
	} {
		if _, err := inferSetOperationResults(sql); err != nil {
			t.Errorf("%s: %v", sql, err)
		}
	}
}

func TestFirstWithCrossesSetBranchesAndLaterWithStaysLocal(t *testing.T) {
	if _, err := inferSetOperationResults("WITH toInt32(7) AS shared SELECT shared AS result UNION ALL SELECT shared"); err != nil {
		t.Fatal(err)
	}
	if _, err := inferSetOperationResults("SELECT toInt32(1) AS result UNION ALL WITH toInt32(2) AS local SELECT local UNION ALL SELECT local"); err == nil || !strings.Contains(err.Error(), "local") {
		t.Fatalf("later local WITH leak error = %v", err)
	}
	if _, err := inferSetOperationResults("(WITH toInt32(7) AS local SELECT local AS result) UNION ALL SELECT local"); err == nil || !strings.Contains(err.Error(), "local") {
		t.Fatalf("parenthesized first-branch WITH leak error = %v", err)
	}
}

func TestReservedForwardCTENamesBlockCatalogAndOuterFallback(t *testing.T) {
	for _, test := range []struct {
		sql  string
		want string
	}{
		{sql: "WITH a AS (SELECT v FROM q), q AS (SELECT toInt16(7) AS v) SELECT v AS result FROM a", want: "Int16"},
		{sql: "WITH toUInt64(100) AS x, inner_q AS (WITH x AS y, toInt16(7) AS x SELECT y AS v) SELECT v AS result FROM inner_q", want: "Int16"},
		{sql: "WITH q AS (SELECT toUInt64(100) AS v), inner_q AS (WITH a AS (SELECT v FROM q), q AS (SELECT toInt16(7) AS v) SELECT v FROM a) SELECT v AS result FROM inner_q", want: "Int16"},
	} {
		results, err := inferSetOperationResults(test.sql)
		if err != nil {
			t.Errorf("%s: %v", test.sql, err)
			continue
		}
		if got := results[0].CHType.String(); got != test.want {
			t.Errorf("%s: result = %s, want %s", test.sql, got, test.want)
		}
	}
}

func TestParenthesizedAndNestedSetQueriesUseOneTypeJoin(t *testing.T) {
	for _, sql := range []string{
		"(SELECT id AS result FROM set_left UNION ALL SELECT num FROM set_left) UNION ALL SELECT num FROM set_right",
		"SELECT id AS result FROM set_left UNION ALL (SELECT num FROM set_left INTERSECT SELECT num FROM set_right)",
		"SELECT nested.result FROM (SELECT id AS result FROM set_left UNION DISTINCT SELECT id FROM set_right) AS nested",
	} {
		results, err := inferSetOperationResults(sql)
		if err != nil {
			t.Errorf("%s: %v", sql, err)
			continue
		}
		if got := results[0].CHType.String(); got != "Int64" && got != "UInt16" {
			t.Errorf("%s: result = %s", sql, got)
		}
	}
}

func TestSetSubqueriesResolveByRole(t *testing.T) {
	for _, sql := range []string{
		"SELECT id IN (SELECT id FROM set_left UNION ALL SELECT id FROM set_right) AS result FROM set_left",
		"SELECT EXISTS(SELECT id FROM set_left UNION ALL SELECT id FROM set_right) AS result FROM set_left",
	} {
		results, err := inferSetOperationResults(sql)
		if err != nil {
			t.Errorf("%s: %v", sql, err)
			continue
		}
		if got := results[0].CHType.String(); got != "UInt8" {
			t.Errorf("%s: result = %s, want UInt8", sql, got)
		}
	}
	if _, err := inferSetOperationResults("SELECT (SELECT id FROM set_left UNION ALL SELECT id FROM set_right) AS result"); err == nil || !strings.Contains(err.Error(), "cardinality") {
		t.Fatalf("scalar set cardinality error = %v", err)
	}
}

func TestSetOperationParametersUseTheirBranchScopes(t *testing.T) {
	query := Query{Name: "set_params", Command: CommandMany, SQL: "SELECT id AS result FROM set_left WHERE id > ? UNION ALL SELECT id FROM set_right WHERE id > ?"}
	schema, err := conformanceSchema(setOperationSchema)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolveQuery(&query, schema); err != nil {
		t.Fatal(err)
	}
	if len(query.Params) != 2 || query.Params[0].CHType.String() != "UInt8" || query.Params[1].CHType.String() != "UInt16" {
		t.Fatalf("params = %#v", query.Params)
	}
}

func TestInsertSelectUsesTheJoinedSetResult(t *testing.T) {
	schema, err := schemaFromDDLErr(t, setOperationSchema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseQueriesWithSchema(t, "-- name: InsertSet :exec\nINSERT INTO set_sink (v) SELECT id FROM set_left UNION ALL SELECT id FROM set_right", schema); err != nil {
		t.Fatal(err)
	}
	if _, err := parseQueriesWithSchema(t, "-- name: InsertBadSet :exec\nINSERT INTO set_bad_sink (v) SELECT id FROM set_left UNION ALL SELECT id FROM set_right", schema); err == nil || !strings.Contains(err.Error(), "INSERT SELECT target column") {
		t.Fatalf("incompatible INSERT SELECT error = %v", err)
	}
}
