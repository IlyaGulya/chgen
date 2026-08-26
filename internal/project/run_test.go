package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGeneratesEveryPackage(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(name, content string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("migrations/000001_init.up.sql", `
CREATE TABLE orders (order_id String, customer_id String) ENGINE = MergeTree ORDER BY order_id;
`)
	mustWrite("migrations/000001_init.down.sql", `DROP TABLE orders;`)
	mustWrite("querygen/external_tables.sql", `-- chgen:external
CREATE TABLE order_keys (order_id String);
`)
	mustWrite("querygen/queries.sql", `-- name: ListOrders :many
SELECT order_id, customer_id FROM orders
WHERE order_id IN (SELECT order_id FROM chgen.external(order_keys));
`)
	mustWrite("servinggen/queries.sql", `-- name: CountOrders :one
SELECT count() AS total FROM orders;
`)
	mustWrite("chgen.yaml", `
version: 1
packages:
  - name: querygen
    output: querygen/queries.sql.go
    queries: querygen/queries.sql
    schema:
      - migrations
      - querygen/external_tables.sql
  - name: servinggen
    output: servinggen/queries.sql.go
    queries: servinggen/queries.sql
    schema: migrations
`)
	if err := Run(filepath.Join(dir, "chgen.yaml")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "querygen/queries.sql.go"))
	if err != nil {
		t.Fatalf("read querygen output: %v", err)
	}
	if !strings.Contains(string(first), "package querygen") || !strings.Contains(string(first), "ListOrders") {
		t.Errorf("querygen output is wrong:\n%s", first)
	}
	if strings.Contains(string(first), "000001_init.down.sql") {
		t.Errorf("down migration must not be read")
	}
	second, err := os.ReadFile(filepath.Join(dir, "servinggen/queries.sql.go"))
	if err != nil {
		t.Fatalf("read servinggen output: %v", err)
	}
	if !strings.Contains(string(second), "package servinggen") {
		t.Errorf("servinggen output is wrong")
	}
}

func TestRunWritesAnAbsoluteOutput(t *testing.T) {
	configDir := t.TempDir()
	output := filepath.Join(t.TempDir(), "generated", "queries.sql.go")
	if err := os.WriteFile(filepath.Join(configDir, "schema.sql"), []byte("CREATE TABLE values (id UInt64) ENGINE = Memory;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "queries.sql"), []byte("-- name: ListValues :many\nSELECT id FROM values;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := "version: 1\npackages:\n  - name: querygen\n    output: " + output + "\n    queries: queries.sql\n    schema: schema.sql\n"
	configPath := filepath.Join(configDir, "chgen.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(configPath); err != nil {
		t.Fatal(err)
	}
	generated, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read absolute output: %v", err)
	}
	if !strings.Contains(string(generated), "package querygen") || !strings.Contains(string(generated), "ListValues") {
		t.Fatalf("absolute output has unexpected content:\n%s", generated)
	}
	mutated := filepath.Join(configDir, output)
	if _, err := os.Stat(mutated); !os.IsNotExist(err) {
		t.Fatalf("the old join mutation wrote %q", mutated)
	}
}
