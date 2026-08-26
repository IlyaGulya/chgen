package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pipelineSchemaSQL = `
CREATE TABLE orders (order_id String, customer_id String) ENGINE = MergeTree ORDER BY order_id;
CREATE TABLE order_events (order_id String) ENGINE = MergeTree ORDER BY order_id;

-- chgen:external
CREATE TABLE order_keys (order_id String);
`

func pipelineCatalogs(t *testing.T) *SchemaCatalogs {
	t.Helper()
	dir := t.TempDir()
	path := writeSchemaFile(t, dir, "schema.sql", pipelineSchemaSQL)
	catalogs, err := ParseSchemaCatalogs([]string{path})
	if err != nil {
		t.Fatalf("ParseSchemaCatalogs: %v", err)
	}
	return catalogs
}

func writeQueryFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestParseQueryFilesRecordsFileAndLine(t *testing.T) {
	dir := t.TempDir()
	path := writeQueryFile(t, dir, "queries.sql", `-- a comment

-- name: ListOrders :many
SELECT order_id FROM orders;
`)
	queries, err := ParseQueryFiles([]string{path}, pipelineCatalogs(t))
	if err != nil {
		t.Fatalf("ParseQueryFiles: %v", err)
	}
	if len(queries) != 1 {
		t.Fatalf("expected 1 query, got %d", len(queries))
	}
	if queries[0].File != path || queries[0].Line != 3 {
		t.Fatalf("got %s:%d", queries[0].File, queries[0].Line)
	}
}

func TestParseQueryFilesDuplicateNameAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	first := writeQueryFile(t, dir, "a.sql", `-- name: ListOrders :many
SELECT order_id FROM orders;
`)
	second := writeQueryFile(t, dir, "b.sql", `
-- name: ListOrders :many
SELECT order_id FROM order_events;
`)
	_, err := ParseQueryFiles([]string{first, second}, pipelineCatalogs(t))
	want := second + ":2: duplicate query name ListOrders; first declared at " + first + ":1"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestParseQueryFilesRejectsDatabaseMarker(t *testing.T) {
	dir := t.TempDir()
	path := writeQueryFile(t, dir, "queries.sql", `-- name: ListOrders :many
SELECT order_id FROM __DATABASE__.orders;
`)
	_, err := ParseQueryFiles([]string{path}, pipelineCatalogs(t))
	want := path + ":1: query ListOrders: __DATABASE__ was removed; the database comes from the connection (clickhouse Auth.Database); use the unqualified name orders"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestParseQueryFilesRejectsQualifiedReference(t *testing.T) {
	dir := t.TempDir()
	path := writeQueryFile(t, dir, "queries.sql", `-- name: ListOrders :many
SELECT order_id FROM metrics.orders;
`)
	_, err := ParseQueryFiles([]string{path}, pipelineCatalogs(t))
	want := path + ":1: query ListOrders: qualified reference metrics.orders is not allowed; the database comes from the connection; use the unqualified name orders"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestParseQueryFilesRejectsQualifiedExecTarget(t *testing.T) {
	dir := t.TempDir()
	path := writeQueryFile(t, dir, "queries.sql", `-- name: InsertOrder :exec
INSERT INTO metrics.orders (order_id, customer_id) VALUES (chgen.arg('OrderID'), chgen.arg('CustomerID'));
`)
	_, err := ParseQueryFiles([]string{path}, pipelineCatalogs(t))
	if err == nil || !strings.Contains(err.Error(), "qualified reference metrics.orders is not allowed") {
		t.Fatalf("got %v", err)
	}
}

func TestParseQueryFilesUnknownTableSuggestion(t *testing.T) {
	dir := t.TempDir()
	path := writeQueryFile(t, dir, "queries.sql", `-- name: ListOrders :many
SELECT order_id FROM ordrs;
`)
	_, err := ParseQueryFiles([]string{path}, pipelineCatalogs(t))
	want := path + `:1: query ListOrders: table "ordrs" is not present in the schema or the query scope; did you mean "orders"?`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestParseQueryFilesUnknownExternalSchema(t *testing.T) {
	dir := t.TempDir()
	path := writeQueryFile(t, dir, "queries.sql", `-- name: ListOrders :many
SELECT order_id FROM orders WHERE order_id IN (SELECT order_id FROM chgen.external(order_kys));
`)
	_, err := ParseQueryFiles([]string{path}, pipelineCatalogs(t))
	want := path + `:1: chgen.external(order_kys): unknown external schema; declare it with "-- chgen:external" before its CREATE TABLE in a schema input; did you mean "order_keys"?`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestParseQueryFilesExternalOverPhysicalTable(t *testing.T) {
	dir := t.TempDir()
	path := writeQueryFile(t, dir, "queries.sql", `-- name: ListOrders :many
SELECT order_id FROM orders WHERE order_id IN (SELECT order_id FROM chgen.external(order_events));
`)
	_, err := ParseQueryFiles([]string{path}, pipelineCatalogs(t))
	want := path + `:1: chgen.external(order_events): unknown external schema; declare it with "-- chgen:external" before its CREATE TABLE in a schema input; a physical table "order_events" exists; chgen.external only accepts schemas marked -- chgen:external`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestParseQueryFilesExternalResolvesMarkedSchema(t *testing.T) {
	dir := t.TempDir()
	path := writeQueryFile(t, dir, "queries.sql", `-- name: ListOrders :many
SELECT order_id FROM orders WHERE order_id IN (SELECT order_id FROM chgen.external(order_keys));
`)
	queries, err := ParseQueryFiles([]string{path}, pipelineCatalogs(t))
	if err != nil {
		t.Fatalf("ParseQueryFiles: %v", err)
	}
	if len(queries[0].ExternalParams) != 1 || queries[0].ExternalParams[0].SchemaName != "order_keys" {
		t.Fatalf("external params: %+v", queries[0].ExternalParams)
	}
}

func TestParseQueryFilesPhysicalNeverResolvesToExternal(t *testing.T) {
	dir := t.TempDir()
	path := writeQueryFile(t, dir, "queries.sql", `-- name: ListKeys :many
SELECT order_id FROM order_keys;
`)
	_, err := ParseQueryFiles([]string{path}, pipelineCatalogs(t))
	if err == nil || !strings.Contains(err.Error(), `table "order_keys" is not present in the schema or the query scope`) {
		t.Fatalf("got %v", err)
	}
}

func TestParseQueryFilesUnknownColumnSuggestion(t *testing.T) {
	dir := t.TempDir()
	path := writeQueryFile(t, dir, "queries.sql", `-- name: ListOrders :many
SELECT ordr_id FROM orders;
`)
	_, err := ParseQueryFiles([]string{path}, pipelineCatalogs(t))
	if err == nil || !strings.Contains(err.Error(), `did you mean "order_id"?`) {
		t.Fatalf("got %v", err)
	}
}

func TestSuggestName(t *testing.T) {
	cases := []struct {
		name       string
		candidates []string
		want       string
	}{
		{"ordrs", []string{"orders", "order_events"}, "orders"},
		{"orsq", []string{"ordq", "orsz"}, ""}, // two candidates tie
		{"zzzz", []string{"orders"}, ""},       // too far away
		{"orders", []string{"orders"}, ""},     // exact name is not a suggestion
	}
	for _, testCase := range cases {
		if got := suggestName(testCase.name, testCase.candidates); got != testCase.want {
			t.Errorf("suggestName(%q, %v) = %q, want %q", testCase.name, testCase.candidates, got, testCase.want)
		}
	}
}

func TestParseQueryFilesUnknownExecTarget(t *testing.T) {
	dir := t.TempDir()
	path := writeQueryFile(t, dir, "queries.sql", `-- name: InsertOrder :exec
INSERT INTO ordrs (order_id) VALUES (chgen.arg('OrderID'));
`)
	_, err := ParseQueryFiles([]string{path}, pipelineCatalogs(t))
	if err == nil || !strings.Contains(err.Error(), `table "ordrs" is not present in the schema or the query scope; did you mean "orders"?`) {
		t.Fatalf("got %v", err)
	}
}

func TestParseQueryFilesRejectsQualifiedReferenceInFixedCommands(t *testing.T) {
	// The check walks every table identifier, so it also covers statements that
	// have no FROM clause. The advice must not name a clause that the statement
	// does not have.
	for _, testCase := range []struct {
		name string
		sql  string
	}{
		{
			name: "insert",
			sql:  "-- name: WriteOrder :exec\nINSERT INTO metrics.orders (order_id) VALUES (chgen.arg('OrderID'))",
		},
		{
			name: "alter update",
			sql:  "-- name: SetStatus :exec\nALTER TABLE metrics.orders UPDATE order_id = chgen.arg('OrderID') WHERE order_id = chgen.arg('OrderID')",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeQueryFile(t, dir, "queries.sql", testCase.sql)

			_, err := ParseQueryFiles([]string{path}, pipelineCatalogs(t))
			if err == nil {
				t.Fatal("ParseQueryFiles() error = nil, want a qualified-reference error")
			}
			if !strings.Contains(err.Error(), "use the unqualified name orders") {
				t.Fatalf("got %v, want the unqualified-name advice", err)
			}
			if strings.Contains(err.Error(), "write FROM") {
				t.Fatalf("got %v, want no FROM advice for a statement without a FROM clause", err)
			}
		})
	}
}

func TestParseQueryFilesRejectsRemovedIncludeDirective(t *testing.T) {
	// The -- chgen:external-schema directive was removed. A file that still
	// carries it must say so directly, because the later "unknown external
	// schema" error does not tell the reader what to do with the stale line.
	dir := t.TempDir()
	queryPath := filepath.Join(dir, "queries.sql")
	source := `-- Header comment.
-- chgen:external-schema external_tables.sql

-- name: ReadOrders :many
SELECT order_id FROM orders`
	if err := os.WriteFile(queryPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ParseQueryFiles([]string{queryPath}, pipelineCatalogs(t))
	want := queryPath + `:2: the -- chgen:external-schema directive was removed; ` +
		`delete this line, add "external_tables.sql" to schema in chgen.yaml, ` +
		`and mark each row schema with -- chgen:external before its CREATE TABLE`
	if err == nil || err.Error() != want {
		t.Fatalf("ParseQueryFiles() error = %v, want %q", err, want)
	}
}

func TestParseQueryFilesRejectsRemovedIncludeDirectiveWithoutPath(t *testing.T) {
	// The bare directive names no file, so the message must not invent one.
	dir := t.TempDir()
	queryPath := filepath.Join(dir, "queries.sql")
	source := "-- chgen:external-schema\n\n-- name: ReadOrders :many\nSELECT order_id FROM orders"
	if err := os.WriteFile(queryPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ParseQueryFiles([]string{queryPath}, pipelineCatalogs(t))
	want := queryPath + `:1: the -- chgen:external-schema directive was removed; ` +
		`delete this line, add the external schema file to schema in chgen.yaml, ` +
		`and mark each row schema with -- chgen:external before its CREATE TABLE`
	if err == nil || err.Error() != want {
		t.Fatalf("ParseQueryFiles() error = %v, want %q", err, want)
	}
}

func TestParseQueryFilesKeepsDirectiveTextInsideStringLiteral(t *testing.T) {
	// The check is lexical, like chgen.arg: the phrase inside a SQL literal is
	// data, not a directive, so it must not fail the file.
	dir := t.TempDir()
	queryPath := filepath.Join(dir, "queries.sql")
	source := `-- name: ReadOrders :many
SELECT order_id, '-- chgen:external-schema x.sql' AS marker FROM orders`
	if err := os.WriteFile(queryPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	queries, err := ParseQueryFiles([]string{queryPath}, pipelineCatalogs(t))
	if err != nil {
		t.Fatalf("ParseQueryFiles() error = %v, want success", err)
	}
	if len(queries) != 1 {
		t.Fatalf("got %d queries, want 1", len(queries))
	}
}
