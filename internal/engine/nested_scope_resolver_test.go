package engine

import (
	"strings"
	"testing"
)

func nestedScopeTestSchema() *Schema {
	return &Schema{Tables: map[string]Table{
		"outer_rows": {
			Name: "outer_rows",
			Columns: map[string]Column{
				"id":    {Name: "id", Type: CHType{Name: "UInt64"}},
				"value": {Name: "value", Type: CHType{Name: "Int32"}},
			},
			ColumnOrder: []string{"id", "value"},
		},
		"inner_rows": {
			Name: "inner_rows",
			Columns: map[string]Column{
				"id":    {Name: "id", Type: CHType{Name: "UInt32"}},
				"value": {Name: "value", Type: CHType{Name: "Int32"}},
			},
			ColumnOrder: []string{"id", "value"},
		},
		"small_rows": {
			Name: "small_rows",
			Columns: map[string]Column{
				"id":    {Name: "id", Type: CHType{Name: "UInt16"}},
				"value": {Name: "value", Type: CHType{Name: "Int16"}},
			},
			ColumnOrder: []string{"id", "value"},
		},
	}}
}

func resolveNestedScopeQuery(sql string) ([]Result, error) {
	query := Query{Name: "nested_scope", Command: CommandOne, SQL: sql}
	err := resolveQuery(&query, nestedScopeTestSchema())
	return query.Results, err
}

func TestScalarSubqueryAddsTheEmptyResultNullable(t *testing.T) {
	results, err := resolveNestedScopeQuery("SELECT (SELECT max(i.value) FROM inner_rows AS i WHERE i.id = o.id) AS found FROM outer_rows AS o")
	if err != nil {
		t.Fatal(err)
	}
	if got := results[0].CHType.String(); got != "Nullable(Int32)" {
		t.Fatalf("scalar result = %s, want Nullable(Int32)", got)
	}
}

func TestExistsAndUncorrelatedInArePredicates(t *testing.T) {
	for _, sql := range []string{
		"SELECT EXISTS(SELECT i.id FROM inner_rows AS i WHERE i.id = o.id) AS found FROM outer_rows AS o",
		"SELECT o.id IN (SELECT i.id FROM inner_rows AS i) AS found FROM outer_rows AS o",
	} {
		results, err := resolveNestedScopeQuery(sql)
		if err != nil {
			t.Errorf("%s: %v", sql, err)
			continue
		}
		if got := results[0].CHType.String(); got != "UInt8" {
			t.Errorf("%s: result = %s, want UInt8", sql, got)
		}
	}
}

func TestCorrelatedInAndDerivedRelationsAreRefused(t *testing.T) {
	for _, test := range []struct {
		sql  string
		want string
	}{
		{sql: "SELECT o.id IN (SELECT i.id FROM inner_rows AS i WHERE i.id = o.id) AS found FROM outer_rows AS o", want: "correlated IN"},
		{sql: "SELECT d.id FROM outer_rows AS o JOIN (SELECT i.id FROM inner_rows AS i WHERE i.id = o.id) AS d ON 1", want: "correlated derived"},
	} {
		_, err := resolveNestedScopeQuery(test.sql)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v, want %q", test.sql, err, test.want)
		}
	}
}

func TestQualifiedLocalAliasStopsOuterFallback(t *testing.T) {
	_, err := resolveNestedScopeQuery("SELECT (SELECT o.value FROM inner_rows AS o LIMIT 1) AS found FROM outer_rows AS o")
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolveNestedScopeQuery("SELECT (SELECT o.missing FROM inner_rows AS o LIMIT 1) AS found FROM outer_rows AS o")
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("local qualifier fallback error = %v", err)
	}
}

func TestTableAliasCaseIsExactAndDuplicatesAreRefused(t *testing.T) {
	for _, sql := range []string{
		"SELECT x.id FROM outer_rows AS X",
		"SELECT a.id FROM outer_rows AS a JOIN inner_rows AS a ON 1",
	} {
		if _, err := resolveNestedScopeQuery(sql); err == nil {
			t.Errorf("query was accepted: %s", sql)
		}
	}
}

func TestProjectionAliasesAreExactAndVisibleInEarlyClauses(t *testing.T) {
	for _, sql := range []string{
		"SELECT value AS Value FROM outer_rows WHERE Value > 0",
		"SELECT value AS Value FROM outer_rows PREWHERE Value > 0",
		"SELECT value AS Value FROM outer_rows GROUP BY Value",
		"SELECT DISTINCT ON (Value) value AS Value FROM outer_rows",
	} {
		if _, err := resolveNestedScopeQuery(sql); err != nil {
			t.Errorf("%s: %v", sql, err)
		}
	}
	if _, err := resolveNestedScopeQuery("SELECT value AS Flag FROM outer_rows WHERE flag > 0"); err == nil {
		t.Fatal("lower-case alias use was accepted")
	}
}

func TestProjectionAliasPlacementUsesTheExpandedExpression(t *testing.T) {
	_, err := resolveNestedScopeQuery("SELECT count() AS flag FROM outer_rows WHERE flag")
	if err == nil || !strings.Contains(err.Error(), "aggregate function") {
		t.Fatalf("aggregate alias placement error = %v", err)
	}
}

func TestJoinUsingGivesTheCommonTypeToMergedAndQualifiedKeys(t *testing.T) {
	results, err := resolveNestedScopeQuery("SELECT id AS merged, o.id AS left_id, i.id AS right_id FROM outer_rows AS o JOIN inner_rows AS i USING (id)")
	if err != nil {
		t.Fatal(err)
	}
	for index, result := range results {
		if got := result.CHType.String(); got != "UInt64" {
			t.Errorf("result %d = %s, want UInt64", index, got)
		}
	}
}

func TestNamedWindowCanUseAnExactProjectionAlias(t *testing.T) {
	_, err := resolveNestedScopeQuery("SELECT value AS x, row_number() OVER w AS row_number FROM outer_rows WINDOW w AS (ORDER BY x)")
	if err != nil {
		t.Fatal(err)
	}
}

func TestLaterJoinDoesNotRewriteQualifiedOrAmbiguousKeys(t *testing.T) {
	results, err := resolveNestedScopeQuery("SELECT s.id AS value FROM outer_rows AS o JOIN inner_rows AS i USING (id) JOIN small_rows AS s ON s.id = o.id")
	if err != nil {
		t.Fatal(err)
	}
	if got := results[0].CHType.String(); got != "UInt16" {
		t.Fatalf("later qualified key = %s, want UInt16", got)
	}
	if _, err := resolveNestedScopeQuery("SELECT id AS value FROM outer_rows AS o JOIN inner_rows AS i ON o.id = i.id JOIN small_rows AS s ON s.id = o.id"); err == nil {
		t.Fatal("ambiguous unqualified key was accepted")
	}
	if _, err := resolveNestedScopeQuery("SELECT s.id AS value FROM outer_rows AS o JOIN inner_rows AS i ON o.id = i.id JOIN small_rows AS s USING (id)"); err == nil {
		t.Fatal("ambiguous left input to later USING was accepted")
	}
}

func TestSubqueryCorrelationUsesExpandedAliasBindings(t *testing.T) {
	for _, sql := range []string{
		"SELECT value AS x, (SELECT r.value FROM inner_rows AS r WHERE r.value = x LIMIT 1) AS found FROM outer_rows AS t",
		"SELECT value AS x, t.id IN (SELECT r.id FROM inner_rows AS r WHERE r.value = x) AS found FROM outer_rows AS t",
		"SELECT value AS x, d.id AS found FROM outer_rows AS t CROSS JOIN (SELECT r.id FROM inner_rows AS r WHERE r.value = x) AS d",
	} {
		if _, err := resolveNestedScopeQuery(sql); err != nil {
			t.Errorf("rebound alias query %s: %v", sql, err)
		}
	}
	for _, sql := range []string{
		"SELECT t.value AS x, (SELECT r.value FROM inner_rows AS r WHERE r.value = x LIMIT 1) AS found FROM outer_rows AS t",
		"SELECT t.value AS x, t.id IN (SELECT r.id FROM inner_rows AS r WHERE r.value = x) AS found FROM outer_rows AS t",
		"SELECT t.value AS x, d.id AS found FROM outer_rows AS t CROSS JOIN (SELECT r.id FROM inner_rows AS r WHERE r.value = x) AS d",
	} {
		if _, err := resolveNestedScopeQuery(sql); err == nil {
			t.Errorf("correlated alias query was accepted: %s", sql)
		}
	}
}

func TestProjectionAliasDependencyAndClauseRoles(t *testing.T) {
	for _, sql := range []string{
		"SELECT x + 1 AS y, value AS x FROM outer_rows",
		"SELECT value + 1 AS value FROM outer_rows",
		"SELECT o.value AS x FROM outer_rows AS o JOIN inner_rows AS i ON i.value = x",
		"SELECT count() AS x FROM outer_rows HAVING x > 0",
		"SELECT value AS x FROM outer_rows LIMIT 1 BY x",
		"SELECT value AS x, row_number() OVER (ORDER BY x) AS row_number FROM outer_rows",
	} {
		if _, err := resolveNestedScopeQuery(sql); err != nil {
			t.Errorf("alias role query %s: %v", sql, err)
		}
	}
	if _, err := resolveNestedScopeQuery("SELECT y AS x, x AS y FROM outer_rows"); err == nil {
		t.Fatal("pure alias cycle was accepted")
	}
}
