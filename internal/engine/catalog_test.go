package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSchemaFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestParseSchemaCatalogsSplitsMarkedTables(t *testing.T) {
	dir := t.TempDir()
	path := writeSchemaFile(t, dir, "schema.sql", `
CREATE TABLE orders (order_id String) ENGINE = MergeTree ORDER BY order_id;

-- chgen:external
CREATE TABLE order_keys
(
    order_id String
);
`)
	catalogs, err := ParseSchemaCatalogs([]string{path})
	if err != nil {
		t.Fatalf("ParseSchemaCatalogs: %v", err)
	}
	if _, ok := catalogs.Physical.Tables["orders"]; !ok {
		t.Errorf("orders missing from the physical catalog")
	}
	if _, ok := catalogs.Physical.Tables["order_keys"]; ok {
		t.Errorf("order_keys must not be in the physical catalog")
	}
	if _, ok := catalogs.External.Tables["order_keys"]; !ok {
		t.Errorf("order_keys missing from the external catalog")
	}
	if _, ok := catalogs.External.Tables["orders"]; ok {
		t.Errorf("orders must not be in the external catalog")
	}
}

func TestParseSchemaCatalogsMarkerAllowsCommentBlock(t *testing.T) {
	dir := t.TempDir()
	path := writeSchemaFile(t, dir, "schema.sql", `
-- chgen:external

-- A reusable key list.
-- Sent with the query over the native protocol.
CREATE TABLE order_keys (order_id String);
`)
	catalogs, err := ParseSchemaCatalogs([]string{path})
	if err != nil {
		t.Fatalf("ParseSchemaCatalogs: %v", err)
	}
	if _, ok := catalogs.External.Tables["order_keys"]; !ok {
		t.Errorf("order_keys missing from the external catalog")
	}
}

func TestParseSchemaCatalogsDanglingMarker(t *testing.T) {
	dir := t.TempDir()
	path := writeSchemaFile(t, dir, "schema.sql", `-- chgen:external
ALTER TABLE orders ADD COLUMN c String;
`)
	_, err := ParseSchemaCatalogs([]string{path})
	want := path + ":1: -- chgen:external marker is not followed by CREATE TABLE"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestParseSchemaCatalogsMarkedTableWithStorageClauses(t *testing.T) {
	dir := t.TempDir()
	path := writeSchemaFile(t, dir, "schema.sql", `-- chgen:external
CREATE TABLE order_keys (order_id String) ENGINE = MergeTree ORDER BY order_id;
`)
	_, err := ParseSchemaCatalogs([]string{path})
	want := path + `:2: external schema "order_keys" must not declare ENGINE/ORDER BY/PARTITION BY/TTL; an external schema is a column list only`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestParseSchemaCatalogsAlterAgainstExternal(t *testing.T) {
	dir := t.TempDir()
	path := writeSchemaFile(t, dir, "schema.sql", `-- chgen:external
CREATE TABLE order_keys (order_id String);

ALTER TABLE order_keys ADD COLUMN c String;
`)
	_, err := ParseSchemaCatalogs([]string{path})
	want := path + `:4: ALTER TABLE "order_keys" targets an external schema; edit its CREATE TABLE instead`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestParseSchemaCatalogsExternalPhysicalCollision(t *testing.T) {
	dir := t.TempDir()
	first := writeSchemaFile(t, dir, "a.sql", `CREATE TABLE order_keys (order_id String) ENGINE = MergeTree ORDER BY order_id;
`)
	second := writeSchemaFile(t, dir, "b.sql", `-- chgen:external
CREATE TABLE order_keys (order_id String);
`)
	_, err := ParseSchemaCatalogs([]string{first, second})
	want := second + `:2: external schema "order_keys" collides with physical table "order_keys" declared at ` + first + ":1; rename one of them"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestParseSchemaCatalogsPhysicalAfterExternalCollision(t *testing.T) {
	dir := t.TempDir()
	first := writeSchemaFile(t, dir, "a.sql", `-- chgen:external
CREATE TABLE order_keys (order_id String);
`)
	second := writeSchemaFile(t, dir, "b.sql", `CREATE TABLE order_keys (order_id String) ENGINE = MergeTree ORDER BY order_id;
`)
	_, err := ParseSchemaCatalogs([]string{first, second})
	if err == nil || !strings.Contains(err.Error(), "collides with") {
		t.Fatalf("got %v", err)
	}
}

func TestParseSchemaCatalogsSkipsSchemaMigrations(t *testing.T) {
	dir := t.TempDir()
	path := writeSchemaFile(t, dir, "schema.sql", `
CREATE TABLE schema_migrations (version Int64, dirty UInt8) ENGINE = MergeTree ORDER BY version;
ALTER TABLE schema_migrations ADD COLUMN note String;
CREATE TABLE orders (order_id String) ENGINE = MergeTree ORDER BY order_id;
`)
	catalogs, err := ParseSchemaCatalogs([]string{path})
	if err != nil {
		t.Fatalf("ParseSchemaCatalogs: %v", err)
	}
	if _, ok := catalogs.Physical.Tables["schema_migrations"]; ok {
		t.Errorf("schema_migrations must not enter the catalog")
	}
	if _, ok := catalogs.Physical.Tables["orders"]; !ok {
		t.Errorf("orders missing")
	}
}

func TestParseSchemaCatalogsDuplicateCreateTable(t *testing.T) {
	dir := t.TempDir()
	first := writeSchemaFile(t, dir, "a.sql", `CREATE TABLE orders (order_id String) ENGINE = MergeTree ORDER BY order_id;
`)
	second := writeSchemaFile(t, dir, "b.sql", `
CREATE TABLE orders (order_id String) ENGINE = MergeTree ORDER BY order_id;
`)
	_, err := ParseSchemaCatalogs([]string{first, second})
	want := second + `:2: duplicate CREATE TABLE "orders"; first declared at ` + first + ":1"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestParseSchemaCatalogsUnsupportedAlterClause(t *testing.T) {
	dir := t.TempDir()
	path := writeSchemaFile(t, dir, "schema.sql", `CREATE TABLE orders (order_id String) ENGINE = MergeTree ORDER BY order_id;
ALTER TABLE orders RENAME COLUMN order_id TO id;
`)
	_, err := ParseSchemaCatalogs([]string{path})
	want := path + ":2: RENAME COLUMN is not supported; supported ALTER TABLE operations: ADD COLUMN, MODIFY COLUMN, DROP COLUMN"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestParseSchemaCatalogsRejectsNonSchemaStatement(t *testing.T) {
	dir := t.TempDir()
	path := writeSchemaFile(t, dir, "schema.sql", `CREATE TABLE orders (order_id String) ENGINE = MergeTree ORDER BY order_id;
SELECT 1;
`)
	_, err := ParseSchemaCatalogs([]string{path})
	want := path + ":2: statement is not CREATE TABLE or a supported ALTER TABLE; move non-schema SQL out of the schema inputs"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestParseSchemaCatalogsIgnoresViewsAndSeedInserts(t *testing.T) {
	dir := t.TempDir()
	path := writeSchemaFile(t, dir, "schema.sql", `
CREATE TABLE orders (order_id String, note String) ENGINE = MergeTree ORDER BY order_id;
CREATE VIEW order_view AS SELECT order_id FROM orders;
INSERT INTO orders (order_id, note) VALUES ('a', 'seed');
`)
	catalogs, err := ParseSchemaCatalogs([]string{path})
	if err != nil {
		t.Fatalf("ParseSchemaCatalogs: %v", err)
	}
	if len(catalogs.Physical.Tables) != 1 {
		t.Errorf("expected 1 physical table, got %d", len(catalogs.Physical.Tables))
	}
}

func TestParseSchemaCatalogsAppliesAlterDeltas(t *testing.T) {
	dir := t.TempDir()
	first := writeSchemaFile(t, dir, "a.sql", `CREATE TABLE orders (order_id String) ENGINE = MergeTree ORDER BY order_id;
`)
	second := writeSchemaFile(t, dir, "b.sql", `ALTER TABLE orders ADD COLUMN channel String;
`)
	catalogs, err := ParseSchemaCatalogs([]string{first, second})
	if err != nil {
		t.Fatalf("ParseSchemaCatalogs: %v", err)
	}
	if _, ok := catalogs.Physical.Tables["orders"].Columns["channel"]; !ok {
		t.Errorf("channel column missing after the ALTER delta")
	}
}
