package engine

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
	"github.com/IlyaGulya/chgen/internal/diagnostic"
	"github.com/IlyaGulya/chgen/internal/sqlir"
)

func bindIRSelect(query *clickhouse.SelectQuery, schema *Schema, sql string) (*sqlir.BoundScope, error) {
	document, err := lowerSQLIR(query, sql)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", sqlir.ErrBindingUnmodeled, err)
	}
	if err := sqlir.BindingDomain(document); err != nil {
		return nil, err
	}
	scope := queryScope{}
	if err := collectTables(query.From.Expr, schema, &scope, nil); err != nil {
		return nil, err
	}
	if len(scope.tables) != 1 {
		return nil, sqlir.ErrBindingUnmodeled
	}
	source := scope.tables[0]
	table := sqlir.Table{Name: source.table.Name, Alias: source.alias}
	for _, name := range slices.Sorted(maps.Keys(source.table.Columns)) {
		column := source.table.Columns[name]
		// A two-part path can be a Tuple field rather than a relation qualifier.
		if isCHTuple(column.Type) {
			return nil, sqlir.ErrBindingUnmodeled
		}
		table.Columns = append(table.Columns, sqlir.Column{Name: name, Type: column.Type.String()})
	}
	bound, err := sqlir.Bind(document, table)
	if err != nil {
		var missing *sqlir.BindingError
		if errors.As(err, &missing) {
			if suggestion := suggestName(missing.Name, scope.columnCandidates(missing.Qualifier)); suggestion != "" {
				err = fmt.Errorf("%w; did you mean %q?", err, suggestion)
			}
			return nil, diagnostic.With(err, diagnostic.Detail{Code: "ir-column-missing", Status: diagnostic.Invalid, Stage: "binding", Hint: "Check the column name and schema migration order."})
		}
	}
	return bound, err
}
