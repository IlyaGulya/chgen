package engine

import (
	"strings"
	"testing"
)

func rawPlaceholderTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events
(
    id UInt64,
    user_id UInt64,
    val Float64,
    ts DateTime64(3)
) ENGINE = MergeTree ORDER BY id;`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

func TestParseWithSchemaRejectsRawPlaceholder(t *testing.T) {
	schema := rawPlaceholderTestSchema(t)
	_, err := parseQueriesWithSchema(t, `-- name: Range :many
SELECT id
FROM events
WHERE ts >= ?`, schema)
	if err == nil {
		t.Fatal("parseQueriesWithSchema() succeeded for a raw positional placeholder")
	}
	for _, want := range []string{"raw positional placeholder", "chgen.arg('Name')"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

func TestParseFileWithSchemaReportsRawPlaceholderLocation(t *testing.T) {
	schema := rawPlaceholderTestSchema(t)
	dir := t.TempDir()
	path := writeQueryFile(t, dir, "queries.sql", `-- name: Range :many
SELECT id
FROM events
WHERE ts >= chgen.arg('Start')
  AND ts < ?
`)
	_, err := parseQueryFileWithSchema(t, path, schema)
	if err == nil {
		t.Fatal("parseQueryFileWithSchema() succeeded for a raw positional placeholder")
	}
	if want := path + ":5:"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err, want)
	}
}

func TestParseWithSchemaAllowsQuestionMarkInLiteralsAndComments(t *testing.T) {
	schema := rawPlaceholderTestSchema(t)
	_, err := parseQueriesWithSchema(t, `-- name: Find :many
SELECT id
FROM events
-- is this a placeholder? no
/* neither is this ? */
WHERE toString(id) = 'what?'
  AND toString(user_id) != 'why?'`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
}

// TestSchemaAwareRejectsRawPlaceholderWithAnnotations replaces the removed
// TestParseWithoutSchemaAllowsRawPlaceholder. The old test asserted that the
// annotation-only mode accepted a raw `?`. That mode no longer exists, and the
// only remaining mode refuses a raw `?` even when -- param annotations name
// it, thus the rule that survives is the refusal.
func TestSchemaAwareRejectsRawPlaceholderWithAnnotations(t *testing.T) {
	schema := rawPlaceholderTestSchema(t)
	_, err := parseQueriesWithSchema(t, `-- name: Find :many
-- param: ID uint64
-- result: Value val float64
SELECT val AS val FROM events WHERE id = ?`, schema)
	if err == nil {
		t.Fatal("parseQueriesWithSchema() accepted a raw positional placeholder")
	}
	if !strings.Contains(err.Error(), "raw positional placeholder") {
		t.Errorf("error = %q, want it to contain %q", err, "raw positional placeholder")
	}
}
