package engine

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
	"github.com/IlyaGulya/chgen/internal/diagnostic"
)

type seriesTableRule struct {
	column, typeName string
	minArgs, maxArgs int
	roles            [3]string
}

var seriesTableRules = map[string]seriesTableRule{
	"numbers":         {"number", "UInt64", 0, 3, [3]string{"Start", "Length", "Step"}},
	"numbers_mt":      {"number", "UInt64", 0, 3, [3]string{"Start", "Length", "Step"}},
	"zeros":           {"zero", "UInt8", 0, 1, [3]string{"Length"}},
	"zeros_mt":        {"zero", "UInt8", 0, 1, [3]string{"Length"}},
	"generate_series": {"generate_series", "UInt64", 2, 3, [3]string{"Start", "Stop", "Step"}},
	"generateSeries":  {"generate_series", "UInt64", 2, 3, [3]string{"Start", "Stop", "Step"}},
}

// Series functions have a fixed relation schema, not a scalar return type.
// The measured domain is unsigned integer literals and direct parameters. Expressions and
// implicit conversions remain unknown rather than inheriting this evidence.
func resolveSeriesTable(function *clickhouse.TableFunctionExpr, alias string) (Table, error) {
	name := clickhouse.Format(function.Name)
	rule, measured := seriesTableRules[name]
	if !measured {
		return Table{}, diagnostic.With(fmt.Errorf("table function %s has no measured relation rule", name), diagnostic.Detail{
			Code: "table-function-unmeasured", Status: diagnostic.Unknown, Stage: "binding",
			Hint: "Use a measured relation source, or explicit server generation against a test database.",
		})
	}
	args := function.Args.Args
	if len(args) < rule.minArgs || len(args) > rule.maxArgs {
		return Table{}, fmt.Errorf("table function %s accepts %d through %d arguments", name, rule.minArgs, rule.maxArgs)
	}
	for i, arg := range args {
		if _, ok := unwrapColumnExpression(arg).(*clickhouse.PlaceHolder); ok {
			continue
		}
		literal, ok := unwrapColumnExpression(arg).(*clickhouse.NumberLiteral)
		if !ok {
			return Table{}, fmt.Errorf("table function %s argument %d requires a measured unsigned integer literal", name, i+1)
		}
		value, err := strconv.ParseUint(literal.Literal, 10, 64)
		if err != nil {
			return Table{}, fmt.Errorf("table function %s argument %d must be an unsigned integer literal", name, i+1)
		}
		if i == 2 && value == 0 {
			return Table{}, fmt.Errorf("table function %s step must be positive", name)
		}
	}
	return Table{Name: alias, ColumnOrder: []string{rule.column}, Columns: map[string]Column{
		rule.column: {Name: rule.column, Type: CHType{Name: rule.typeName}},
	}}, nil
}

func inferSeriesParams(function *clickhouse.TableFunctionExpr, types map[*clickhouse.PlaceHolder]CHType, names map[*clickhouse.PlaceHolder]string) {
	rule, measured := seriesTableRules[clickhouse.Format(function.Name)]
	if !measured {
		return
	}
	args := function.Args.Args
	for i, arg := range args {
		if placeholder, ok := unwrapColumnExpression(arg).(*clickhouse.PlaceHolder); ok {
			types[placeholder] = CHType{Name: "UInt64"}
			role := rule.roles[i]
			if len(args) == 1 {
				role = "Length"
			}
			names[placeholder] = role
		}
	}
}

// parseWithSeriesParameters repairs only a frontend gap for bare parameters in
// table arguments. Each temporary literal must become its original placeholder
// again before resolution. Positions and generated SQL are never changed.
func parseWithSeriesParameters(sql string) ([]clickhouse.Expr, error) {
	statements, originalErr := clickhouse.NewParser(sql).ParseStmts()
	if originalErr == nil || !strings.Contains(sql, "?") {
		return statements, originalErr
	}
	tokens, err := scanSQLBoundary(sql)
	if err != nil {
		return nil, originalErr
	}
	tokens = slices.DeleteFunc(tokens, func(token sqlBoundaryToken) bool { return token.comment })
	masked := []byte(sql)
	positions := make(map[int]bool)
	for i, token := range tokens {
		if token.quoted || i+1 >= len(tokens) || tokens[i+1].text != "(" {
			continue
		}
		if _, measured := seriesTableRules[token.text]; !measured {
			continue
		}
		depth := 1
		for j := i + 2; j < len(tokens) && depth > 0; j++ {
			current := tokens[j]
			if current.quoted {
				continue
			}
			switch current.text {
			case "(":
				depth++
			case ")":
				depth--
			case "?":
				if depth == 1 && j+1 < len(tokens) && (tokens[j-1].text == "(" || tokens[j-1].text == ",") && (tokens[j+1].text == "," || tokens[j+1].text == ")") {
					masked[current.start] = '0'
					positions[current.start] = true
				}
			}
		}
	}
	if len(positions) == 0 {
		return nil, originalErr
	}
	statements, err = clickhouse.NewParser(string(masked)).ParseStmts()
	if err != nil {
		return nil, err
	}
	for _, statement := range statements {
		clickhouse.Walk(statement, func(node clickhouse.Expr) bool {
			function, ok := node.(*clickhouse.TableFunctionExpr)
			if !ok {
				return true
			}
			for i, arg := range function.Args.Args {
				literal, ok := arg.(*clickhouse.NumberLiteral)
				if ok && positions[int(literal.Pos())] {
					function.Args.Args[i] = &clickhouse.PlaceHolder{PlaceholderPos: literal.Pos(), PlaceHolderEnd: literal.End(), Type: "?"}
					delete(positions, int(literal.Pos()))
				}
			}
			return true
		})
	}
	if len(positions) != 0 {
		return nil, fmt.Errorf("series parameter adapter cannot preserve this expression: %w", originalErr)
	}
	return statements, nil
}
