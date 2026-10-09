package engine

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
	"github.com/IlyaGulya/chgen/internal/diagnostic"
	"github.com/IlyaGulya/chgen/internal/sqlir"
)

type irSelectBinding struct {
	root  *sqlir.BoundScope
	nodes map[*clickhouse.SelectQuery]*sqlir.BoundScope
}

func (binding *irSelectBinding) Report() sqlir.BindingReport {
	report := binding.root.Report()
	report.References = slices.Clone(report.References)
	for _, node := range slices.SortedFunc(maps.Keys(binding.nodes), func(a, b *clickhouse.SelectQuery) int { return cmp.Compare(a.Pos(), b.Pos()) }) {
		if bound := binding.nodes[node]; bound != binding.root {
			report.References = append(report.References, bound.Report().References...)
		}
	}
	return report
}

func bindIRSelect(query *clickhouse.SelectQuery, schema *Schema, sql string) (*irSelectBinding, error) {
	document, err := lowerSQLIR(query, sql)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", sqlir.ErrBindingUnmodeled, err)
	}
	if err := sqlir.BindingDomain(document); err != nil {
		return nil, err
	}
	scoped := map[*clickhouse.SelectQuery]queryScope{}
	sourceKind := document.Select.From[0].Kind
	if len(document.Select.With) == 0 && (sourceKind == "table" || sourceKind == "function") {
		scope := queryScope{}
		if err := collectTables(query.From.Expr, schema, &scope, nil); err != nil {
			return nil, err
		}
		scoped[query] = scope
	} else {
		_, index, err := resolveScope(query, schema)
		if err != nil {
			return nil, err
		}
		scoped = index.selects
	}
	binding := &irSelectBinding{nodes: make(map[*clickhouse.SelectQuery]*sqlir.BoundScope)}
	for _, node := range slices.SortedFunc(maps.Keys(scoped), func(a, b *clickhouse.SelectQuery) int { return cmp.Compare(a.Pos(), b.Pos()) }) {
		nested, err := lowerSelectIR(node)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", sqlir.ErrBindingUnmodeled, err)
		}
		bound, err := bindIRCatalogScope(nested, scoped[node])
		if err != nil {
			return nil, err
		}
		binding.nodes[node] = bound
	}
	binding.root = binding.nodes[query]
	return binding, nil
}

func catalogIRContext(scope queryScope, aliasScope *queryScope) (*sqlir.ScopeContext, error) {
	var tables []sqlir.Table
	for _, source := range scope.tables {
		table := sqlir.Table{Name: source.table.Name, Alias: source.alias}
		for _, name := range slices.Sorted(maps.Keys(source.table.Columns)) {
			column := source.table.Columns[name]
			// A two-part path can be a Tuple field rather than a relation qualifier.
			if isCHTuple(column.Type) {
				return nil, sqlir.ErrBindingUnmodeled
			}
			table.Columns = append(table.Columns, sqlir.Column{Name: name, Type: column.Type.String()})
		}
		tables = append(tables, table)
	}
	merged := make(map[string]string)
	for name, typ := range scope.usingTypes {
		merged[name] = typ.String()
	}
	for name, typ := range scope.usingQualified {
		merged[name] = typ.String()
	}
	context := &sqlir.ScopeContext{Tables: tables, Merged: merged, Scalars: make(map[string]string)}
	for name, typ := range scope.scalars {
		context.Scalars[name] = typ.String()
	}
	if aliasScope != nil {
		for name, expression := range scope.projectionExprs {
			local := scope
			// A lexical parent without row bindings exports alias expressions;
			// these are rebound against the child's visible rows, as in the
			// existing resolver, rather than inventing an outer table binding.
			if len(scope.tables) == 0 {
				local = *aliasScope
			}
			local.aliasExpansion = cloneAliasExpansion(local.aliasExpansion)
			local.aliasExpansion[name] = true
			typ, err := inferExprType(expression, local)
			if err != nil {
				// A parent snapshot taken while resolving FROM can include
				// projections of later sources. They are not visible here yet.
				continue
			}
			context.Scalars[name] = typ.String()
		}
	}
	if scope.parent != nil {
		parent, err := catalogIRContext(*scope.parent, &scope)
		if err != nil {
			return nil, err
		}
		context.Parent = parent
	}
	return context, nil
}

func bindIRCatalogScope(document *sqlir.Document, scope queryScope) (*sqlir.BoundScope, error) {
	if len(scope.tables) == 0 {
		return nil, sqlir.ErrBindingUnmodeled
	}
	context, err := catalogIRContext(scope, nil)
	if err != nil {
		return nil, err
	}
	bound, err := sqlir.BindContext(document, context)
	if err != nil {
		var missing *sqlir.BindingError
		if errors.As(err, &missing) {
			if suggestion := suggestName(missing.Name, scope.columnCandidates(missing.Qualifier)); suggestion != "" {
				err = fmt.Errorf("%w; did you mean %q?", err, suggestion)
			}
			return nil, diagnostic.With(err, diagnostic.Detail{Code: cmp.Or(missing.Code, "ir-column-missing"), Status: diagnostic.Invalid, Stage: "binding", Hint: "Check column names, projection aliases and schema migration order."})
		}
	}
	return bound, err
}
