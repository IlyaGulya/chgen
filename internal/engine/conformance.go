package engine

import (
	"fmt"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// InferExpressionType infers one expression against one fixture DDL.
func InferExpressionType(ddl, tableName, expression string) (CHType, error) {
	schema, err := conformanceSchema(ddl)
	if err != nil {
		return CHType{}, err
	}
	queryText := "SELECT " + expression
	if tableName != "" {
		queryText += " FROM " + tableName
	}
	queries, err := clickhouse.NewParser(queryText).ParseStmts()
	if err != nil || len(queries) != 1 {
		return CHType{}, fmt.Errorf("parse conformance expression %q: %w", expression, err)
	}
	query, ok := queries[0].(*clickhouse.SelectQuery)
	if !ok || len(query.SelectItems) != 1 {
		return CHType{}, fmt.Errorf("conformance expression did not make one SELECT item")
	}
	scope, _, err := resolveScope(query, schema)
	if err != nil {
		return CHType{}, err
	}
	return inferExprType(query.SelectItems[0].Expr, scope)
}

// InferQueryResultType resolves one result from a complete SELECT statement.
func InferQueryResultType(ddl, statement string) (CHType, error) {
	results, err := inferQueryResults(ddl, statement)
	if err != nil {
		return CHType{}, err
	}
	if len(results) != 1 {
		return CHType{}, fmt.Errorf("conformance statement has %d results, want 1", len(results))
	}
	return results[0].CHType, nil
}

func inferQueryResults(ddl, statement string) ([]Result, error) {
	schema, err := conformanceSchema(ddl)
	if err != nil {
		return nil, err
	}
	query := &Query{Name: "ConformanceStatement", Command: CommandMany, SQL: statement}
	if err := resolveQuery(query, schema); err != nil {
		return nil, err
	}
	return append([]Result(nil), query.Results...), nil
}

func conformanceSchema(ddl string) (*Schema, error) {
	statements, err := clickhouse.NewParser(stripUnsupportedTTLRollup(ddl)).ParseStmts()
	if err != nil {
		return nil, fmt.Errorf("parse fixture DDL: %w", err)
	}
	schema := &Schema{Tables: make(map[string]Table)}
	for _, statement := range statements {
		create, ok := statement.(*clickhouse.CreateTable)
		if !ok {
			continue
		}
		table, parseErr := parseCreateTable(create)
		if parseErr != nil {
			return nil, parseErr
		}
		schema.Tables[table.Name] = table
	}
	if len(schema.Tables) == 0 {
		return nil, fmt.Errorf("fixture DDL has no table")
	}
	return schema, nil
}
