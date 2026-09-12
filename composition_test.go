package chgen_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen"
)

func TestComposedPaginationGeneratedRuntime(t *testing.T) {
	const ddl = `CREATE TABLE paged_spans (
scope_type String, scope_key String, id String, run_span_id String, version UInt64, payload String
) ENGINE=ReplacingMergeTree(version) ORDER BY (scope_type, scope_key, id);
CREATE TABLE paged_spans_archive (
scope_type String, scope_key String, id String, run_span_id String, version UInt64, payload String
) ENGINE=ReplacingMergeTree(version) ORDER BY (scope_type, scope_key, id);
-- chgen:external
CREATE TABLE requested_runs (id String);`
	const sql = `-- name: ReadPage :many
-- chgen:table Source paged_spans paged_spans_archive
SELECT id, payload FROM chgen.table('Source') FINAL
WHERE scope_type = chgen.arg('ScopeType') AND scope_key = chgen.arg('ScopeKey')
AND id IN (
  SELECT id FROM chgen.table('Source') FINAL
  WHERE scope_type = chgen.arg('ScopeType') AND scope_key = chgen.arg('ScopeKey')
  AND (id IN (SELECT id FROM chgen.external('Keys', requested_runs))
    OR run_span_id IN (SELECT id FROM chgen.external('Keys', requested_runs)))
)
-- chgen:if After
AND id > chgen.arg('AfterID')
-- chgen:end
ORDER BY id LIMIT chgen.arg('PageRows');
-- name: Filtered :many
-- result-capacity: Keys
SELECT id, payload FROM paged_spans FINAL
WHERE scope_type=chgen.arg('ScopeType') AND scope_key=chgen.arg('ScopeKey')
-- chgen:if Requested
AND id IN (SELECT id FROM chgen.external('Keys', requested_runs))
-- chgen:end
-- chgen:if After
AND id > chgen.arg('AfterID') AND scope_key=chgen.arg('ScopeKey')
-- chgen:end
ORDER BY id;
-- name: TimeProbe :one
-- param: At time.Time
SELECT CAST('2024-01-01 00:00:00' AS DateTime64(3, 'UTC')) AS value
-- chgen:if After
WHERE value > chgen.arg('At')
-- chgen:end
;`
	queries, err := parsePublicQuery(t, ddl, sql)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := chgen.Generate("composition", queries)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/composition/runtime_test.go")
	if err != nil {
		t.Fatal(err)
	}
	runGeneratedRuntime(t, "composition", generated, fixture)
}

func TestCompositionChecksEveryVariant(t *testing.T) {
	const ddl = `CREATE TABLE a (id UInt64, value UInt64) ENGINE=Memory;
CREATE TABLE b (id UInt64, value String) ENGINE=Memory;
-- chgen:external
CREATE TABLE ext_keys (id UInt64);`
	for _, body := range []string{
		"-- chgen:if After\nSELECT id FROM a",
		"SELECT id FROM a\n-- chgen:if After\nWHERE missing = 1\n-- chgen:end",
		"SELECT id\n-- chgen:if Extra\n,value\n-- chgen:end\nFROM a",
		"-- chgen:table Source a b\nSELECT value FROM chgen.table('Source')",
		"-- chgen:table Source a b\nSELECT id FROM chgen.table('Source') WHERE value = chgen.arg('Value')",
		"-- chgen:table Source a absent\nSELECT id FROM chgen.table('Source')",
		"-- chgen:table Source ext_keys\nSELECT id FROM chgen.table('Source')",
		"-- chgen:table Source a\nSELECT id FROM a",
		"SELECT id FROM chgen.table('Undeclared')",
		"SELECT id FROM a\n-- chgen:if After\nWHERE id=1",
		"SELECT id FROM a\n-- chgen:end",
		"SELECT id FROM a\n-- chgen:if After\nWHERE id=1\n-- chgen:else\nAND id=2\n-- chgen:end",
		"SELECT id FROM a\n-- chgen:if A\n-- chgen:if B\nWHERE id=1\n-- chgen:end\n-- chgen:end",
		"SELECT id FROM a WHERE id=chgen.arg('After')\n-- chgen:if After\nAND id=1\n-- chgen:end",
	} {
		t.Run(body, func(t *testing.T) {
			config, _ := writeCheckProject(t, ddl, "-- name: Read :many\n"+body)
			report, err := chgen.Check(config)
			if err == nil || report.CanGenerate {
				t.Fatalf("accepted an unchecked or inconsistent variant: %+v", report)
			}
		})
	}
}

func TestCompositionKeepsAssumptionProvenance(t *testing.T) {
	const ddl = "CREATE TABLE a (id UInt64) ENGINE=Memory"
	const sql = `-- name: Read :many
SELECT id FROM a
-- chgen:if Filter
WHERE chgen.assumeType(intDiv(id, toUInt64(2)), 'UInt64') > chgen.arg('After')
-- chgen:end
ORDER BY id;`
	config, _ := writeCheckProject(t, ddl, sql)
	report, err := chgen.Check(config)
	if err != nil || !report.CanGenerate || report.Status != "unknown" || len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != "expression-type-asserted" {
		t.Fatalf("the default variant hid an optional assertion: %+v, %v", report, err)
	}
	cli := buildPublicCLI(t)
	output, err := exec.CommandContext(t.Context(), cli, "check", "-f", config, "-json", "-require-confirmed").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "expression-type-asserted") {
		t.Fatalf("strict check hid an optional assertion: %s, %v", output, err)
	}
}

func TestCompositionRejectsGeneratedNameCollisions(t *testing.T) {
	const ddl = "CREATE TABLE a_b (id UInt64) ENGINE=Memory; CREATE TABLE aB (id UInt64) ENGINE=Memory;"
	for _, sql := range []string{
		"-- name: Read :many\n-- chgen:table Source a_b aB\nSELECT id FROM chgen.table('Source')",
		"-- name: Read :many\nSELECT id FROM a_b\n-- chgen:if Page\nWHERE id=1\n-- chgen:end\n-- name: ReadPage :many\nSELECT id FROM a_b",
	} {
		config, _ := writeCheckProject(t, ddl, sql)
		if report, err := chgen.Check(config); err == nil || report.CanGenerate {
			t.Fatalf("generated duplicate declarations: %+v", report)
		}
	}
}

func TestCompositionLimitsAndLexicalBoundaries(t *testing.T) {
	var sql strings.Builder
	sql.WriteString("-- name: Read :many\nSELECT id FROM a\nWHERE 1\n")
	for _, option := range []string{"A", "B", "C", "D", "E", "F"} {
		sql.WriteString("-- chgen:if " + option + "\nAND id=1\n-- chgen:end\n")
	}
	if _, err := parsePublicQuery(t, "CREATE TABLE a (id UInt64) ENGINE=Memory", sql.String()); err == nil || !strings.Contains(err.Error(), "32 fully checked variants") {
		t.Fatalf("variant explosion was not bounded: %v", err)
	}
	const literal = `-- name: Read :many
SELECT 'a
-- chgen:if Hidden
-- chgen:table Source absent
-- chgen:end
' AS text FROM a
/*
-- chgen:if Hidden
*/
-- chgen:if Active
WHERE id=chgen.arg('ID')
-- chgen:end
;`
	config, output := writeCheckProject(t, "CREATE TABLE a (id UInt64) ENGINE=Memory", literal)
	if err := chgen.Run(config); err != nil {
		t.Fatal(err)
	}
	code, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(code), "-- chgen:table Source absent") || strings.Contains(string(code), "ReadHiddenParams") || !strings.Contains(string(code), "ReadActiveParams") {
		t.Fatalf("interpreted SQL data as composition directives:\n%s", code)
	}
}
