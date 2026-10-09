package engine

import "testing"

func TestPinnedSystemCatalogTypes(t *testing.T) {
	tables, err := systemColumns()
	if err != nil {
		t.Fatal(err)
	}
	for name := range tables {
		t.Run(name, func(t *testing.T) {
			table, found, err := systemTable(name)
			if err != nil {
				t.Fatal(err)
			}
			if !found || len(table.Columns) != len(table.ColumnOrder) {
				t.Fatalf("incomplete or duplicate columns for %s", name)
			}
		})
	}
}
