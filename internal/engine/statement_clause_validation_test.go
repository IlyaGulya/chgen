package engine

import (
	"strings"
	"testing"
)

const statementClauseDDL = `
CREATE TABLE clause_left (id UInt64, grp UInt8, value Int32) ENGINE = MergeTree ORDER BY tuple();
CREATE TABLE clause_right (id UInt64, value Int32) ENGINE = MergeTree ORDER BY tuple();
CREATE TABLE clause_third (id UInt32, value Int32) ENGINE = MergeTree ORDER BY tuple();
CREATE TABLE clause_bad (id Array(Int32), value Int32) ENGINE = MergeTree ORDER BY tuple();
CREATE TABLE clause_replacing (id UInt64, value Int32, version UInt64) ENGINE = ReplacingMergeTree(version) ORDER BY id;
`

func TestExecutableSelectClauseValidation(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantError string
	}{
		{name: "where", query: "SELECT value FROM clause_left WHERE id = 1"},
		{name: "where unknown", query: "SELECT value FROM clause_left WHERE missing = 1", wantError: "WHERE condition"},
		{name: "where domain", query: "SELECT value FROM clause_left WHERE value", wantError: "want UInt8 or Bool"},
		{name: "prewhere", query: "SELECT value FROM clause_left PREWHERE id = 1"},
		{name: "prewhere unknown", query: "SELECT value FROM clause_left PREWHERE missing = 1", wantError: "PREWHERE condition"},
		{name: "prewhere domain", query: "SELECT value FROM clause_left PREWHERE value", wantError: "want UInt8 or Bool"},
		{name: "join on", query: "SELECT clause_left.value FROM clause_left INNER JOIN clause_right ON clause_left.id = clause_right.id"},
		{name: "join on unknown", query: "SELECT clause_left.value FROM clause_left INNER JOIN clause_right ON clause_left.missing = clause_right.id", wantError: "JOIN ON condition"},
		{name: "join on domain", query: "SELECT clause_left.value FROM clause_left INNER JOIN clause_right ON clause_left.value", wantError: "want UInt8 or Bool"},
		{name: "order by", query: "SELECT value FROM clause_left ORDER BY id"},
		{name: "order by unknown", query: "SELECT value FROM clause_left ORDER BY missing", wantError: "ORDER BY expression"},
		{name: "limit", query: "SELECT value FROM clause_left LIMIT 1"},
		{name: "limit unknown", query: "SELECT value FROM clause_left LIMIT missing", wantError: "LIMIT expression"},
		{name: "limit domain", query: "SELECT value FROM clause_left LIMIT 'x'", wantError: "want an unsigned integer"},
		{name: "offset", query: "SELECT value FROM clause_left LIMIT 1 OFFSET 0"},
		{name: "offset unknown", query: "SELECT value FROM clause_left LIMIT 1 OFFSET missing", wantError: "LIMIT OFFSET expression"},
		{name: "offset domain", query: "SELECT value FROM clause_left LIMIT 1 OFFSET 'x'", wantError: "want an unsigned integer"},
		{name: "limit by", query: "SELECT value FROM clause_left LIMIT 1 BY grp"},
		{name: "limit by key unknown", query: "SELECT value FROM clause_left LIMIT 1 BY missing", wantError: "LIMIT BY key expression"},
		{name: "limit by domain", query: "SELECT value FROM clause_left LIMIT 'x' BY grp", wantError: "want an unsigned integer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := inferQueryResults(statementClauseDDL, test.query)
			if test.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestExecutableClauseSafetyBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantError string
	}{
		{name: "distinct on", query: "SELECT DISTINCT ON (id) value FROM clause_left"},
		{name: "distinct on unknown", query: "SELECT DISTINCT ON (missing) value FROM clause_left", wantError: "DISTINCT ON identifier"},
		{name: "first join unknown", query: "SELECT l.value FROM clause_left AS l JOIN clause_right AS r ON l.missing = r.id JOIN clause_third AS t ON r.id = t.id", wantError: "JOIN ON condition"},
		{name: "first join later table", query: "SELECT l.value FROM clause_left AS l JOIN clause_right AS r ON l.id = t.id JOIN clause_third AS t ON r.id = t.id", wantError: "JOIN ON condition"},
		{name: "second join unknown", query: "SELECT l.value FROM clause_left AS l JOIN clause_right AS r ON l.id = r.id JOIN clause_third AS t ON r.missing = t.id", wantError: "JOIN ON condition"},
		{name: "all joins", query: "SELECT l.value FROM clause_left AS l JOIN clause_right AS r ON l.id = r.id JOIN clause_third AS t ON r.id = t.id"},
		{name: "join using width", query: "SELECT clause_left.value FROM clause_left JOIN clause_third USING (id)"},
		{name: "join using domain", query: "SELECT clause_left.value FROM clause_left JOIN clause_bad USING (id)", wantError: "has no common type"},
		{name: "join using qualified", query: "SELECT clause_left.value FROM clause_left JOIN clause_third USING (clause_left.id)", wantError: "not a bare column name"},
		{name: "mixed aggregate", query: "SELECT value + count() FROM clause_left", wantError: "aggregate projection"},
		{name: "grouped aggregate", query: "SELECT grp + count() AS value FROM clause_left GROUP BY grp"},
		{name: "aggregate where", query: "SELECT value FROM clause_left WHERE count() > 0", wantError: "WHERE cannot contain an aggregate function"},
		{name: "window where", query: "SELECT value FROM clause_left WHERE row_number() OVER () > 0", wantError: "WHERE cannot contain a window function"},
		{name: "window having", query: "SELECT count() AS value FROM clause_left HAVING row_number() OVER () > 0", wantError: "HAVING cannot contain a window function"},
		{name: "aggregate order column", query: "SELECT count() AS value FROM clause_left ORDER BY clause_left.value", wantError: "ORDER BY aggregate coverage"},
		{name: "aggregate order alias", query: "SELECT count() AS value FROM clause_left ORDER BY value"},
		{name: "group aggregate order column", query: "SELECT count() AS value FROM clause_left GROUP BY id ORDER BY clause_left.value", wantError: "ORDER BY aggregate coverage"},
		{name: "group aggregate order key", query: "SELECT count() AS value FROM clause_left GROUP BY id ORDER BY id"},
		{name: "aggregate having column", query: "SELECT count() AS value FROM clause_left HAVING clause_left.value > 0", wantError: "HAVING aggregate coverage"},
		{name: "aggregate limit by column", query: "SELECT count() AS value FROM clause_left LIMIT 1 BY clause_left.value", wantError: "LIMIT BY aggregate coverage"},
		{name: "aggregate window column", query: "SELECT count() AS total, row_number() OVER w AS value FROM clause_left WINDOW w AS (ORDER BY clause_left.value)", wantError: "GROUP BY does not cover selected column"},
		{name: "aggregate window group key", query: "SELECT count() AS total, row_number() OVER w AS value FROM clause_left GROUP BY id WINDOW w AS (ORDER BY id)"},
		{name: "direct window inner aggregate", query: "SELECT first_value(value) OVER (ORDER BY count()) AS value FROM clause_left", wantError: "GROUP BY does not cover selected column value"},
		{name: "direct window inner aggregate grouped", query: "SELECT row_number() OVER (ORDER BY count()) AS value FROM clause_left"},
		{name: "named window inner aggregate", query: "SELECT first_value(value) OVER w AS value FROM clause_left WINDOW w AS (ORDER BY count())", wantError: "GROUP BY does not cover selected column value"},
		{name: "named window inner aggregate grouped", query: "SELECT row_number() OVER w AS value FROM clause_left WINDOW w AS (ORDER BY count())"},
		{name: "aggregate group", query: "SELECT count() FROM clause_left GROUP BY count()", wantError: "GROUP BY cannot contain an aggregate function"},
		{name: "fill", query: "SELECT id FROM clause_left ORDER BY id WITH FILL FROM 0 TO 3 STEP 1"},
		{name: "fill unknown", query: "SELECT id FROM clause_left ORDER BY id WITH FILL FROM missing TO 3 STEP 1", wantError: "ORDER BY WITH FILL FROM"},
		{name: "fill string", query: "SELECT id FROM clause_left ORDER BY id WITH FILL FROM 'x' TO 3 STEP 1", wantError: "incompatible with order key"},
		{name: "fill zero step", query: "SELECT id FROM clause_left ORDER BY id WITH FILL FROM 0 TO 3 STEP 0", wantError: "positive numeric literal"},
		{name: "fill column", query: "SELECT id FROM clause_left ORDER BY id WITH FILL FROM id TO 3 STEP 1", wantError: "measured constant literal"},
		{name: "asof equality", query: "SELECT l.value FROM clause_left AS l ASOF JOIN clause_right AS r ON l.id = r.id", wantError: "cross-input inequality"},
		{name: "asof constant inequality", query: "SELECT l.value FROM clause_left AS l ASOF JOIN clause_right AS r ON l.id = r.id AND 1 < 2", wantError: "cross-input inequality"},
		{name: "asof same-side inequality", query: "SELECT l.value FROM clause_left AS l ASOF JOIN clause_right AS r ON l.id = r.id AND l.value >= l.grp", wantError: "cross-input inequality"},
		{name: "asof two inequalities", query: "SELECT l.value FROM clause_left AS l ASOF JOIN clause_right AS r ON l.id = r.id AND l.value >= r.value AND l.id >= r.id", wantError: "exactly one"},
		{name: "asof inequality", query: "SELECT l.value FROM clause_left AS l ASOF JOIN clause_right AS r ON l.id = r.id AND l.value >= r.value"},
		{name: "sample", query: "SELECT value FROM clause_left SAMPLE 0.1", wantError: "SAMPLE capability"},
		{name: "final", query: "SELECT value FROM clause_left FINAL", wantError: "engine MergeTree"},
		{name: "final replacing", query: "SELECT value FROM clause_replacing FINAL"},
		{name: "top", query: "SELECT TOP 1 value FROM clause_left"},
		{name: "top domain", query: "SELECT TOP 1.5 value FROM clause_left", wantError: "want an unsigned integer"},
		{name: "top ties", query: "SELECT TOP 1 WITH TIES value FROM clause_left ORDER BY id"},
		{name: "top ties without order", query: "SELECT TOP 1 WITH TIES value FROM clause_left", wantError: "requires ORDER BY"},
		{name: "limit ties", query: "SELECT value FROM clause_left ORDER BY id LIMIT 1 WITH TIES"},
		{name: "limit ties without order", query: "SELECT value FROM clause_left LIMIT 1 WITH TIES", wantError: "requires ORDER BY"},
		{name: "limit ties with inner order", query: "SELECT value FROM clause_left WHERE id IN (SELECT id FROM clause_right ORDER BY id) LIMIT 1 WITH TIES", wantError: "same SELECT query"},
		{name: "limit ties with comment order", query: "SELECT value FROM clause_left /* ORDER BY id */ LIMIT 1 WITH TIES", wantError: "same SELECT query"},
		{name: "limit ties with string order", query: "SELECT 'ORDER BY' AS value FROM clause_left LIMIT 1 WITH TIES", wantError: "same SELECT query"},
		{name: "alias exact", query: "SELECT value AS Foo FROM clause_left ORDER BY Foo"},
		{name: "alias wrong case", query: "SELECT value AS Foo FROM clause_left ORDER BY foo", wantError: "column \"foo\""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := inferQueryResults(statementClauseDDL, test.query)
			if test.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestCompositePlaceholderDoesNotHideUnknownNames(t *testing.T) {
	schema, err := schemaFromDDLErr(t, statementClauseDDL)
	if err != nil {
		t.Fatal(err)
	}
	queries := []struct {
		sql  string
		want string
	}{
		{sql: "-- name: BadLimit :many\nSELECT value FROM clause_left LIMIT missing + chgen.arg('Limit')", want: "composite placeholder shape"},
		{sql: "-- name: BadSetting :many\nSELECT value FROM clause_left SETTINGS max_threads = missing + chgen.arg('Threads')", want: "unexpected token"},
	}
	for _, query := range queries {
		if _, err := parseQueriesWithSchema(t, query.sql, schema); err == nil || !strings.Contains(err.Error(), query.want) {
			t.Fatalf("error = %v, want %q", err, query.want)
		}
	}
}

func TestUnmodeledSelectClauseShapesFailClosed(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{query: "SELECT value FROM clause_left GROUP BY grp", want: "GROUP BY does not cover selected column"},
		{query: "SELECT count() AS value FROM clause_left HAVING value", want: "want UInt8 or Bool"},
		{query: "SELECT value FROM clause_left SETTINGS unknown_clause_setting = 1", want: "is not in the measured resolver roster"},
	}
	for _, test := range tests {
		_, err := inferQueryResults(statementClauseDDL, test.query)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v, want %q", test.query, err, test.want)
		}
	}
	for _, query := range []string{
		"SELECT value FROM clause_left UNION ALL SELECT value FROM clause_right",
		"(SELECT value FROM clause_left)",
	} {
		if _, err := inferQueryResults(statementClauseDDL, query); err != nil {
			t.Errorf("%s: unexpected error = %v", query, err)
		}
	}
}

func TestNestedSelectClausesUseTheirOwnScope(t *testing.T) {
	tests := []string{
		"SELECT nested.value FROM (SELECT value FROM clause_left WHERE missing = 1) AS nested",
		"SELECT (SELECT value FROM clause_left WHERE missing = 1) AS value",
		"WITH nested AS (SELECT value FROM clause_left WHERE missing = 1) SELECT value FROM nested",
	}
	for _, query := range tests {
		_, err := inferQueryResults(statementClauseDDL, query)
		if err == nil || !strings.Contains(err.Error(), "WHERE condition") {
			t.Errorf("%s: error = %v, want nested WHERE refusal", query, err)
		}
	}
}
