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
