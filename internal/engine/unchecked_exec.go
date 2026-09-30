package engine

import (
	"fmt"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

const uncheckedExecDirective = "-- chgen:unchecked-exec"

// scanSingleExecSQL counts bind markers in one static statement, without
// asking the parser to understand its ClickHouse grammar. It does not claim
// that ClickHouse accepts or safely executes the statement.
func scanSingleExecSQL(sql, context string) (int, error) {
	placeholders := 0
	hasContent := false
	ended := false
	for index := 0; index < len(sql); {
		switch {
		case sql[index] == ' ' || sql[index] == '\t' || sql[index] == '\r' || sql[index] == '\n':
			index++
		case strings.HasPrefix(sql[index:], "--"):
			index = skipSQLLineComment(sql, index)
		case strings.HasPrefix(sql[index:], "/*"):
			end := strings.Index(sql[index+2:], "*/")
			if end < 0 {
				return 0, fmt.Errorf("%s: unterminated block comment", context)
			}
			index += end + 4
		case sql[index] == '\'' || sql[index] == '"' || sql[index] == '`':
			if ended {
				return 0, fmt.Errorf("%s requires exactly one SQL statement", context)
			}
			end, err := scanExecQuote(sql, index)
			if err != nil {
				return 0, fmt.Errorf("%s: %w", context, err)
			}
			hasContent = true
			index = end
		case sql[index] == ';':
			if ended || !hasContent {
				return 0, fmt.Errorf("%s requires exactly one SQL statement", context)
			}
			ended = true
			index++
		default:
			if ended {
				return 0, fmt.Errorf("%s requires exactly one SQL statement", context)
			}
			if sql[index] == '?' {
				placeholders++
			}
			hasContent = true
			index++
		}
	}
	if !hasContent {
		return 0, fmt.Errorf("%s requires a SQL statement", context)
	}
	return placeholders, nil
}

func scanExecQuote(sql string, start int) (int, error) {
	quote := sql[start]
	for index := start + 1; index < len(sql); index++ {
		if sql[index] == '\\' {
			index++
			continue
		}
		if sql[index] != quote {
			continue
		}
		if index+1 < len(sql) && sql[index+1] == quote {
			index++
			continue
		}
		return index + 1, nil
	}
	return 0, fmt.Errorf("unterminated quoted SQL")
}

func validateUncheckedExecSQL(sql string) (int, error) {
	return scanSingleExecSQL(sql, uncheckedExecDirective)
}

func resolveUncheckedExecParams(query *Query) error {
	count, err := validateUncheckedExecSQL(query.SQL)
	if err != nil {
		return err
	}
	if len(query.NamedParamNames) != 0 && len(query.NamedParamNames) != count {
		return fmt.Errorf("%s cannot mix chgen.arg('Name') with raw positional placeholders", uncheckedExecDirective)
	}
	placeholders := make([]*clickhouse.PlaceHolder, count)
	for index := range placeholders {
		placeholders[index] = new(clickhouse.PlaceHolder)
	}
	return finalizeParams(query, placeholders, nil, nil, nil)
}
