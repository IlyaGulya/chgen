package engine

import (
	"strings"
	"testing"
)

// clickhouse-go scans Array(Nullable(T)) only into []*T and
// Map(K, Nullable(V)) only into map[K]*V. Dropping the pointer generates
// code that fails at rows.Scan.

func TestParseWithSchemaKeepsNullableArrayElement(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events (id UInt64, tags Array(Nullable(String)));`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadTags :one
SELECT tags
FROM events
WHERE id = chgen.arg('ID')`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Results[0].GoType, "[]*string"; got != want {
		t.Errorf("Array(Nullable(String)) Go type = %q, want %q", got, want)
	}
}

func TestParseWithSchemaKeepsNullableMapValue(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events (id UInt64, attrs Map(String, Nullable(UInt64)));`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadAttrs :one
SELECT attrs
FROM events
WHERE id = chgen.arg('ID')`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Results[0].GoType, "map[string]*uint64"; got != want {
		t.Errorf("Map(String, Nullable(UInt64)) Go type = %q, want %q", got, want)
	}
}

func TestParseWithSchemaRejectsNullableMapKey(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events (id UInt64, attrs Map(Nullable(String), UInt64));`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	_, err = parseQueriesWithSchema(t, `-- name: ReadAttrs :one
SELECT attrs
FROM events
WHERE id = chgen.arg('ID')`, schema)
	if err == nil {
		t.Fatal("parseQueriesWithSchema() succeeded for a Nullable map key")
	}
	if !strings.Contains(err.Error(), "Nullable") || !strings.Contains(err.Error(), "key") {
		t.Errorf("error = %q, want it to mention the Nullable map key", err)
	}
}
