package engine

import (
	"testing"

	"github.com/IlyaGulya/chgen/internal/conformance"
)

func TestInferQueryResultTypeUsesCompleteStatementScope(t *testing.T) {
	ddl := "CREATE TABLE t (id UInt64, value Int32) ENGINE = Memory"
	got, err := InferQueryResultType(ddl, "WITH scoped AS (SELECT value FROM t) SELECT scoped.value FROM scoped")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Int32" {
		t.Fatalf("InferQueryResultType returned %s", got.String())
	}
}

func TestConformanceFixtureColumnsComeFromDDL(t *testing.T) {
	columns, err := conformance.FixtureColumnsFromDDL(oracleSchemaDDL)
	if err != nil {
		t.Fatalf("parse the current fixture with the shared conformance runner: %v", err)
	}
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatalf("parse the current fixture with chgen: %v", err)
	}
	table := schema.Tables["t"]
	if len(columns) != len(table.ColumnOrder) {
		t.Fatalf("shared runner read %d columns, chgen read %d", len(columns), len(table.ColumnOrder))
	}
	for index, column := range columns {
		if column.Name != table.ColumnOrder[index] {
			t.Fatalf("column %d is %s, want %s", index, column.Name, table.ColumnOrder[index])
		}
	}
}
