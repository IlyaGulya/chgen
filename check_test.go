package chgen_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IlyaGulya/chgen"
)

func writeCheckProject(t *testing.T, ddl, sql string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"chgen.yaml":  "version: 1\npackages:\n  - name: queries\n    output: generated/queries.go\n    schema: schema.sql\n    queries: queries.sql\n",
		"schema.sql":  ddl,
		"queries.sql": sql,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "chgen.yaml"), filepath.Join(dir, "generated", "queries.go")
}

func TestWildcardResultsFollowSourceColumnOrder(t *testing.T) {
	const ddl = "CREATE TABLE events (z UInt64, a String DEFAULT 'value', m UInt64 MATERIALIZED z, x UInt64 ALIAS z) ENGINE=Memory;"
	queries, err := parsePublicQuery(t, ddl, "-- name: Read :many\nSELECT * FROM events ORDER BY z;")
	if err != nil {
		t.Fatal(err)
	}
	if len(queries[0].Results) != 2 || queries[0].Results[0].SQLName != "z" || queries[0].Results[0].GoType != "uint64" || queries[0].Results[1].SQLName != "a" || queries[0].Results[1].GoType != "string" {
		t.Fatalf("wrong wildcard results: %+v", queries[0].Results)
	}
	if queries[0].SQL != "SELECT * FROM events ORDER BY z;" {
		t.Fatalf("SQL was rewritten: %q", queries[0].SQL)
	}
	if _, err := chgen.Generate("queries", queries); err != nil {
		t.Fatal(err)
	}
}

func TestQualifiedWildcardResolvesThroughNestedScopes(t *testing.T) {
	for _, sql := range []string{
		"SELECT e.* FROM events AS e ORDER BY z;",
		"WITH source AS (SELECT * FROM events) SELECT source.* FROM source ORDER BY z;",
		"SELECT nested.* FROM (SELECT * FROM events) AS nested ORDER BY z;",
		"SELECT * FROM numbers(3);",
	} {
		queries, err := parsePublicQuery(t, "CREATE TABLE events (z UInt64, a String) ENGINE=Memory;", "-- name: Read :many\n"+sql)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		want := 2
		if strings.Contains(sql, "numbers") {
			want = 1
		}
		if len(queries[0].Results) != want || queries[0].Results[0].GoType != "uint64" {
			t.Fatalf("%s: %+v", sql, queries[0].Results)
		}
		if _, err := chgen.Generate("queries", queries); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWildcardInclusionSettingsChangeTheResultShape(t *testing.T) {
	queries, err := parsePublicQuery(t,
		"CREATE TABLE events (z UInt64, a String, m UInt64 MATERIALIZED z, x UInt64 ALIAS z) ENGINE=Memory;",
		"-- name: Read :many\nSELECT * FROM events SETTINGS asterisk_include_materialized_columns=1, asterisk_include_alias_columns=true;")
	if err != nil {
		t.Fatal(err)
	}
	if len(queries[0].Results) != 4 || queries[0].Results[2].SQLName != "m" || queries[0].Results[3].SQLName != "x" {
		t.Fatalf("wrong inclusion shape: %+v", queries[0].Results)
	}
	if _, err := chgen.Generate("queries", queries); err != nil {
		t.Fatal(err)
	}
}

func TestWildcardGenerationRejectsStaleOrUnresolvedShapes(t *testing.T) {
	const ddl = "CREATE TABLE events (z UInt64, a String) ENGINE=Memory;"
	for _, sql := range []string{
		"SELECT * FROM events e CROSS JOIN events f;",
		"SELECT unknown.* FROM events;",
		"SELECT * EXCEPT(a) FROM events;",
		"-- result: Missing missing\nSELECT * FROM events;",
		"SELECT * FROM events SETTINGS asterisk_include_alias_columns=2;",
	} {
		if _, err := parsePublicQuery(t, ddl, "-- name: Read :many\n"+sql); err == nil {
			t.Fatalf("unsupported or inconsistent wildcard accepted: %s", sql)
		}
	}
	queries, err := parsePublicQuery(t, ddl, "-- name: Read :many\nSELECT * FROM events;")
	if err != nil {
		t.Fatal(err)
	}
	queries[0].SQL = "SELECT * FROM numbers(2);"
	if _, err := chgen.Generate("queries", queries); err == nil || !strings.Contains(err.Error(), "changed after catalog resolution") {
		t.Fatalf("stale output shape accepted: %v", err)
	}
	if _, err := chgen.Generate("queries", []chgen.Query{{Name: "Read", Command: chgen.CommandMany, SQL: "SELECT * FROM events;"}}); err == nil {
		t.Fatal("unresolved wildcard generated")
	}
}

func TestWildcardGeneratedReadersCheckTheShapeBeforeScan(t *testing.T) {
	queries, err := parsePublicQuery(t, "CREATE TABLE events (z UInt64) ENGINE=Memory;", "-- name: Read :many\nSELECT * FROM events;")
	if err != nil {
		t.Fatal(err)
	}
	generated, err := chgen.Generate("queries", queries)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "chgenCheckResultContract(rows,") || !strings.Contains(string(generated), `"UInt64"`) {
		t.Fatal("wildcard reader does not enforce catalog shape")
	}
}

func TestSeriesTableFunctionsResolveAcrossQueryScopes(t *testing.T) {
	queries, err := parsePublicQuery(t, "", `-- name: Series :many
WITH series AS (SELECT number AS id FROM numbers(10, 6, 2))
SELECT s.id, z.zero FROM series AS s CROSS JOIN zeros(1) AS z ORDER BY s.id;
-- name: Parallel :many
SELECT n.number, z.zero FROM numbers_mt(2) AS n CROSS JOIN zeros_mt(1) AS z ORDER BY n.number;`)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range queries {
		for i, result := range query.Results {
			if result.GoType != []string{"uint64", "uint8"}[i] {
				t.Fatalf("series result: %+v", result)
			}
		}
	}
	if _, err := chgen.Generate("queries", queries); err != nil {
		t.Fatal(err)
	}
}

func TestInclusiveSeriesRelationsResolve(t *testing.T) {
	queries, err := parsePublicQuery(t, "", `-- name: Inclusive :many
SELECT generate_series AS value FROM generate_series(2,8,2) ORDER BY value;
-- name: Camel :many
SELECT generate_series AS value FROM generateSeries(2,8,2) ORDER BY value;`)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range queries {
		if len(query.Results) != 1 || query.Results[0].GoType != "uint64" {
			t.Fatalf("inclusive series result: %+v", query.Results)
		}
	}
	if _, err := chgen.Generate("series", queries); err != nil {
		t.Fatal(err)
	}
}

func TestSeriesTableFunctionDomainsRemainBounded(t *testing.T) {
	for _, sql := range []string{
		"SELECT number FROM numbers(-1)", "SELECT number FROM numbers(1.5)", "SELECT number FROM numbers('3')",
		"SELECT number FROM numbers(1,3,0)", "SELECT number FROM numbers(1,2,3,4)",
		"SELECT zero FROM zeros(1,2)", "SELECT number FROM numbers(1) FINAL", "SELECT number FROM numbers(1) SAMPLE 1",
		"SELECT number FROM numbers(1 + 2)", "SELECT number FROM numbers_mt(1 + 2)",
		"SELECT zero FROM zeros(1 + 2)", "SELECT zero FROM zeros_mt(1 + 2)",
		"SELECT generate_series FROM generate_series(1)", "SELECT generate_series FROM generateSeries()",
		"SELECT generate_series FROM generate_series(1,3,0)", "SELECT generate_series FROM generateSeries(-1,3)",
		"SELECT missing FROM numbers(1)", "SELECT number FROM numbers(1) AS n CROSS JOIN numbers(1) AS n",
		"SELECT number FROM client_table_function(1)",
	} {
		if _, err := parsePublicQuery(t, "", "-- name: Read :many\n"+sql); err == nil {
			t.Errorf("accepted %s", sql)
		}
	}
}

func TestSeriesParametersAreUInt64(t *testing.T) {
	queries, err := parsePublicQuery(t, "", `-- name: Read :many
SELECT number FROM numbers(chgen.arg('Start'), chgen.arg('Length'), chgen.arg('Step')) ORDER BY number;`)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries[0].Params) != 3 {
		t.Fatalf("series parameters: %+v", queries[0].Params)
	}
	for _, param := range queries[0].Params {
		if param.GoType != "uint64" || param.CHType.String() != "UInt64" {
			t.Fatalf("series parameter: %+v", param)
		}
	}
	if _, err := chgen.Generate("series", queries); err != nil {
		t.Fatal(err)
	}
}

func TestSeriesParameterAdapterPreservesSQLAndOtherParserAdapters(t *testing.T) {
	queries, err := parsePublicQuery(t, "", `-- name: Quoted :many
SELECT 'numbers(?)' AS label, number FROM numbers(chgen.arg('Length')) ORDER BY number;
-- name: Ties :many
SELECT number FROM numbers(chgen.arg('Length')) ORDER BY number LIMIT 1 WITH TIES;`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(queries[0].SQL, "'numbers(?)'") || !strings.Contains(queries[0].SQL, "FROM numbers(?)") || !strings.Contains(queries[1].SQL, "WITH TIES") {
		t.Fatalf("parser-only normalization changed runtime SQL: %+v", queries)
	}
	for _, query := range queries {
		if len(query.Params) != 1 || query.Params[0].CHType.String() != "UInt64" {
			t.Fatalf("parameter binding was lost: %+v", query.Params)
		}
	}
	if _, err := chgen.Generate("series", queries); err != nil {
		t.Fatal(err)
	}
}

func TestCoverageCLIReportsIndependentStagesAndContinuesAfterRefusal(t *testing.T) {
	cli := buildPublicCLI(t)
	corpus := filepath.Join(t.TempDir(), "corpus.json")
	data := `{"version":1,"clickhouse_version":"25.8.29.51","cases":[
	{"id":"good","family":"select","source":"client","sql":"SELECT 1 AS value","expected_server":"accept"},
	{"id":"bad","family":"invalid","source":"client","sql":"SELECT (","expected_server":"refuse"},
	{"id":"script","family":"ddl","source":"client","scope":"parse","sql":"CREATE TABLE t (id UInt64) ENGINE=Memory;","expected_server":"unknown"}]}`
	if err := os.WriteFile(corpus, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), cli, "coverage", "-corpus", corpus).Output()
	if err != nil {
		t.Fatalf("coverage failed: %v\n%s", err, out)
	}
	var report struct {
		Cases []struct {
			ID     string `json:"id"`
			Stages map[string]struct {
				Status string `json:"status"`
			} `json:"stages"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Cases) != 3 || report.Cases[0].Stages["generate"].Status != "passed" || report.Cases[1].Stages["parse"].Status == "passed" || report.Cases[2].Stages["resolve"].Status != "not_run" || report.Cases[0].Stages["execution"].Status != "not_run" {
		t.Fatalf("misleading stage report: %s", out)
	}
}

func TestCoverageIRPreservesIdentifierSourceRanges(t *testing.T) {
	cli := buildPublicCLI(t)
	path := filepath.Join(t.TempDir(), "corpus.json")
	corpus := `{"version":1,"clickhouse_version":"25.8.29.51","cases":[{"id":"ranges","family":"binding","source":"client","schema":"CREATE TABLE events (id UInt64) ENGINE=Memory;","sql":"SELECT\n id FROM events\nWHERE id > 1","expected_server":"accept"}]}`
	if err := os.WriteFile(path, []byte(corpus), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), cli, "coverage", "-corpus", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Cases []struct {
			IR struct {
				Select struct {
					Items []struct {
						Expr struct{ Span *struct{ Start, End int } }
					}
				}
			}
		}
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Cases) != 1 || len(report.Cases[0].IR.Select.Items) != 1 {
		t.Fatalf("missing tree: %s", out)
	}
	span := report.Cases[0].IR.Select.Items[0].Expr.Span
	if span == nil || span.Start != 8 || span.End != 10 {
		t.Fatalf("identifier range lost: %s", out)
	}
}

func TestCoverageReportsParserIndependentColumnBindings(t *testing.T) {
	cli := buildPublicCLI(t)
	path := filepath.Join(t.TempDir(), "corpus.json")
	corpus := `{"version":1,"clickhouse_version":"25.8.29.51","cases":[{"id":"bound","family":"binding","source":"client","schema":"CREATE TABLE events (id UInt64) ENGINE=Memory;","sql":"SELECT\n id FROM events\nWHERE id > 1","expected_server":"accept"}]}`
	if err := os.WriteFile(path, []byte(corpus), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), cli, "coverage", "-corpus", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Cases []struct {
			Stages  map[string]struct{ Status string }
			Binding *struct {
				Backend    string
				References []struct {
					Table, Column, Type string
					Span                struct{ Start, End int }
				}
			}
		}
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Cases) != 1 || report.Cases[0].Binding == nil {
		t.Fatalf("binding observation absent: %s", out)
	}
	got := report.Cases[0]
	if got.Stages["bind"].Status != "passed" || got.Binding.Backend != "sqlir" || len(got.Binding.References) != 2 || got.Binding.References[0].Table != "events" || got.Binding.References[0].Column != "id" || got.Binding.References[0].Type != "UInt64" || got.Binding.References[0].Span.Start != 8 {
		t.Fatalf("wrong binding: %s", out)
	}
}

func TestProductionIRBindingRefusesAnUnknownPredicateColumn(t *testing.T) {
	_, err := parsePublicQuery(t, "CREATE TABLE events (id UInt64) ENGINE=Memory;", "-- name: Read :many\nSELECT id FROM events\nWHERE missing > 1;")
	if err == nil {
		t.Fatal("unknown predicate column accepted")
	}
	detail := chgen.ExplainError(err)
	if detail.Code != "ir-column-missing" || detail.Status != "invalid" || detail.Stage != "binding" || !strings.Contains(err.Error(), `column "missing"`) {
		t.Fatalf("production did not use the IR binder: %+v; %v", detail, err)
	}
}

func TestCoverageBindsComputedAliasChainsToTheirSources(t *testing.T) {
	cli := buildPublicCLI(t)
	path := filepath.Join(t.TempDir(), "corpus.json")
	corpus := `{"version":1,"clickhouse_version":"25.8.29.51","cases":[{"id":"alias","family":"binding","source":"client","schema":"CREATE TABLE events (id UInt64) ENGINE=Memory;","sql":"SELECT toUInt64(e.id) AS first_id, first_id AS event_id FROM events e WHERE event_id > 1 ORDER BY event_id","expected_server":"accept"}]}`
	if err := os.WriteFile(path, []byte(corpus), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), cli, "coverage", "-corpus", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Cases []struct {
			Stages  map[string]struct{ Status string }
			Binding *struct {
				References []struct{ Table, Column, Type string }
			}
		}
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Cases) != 1 || report.Cases[0].Stages["bind"].Status != "passed" || report.Cases[0].Stages["generate"].Status != "passed" || report.Cases[0].Binding == nil {
		t.Fatalf("alias was not bound and generated: %s", out)
	}
	refs := report.Cases[0].Binding.References
	if len(refs) != 4 {
		t.Fatalf("missing alias references: %s", out)
	}
	for _, ref := range refs {
		if ref.Table != "events" || ref.Column != "id" || ref.Type != "UInt64" {
			t.Fatalf("alias lost its catalog identity: %s", out)
		}
	}
}

func TestColumnAliasBindingPreservesPublicQueryContract(t *testing.T) {
	const sql = "SELECT e.id AS event_id FROM events e WHERE event_id > 1 ORDER BY event_id;"
	queries, err := parsePublicQuery(t, "CREATE TABLE events (id UInt64) ENGINE=Memory;", "-- name: Read :many\n"+sql)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 || len(queries[0].Results) != 1 || queries[0].Results[0].SQLName != "event_id" || queries[0].Results[0].GoType != "uint64" || queries[0].SQL != sql {
		t.Fatalf("alias changed the query contract: %+v", queries)
	}
	if _, err := chgen.Generate("queries", queries); err != nil {
		t.Fatal(err)
	}
}

func TestComputedAliasChainKeepsItsExpressionType(t *testing.T) {
	const sql = "SELECT small_id AS value, toUInt32(id) AS small_id FROM events WHERE value > 1 ORDER BY value;"
	queries, err := parsePublicQuery(t, "CREATE TABLE events (id UInt64) ENGINE=Memory;", "-- name: Read :many\n"+sql)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries[0].Results) != 2 || queries[0].Results[0].GoType != "uint32" || queries[0].Results[1].GoType != "uint32" || queries[0].SQL != sql {
		t.Fatalf("alias inherited the source type instead of the expression type: %+v", queries)
	}
	if _, err := chgen.Generate("queries", queries); err != nil {
		t.Fatal(err)
	}
}

func TestCyclicProjectionAliasesFailAtBinding(t *testing.T) {
	for _, sql := range []string{
		"SELECT second_id AS first_id, first_id AS second_id FROM events;",
		"SELECT toUInt64(value) AS value FROM events;",
	} {
		t.Run(sql, func(t *testing.T) {
			_, err := parsePublicQuery(t, "CREATE TABLE events (id UInt64) ENGINE=Memory;", "-- name: Read :many\n"+sql)
			if err == nil {
				t.Fatal("cyclic aliases accepted")
			}
			detail := chgen.ExplainError(err)
			if detail.Code != "ir-alias-cycle" || detail.Stage != "binding" || detail.Status != "invalid" {
				t.Fatalf("cycle was not diagnosed: %+v; %v", detail, err)
			}
		})
	}
}

func TestNestedIRScopesStillRejectMissingColumns(t *testing.T) {
	for _, sql := range []string{
		"SELECT (SELECT missing FROM events LIMIT 1) AS value FROM events;",
		"WITH missing AS value SELECT value FROM events;",
		"WITH source AS (SELECT missing FROM events) SELECT id FROM source;",
		"SELECT nested.missing FROM (SELECT id FROM events) AS nested;",
		"SELECT a.id FROM events a JOIN events b ON a.id = b.missing;",
	} {
		t.Run(sql, func(t *testing.T) {
			_, err := parsePublicQuery(t, "CREATE TABLE events (id UInt64) ENGINE=Memory;", "-- name: Read :many\n"+sql)
			if err == nil || !strings.Contains(err.Error(), `column "missing"`) {
				t.Fatalf("nested column error was lost: %v", err)
			}
		})
	}
}

func TestResultTypeAssertionStillWorksWithRelationCTE(t *testing.T) {
	queries, err := parsePublicQuery(t, "CREATE TABLE events (id UInt64) ENGINE=Memory;", "-- name: Read :many\n-- result-chtype: value UInt64\nWITH source AS (SELECT id FROM events) SELECT clientFunction(id) AS value FROM source;")
	if err != nil {
		t.Fatal(err)
	}
	if len(queries[0].Results) != 1 || queries[0].Results[0].GoType != "uint64" {
		t.Fatalf("asserted type was lost: %+v", queries)
	}
	if _, err := chgen.Generate("queries", queries); err != nil {
		t.Fatal(err)
	}
}

func TestScalarIRScopesKeepCardinalityAndVisibilityChecks(t *testing.T) {
	for _, sql := range []string{
		"SELECT (SELECT id FROM events) AS value FROM events",
		"SELECT (SELECT id, id FROM events LIMIT 1) AS value FROM events",
		"WITH second AS first, first AS second SELECT first AS value",
		"SELECT (SELECT r.id FROM events r WHERE r.id = e.id LIMIT 1) AS value FROM events e",
	} {
		t.Run(sql, func(t *testing.T) {
			_, err := parsePublicQuery(t, "CREATE TABLE events (id UInt64) ENGINE=Memory;", "-- name: Read :many\n"+sql)
			if err == nil {
				t.Fatal("invalid scalar query accepted")
			}
		})
	}
}

func TestWindowAndLambdaBindingKeepsValidation(t *testing.T) {
	for _, example := range []struct{ sql, message string }{
		{"SELECT sum(id) OVER (PARTITION BY missing) AS value FROM events", `column "missing"`},
		{"SELECT sum(id) OVER (ORDER BY missing) AS value FROM events", `column "missing"`},
		{"SELECT sum(id) OVER absent AS value FROM events", "window absent is not defined"},
		{"SELECT sum(id) OVER w AS value FROM events WINDOW w AS (other), other AS (w)", "recursive"},
		{"SELECT sum(id) OVER (ORDER BY id ROWS BETWEEN 1 FOLLOWING AND CURRENT ROW) AS value FROM events", "frame"},
		{"SELECT id FROM events WHERE row_number() OVER () > 1", "window"},
		{"SELECT arrayMap(x -> x + missing, values) AS value FROM events", `column "missing"`},
		{"SELECT arrayMap(x -> X, values) AS value FROM events", "X"},
		{"SELECT arrayMap((x, y) -> x + y, values) AS value FROM events", "lambda parameters"},
	} {
		t.Run(example.sql, func(t *testing.T) {
			_, err := parsePublicQuery(t, "CREATE TABLE events (id UInt64, values Array(Int32)) ENGINE=Memory;", "-- name: Read :many\n"+example.sql)
			if err == nil || !strings.Contains(err.Error(), example.message) {
				t.Fatalf("validation lost: want %q, got %v", example.message, err)
			}
		})
	}
}

func TestLambdaTupleElementsKeepLegacyTyping(t *testing.T) {
	queries, err := parsePublicQuery(t, "CREATE TABLE events (values Array(Tuple(value UInt64))) ENGINE=Memory;", "-- name: Read :many\nSELECT arrayMap(x -> tupleElement(x, 1), values) AS mapped FROM events;")
	if err != nil {
		t.Fatal(err)
	}
	if len(queries[0].Results) != 1 || queries[0].Results[0].GoType != "[]uint64" {
		t.Fatalf("lambda tuple field type changed: %+v", queries)
	}
	if _, err := chgen.Generate("queries", queries); err != nil {
		t.Fatal(err)
	}
}

func TestLambdaBindingReportsCapturesNotLocalParameters(t *testing.T) {
	cli := buildPublicCLI(t)
	path := filepath.Join(t.TempDir(), "corpus.json")
	corpus := `{"version":1,"clickhouse_version":"25.8.29.51","cases":[{"id":"lambda","family":"binding","source":"client","schema":"CREATE TABLE events (id UInt64, values Array(Int32)) ENGINE=Memory;","sql":"SELECT arrayMap(id -> id + events.id, values) AS mapped FROM events","expected_server":"accept"}]}`
	if err := os.WriteFile(path, []byte(corpus), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), cli, "coverage", "-corpus", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Cases []struct {
			Binding *struct {
				References []struct{ Table, Column, Type string }
			}
		}
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Cases) != 1 || report.Cases[0].Binding == nil {
		t.Fatalf("no lambda binding report: %s", out)
	}
	references := report.Cases[0].Binding.References
	if len(references) != 2 || references[0].Table != "events" || references[0].Column != "id" || references[0].Type != "UInt64" || references[1].Table != "events" || references[1].Column != "values" || references[1].Type != "Array(Int32)" {
		t.Fatalf("lambda parameter became a catalog reference: %s", out)
	}
}

func TestGroupedIRBindingKeepsAggregateValidation(t *testing.T) {
	for _, sql := range []string{
		"SELECT id, count() AS rows FROM events GROUP BY missing",
		"SELECT id, count() AS rows FROM events GROUP BY id HAVING missing > 0",
		"SELECT id FROM events GROUP BY count()",
		"SELECT id, count() AS rows FROM events",
		"SELECT id FROM events WHERE count() > 0",
	} {
		t.Run(sql, func(t *testing.T) {
			_, err := parsePublicQuery(t, "CREATE TABLE events (id UInt64) ENGINE=Memory;", "-- name: Read :many\n"+sql)
			if err == nil {
				t.Fatal("invalid grouped query accepted")
			}
			if strings.Contains(sql, "missing") && !strings.Contains(err.Error(), `column "missing"`) {
				t.Fatalf("missing column diagnostic lost: %v", err)
			}
		})
	}
}

func TestColumnAliasBindingDoesNotHideAnUnknownSource(t *testing.T) {
	_, err := parsePublicQuery(t, "CREATE TABLE events (id UInt64) ENGINE=Memory;", "-- name: Read :many\nSELECT e.missing AS event_id FROM events e WHERE event_id > 1;")
	if err == nil {
		t.Fatal("unknown alias source accepted")
	}
	detail := chgen.ExplainError(err)
	if detail.Code != "ir-column-missing" || detail.Stage != "binding" || !strings.Contains(err.Error(), `column "missing"`) {
		t.Fatalf("unknown alias source was masked: %+v; %v", detail, err)
	}
}

func TestAliasBindingPreservesLegacyPrecedenceBoundaries(t *testing.T) {
	cli := buildPublicCLI(t)
	for _, example := range []struct{ sql, bind, generate string }{
		{"SELECT arrayMap(x -> (SELECT toUInt32(7)), values) AS value FROM events", "unknown", "not_run"},
		{"SELECT arraySum(id -> toUInt32(id), values) AS value FROM events", "passed", "passed"},
		{"SELECT arrayMap((x, y) -> x + y + id, values, values) AS value FROM events", "passed", "passed"},
		{"SELECT arrayMap(x -> arrayMap(y -> y + x + id, values), values) AS value FROM events", "passed", "passed"},
		{"SELECT sum(id) OVER ordered AS total FROM events WINDOW base AS (PARTITION BY id), ordered AS (base ORDER BY id)", "passed", "passed"},
		{"SELECT sum(id) OVER (ORDER BY id ROWS BETWEEN 1 PRECEDING AND CURRENT ROW) AS total FROM events", "passed", "passed"},
		{"SELECT row_number() OVER (PARTITION BY id ORDER BY id) AS position FROM events", "passed", "passed"},
		{"WITH toUInt64(100) AS x, inner_q AS (WITH x AS y, toInt16(7) AS x SELECT y AS value) SELECT value FROM inner_q", "passed", "passed"},
		{"WITH (SELECT count() FROM events) AS total SELECT id, total FROM events", "passed", "passed"},
		{"WITH later AS threshold, toUInt32(2) AS later SELECT threshold AS value", "passed", "passed"},
		{"SELECT id, (SELECT toUInt32(7)) AS value FROM events", "passed", "passed"},
		{"SELECT id, (SELECT count() FROM events) AS total FROM events", "passed", "passed"},
		{"WITH toUInt32(2) AS threshold SELECT id FROM events WHERE id > threshold", "passed", "passed"},
		{"SELECT toUInt32(id) AS key, count() AS rows FROM events GROUP BY key HAVING rows > 0 AND key > 1 ORDER BY key", "passed", "passed"},
		{"SELECT id AS id FROM events ORDER BY id", "passed", "passed"},
		{"SELECT id AS first_id, first_id AS second_id FROM events ORDER BY second_id", "passed", "passed"},
		{"SELECT id AS value, id AS value FROM events ORDER BY value", "unknown", "not_run"},
		{"SELECT toUInt64(id) AS value FROM events ORDER BY value", "passed", "passed"},
		{"SELECT arraySum(x -> toUInt32(x), values) AS value FROM events", "passed", "passed"},
		{"WITH source AS (SELECT id FROM events) SELECT source.id FROM source", "passed", "passed"},
		{"SELECT nested.id FROM (SELECT id FROM events) AS nested", "passed", "passed"},
		{"SELECT a.id FROM events a INNER JOIN events b ON a.id = b.id", "passed", "passed"},
		{"SELECT id FROM events a LEFT JOIN events b USING (id)", "passed", "passed"},
		{"WITH source AS (SELECT id FROM events) SELECT nested.id FROM (SELECT id FROM source) AS nested", "passed", "passed"},
		{"SELECT toUInt32(id) AS id FROM events WHERE id > 1 ORDER BY id", "passed", "passed"},
		{"SELECT id AS outer_id, nested.id AS found FROM events e CROSS JOIN (SELECT id FROM events r WHERE r.id = outer_id) AS nested", "passed", "passed"},
	} {
		t.Run(example.sql, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "corpus.json")
			corpus := `{"version":1,"clickhouse_version":"25.8.29.51","cases":[{"id":"legacy","family":"binding","source":"client","schema":"CREATE TABLE events (id UInt64, values Array(Int32)) ENGINE=Memory;","sql":` + strconv.Quote(example.sql) + `,"expected_server":"accept"}]}`
			if err := os.WriteFile(path, []byte(corpus), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.CommandContext(t.Context(), cli, "coverage", "-corpus", path).Output()
			if err != nil {
				t.Fatal(err)
			}
			var report struct {
				Cases []struct {
					Stages map[string]struct{ Status string }
				}
			}
			if err := json.Unmarshal(out, &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Cases) != 1 || report.Cases[0].Stages["bind"].Status != example.bind || report.Cases[0].Stages["generate"].Status != example.generate {
				t.Fatalf("legacy alias rules were lost: %s", out)
			}
		})
	}
}

func TestQualifiedWildcardSourceRangeUsesUTF8ByteOffsets(t *testing.T) {
	cli := buildPublicCLI(t)
	path := filepath.Join(t.TempDir(), "corpus.json")
	corpus := `{"version":1,"clickhouse_version":"25.8.29.51","cases":[{"id":"utf8","family":"binding","source":"client","schema":"CREATE TABLE events (id UInt64) ENGINE=Memory;","sql":"SELECT /*λ*/ e.* FROM events e","expected_server":"accept"}]}`
	if err := os.WriteFile(path, []byte(corpus), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), cli, "coverage", "-corpus", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Cases []struct {
			IR struct {
				Select struct {
					Items []struct {
						Expr struct {
							Kind string
							Span struct{ Start, End int }
						}
					}
				}
			}
		}
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatal(err)
	}
	expression := report.Cases[0].IR.Select.Items[0].Expr
	if expression.Kind != "wildcard" || expression.Span.Start != 14 || expression.Span.End != 17 {
		t.Fatalf("qualified wildcard range is not a complete UTF-8 byte range: %s", out)
	}
}

func TestFrontendStructureComparisonDoesNotConfuseSourceRangesWithSyntax(t *testing.T) {
	cli := buildPublicCLI(t)
	dir := t.TempDir()
	corpus := filepath.Join(dir, "corpus.json")
	if err := os.WriteFile(corpus, []byte(`{"version":1,"clickhouse_version":"25.8.29.51","cases":[{"id":"source","family":"binding","source":"client","sql":"SELECT number FROM numbers(3)","expected_server":"accept"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), cli, "coverage", "-corpus", corpus).Output()
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatal(err)
	}
	report["frontend"] = "spanless-candidate"
	var removeSpans func(any)
	removeSpans = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			delete(value, "span")
			for _, child := range value {
				removeSpans(child)
			}
		case []any:
			for _, child := range value {
				removeSpans(child)
			}
		}
	}
	removeSpans(report)
	candidate := filepath.Join(dir, "candidate.json")
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = exec.CommandContext(t.Context(), cli, "coverage", "-corpus", corpus, "-candidate", candidate).CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"status": "matched"`) {
		t.Fatalf("source provenance changed semantic comparison: %v: %s", err, out)
	}
}

func TestCoverageCLIExposesCompleteIRWithoutErasingUnknownProperties(t *testing.T) {
	cli := buildPublicCLI(t)
	path := filepath.Join(t.TempDir(), "corpus.json")
	data := `{"version":1,"clickhouse_version":"25.8.29.51","cases":[
	{"id":"filter","family":"series","source":"client","sql":"SELECT number AS value FROM numbers(3) WHERE number > 1 ORDER BY number","expected_server":"accept"},
	{"id":"distinct","family":"series","source":"client","sql":"SELECT DISTINCT number FROM numbers(3)","expected_server":"accept"},
 {"id":"ties","family":"series","source":"client","sql":"SELECT number FROM numbers(3) ORDER BY number LIMIT 1 WITH TIES","expected_server":"accept"}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), cli, "coverage", "-corpus", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Cases []struct {
			Stages map[string]struct {
				Status string `json:"status"`
			} `json:"stages"`
			IR *struct {
				Select struct {
					Where struct{ Kind, Value string } `json:"where"`
				} `json:"select"`
			} `json:"ir"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Cases) != 3 || report.Cases[0].IR == nil || report.Cases[0].IR.Select.Where.Kind != "operator" || report.Cases[0].IR.Select.Where.Value != ">" || report.Cases[0].Stages["lower"].Status != "passed" || report.Cases[1].IR != nil || report.Cases[1].Stages["lower"].Status != "unknown" || report.Cases[2].IR != nil || report.Cases[2].Stages["lower"].Status != "unknown" {
		t.Fatalf("incomplete IR was claimed as complete: %s", out)
	}
}

func TestCoverageCLIRejectsLostPredicateInCandidate(t *testing.T) {
	cli := buildPublicCLI(t)
	dir := t.TempDir()
	corpus := filepath.Join(dir, "corpus.json")
	if err := os.WriteFile(corpus, []byte(`{"version":1,"clickhouse_version":"25.8.29.51","cases":[{"id":"filter","family":"series","source":"client","sql":"SELECT number AS value FROM numbers(3) WHERE number > 1","expected_server":"accept"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), cli, "coverage", "-corpus", corpus).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	var candidate map[string]any
	if err := json.Unmarshal(out, &candidate); err != nil {
		t.Fatal(err)
	}
	candidate["frontend"] = "candidate-test"
	path := filepath.Join(dir, "candidate.json")
	for _, lost := range []bool{false, true} {
		if lost {
			delete(candidate["cases"].([]any)[0].(map[string]any)["ir"].(map[string]any)["select"].(map[string]any), "where")
		}
		data, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		out, err = exec.CommandContext(t.Context(), cli, "coverage", "-corpus", corpus, "-candidate", path).CombinedOutput()
		if !lost {
			if err != nil || !strings.Contains(string(out), `"status": "matched"`) {
				t.Fatalf("matching candidate: %v: %s", err, out)
			}
		} else {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), "/select/where") {
				t.Fatalf("lost WHERE accepted: %v: %s", err, out)
			}
		}
	}
	for _, mutate := range []func(map[string]any){
		func(r map[string]any) { r["corpus_sha256"] = "different" },
		func(r map[string]any) { r["unknown"] = true },
		func(r map[string]any) { r["compared_frontend"] = "already-combined" },
		func(r map[string]any) { r["cases"].([]any)[0].(map[string]any)["source"] = "different-source" },
		func(r map[string]any) { r["cases"].([]any)[0].(map[string]any)["ir"] = nil },
	} {
		var invalid map[string]any
		baseline, err := exec.CommandContext(t.Context(), cli, "coverage", "-corpus", corpus).Output()
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(baseline, &invalid); err != nil {
			t.Fatal(err)
		}
		invalid["frontend"] = "candidate-test"
		mutate(invalid)
		data, err := json.Marshal(invalid)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.CommandContext(t.Context(), cli, "coverage", "-corpus", corpus, "-candidate", path).CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Fatalf("invalid candidate accepted: %v: %s", err, out)
		}
	}
}

func TestCoverageCLIRejectsAmbiguousCorpus(t *testing.T) {
	cli := buildPublicCLI(t)
	for _, data := range []string{
		`{"version":1,"version":1,"clickhouse_version":"25.8.29.51","cases":[]}`,
		`{"version":1,"unknown":true,"clickhouse_version":"25.8.29.51","cases":[]}`,
		`{"version":1,"clickhouse_version":"25.8.29.51","cases":[{"id":"a","family":"f","source":"s","sql":"SELECT 1","expected_server":"accept"},{"id":"a","family":"f","source":"s","sql":"SELECT 2","expected_server":"accept"}]}`,
	} {
		path := filepath.Join(t.TempDir(), "corpus.json")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.CommandContext(t.Context(), cli, "coverage", "-corpus", path).CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Fatalf("ambiguous corpus accepted: %v\n%s", err, out)
		}
	}
}

func TestCoverageCLIComparesOrderedTypesAndRefusesWrongServerVersion(t *testing.T) {
	cli := buildPublicCLI(t)
	path := filepath.Join(t.TempDir(), "corpus.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"clickhouse_version":"25.8.29.51","cases":[{"id":"a","family":"select","source":"client","sql":"SELECT 1 AS value","expected_server":"accept"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var version atomic.Value
	version.Store("25.8.29.51")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "SELECT version()") {
			fmt.Fprintf(w, `{"data":[{"version":%q}]}`, version.Load())
		} else {
			io.WriteString(w, `{"data":[{"name":"value","type":"UInt64"}]}`)
		}
	}))
	defer server.Close()
	out, err := exec.CommandContext(t.Context(), cli, "coverage", "-corpus", path, "-server", server.URL).CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), `"status": "mismatch"`) {
		t.Fatalf("type mismatch not reported: %v\n%s", err, out)
	}
	version.Store("26.1")
	out, err = exec.CommandContext(t.Context(), cli, "coverage", "-corpus", path, "-server", server.URL).CombinedOutput()
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(out), "server answers 26.1") {
		t.Fatalf("wrong server version accepted: %v\n%s", err, out)
	}
}

func TestCheckDoesNotWriteGeneratedFiles(t *testing.T) {
	config, output := writeCheckProject(t, "CREATE TABLE events (id UInt64) ENGINE=Memory;",
		"-- name: Read :many\nSELECT id FROM events;")
	for _, existing := range []bool{false, true} {
		if existing {
			if err := os.Mkdir(filepath.Dir(output), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(output, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		report, err := chgen.Check(config)
		if err != nil {
			t.Fatal(err)
		}
		if !report.CanGenerate || report.Status != "confirmed" || report.Queries != 1 || report.Packages != 1 {
			t.Fatalf("unexpected report: %+v", report)
		}
		if existing {
			data, err := os.ReadFile(output)
			if err != nil || string(data) != "untouched" {
				t.Fatalf("check changed output: %q, %v", data, err)
			}
		} else if _, err := os.Stat(filepath.Dir(output)); !os.IsNotExist(err) {
			t.Fatalf("check created output directory: %v", err)
		}
	}
}

func TestCheckExplainsUnknownAndInvalidSeparately(t *testing.T) {
	for _, tc := range []struct{ name, sql, status, code string }{
		{"missing rule", "SELECT clientFunction(id) AS value FROM events", "unknown", "function-rule-missing"},
		{"unknown setting", "SELECT id FROM events SETTINGS client_setting=1", "unknown", "setting-unmeasured"},
		{"contract conflict", "-- result-chtype: value String\nSELECT id AS value FROM events", "invalid", "result-contract-conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, _ := writeCheckProject(t, "CREATE TABLE events (id UInt64) ENGINE=Memory;", "-- name: Read :many\n"+tc.sql)
			report, err := chgen.Check(config)
			if err == nil || report.CanGenerate || report.Status != tc.status || len(report.Diagnostics) != 1 {
				t.Fatalf("report=%+v; err=%v", report, err)
			}
			d := report.Diagnostics[0]
			if d.Code != tc.code || d.Status != tc.status || d.Package != "queries" || d.Query != "Read" || d.Line != 1 || filepath.Base(d.File) != "queries.sql" || d.Hint == "" {
				t.Fatalf("incomplete diagnostic: %+v", d)
			}
			if explained := chgen.ExplainError(err); explained != d {
				t.Fatalf("Check and ExplainError disagree: %+v != %+v", explained, d)
			}
		})
	}
}

func TestCheckDisclosesExplicitTrustWithoutBlockingGeneration(t *testing.T) {
	config, _ := writeCheckProject(t, "CREATE TABLE events (id UInt64) ENGINE=Memory;", `-- name: Read :many
-- result-chtype: value UInt64
-- chgen:unchecked-setting client_setting
SELECT clientFunction(id) AS value FROM events SETTINGS client_setting=1;`)
	report, err := chgen.Check(config)
	if err != nil || !report.CanGenerate || report.Status != "unknown" {
		t.Fatalf("report=%+v; err=%v", report, err)
	}
	if len(report.Diagnostics) != 2 || report.Diagnostics[0].Code != "result-type-asserted" || report.Diagnostics[1].Code != "setting-unchecked" {
		t.Fatalf("trust boundaries hidden: %+v", report.Diagnostics)
	}
	if err := chgen.Run(config); err != nil {
		t.Fatalf("disclosing trust changed normal generation: %v", err)
	}
}

func TestCheckDoesNotTreatVerifiedAnnotationsAsUnknown(t *testing.T) {
	config, _ := writeCheckProject(t, "CREATE TABLE events (id UInt64) ENGINE=Memory", `-- name: Read :many
-- result-chtype: value UInt64
-- chgen:unchecked-setting max_threads
SELECT id AS value FROM events SETTINGS max_threads=1;`)
	report, err := chgen.Check(config)
	if err != nil || !report.CanGenerate || report.Status != "confirmed" || len(report.Diagnostics) != 0 {
		t.Fatalf("known rules did not win over annotations: %+v; %v", report, err)
	}
}

func TestScopedExpressionContractFlowsThroughCTE(t *testing.T) {
	const ddl = "CREATE TABLE events (id UInt64) ENGINE=Memory;"
	const sql = `-- name: Read :many
WITH values AS (
  SELECT chgen.assumeType(intDiv(id, toUInt64(2)), 'UInt64') AS half FROM events
)
SELECT toInt64(half) AS value FROM values WHERE half > chgen.arg('After');`
	config, output := writeCheckProject(t, ddl, sql)
	report, err := chgen.Check(config)
	if err != nil || !report.CanGenerate || report.Status != "unknown" {
		t.Fatalf("report=%+v; err=%v", report, err)
	}
	if len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != "expression-type-asserted" {
		t.Fatalf("lost intermediate assertion provenance: %+v", report.Diagnostics)
	}
	queries, err := parsePublicQuery(t, ddl, sql)
	if err != nil {
		t.Fatal(err)
	}
	if queries[0].Results[0].GoType != "int64" || queries[0].Params[0].GoType != "uint64" {
		t.Fatalf("contract did not supply the CTE type: %+v", queries[0])
	}
	if err := chgen.Run(config); err != nil {
		t.Fatal(err)
	}
	generated, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(generated), "chgen.assumeType") || !strings.Contains(string(generated), "intDiv(id, toUInt64(2))") {
		t.Fatalf("source contract leaked or changed the SQL expression:\n%s", generated)
	}
}

func TestScopedExpressionContractKeepsLexicalScopeAndNullability(t *testing.T) {
	const ddl = "CREATE TABLE events (id UInt64, n Nullable(UInt64)) ENGINE=Memory;"
	const sql = `-- name: Read :many
WITH a AS (SELECT chgen.assumeType(intDiv(id, toUInt64(2)), 'UInt64') AS value FROM events),
b AS (SELECT chgen.assumeType(intDiv(n, toUInt64(2)), 'Nullable(UInt64)') AS value FROM events)
SELECT toInt64(a.value) AS plain, toInt64(b.value) AS nullable FROM a CROSS JOIN b;`
	queries, err := parsePublicQuery(t, ddl, sql)
	if err != nil {
		t.Fatal(err)
	}
	if queries[0].Results[0].GoType != "int64" || queries[0].Results[1].GoType != "*int64" {
		t.Fatalf("scopes or nullable wrappers were conflated: %+v", queries[0].Results)
	}
	if _, err := chgen.Generate("queries", queries); err != nil {
		t.Fatal(err)
	}
}

func TestScopedExpressionContractUsesLambdaScope(t *testing.T) {
	const ddl = "CREATE TABLE events (ids Array(UInt64)) ENGINE=Memory"
	const sql = `-- name: Read :many
SELECT arrayMap(x -> chgen.assumeType(intDiv(x, toUInt64(2)), 'UInt64'), ids) AS values FROM events;`
	config, _ := writeCheckProject(t, ddl, sql)
	report, err := chgen.Check(config)
	if err != nil || !report.CanGenerate || report.Status != "unknown" || len(report.Diagnostics) != 1 {
		t.Fatalf("lost lambda scope or assertion provenance: %+v, %v", report, err)
	}
}

func TestScopedExpressionContractAcceptsParameterizedTypes(t *testing.T) {
	const sql = `-- name: Read :many
SELECT chgen.assumeType(clientTime(id), 'DateTime64(3, \'UTC\')') AS at FROM events;`
	queries, err := parsePublicQuery(t, "CREATE TABLE events (id UInt64) ENGINE=Memory", sql)
	if err != nil {
		t.Fatal(err)
	}
	if got := queries[0].Results[0]; got.CHType.String() != "DateTime64(3, 'UTC')" || got.GoType != "time.Time" {
		t.Fatalf("parameterized contract: %+v", got)
	}
}

func TestScopedExpressionContractCannotHideErrors(t *testing.T) {
	for _, expr := range []string{
		"chgen.assumeType(id, 'String')",
		"chgen.assumeType(toInt64(id, id), 'Int64')",
		"chgen.assumeType(toInt64(intDiv(id, 2)), 'Int64')",
		"chgen.assumeType(intDiv(missing, 2), 'UInt64')",
		"chgen.assumeType(intDiv(anotherUnknown(id), 2), 'UInt64')",
		"chgen.assumeType(intDiv(id, 2), id)",
		"chgen.assumeType(intDiv(id, 2), 'UInt64 DEFAULT 1')",
		"chgen.assumeType(intDiv(id, 2), 'UInt64', 'String')",
		"chgen.assumeType(intDiv(id, 2))",
		"__chgen_assume_type(intDiv(id, 2), 'UInt64')",
		"bitShiftRight(chgen.assumeType(clientFunction(id), 'String'), 1)",
	} {
		t.Run(expr, func(t *testing.T) {
			_, err := parsePublicQuery(t, "CREATE TABLE events (id UInt64) ENGINE=Memory", "-- name: Read :many\nSELECT "+expr+" AS value FROM events;")
			if err == nil {
				t.Fatal("contract hid an invalid or unresolved expression")
			}
		})
	}
}

func TestScopedExpressionContractPreservesSQLData(t *testing.T) {
	const sql = `-- name: Read :many
SELECT chgen.assumeType(toUInt64(1), 'UInt64') AS value,
'chgen.assumeType(id, \'String\')' AS text
/* chgen.assumeType(broken */;`
	config, _ := writeCheckProject(t, "CREATE TABLE events (id UInt64) ENGINE=Memory", sql)
	report, err := chgen.Check(config)
	if err != nil || report.Status != "confirmed" {
		t.Fatalf("known contract or SQL data became an assumption: %+v, %v", report, err)
	}
	queries, err := parsePublicQuery(t, "CREATE TABLE events (id UInt64) ENGINE=Memory", sql)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(queries[0].SQL, "'chgen.assumeType(id, \\'String\\')'") || !strings.Contains(queries[0].SQL, "/* chgen.assumeType(broken */") {
		t.Fatalf("changed quoted data or comments: %s", queries[0].SQL)
	}
}

func TestScopedExpressionContractRejectsExec(t *testing.T) {
	_, err := parsePublicQuery(t, "CREATE TABLE events (id UInt64) ENGINE=Memory",
		"-- name: Write :exec\nINSERT INTO events (id) VALUES (chgen.assumeType(intDiv(toUInt64(4), toUInt64(2)), 'UInt64'));")
	if err == nil || !strings.Contains(err.Error(), "assumeType is supported only in :one and :many") {
		t.Fatalf("exec assertions must not silently evade provenance checks: %v", err)
	}
}

func TestScopedContractsGeneratedRuntime(t *testing.T) {
	const ddl = "CREATE TABLE scoped_events (id UInt64, n Nullable(UInt64)) ENGINE=Memory;"
	const sql = `-- name: Read :many
WITH values AS (SELECT id, chgen.assumeType(intDiv(n, toUInt64(2)), 'Nullable(UInt64)') AS half FROM scoped_events)
SELECT id, toInt64(half) AS value FROM values ORDER BY id;
-- name: WrongContract :many
SELECT chgen.assumeType(intDiv(id, toUInt64(2)), 'UInt32') AS value
FROM scoped_events WHERE id > chgen.arg('After');
-- name: Lambda :many
SELECT arrayMap(x -> chgen.assumeType(intDiv(x, toUInt64(2)), 'UInt64'), [id]) AS values
FROM scoped_events ORDER BY id;`
	queries, err := parsePublicQuery(t, ddl, sql)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := chgen.Generate("scopedcontracts", queries)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/scopedcontracts/runtime_test.go")
	if err != nil {
		t.Fatal(err)
	}
	runGeneratedRuntime(t, "scopedcontracts", generated, fixture)
}

func TestCheckPreservesIOErrorIdentity(t *testing.T) {
	_, err := chgen.Check(filepath.Join(t.TempDir(), "missing.yaml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost IO error identity: %v", err)
	}
	if chgen.ExplainError(err).Status != "unknown" {
		t.Fatalf("IO failure is not evidence of invalid SQL: %v", err)
	}
	if d := chgen.ExplainError(nil); d != (chgen.Diagnostic{}) {
		t.Fatalf("nil error: %+v", d)
	}
}

func buildPublicCLI(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chgen")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", path, "./cmd/chgen")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	return path
}

func TestCheckCLIJSONAndStrictTrustPolicy(t *testing.T) {
	cli := buildPublicCLI(t)
	config, outputPath := writeCheckProject(t, "CREATE TABLE events (id UInt64) ENGINE=Memory;", `-- name: Read :many
-- result-chtype: value UInt64
SELECT clientFunction(id) AS value FROM events;`)
	for _, strict := range []bool{false, true} {
		args := []string{"check", "-f", config, "-json"}
		if strict {
			args = append(args, "-require-confirmed")
		}
		output, err := exec.CommandContext(t.Context(), cli, args...).Output()
		if (err != nil) != strict {
			t.Fatalf("strict=%v: %v\n%s", strict, err, output)
		}
		var report chgen.CheckReport
		if err := json.Unmarshal(output, &report); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, output)
		}
		if !report.CanGenerate || report.Status != "unknown" {
			t.Fatalf("report=%+v", report)
		}
	}
	if _, err := os.Stat(filepath.Dir(outputPath)); !os.IsNotExist(err) {
		t.Fatalf("CLI wrote output: %v", err)
	}
}

func TestGenerateCLIExplainsTheUnknownRuleEscapeHatch(t *testing.T) {
	cli := buildPublicCLI(t)
	config, _ := writeCheckProject(t, "CREATE TABLE events (id UInt64) ENGINE=Memory", "-- name: Read :many\nSELECT clientFunction(id) AS value FROM events")
	output, err := exec.CommandContext(t.Context(), cli, "-f", config).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "[unknown/function-rule-missing]") || !strings.Contains(string(output), "chgen describe") {
		t.Fatalf("generation did not explain the escape hatch: %v\n%s", err, output)
	}
}

func TestCheckKeepsParserAndCatalogGapsUnknown(t *testing.T) {
	for _, tc := range []struct{ name, ddl, sql, code string }{
		{"query parser", "CREATE TABLE events (at DateTime64(3)) ENGINE=Memory", "SELECT at + INTERVAL 1 MICROSECOND AS value FROM events", "query-parser-refusal"},
		{"catalog operation", "CREATE TABLE events (id UInt64) ENGINE=Memory; TRUNCATE TABLE events;", "SELECT id FROM events", "schema-operation-unmodeled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, _ := writeCheckProject(t, tc.ddl, "-- name: Read :many\n"+tc.sql)
			report, err := chgen.Check(config)
			if err == nil || report.Status != "unknown" || len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != tc.code {
				t.Fatalf("report=%+v; err=%v", report, err)
			}
		})
	}
}

func TestUnknownComparisonDomainDoesNotBecomeAProvenBoolean(t *testing.T) {
	for _, typeName := range []string{"FutureScalar", "Array(FutureScalar)", "Tuple(FutureScalar)"} {
		t.Run(typeName, func(t *testing.T) {
			for _, expression := range []string{"a = b", "a = 'text'", "'text' = a"} {
				_, err := chgen.InferExpressionType("CREATE TABLE events (a "+typeName+", b "+typeName+") ENGINE=Memory", "events", expression)
				d := chgen.ExplainError(err)
				if err == nil || d.Status != "unknown" || d.Code != "comparison-domain-unmeasured" {
					t.Fatalf("%s: unknown comparison was accepted or mislabeled: %v; %+v", expression, err, d)
				}
			}
		})
	}
}

func TestDescribeCLIUsesOnlyExplicitServerAnalysis(t *testing.T) {
	cli := buildPublicCLI(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Query().Get("readonly") != "1" {
			t.Errorf("request is not constrained to readonly analysis: %s %s", r.Method, r.URL)
		}
		body, _ := io.ReadAll(r.Body)
		sql := string(body)
		if strings.HasPrefix(sql, "SELECT version()") {
			_, _ = fmt.Fprint(w, `{"data":[{"version":"25.8.29.51"}]}`)
		} else if strings.HasPrefix(sql, "DESCRIBE ") {
			_, _ = fmt.Fprint(w, `{"data":[{"name":"value","type":"UInt64"}]}`)
		} else {
			t.Errorf("executed source query instead of DESCRIBE: %s", sql)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "query.sql")
	if err := os.WriteFile(path, []byte("SELECT intDiv(toUInt64(8), toUInt64(2)) AS value; -- source"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.CommandContext(t.Context(), cli, "describe", "-server", server.URL, "-sql", path).CombinedOutput()
	if err != nil {
		t.Fatalf("describe: %v\n%s", err, output)
	}
	var report struct {
		Provenance    string                                    `json:"provenance"`
		ServerVersion string                                    `json:"server_version"`
		QuerySHA256   string                                    `json:"query_sha256"`
		Columns       []struct{ Name, Type, Annotation string } `json:"columns"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("decode: %v\n%s", err, output)
	}
	if report.Provenance != "server-analysis" || report.ServerVersion != "25.8.29.51" || len(report.QuerySHA256) != 64 || len(report.Columns) != 1 || report.Columns[0].Annotation != "-- result-chtype: value UInt64" {
		t.Fatalf("incorrect analysis report: %+v", report)
	}
	before := requests.Load()
	for _, sql := range []string{"DROP TABLE events", "SELECT 1; DROP TABLE events", "SELECT 1 FORMAT CSV"} {
		if err := os.WriteFile(path, []byte(sql), 0o600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.CommandContext(t.Context(), cli, "describe", "-server", server.URL, "-sql", path).CombinedOutput(); err == nil {
			t.Fatalf("accepted unsafe input %q: %s", sql, output)
		}
	}
	if requests.Load() != before {
		t.Fatal("invalid input contacted server")
	}
}

func TestDescribeLiveContractRoundTrip(t *testing.T) {
	server := os.Getenv("CHGEN_ORACLE_URL")
	if server == "" {
		t.Skip("set CHGEN_ORACLE_URL to a disposable test ClickHouse")
	}
	cli := buildPublicCLI(t)
	const sql = "SELECT intDiv(number, toUInt64(2)) AS bucket FROM numbers(3)"
	path := filepath.Join(t.TempDir(), "query.sql")
	if err := os.WriteFile(path, []byte(sql), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.CommandContext(t.Context(), cli, "describe", "-server", server, "-sql", path).CombinedOutput()
	if err != nil {
		t.Fatalf("describe: %v\n%s", err, output)
	}
	var report struct {
		ServerVersion string `json:"server_version"`
		Columns       []struct{ Name, Type, Annotation string }
	}
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatal(err)
	}
	if report.ServerVersion != chgen.MeasuredCHVersion || len(report.Columns) != 1 || report.Columns[0].Type != "UInt64" {
		t.Fatalf("wrong real-column analysis: %+v", report)
	}
	// Replace only the fixture relation with the same typed catalog column.
	// The original unknown expression stays intact and needs no new type rule.
	ddl := "CREATE TABLE events (number UInt64) ENGINE=Memory"
	querySQL := "SELECT intDiv(number, toUInt64(2)) AS bucket FROM events"
	config, _ := writeCheckProject(t, ddl, "-- name: Read :many\n"+querySQL)
	if _, err := chgen.Check(config); err == nil || chgen.ExplainError(err).Code != "function-rule-missing" {
		t.Fatalf("expected missing rule, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(config), "queries.sql"), []byte("-- name: Read :many\n"+report.Columns[0].Annotation+"\n"+querySQL), 0o600); err != nil {
		t.Fatal(err)
	}
	checked, err := chgen.Check(config)
	if err != nil || !checked.CanGenerate || checked.Status != "unknown" {
		t.Fatalf("server-derived contract not usable: %+v; %v", checked, err)
	}
	if err := chgen.Run(config); err != nil {
		t.Fatal(err)
	}
}

// Every directory is a complete client scenario, discovered without a second
// roster or count. These fixed witnesses complement (not replace) the oracle.
func TestUserScenarioCorpus(t *testing.T) {
	entries, err := os.ReadDir("testdata/scenarios")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("scenario corpus is empty")
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Fatalf("unexpected scenario entry %s", entry.Name())
		}
		t.Run(entry.Name(), func(t *testing.T) {
			read := func(name string) []byte {
				data, err := os.ReadFile(filepath.Join("testdata/scenarios", entry.Name(), name))
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			var expected struct {
				Code    string
				Results map[string][]string
			}
			if err := json.Unmarshal(read("expect.json"), &expected); err != nil {
				t.Fatal(err)
			}
			config, _ := writeCheckProject(t, string(read("schema.sql")), string(read("queries.sql")))
			report, err := chgen.Check(config)
			if expected.Code != "" {
				if err == nil || chgen.ExplainError(err).Code != expected.Code {
					t.Fatalf("want %s, got %+v; %v", expected.Code, report, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(filepath.Dir(config), "schema.sql")})
			if err != nil {
				t.Fatal(err)
			}
			queries, err := chgen.ParseQueryFiles([]string{filepath.Join(filepath.Dir(config), "queries.sql")}, catalogs)
			if err != nil {
				t.Fatal(err)
			}
			if len(queries) != len(expected.Results) {
				t.Fatalf("got %d queries, want %d", len(queries), len(expected.Results))
			}
			for _, query := range queries {
				want := expected.Results[query.Name]
				if len(query.Results) != len(want) {
					t.Fatalf("%s: missing expected results", query.Name)
				}
				for i, result := range query.Results {
					if result.CHType.String() != want[i] {
						t.Errorf("%s result %d: %s != %s", query.Name, i, result.CHType, want[i])
					}
				}
			}
			if err := chgen.Run(config); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDescribeDoesNotFollowRedirectsOrSuggestInvalidContracts(t *testing.T) {
	cli := buildPublicCLI(t)
	path := filepath.Join(t.TempDir(), "query.sql")
	if err := os.WriteFile(path, []byte("SELECT 1 AS value"), 0o600); err != nil {
		t.Fatal(err)
	}
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(destination.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	if _, err := exec.CommandContext(t.Context(), cli, "describe", "-server", redirector.URL, "-sql", path).CombinedOutput(); err == nil {
		t.Fatal("accepted a redirected server")
	}
	if redirected.Load() != 0 {
		t.Fatal("forwarded SQL or credentials to a redirect target")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.HasPrefix(string(body), "SELECT version()") {
			_, _ = fmt.Fprint(w, `{"data":[{"version":"25.8.29.51"}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":[{"name":"a.b","type":"UInt64","annotation":"unsafe"},{"name":"state","type":"AggregateFunction(sum, UInt64)"},{"name":"same","type":"String"},{"name":"same","type":"UInt64"}]}`)
	}))
	t.Cleanup(server.Close)
	output, err := exec.CommandContext(t.Context(), cli, "describe", "-server", server.URL, "-sql", path).CombinedOutput()
	if err != nil {
		t.Fatalf("describe: %v\n%s", err, output)
	}
	var report struct {
		Columns []struct{ Annotation, Note string }
	}
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Columns) != 4 {
		t.Fatalf("columns lost: %s", output)
	}
	for _, column := range report.Columns {
		if column.Annotation != "" || column.Note == "" {
			t.Fatalf("invalid contract proposed: %s", output)
		}
	}
}
