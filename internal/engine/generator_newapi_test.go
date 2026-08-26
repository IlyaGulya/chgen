package engine

import (
	"strings"
	"testing"
)

func TestGenerateNewTakesOnlyConnection(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE orders (order_id String);`)
	if err != nil {
		t.Fatal(err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ListOrders :many
SELECT order_id FROM orders;
`, schema)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := Generate("gen", queries)
	if err != nil {
		t.Fatal(err)
	}
	source := string(generated)
	if !strings.Contains(source, "func New(conn driver.Conn) *Queries {") {
		t.Errorf("New must take only the connection:\n%s", source)
	}
	if strings.Contains(source, "bindDatabase") {
		t.Errorf("bindDatabase must be removed")
	}
	if strings.Contains(source, "database string") || strings.Contains(source, "__DATABASE__") {
		t.Errorf("no database plumbing must remain")
	}
	if strings.Contains(source, "\"strings\"") || strings.Contains(source, "\"unicode\"") {
		t.Errorf("unused imports must not be generated")
	}
	if !strings.Contains(source, "panic(") {
		t.Errorf("New must panic on a nil connection")
	}
}
