package engine

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
	"github.com/IlyaGulya/chgen/internal/diagnostic"
)

const resultCHTypeDirective = "-- result-chtype:"

type resultTypeContract struct {
	typeOf CHType
	line   int
}

// An annotation-looking line inside a string or block comment is SQL data.
func resultContractCommentLines(sql string) map[int]bool {
	lines := make(map[int]bool)
	for i := 0; i < len(sql); {
		switch {
		case sql[i] == '\'' || sql[i] == '"' || sql[i] == '`':
			i = skipSQLQuoted(sql, i, sql[i])
		case strings.HasPrefix(sql[i:], "/*"):
			i = skipSQLBlockComment(sql, i)
		case strings.HasPrefix(sql[i:], "--"):
			end := skipSQLLineComment(sql, i)
			start := strings.LastIndexByte(sql[:i], '\n') + 1
			if strings.TrimSpace(sql[start:i]) == "" && strings.HasPrefix(sql[i:end], resultCHTypeDirective) {
				lines[lineOfOffset(sql, i)] = true
			}
			i = end
		default:
			i++
		}
	}
	return lines
}

// Only a missing registry entry is eligible, not invalid calls or other
// resolver failures. Keep this distinct from the user-facing error text.
type unregisteredFunctionError struct{ name string }

func (e *unregisteredFunctionError) Error() string {
	return fmt.Sprintf("function %s has no registered type rule; %s", e.name, pinTypeHint)
}

func (e *unregisteredFunctionError) Diagnostic() diagnostic.Detail {
	return diagnostic.Detail{
		Code: "function-rule-missing", Status: diagnostic.Unknown, Stage: "inference",
		Hint: "chgen has no type rule for this function. Use chgen describe with a concrete SELECT on a test server to discover types. After verification, put chgen.assumeType(call, 'ClickHouseType') on each unknown call to supply its type within a CTE or a larger expression. A direct outer SELECT call can also use -- result-chtype: Alias ClickHouseType. Contracts do not override known errors; -- result: only changes Go mapping.",
	}
}

func parseResultTypeContract(line string, lineNumber int) (string, resultTypeContract, error) {
	text := strings.TrimSpace(strings.TrimPrefix(line, resultCHTypeDirective))
	separator := strings.IndexFunc(text, unicode.IsSpace)
	if separator < 1 {
		return "", resultTypeContract{}, fmt.Errorf("expected -- result-chtype: SQLAlias ClickHouseType")
	}
	alias, typeText := text[:separator], strings.TrimSpace(text[separator:])
	validAlias := alias != "" && (alias[0] == '_' || alias[0] >= 'a' && alias[0] <= 'z' || alias[0] >= 'A' && alias[0] <= 'Z')
	for i := range alias {
		validAlias = validAlias && isSQLIdentifierByte(alias[i])
	}
	if !validAlias {
		return "", resultTypeContract{}, fmt.Errorf("result-chtype alias %q must be a simple identifier", alias)
	}
	const prefix = "CREATE TABLE __chgen_contract (value "
	statements, err := clickhouse.NewParser(prefix + typeText + ")").ParseStmts()
	if err != nil || len(statements) != 1 {
		return "", resultTypeContract{}, fmt.Errorf("invalid result-chtype %q", typeText)
	}
	create, ok := statements[0].(*clickhouse.CreateTable)
	if !ok || create.TableSchema == nil || len(create.TableSchema.Columns) != 1 {
		return "", resultTypeContract{}, fmt.Errorf("expected exactly one ClickHouse type")
	}
	column, ok := create.TableSchema.Columns[0].(*clickhouse.ColumnDef)
	if !ok || column.Type == nil {
		return "", resultTypeContract{}, fmt.Errorf("result-chtype must contain only a ClickHouse type")
	}
	end := int(column.Type.End()) - len(prefix)
	// Upstream scalar ends are exclusive; parameterized ends point at ')'.
	if end != len(typeText) && !(end == len(typeText)-1 && typeText[end] == ')') {
		return "", resultTypeContract{}, fmt.Errorf("result-chtype must contain only a ClickHouse type")
	}
	typeOf, err := parseCHType(column.Type)
	if err != nil {
		return "", resultTypeContract{}, err
	}
	if _, err := goType(typeOf); err != nil {
		return "", resultTypeContract{}, err
	}
	return alias, resultTypeContract{typeOf: typeOf, line: lineNumber}, nil
}

func validateResultContractTargets(query *Query, selectQuery *clickhouse.SelectQuery) error {
	if len(query.resultContracts) == 0 {
		return nil
	}
	if selectQueryHasSetOperation(selectQuery) {
		return fmt.Errorf("result-chtype supports ordinary outer SELECT outputs, not set operations")
	}
	aliases := make(map[string]int)
	for _, item := range selectQuery.SelectItems {
		if isStarArgument(item.Expr) {
			return fmt.Errorf("result-chtype does not support star expansion")
		}
		if item.Alias != nil {
			aliases[item.Alias.Name]++
		}
	}
	for alias, contract := range query.resultContracts {
		if aliases[alias] != 1 {
			return fmt.Errorf("%s:%d: result-chtype %q requires one unique explicit output alias", query.File, contract.line, alias)
		}
	}
	return nil
}

func resolveResultContracts(query *Query, selectQuery *clickhouse.SelectQuery, scope queryScope, scopes *scopeIndex) error {
	if len(query.resultContracts) == 0 {
		return nil
	}
	results := make([]scopedQueryResult, 0, len(selectQuery.SelectItems))
	for _, item := range selectQuery.SelectItems {
		name, err := selectItemSQLName(item)
		if err != nil {
			return err
		}
		inferred, inferErr := inferSelectItemType(item, scope)
		contract, pinned := query.resultContracts[name]
		if !pinned {
			if inferErr != nil {
				return fmt.Errorf("result %s: %w", name, inferErr)
			}
		} else {
			if inferErr == nil && inferred.String() != contract.typeOf.String() {
				return diagnostic.With(fmt.Errorf("%s:%d: result-chtype %s asserts %s, inferred %s", query.File, contract.line, name, contract.typeOf, inferred), diagnostic.Detail{
					Code: "result-contract-conflict", Status: diagnostic.Invalid, Stage: "contract",
					Hint: "Correct or remove the result-chtype annotation. A contract cannot override a known inferred type.",
				})
			}
			if inferErr != nil {
				var missing *unregisteredFunctionError
				if !errors.As(inferErr, &missing) && !errors.Is(inferErr, errPlaceholderResultType) {
					return fmt.Errorf("result %s: %w", name, inferErr)
				}
				// Validate all arguments, including siblings that ordinary
				// inference may not have reached after its first refusal.
				if err := validateAssertedFunction(item.Expr, scope); err != nil {
					return fmt.Errorf("%s:%d: result-chtype %s: %w", query.File, contract.line, name, err)
				}
				query.unverifiedResults = append(query.unverifiedResults, name)
			}
			inferred = contract.typeOf
		}
		results = append(results, scopedQueryResult{name: name, typeOf: inferred})
	}
	scopes.queryResults = results
	return nil
}

// An unknown call may take fully typed expressions or further unknown calls.
// Do not accept a known operator/function with unknown operands: its domain
// cannot be checked from the asserted type of the final output.
func validateAssertedFunction(expression clickhouse.Expr, scope queryScope) error {
	function, ok := expression.(*clickhouse.FunctionExpr)
	if !ok || function.Name == nil {
		return fmt.Errorf("a result contract cannot validate this expression's unknown operand types; assert a direct unregistered function call")
	}
	if _, known := functionRegistry[strings.ToLower(function.Name.Name)]; known {
		return fmt.Errorf("a result contract cannot bypass argument checks for known function %s", function.Name.Name)
	}
	if function.Params == nil || function.Params.ColumnArgList != nil {
		return fmt.Errorf("result-chtype does not support unregistered parametric function calls")
	}
	for _, arg := range functionArgs(function) {
		_, err := inferExprType(arg, scope)
		_, bareParameter := unwrapColumnExpr(arg).(*clickhouse.PlaceHolder)
		if err == nil || bareParameter && errors.Is(err, errPlaceholderResultType) {
			// resolveParams has already required a valid parameter contract.
			// An unknown function has no known argument domain to apply to it.
			continue
		}
		var missing *unregisteredFunctionError
		if !errors.As(err, &missing) && !errors.Is(err, errPlaceholderResultType) {
			return err
		}
		if err := validateAssertedFunction(arg, scope); err != nil {
			return err
		}
	}
	return nil
}
