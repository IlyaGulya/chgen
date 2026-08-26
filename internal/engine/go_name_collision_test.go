package engine

import (
	"strings"
	"testing"
)

func TestParseWithSchemaRejectsResultGoNameCollision(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE c (id UInt64, name String, val Float64);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	_, err = parseQueriesWithSchema(t, `-- name: D :one
SELECT name AS foo_bar, val AS foo__bar
FROM c
WHERE id = chgen.arg('ID')`, schema)
	if err == nil {
		t.Fatal("parseQueriesWithSchema() succeeded for colliding result field names")
	}
	for _, want := range []string{"foo_bar", "foo__bar", "FooBar"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

// TestParseRejectsAnnotatedResultGoNameCollision keeps the coverage of the
// removed annotation-only mode: two -- result annotations that export to one
// Go field are still refused, now on the catalog pipeline.
func TestParseRejectsAnnotatedResultGoNameCollision(t *testing.T) {
	_, err := parseQueriesWithDDL(t, `CREATE TABLE c (id UInt64, name String, val Float64);`, `-- name: D :one
-- param: ID uint64
-- result: FooBar foo_bar string
-- result: FooBar foo__bar float64
SELECT name AS foo_bar, val AS foo__bar FROM c WHERE id = chgen.arg('ID')`)
	if err == nil {
		t.Fatal("parseQueriesWithDDL() succeeded for colliding result field names")
	}
	if !strings.Contains(err.Error(), "FooBar") {
		t.Errorf("error = %q, want it to contain the colliding Go name", err)
	}
}

func TestParseWithSchemaRejectsParamGoNameCollision(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE c (id UInt64, name String);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	_, err = parseQueriesWithSchema(t, `-- name: D :one
SELECT name AS name
FROM c
WHERE id = chgen.arg('foo_bar') AND name = chgen.arg('foo__bar')`, schema)
	if err == nil {
		t.Fatal("parseQueriesWithSchema() succeeded for two argument names with one Go field")
	}
	for _, want := range []string{"foo_bar", "foo__bar", "FooBar"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}
