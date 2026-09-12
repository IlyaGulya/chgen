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
