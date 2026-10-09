package engine

import (
	"fmt"
	"sync"

	"github.com/IlyaGulya/chgen/internal/systemcatalog"
)

// System relations are separate from the replayed application catalog.
var systemColumns = sync.OnceValues(func() (map[string][]systemcatalog.Column, error) {
	snapshot, err := systemcatalog.Pinned()
	if err != nil {
		return nil, err
	}
	if snapshot.Version != MeasuredCHVersion {
		return nil, fmt.Errorf("system catalog version %s differs from measured version %s", snapshot.Version, MeasuredCHVersion)
	}
	tables := make(map[string][]systemcatalog.Column)
	for _, column := range snapshot.Columns {
		tables[column.Table] = append(tables[column.Table], column)
	}
	return tables, nil
})

func systemTable(name string) (Table, bool, error) {
	tables, err := systemColumns()
	if err != nil {
		return Table{}, false, err
	}
	columns, found := tables[name]
	if !found {
		return Table{}, false, nil
	}
	table := Table{Name: "system." + name, Columns: make(map[string]Column)}
	for _, column := range columns {
		typ, err := parseCHTypeName(column.Type)
		if err != nil {
			return Table{}, false, fmt.Errorf("system.%s.%s: %w", name, column.Name, err)
		}
		table.Columns[column.Name] = Column{Name: column.Name, Type: typ}
		table.ColumnOrder = append(table.ColumnOrder, column.Name)
	}
	return table, true, nil
}
