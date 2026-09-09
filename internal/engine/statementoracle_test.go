//go:build fuzzoracle

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IlyaGulya/chgen/internal/conformance"
)

// TestCatalogExchangeAgainstClickHouse checks both swapped definitions and rows.
func TestCatalogExchangeAgainstClickHouse(t *testing.T) {
	const ddl = `
CREATE TABLE serving (id UInt64)
ENGINE = MergeTree ORDER BY id;

CREATE TABLE staged (id String, workflow_path String)
ENGINE = MergeTree ORDER BY (workflow_path, id);
`
	parts := statementFixtureParts(ddl)
	oracle := execWitnessFixtureWithDDL(t, parts[0], "INSERT INTO serving VALUES (7)")
	for _, sql := range []string{parts[1], "INSERT INTO staged VALUES ('new', 'ci')"} {
		if _, err := oracle.exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	if version, err := oracle.exec("SELECT version()"); err != nil || strings.TrimSpace(version) != MeasuredCHVersion {
		t.Fatalf("server version = %q, error = %v", version, err)
	}
	const exchange = "EXCHANGE TABLES serving AND staged"
	if _, err := oracle.exec(exchange); err != nil {
		t.Fatal(err)
	}
	catalogs := catalogsFromDDL(t, ddl+exchange)
	for _, test := range []struct {
		sql  string
		want string
	}{
		{"SELECT id, workflow_path FROM serving", "new\tci"},
		{"SELECT id FROM staged", "7"},
	} {
		queries, err := parseQueriesWithCatalogs(t, "-- name: Read :many\n"+test.sql, catalogs)
		if err != nil {
			t.Fatal(err)
		}
		results, err := arrayJoinServerResults(oracle, test.sql)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != len(queries[0].Results) {
			t.Fatal("result width differs")
		}
		for i, result := range queries[0].Results {
			if results[i].Name != result.SQLName || results[i].Type != result.CHType.String() {
				t.Fatalf("server result %#v differs from %#v", results[i], result)
			}
		}
		if got, err := oracle.exec(test.sql + " FORMAT TabSeparated"); err != nil || strings.TrimSpace(got) != test.want {
			t.Fatalf("rows = %q, error = %v, want %q", got, err, test.want)
		}
	}
}

// TestStatementOracle measures expressions inside complete query contexts.
func TestStatementOracle(t *testing.T) {
	baseURL := os.Getenv("CHGEN_ORACLE_URL")
	if baseURL == "" {
		t.Skip("CHGEN_ORACLE_URL is not set; start a disposable ClickHouse and set the URL to run the oracle")
	}
	oracle := &chOracle{url: baseURL, client: &http.Client{Timeout: 60 * time.Second}}
	database := fmt.Sprintf("chgen_statement_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := oracle.adminExec("CREATE DATABASE " + database); err != nil {
		t.Fatalf("create run database: %v", err)
	}
	oracle.database = database
	t.Cleanup(func() {
		if _, err := oracle.adminExec("DROP DATABASE IF EXISTS " + database); err != nil {
			t.Logf("drop run database: %v", err)
		}
	})
	for _, statement := range statementFixtureParts(conformance.StatementFixtureDDL + conformance.StatementFixtureSeed) {
		if _, err := oracle.exec(statement); err != nil {
			t.Fatalf("prepare statement fixture with %q: %v", statement, err)
		}
	}
	runner := conformance.Runner{
		Server: conformance.NewHTTPServer(baseURL, database),
		InferResults: func(input conformance.Input) ([]conformance.RawResultType, error) {
			inferred, err := inferQueryResults(conformance.StatementFixtureDDL, input.Query)
			if err != nil {
				return nil, err
			}
			results := make([]conformance.RawResultType, 0, len(inferred))
			for _, result := range inferred {
				results = append(results, conformance.RawResultType{Name: result.SQLName, Type: result.CHType.String()})
			}
			return results, nil
		},
	}
	report, err := runner.Run(context.Background(), conformance.StatementFixtureDDL,
		conformance.StatementFixtureSeed, conformance.StatementMatrix())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if output := os.Getenv("CHGEN_STATEMENT_ORACLE_OUT"); output != "" {
		if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("statement contexts=%d cells=%d server_run=%d", len(conformance.StatementContexts()), len(report.Cells), report.Metadata.Server.ServerRun)
}

func statementFixtureParts(sql string) []string {
	var parts []string
	for _, part := range strings.Split(sql, ";") {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

// TestSettingsReadControlsAgainstClickHouse checks both the server result types
// and the executed row. This is a correctness witness, not a performance claim.
func TestSettingsReadControlsAgainstClickHouse(t *testing.T) {
	const ddl = "CREATE TABLE settings_ordered (id UInt64, value Int32) ENGINE = MergeTree ORDER BY id"
	oracle := execWitnessFixtureWithDDL(t, ddl, "INSERT INTO settings_ordered VALUES (3, 30), (1, 10), (2, 20)")
	version, err := oracle.exec("SELECT version()")
	if err != nil || strings.TrimSpace(version) != MeasuredCHVersion {
		t.Fatalf("server version = %q, error = %v; want %s", version, err, MeasuredCHVersion)
	}
	for _, settings := range []string{
		"optimize_read_in_order = 0, max_threads = 1",
		"optimize_read_in_order = 1, max_threads = 1",
		"optimize_read_in_order = true, max_threads = 2",
		"optimize_read_in_order = false, max_threads = 0",
		"optimize_read_in_order = 1, max_threads = 1, max_rows_to_read = 100",
	} {
		t.Run(settings, func(t *testing.T) {
			sql := "SELECT id, value FROM settings_ordered ORDER BY id LIMIT 1 SETTINGS " + settings
			header := "-- name: ReadFirst :one\n"
			if strings.Contains(settings, "max_rows_to_read") {
				header += uncheckedSettingDirective + " max_rows_to_read\n"
			}
			queries, err := parseQueriesWithDDL(t, ddl, header+sql)
			if err != nil {
				t.Fatal(err)
			}
			analysis, err := arrayJoinServerResults(oracle, sql)
			if err != nil {
				t.Fatal(err)
			}
			if len(analysis) != len(queries[0].Results) {
				t.Fatalf("server results = %#v; inferred = %#v", analysis, queries[0].Results)
			}
			for index, result := range queries[0].Results {
				if analysis[index].Name != result.SQLName || analysis[index].Type != result.CHType.String() {
					t.Fatalf("server result = %#v; inferred = %#v", analysis[index], result)
				}
			}
			got, err := oracle.exec(queries[0].SQL + " FORMAT TabSeparated")
			if err != nil || strings.TrimSpace(got) != "1\t10" {
				t.Fatalf("execution = %q, error = %v; want first row 1, 10", got, err)
			}
		})
	}
}
