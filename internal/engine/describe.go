package engine

import (
	"fmt"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// PrepareDescribeSQL accepts one concrete SELECT without running inference.
// It is a boundary guard, not an alternative SQL parser. Source expressions
// are preserved; only a statement terminator becomes whitespace.
func PrepareDescribeSQL(sql string) (string, error) {
	statements, err := clickhouse.NewParser(sql).ParseStmts()
	if err != nil {
		return "", fmt.Errorf("parse describe input: %w", err)
	}
	if len(statements) != 1 {
		return "", fmt.Errorf("describe requires exactly one SELECT")
	}
	if _, ok := statements[0].(*clickhouse.SelectQuery); !ok {
		return "", fmt.Errorf("describe requires a SELECT, not a mutation or DDL statement")
	}
	var forbidden error
	clickhouse.Walk(statements[0], func(node clickhouse.Expr) bool {
		switch node.(type) {
		case *clickhouse.FormatClause:
			forbidden = fmt.Errorf("describe input must omit FORMAT; the analysis response uses JSON")
		case *clickhouse.PlaceHolder:
			forbidden = fmt.Errorf("describe requires concrete values instead of placeholders")
		}
		return forbidden == nil
	})
	if forbidden != nil {
		return "", forbidden
	}
	copySQL := []byte(sql)
	for i := 0; i < len(sql); {
		switch {
		case sql[i] == '\'' || sql[i] == '"' || sql[i] == '`':
			i = skipSQLQuoted(sql, i, sql[i])
		case strings.HasPrefix(sql[i:], "--"):
			i = skipSQLLineComment(sql, i)
		case strings.HasPrefix(sql[i:], "/*"):
			i = skipSQLBlockComment(sql, i)
		default:
			if sql[i] == ';' {
				copySQL[i] = ' '
			}
			i++
		}
	}
	return "DESCRIBE TABLE (\n" + string(copySQL) + "\n) FORMAT JSON", nil
}

// ResultTypeAnnotation proposes syntax only. The ordinary resolver still owns
// placement, inferred-type conflicts, and validation of argument expressions.
func ResultTypeAnnotation(alias, typeName string) (string, error) {
	annotation := resultCHTypeDirective + " " + alias + " " + typeName
	_, _, err := parseResultTypeContract(annotation, 1)
	return annotation, err
}
