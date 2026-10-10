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
	root      *sqlir.BoundScope
	nodes     map[*clickhouse.SelectQuery]*sqlir.BoundScope
	documents map[*clickhouse.SelectQuery]*sqlir.Document
	failure   error
}

func (binding *irSelectBinding) Report() sqlir.BindingReport {
	report := sqlir.BindingReport{Backend: "sqlir", References: []sqlir.Reference{}}
	if binding.root != nil {
		report = binding.root.Report()
	}
	report.References = slices.Clone(report.References)
	for _, node := range slices.SortedFunc(maps.Keys(binding.nodes), func(a, b *clickhouse.SelectQuery) int { return cmp.Compare(a.Pos(), b.Pos()) }) {
		if bound := binding.nodes[node]; bound != binding.root {
			report.References = append(report.References, bound.Report().References...)
		}
	}
	return report
}

func bindIRSelect(query *clickhouse.SelectQuery, schema *Schema, sql string) (*irSelectBinding, error) {
	binding, err := prepareIRSelect(query, sql)
	if err != nil {
		return nil, err
	}
	_, scopes, err := resolveScopeWithIRBinding(query, schema, binding)
	if err != nil {
		return nil, err
	}
	if binding.failure != nil {
		return nil, binding.failure
	}
	for node, scope := range scopes.selects {
		if scope.irBinding != nil {
			binding.nodes[node] = scope.irBinding
		}
	}
	binding.root = binding.nodes[query]
	return binding, nil
}

// Prepare structure without a preliminary typed resolver pass. Each SELECT is
// bound when its lexical sources become available during type resolution.
func prepareIRSelect(query *clickhouse.SelectQuery, sql string) (*irSelectBinding, error) {
	document, err := lowerSQLIR(query, sql)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", sqlir.ErrBindingUnmodeled, err)
	}
	if err := sqlir.BindingDomain(document); err != nil {
		return nil, err
	}
	binding := &irSelectBinding{
		nodes:     make(map[*clickhouse.SelectQuery]*sqlir.BoundScope),
		documents: make(map[*clickhouse.SelectQuery]*sqlir.Document),
	}
	var lowerErr error
	modifiers := irLimitModifiers(query, sql)
	clickhouse.Walk(query, func(node clickhouse.Expr) bool {
		if nested, ok := node.(*clickhouse.SelectQuery); ok {
			binding.documents[nested], lowerErr = lowerSelectIR(nested)
			if lowerErr == nil {
				markIRLimitModifiers(binding.documents[nested], modifiers)
			}
		}
		return lowerErr == nil
	})
	if lowerErr != nil {
		return nil, fmt.Errorf("%w: %v", sqlir.ErrBindingUnmodeled, lowerErr)
	}
	return binding, nil
}

func catalogIRContext(scope queryScope, aliasScope *queryScope) (*sqlir.ScopeContext, error) {
	var tables []sqlir.Table
	for _, source := range scope.tables {
		table := sqlir.Table{Name: source.table.Name, Alias: source.alias}
		for _, name := range slices.Sorted(maps.Keys(source.table.Columns)) {
			column := source.table.Columns[name]
			lowered := sqlir.Column{Name: name, Type: column.Type.String()}
			if isCHTuple(column.Type) {
				lowered.Fields = make(map[string]string)
				for _, field := range column.Type.ParamNames {
					if field == "" {
						continue
					}
					typ, err := tupleElementByName(column.Type, field, true)
					if err != nil {
						return nil, err
					}
					lowered.Fields[field] = typ.String()
				}
			}
			table.Columns = append(table.Columns, lowered)
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
	for name, typ := range scope.arrayJoinTypes {
		merged[name] = typ.String()
		if isCHTuple(typ) {
			for _, field := range typ.ParamNames {
				if field == "" {
					continue
				}
				element, err := tupleElementByName(typ, field, true)
				if err != nil {
					return nil, err
				}
				merged[name+"."+field] = element.String()
			}
		}
	}
	for name, typ := range scope.arrayJoinQualified {
		merged[name] = typ.String()
	}
	context := &sqlir.ScopeContext{Tables: tables, Merged: merged, Scalars: make(map[string]string)}
	context.Reserved = maps.Clone(scope.reservedScalars)
	context.Inputs = make(map[int]*sqlir.ScopeContext, len(scope.arrayJoinInputs))
	for position, input := range scope.arrayJoinInputs {
		before, err := catalogIRContext(input, nil)
		if err != nil {
			return nil, err
		}
		context.Inputs[position] = before
	}
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
