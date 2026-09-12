package engine

import (
	"errors"
	"fmt"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
	"github.com/IlyaGulya/chgen/internal/diagnostic"
)

const expressionContractMacro = "chgen.assumeType"
const expressionContractFunction = "__chgen_assume_type"

// Expression contracts belong to an expression node, not an alias namespace.
// The resolver sees an internal call; the server sees only the parenthesized
// original expression. This is an assertion, never a SQL CAST or a new rule.
func rewriteExpressionContracts(sql string, runtimeSQL bool) (string, error) {
	var out strings.Builder
	for i := 0; i < len(sql); {
		end := sqlDataEnd(sql, i)
		if end > i {
			out.WriteString(sql[i:end])
			i = end
			continue
		}
		if sqlKeywordAt(sql, i, expressionContractFunction) {
			return "", fmt.Errorf("%s is reserved; use %s(expression, 'ClickHouseType')", expressionContractFunction, expressionContractMacro)
		}
		if !sqlKeywordAt(sql, i, expressionContractMacro) || i > 0 && sql[i-1] == '.' {
			out.WriteByte(sql[i])
			i++
			continue
		}
		open := skipSQLWhitespace(sql, i+len(expressionContractMacro))
		if open >= len(sql) || sql[open] != '(' {
			return "", fmt.Errorf("%s requires (expression, 'ClickHouseType')", expressionContractMacro)
		}
		comma, close, err := expressionContractBounds(sql, open)
		if err != nil {
			return "", err
		}
		inner, err := rewriteExpressionContracts(sql[open+1:comma], runtimeSQL)
		if err != nil {
			return "", err
		}
		if !runtimeSQL {
			out.WriteString(expressionContractFunction)
		}
		out.WriteByte('(')
		out.WriteString(inner)
		if !runtimeSQL {
			out.WriteString(sql[comma:close])
		}
		out.WriteByte(')')
		i = close + 1
	}
	return out.String(), nil
}

// sqlDataEnd skips quoted data and comments without interpreting their content.
func sqlDataEnd(sql string, i int) int {
	switch {
	case sql[i] == '\'' || sql[i] == '"' || sql[i] == '`':
		return skipSQLQuoted(sql, i, sql[i])
	case strings.HasPrefix(sql[i:], "--"):
		return skipSQLLineComment(sql, i)
	case strings.HasPrefix(sql[i:], "/*"):
		return skipSQLBlockComment(sql, i)
	default:
		return i
	}
}

func expressionContractBounds(sql string, open int) (int, int, error) {
	depth, comma := 1, -1
	for i := open + 1; i < len(sql); i++ {
		if end := sqlDataEnd(sql, i); end > i {
			i = end - 1
			continue
		}
		switch sql[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
			if depth == 0 {
				if comma < 0 || strings.TrimSpace(sql[open+1:comma]) == "" {
					break
				}
				return comma, i, nil
			}
		case ',':
			if depth == 1 {
				if comma >= 0 {
					return 0, 0, fmt.Errorf("%s requires exactly two arguments", expressionContractMacro)
				}
				comma = i
			}
		}
		if depth == 0 {
			break
		}
	}
	return 0, 0, fmt.Errorf("%s requires (expression, 'ClickHouseType')", expressionContractMacro)
}

func inferExpressionContract(function *clickhouse.FunctionExpr, scope queryScope) (CHType, bool, error) {
	args := functionArgs(function)
	if len(args) != 2 || function.Params.ColumnArgList != nil {
		return CHType{}, false, fmt.Errorf("%s requires exactly two arguments", expressionContractMacro)
	}
	literal, ok := args[1].(*clickhouse.StringLiteral)
	if !ok {
		return CHType{}, false, fmt.Errorf("%s type must be a constant string", expressionContractMacro)
	}
	typeText, err := contractTypeLiteral(literal.Literal)
	if err != nil {
		return CHType{}, false, err
	}
	_, contract, err := parseResultTypeContract(resultCHTypeDirective+" expression "+typeText, 0)
	if err != nil {
		return CHType{}, false, err
	}
	inferred, err := inferExprType(args[0], scope)
	if err == nil {
		if inferred.String() != contract.typeOf.String() {
			return CHType{}, false, diagnostic.With(fmt.Errorf("%s asserts %s, inferred %s", expressionContractMacro, contract.typeOf, inferred), diagnostic.Detail{
				Code: "expression-contract-conflict", Status: diagnostic.Invalid, Stage: "contract",
				Hint: "Correct or remove the assertion. Known types and invalid operations cannot be overridden.",
			})
		}
		return inferred, false, nil
	}
	var missing *unregisteredFunctionError
	if !errors.As(err, &missing) {
		return CHType{}, false, err
	}
	call, ok := unwrapColumnExpression(args[0]).(*clickhouse.FunctionExpr)
	if !ok || call.Name.Name != missing.name || call.Params == nil || call.Params.ColumnArgList != nil {
		return CHType{}, false, fmt.Errorf("%s cannot bypass a known operation's argument checks: %w; put contracts on its unknown operands", expressionContractMacro, err)
	}
	// A contract for f(g(x)) says nothing about g's type or f's argument
	// domain. Require independently typed arguments, including each sibling.
	for _, arg := range functionArgs(call) {
		if _, err := inferExprType(arg, scope); err != nil {
			return CHType{}, false, fmt.Errorf("%s argument: %w", expressionContractMacro, err)
		}
	}
	return contract.typeOf, true, nil
}

// The parser retains escapes inside StringLiteral.Literal. Decode the outer
// macro string before parsing its contents as a type (including quoted zones
// or enum labels). Refuse unsupported escapes instead of changing their meaning.
func contractTypeLiteral(raw string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' {
			i++
			if i >= len(raw) {
				return "", fmt.Errorf("unterminated contract type escape")
			}
			switch raw[i] {
			case '\\', '\'', '"':
				out.WriteByte(raw[i])
			case 'n':
				out.WriteByte('\n')
			case 'r':
				out.WriteByte('\r')
			case 't':
				out.WriteByte('\t')
			default:
				return "", fmt.Errorf("unsupported contract type escape \\%c", raw[i])
			}
			continue
		}
		if raw[i] == '\'' && i+1 < len(raw) && raw[i+1] == '\'' {
			i++
		}
		out.WriteByte(raw[i])
	}
	return out.String(), nil
}

func collectExpressionContracts(query *Query, statement clickhouse.Expr, scope queryScope, scopes *scopeIndex) error {
	var contractErr error
	clickhouse.Walk(statement, func(node clickhouse.Expr) bool {
		if contractErr != nil {
			return false
		}
		call, ok := node.(*clickhouse.FunctionExpr)
		if !ok || call.Name.Name != expressionContractFunction {
			return true
		}
		// Higher-order inference already validated lambda-local bindings.
		// Re-inferring such a call in its enclosing SELECT would lose them.
		asserted, checked := scopes.expressionContracts[call]
		if !checked {
			_, asserted, contractErr = inferExpressionContract(call, scopes.lookup(node, scope))
		}
		if asserted {
			query.unverifiedExpressions = append(query.unverifiedExpressions, clickhouse.Format(functionArgs(call)[0]))
		}
		return true
	})
	return contractErr
}
