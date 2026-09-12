package engine

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
	"github.com/IlyaGulya/chgen/internal/diagnostic"
)

type queryScope struct {
	tables              []scopedTable
	scalars             map[string]CHType
	projectionAliases   map[string]CHType
	projectionExprs     map[string]clickhouse.Expr
	aliasExpansion      map[string]bool
	fromBindingStart    int
	relations           map[string]Table
	reservedRelations   map[string]bool
	reservedScalars     map[string]bool
	usingTypes          map[string]CHType
	usingQualified      map[string]CHType
	arrayJoinTypes      map[string]CHType
	arrayJoinQualified  map[string]CHType
	exactScalarNames    bool
	windows             map[string]*clickhouse.WindowExpr
	scalarSubqueries    map[*clickhouse.SelectQuery]CHType
	expressionContracts map[*clickhouse.FunctionExpr]bool
	parent              *queryScope
}

type scopedTable struct {
	table Table
	alias string
}

// scopeIndex records the lexical scope for every AST node. clickhouse-sql-parser's
// generic walker traverses nested CTEs and derived tables, but does not expose a
// parent stack. Keeping the resolved scope by node lets parameter inference use
// the columns visible at the placeholder's actual SELECT level.
type scopeIndex struct {
	byNode              map[clickhouse.Expr]queryScope
	selects             map[*clickhouse.SelectQuery]queryScope
	scalarSubqueries    map[*clickhouse.SelectQuery]CHType
	resolvingScalar     map[*clickhouse.SelectQuery]bool
	queryResults        []scopedQueryResult
	expressionContracts map[*clickhouse.FunctionExpr]bool
}

type scopedQueryResult struct {
	name   string
	typeOf CHType
}

type functionTypeRule func([]CHType) (CHType, error)

// errPlaceholderResultType marks the one argument failure that a fixed
// result type may absorb: a bare positional placeholder. Every other
// argument failure must stay an explicit refusal, because a fixed result
// without the argument wrappers can silently drop a Nullable.
var errPlaceholderResultType = errors.New("positional placeholder has no result type")

// Go representation overrides cannot repair missing ClickHouse inference.
// A result-contract suggestion is added only at the eligible output boundary.
const pinTypeHint = "use a supported expression or add a measured type rule; -- result: changes Go mapping, not ClickHouse type inference"

// functionWrapperClass says how a function moves the Nullable and
// LowCardinality wrappers of its arguments into its result.
type functionWrapperClass int

const (
	// wrapperClassUnset is the zero value, and it is never legal. A
	// registry spec that omits its class therefore fails the guard test
	// TestEverySpecDeclaresAClass instead of being read, by accident, as
	// a genuine wrapperOpaque function. A lookup miss (an unknown
	// function name) also reports this value, and MUST NOT be read as
	// wrapperOpaque: "nobody answered this question" is a different
	// fact from "this function's result never carries a wrapper from
	// its arguments". See functionClassFor.
	wrapperClassUnset functionWrapperClass = iota
	// wrapperOpaque: the result never carries a wrapper from the
	// arguments (count, isNull, has), or the function's own type rule
	// already handles the wrappers (if, coalesce, groupArray).
	wrapperOpaque
	// wrapperTransparent: the result is Nullable when any argument is
	// Nullable. The result is LowCardinality when exactly one argument
	// is LowCardinality and each other argument is a constant literal.
	wrapperTransparent
	// wrapperAggregate: the result is Nullable when a data argument is
	// Nullable. The LowCardinality wrapper is always removed.
	wrapperAggregate
	// wrapperClassLimit is not a class. It is one past the last legal
	// member, and it exists so that a guard test can DERIVE the member
	// set instead of restating it.
	//
	// A hand-written list of the members inside a test is a second copy
	// of this enum, and it does not grow when a member is added here.
	// A test that walks such a list reports success for a new member
	// that no switch answers, which is the exact defect the guard
	// exists to prevent. This was measured: a fourth member added to
	// this block with no case in transportForClass left both guard
	// tests passing.
	//
	// Every new member MUST go above this line.
	wrapperClassLimit
)

func resolveQuery(query *Query, schema *Schema) error {
	return resolveQueryWithUncheckedSettings(query, schema, nil)
}

func resolveQueryWithUncheckedSettings(query *Query, schema *Schema, unchecked []string) error {
	statements, err := parseChgenStatements(query.SQL, query.Command)
	if err != nil {
		return fmt.Errorf("parse SQL for type resolution: %w", err)
	}
	if len(statements) != 1 {
		return fmt.Errorf("expected exactly one SQL statement, got %d", len(statements))
	}
	if err := omitUncheckedSettingsForResolution(statements[0], unchecked); err != nil {
		return err
	}
	for _, name := range unchecked {
		if _, measured := selectSettingRoster[name]; !measured {
			query.uncheckedSettings = append(query.uncheckedSettings, name)
		}
	}

	if query.Command == CommandExec {
		return resolveExecParams(query, statements[0], schema)
	}
	selectQuery, ok := statements[0].(*clickhouse.SelectQuery)
	if !ok {
		return fmt.Errorf("%s query must be a SELECT, got %T", query.Command, statements[0])
	}
	if err := validateResultContractTargets(query, selectQuery); err != nil {
		return err
	}
	scope, scopes, err := resolveScope(selectQuery, schema)
	if err != nil {
		var missing *unregisteredFunctionError
		if len(query.resultContracts) > 0 && errors.As(err, &missing) {
			return fmt.Errorf("%w; result-chtype does not supply types to unresolved nested scopes or aliases used in other clauses", err)
		}
		return err
	}
	if err := resolveParams(query, statements[0], scope, scopes); err != nil {
		return err
	}
	resultOverrides := make(map[string]Result, len(query.Results))
	for _, result := range query.Results {
		if _, exists := resultOverrides[result.SQLName]; exists {
			return fmt.Errorf("duplicate result annotation for %q", result.SQLName)
		}
		resultOverrides[result.SQLName] = result
	}
	if err := resolveResultContracts(query, selectQuery, scope, scopes); err != nil {
		return err
	}
	if err := ensureTopLevelQueryResults(selectQuery, scope, scopes, resultOverrides, true); err != nil {
		return err
	}
	if err := collectExpressionContracts(query, statements[0], scope, scopes); err != nil {
		return err
	}
	resolvedResults := make([]Result, 0, len(scopes.queryResults))
	for _, queryResult := range scopes.queryResults {
		itemName := queryResult.name
		result, ok := resultOverrides[itemName]
		if !ok {
			result = Result{GoName: exportedIdentifier(itemName), SQLName: itemName}
		}
		inferred := queryResult.typeOf
		inferredGoType, err := goType(inferred)
		if err != nil {
			return fmt.Errorf("result %s (%s): %w", result.GoName, result.SQLName, err)
		}
		if result.GoType == "" {
			result.GoType = inferredGoType
		}
		// Keep the ClickHouse type, also when an explicit -- result annotation
		// fixed the Go type. The generator derives the read-side check plan
		// from it, and the wrap that the driver applies depends on the column,
		// not on the Go spelling.
		result.CHType = inferred
		_, result.Asserted = query.resultContracts[itemName]
		// Output metadata is a last boundary check, not proof of intermediate
		// contracts (especially a contract used only by a WHERE predicate).
		result.Asserted = result.Asserted || len(query.unverifiedExpressions) > 0
		if result.GoName == "" {
			result.GoName = exportedIdentifier(result.SQLName)
		}
		if result.GoType != "" {
			if err := validateGoType(result.GoType); err != nil {
				return fmt.Errorf("result %s: %w", result.GoName, err)
			}
		}
		resolvedResults = append(resolvedResults, result)
	}
	if err := validateResultGoNames(resolvedResults); err != nil {
		return err
	}
	query.Results = resolvedResults
	return nil
}

func scalarSubqueryHasGuaranteedRow(subquery *clickhouse.SubQuery) bool {
	if subquery == nil || subquery.Select == nil || len(subquery.Select.SelectItems) != 1 {
		return false
	}
	selectQuery := subquery.Select
	if selectQuery.Where != nil || selectQuery.Prewhere != nil || selectQuery.Having != nil || selectQuery.Limit != nil ||
		selectQuery.LimitBy != nil || selectQuery.GroupBy != nil || selectQuery.Top != nil {
		return false
	}
	if selectQuery.From == nil {
		return true
	}
	return containsAggregateCall(selectQuery.SelectItems[0].Expr)
}

func scalarSubqueryHasAtMostOneRow(subquery *clickhouse.SubQuery) bool {
	if subquery == nil || subquery.Select == nil {
		return false
	}
	if selectQueryHasSetOperation(subquery.Select) {
		return false
	}
	if subquery.Select.From == nil {
		return true
	}
	if subquery.Select.GroupBy == nil && len(subquery.Select.SelectItems) == 1 && containsAggregateCall(subquery.Select.SelectItems[0].Expr) {
		return true
	}
	if subquery.Select.Limit != nil {
		literal, ok := unwrapColumnExpression(subquery.Select.Limit.Limit).(*clickhouse.NumberLiteral)
		if !ok {
			return false
		}
		value := strings.TrimSpace(literal.Literal)
		return value == "0" || value == "1"
	}
	return false
}

func selectQueryHasSetOperation(selectQuery *clickhouse.SelectQuery) bool {
	return selectQuery != nil && (selectQuery.InnerQuery != nil || selectQuery.UnionAll != nil || selectQuery.UnionDistinct != nil ||
		selectQuery.Except != nil || selectQuery.Intersect != nil)
}

func parseChgenStatements(sql string, command Command) ([]clickhouse.Expr, error) {
	statements, err := parseChgenStatementsWithAdapters(sql, command)
	return statements, diagnostic.With(err, diagnostic.Detail{
		Code: "query-parser-refusal", Status: diagnostic.Unknown, Stage: "parser",
		Hint: "Check SQL syntax against your ClickHouse version. If ClickHouse accepts it, this is a chgen parser coverage gap; a type annotation cannot repair parsing.",
	})
}

func parseChgenStatementsWithAdapters(sql string, command Command) ([]clickhouse.Expr, error) {
	statements, err := clickhouse.NewParser(sql).ParseStmts()
	if err == nil {
		return statements, err
	}

	// The parser-only copy has the same byte length and line breaks as sql.
	// Runtime SQL and source locations stay unchanged.
	normalized, unsafeCasePrefix, casePrefixPlus := normalizeCasePrefixOperands(sql)
	if casePrefixPlus {
		return nil, fmt.Errorf(`CASE operand: cannot infer type for unary operator "+"`)
	}
	if normalized != sql {
		normalizedStatements, normalizedErr := clickhouse.NewParser(normalized).ParseStmts()
		if normalizedErr == nil {
			return normalizedStatements, nil
		}
	}
	if unsafeCasePrefix {
		return nil, fmt.Errorf("CASE prefix operand cannot be parsed without changing source locations: %w", err)
	}

	// ClickHouse supports LIMIT ... WITH TIES, but the parser version used by
	// chgen does not model the modifier yet. Remove only the modifier from the
	// parser/type-resolution copy; generated runtime SQL remains byte-for-byte
	// unchanged. Keeping this normalization here lets query authors use native
	// ClickHouse boundary semantics without falling back to handwritten wrappers.
	withoutLimitModifier := stripLimitWithTiesModifiers(normalized)
	if withoutLimitModifier != normalized {
		if err := validateLimitWithTiesOwnership(normalized); err != nil {
			return nil, err
		}
		normalizedStatements, normalizedErr := clickhouse.NewParser(withoutLimitModifier).ParseStmts()
		if normalizedErr == nil {
			return normalizedStatements, nil
		}
		err = fmt.Errorf("original: %w; without unsupported LIMIT modifier: %v", err, normalizedErr)
	}

	if command != CommandExec {
		return nil, err
	}
	// The parser version used by the pilot does not model the trailing SETTINGS
	// clause on ALTER TABLE ... DELETE, which ClickHouse itself accepts, so
	// strip it for RESOLUTION only while the original SQL is preserved verbatim
	// for generated execution.
	stripped := stripTrailingSettingsClause(withoutLimitModifier)
	if stripped == withoutLimitModifier {
		return nil, err
	}
	strippedStatements, strippedErr := clickhouse.NewParser(stripped).ParseStmts()
	if strippedErr == nil {
		return strippedStatements, nil
	}
	return nil, fmt.Errorf("original: %w; without unsupported modifiers: %v", err, strippedErr)
}

func validateLimitWithTiesOwnership(sql string) error {
	depth := 0
	ordered := make(map[int]bool)
	limit := make(map[int]bool)
	nonScalar := make(map[int]bool)
	for index := 0; index < len(sql); {
		switch {
		case sql[index] == '\'' || sql[index] == '"' || sql[index] == '`':
			index = skipSQLQuoted(sql, index, sql[index])
		case strings.HasPrefix(sql[index:], "--"):
			index = skipSQLLineComment(sql, index)
		case strings.HasPrefix(sql[index:], "/*"):
			index = skipSQLBlockComment(sql, index)
		case sql[index] == '(':
			depth++
			context := previousSQLWord(sql, index)
			nonScalar[depth] = strings.EqualFold(context, "FROM") || strings.EqualFold(context, "JOIN") ||
				strings.EqualFold(context, "IN") || strings.EqualFold(context, "EXISTS") || strings.EqualFold(context, "AS")
			index++
		case sql[index] == ')':
			delete(ordered, depth)
			delete(limit, depth)
			delete(nonScalar, depth)
			if depth > 0 {
				depth--
			}
			index++
		case sqlKeywordAt(sql, index, "SELECT"):
			ordered[depth] = false
			limit[depth] = false
			index += len("SELECT")
		case sqlKeywordAt(sql, index, "ORDER"):
			next := skipSQLWhitespace(sql, index+len("ORDER"))
			if sqlKeywordAt(sql, next, "BY") {
				ordered[depth] = true
			}
			index += len("ORDER")
		case sqlKeywordAt(sql, index, "LIMIT"):
			limit[depth] = true
			index += len("LIMIT")
		case sqlKeywordAt(sql, index, "WITH"):
			next := skipSQLWhitespace(sql, index+len("WITH"))
			if sqlKeywordAt(sql, next, "TIES") && limit[depth] {
				if depth > 0 && !nonScalar[depth] {
					return fmt.Errorf("nested LIMIT WITH TIES is not supported")
				}
				if !ordered[depth] {
					return fmt.Errorf("LIMIT WITH TIES requires ORDER BY in the same SELECT query")
				}
			}
			index += len("WITH")
		default:
			index++
		}
	}
	return nil
}

func previousSQLWord(sql string, index int) string {
	for index > 0 && (sql[index-1] == ' ' || sql[index-1] == '\t' || sql[index-1] == '\n' || sql[index-1] == '\r') {
		index--
	}
	end := index
	for index > 0 && isSQLIdentifierByte(sql[index-1]) {
		index--
	}
	return sql[index:end]
}

func stripLimitWithTiesModifiers(sql string) string {
	normalized := []byte(sql)
	changed := false
	for index := 0; index < len(sql); {
		switch {
		case sql[index] == '\'' || sql[index] == '"' || sql[index] == '`':
			index = skipSQLQuoted(sql, index, sql[index])
		case strings.HasPrefix(sql[index:], "--"):
			index = skipSQLLineComment(sql, index)
		case strings.HasPrefix(sql[index:], "/*"):
			index = skipSQLBlockComment(sql, index)
		case sqlKeywordAt(sql, index, "WITH"):
			tiesStart := skipSQLWhitespace(sql, index+len("WITH"))
			if !sqlKeywordAt(sql, tiesStart, "TIES") {
				index++
				continue
			}
			tiesEnd := tiesStart + len("TIES")
			next := skipSQLWhitespace(sql, tiesEnd)
			if next < len(sql) && sql[next] != ')' && sql[next] != ';' &&
				!sqlKeywordAt(sql, next, "SETTINGS") &&
				!sqlKeywordAt(sql, next, "FORMAT") &&
				!sqlKeywordAt(sql, next, "OFFSET") {
				index++
				continue
			}
			for position := index; position < tiesEnd; position++ {
				if normalized[position] != '\n' && normalized[position] != '\r' {
					normalized[position] = ' '
				}
			}
			changed = true
			index = tiesEnd
		default:
			index++
		}
	}
	if !changed {
		return sql
	}
	return string(normalized)
}

func sqlKeywordAt(sql string, index int, keyword string) bool {
	if index < 0 || index+len(keyword) > len(sql) ||
		!strings.EqualFold(sql[index:index+len(keyword)], keyword) {
		return false
	}
	beforeOK := index == 0 || !isSQLIdentifierByte(sql[index-1])
	after := index + len(keyword)
	afterOK := after == len(sql) || !isSQLIdentifierByte(sql[after])
	return beforeOK && afterOK
}

func stripTrailingSettingsClause(sql string) string {
	last := -1
	for index := 0; index < len(sql); {
		switch {
		case sql[index] == '\'' || sql[index] == '"' || sql[index] == '`':
			index = skipSQLQuoted(sql, index, sql[index])
		case strings.HasPrefix(sql[index:], "--"):
			index = skipSQLLineComment(sql, index)
		case strings.HasPrefix(sql[index:], "/*"):
			index = skipSQLBlockComment(sql, index)
		default:
			if index+len("SETTINGS") <= len(sql) && strings.EqualFold(sql[index:index+len("SETTINGS")], "SETTINGS") {
				beforeOK := index == 0 || !isSQLIdentifierByte(sql[index-1])
				after := index + len("SETTINGS")
				afterOK := after == len(sql) || !isSQLIdentifierByte(sql[after])
				if beforeOK && afterOK {
					last = index
				}
			}
			index++
		}
	}
	if last < 0 {
		return sql
	}
	return strings.TrimSpace(sql[:last])
}

func resolveParams(query *Query, statement clickhouse.Expr, fallback queryScope, scopes *scopeIndex) error {
	var placeholders []*clickhouse.PlaceHolder
	inferredTypes := make(map[*clickhouse.PlaceHolder]CHType)
	paramNames := make(map[*clickhouse.PlaceHolder]string)
	collectionParams := make(map[*clickhouse.PlaceHolder]bool)
	clickhouse.Walk(statement, func(node clickhouse.Expr) bool {
		if placeholder, ok := node.(*clickhouse.PlaceHolder); ok {
			placeholders = append(placeholders, placeholder)
		}
		if function, ok := node.(*clickhouse.FunctionExpr); ok && strings.EqualFold(function.Name.Name, "startsWith") {
			args := functionArgs(function)
			if len(args) >= 2 {
				if columnName, ok := directColumnName(args[0]); ok {
					if placeholder, ok := args[1].(*clickhouse.PlaceHolder); ok {
						paramNames[placeholder] = columnName
						if inferred, err := inferExprType(args[0], scopes.lookup(args[0], fallback)); err == nil {
							inferredTypes[placeholder] = inferred
						}
					}
				}
			}
		}
		binary, ok := node.(*clickhouse.BinaryOperation)
		if !ok {
			return true
		}
		if placeholder, ok := binary.LeftExpr.(*clickhouse.PlaceHolder); ok {
			if inferred, err := inferExprType(binary.RightExpr, scopes.lookup(binary.RightExpr, fallback)); err == nil {
				inferredTypes[placeholder] = inferred
			}
			if name, ok := directColumnName(binary.RightExpr); ok {
				paramNames[placeholder] = name
			}
		}
		if placeholder, ok := binary.RightExpr.(*clickhouse.PlaceHolder); ok {
			if inferred, err := inferExprType(binary.LeftExpr, scopes.lookup(binary.LeftExpr, fallback)); err == nil {
				inferredTypes[placeholder] = inferred
			}
			if name, ok := directColumnName(binary.LeftExpr); ok {
				paramNames[placeholder] = name
			}
		}
		if strings.EqualFold(string(binary.Operation), "IN") || strings.EqualFold(string(binary.Operation), "NOT IN") {
			leftType, err := inferExprType(binary.LeftExpr, scopes.lookup(binary.LeftExpr, fallback))
			if err == nil {
				leftName, hasLeftName := directColumnName(binary.LeftExpr)
				clickhouse.Walk(binary.RightExpr, func(rightNode clickhouse.Expr) bool {
					placeholder, ok := rightNode.(*clickhouse.PlaceHolder)
					if ok {
						inferredTypes[placeholder] = CHType{Name: "Array", Params: []CHType{leftType}}
						collectionParams[placeholder] = true
						if hasLeftName {
							paramNames[placeholder] = leftName
						}
					}
					return true
				})
			}
		}
		return true
	})
	for limit := range scopes.selects {
		if limit.Limit == nil {
			continue
		}
		if placeholder, ok := limit.Limit.Limit.(*clickhouse.PlaceHolder); ok {
			inferredTypes[placeholder] = CHType{Name: "UInt64"}
			paramNames[placeholder] = "Limit"
		}
		if placeholder, ok := limit.Limit.Offset.(*clickhouse.PlaceHolder); ok {
			inferredTypes[placeholder] = CHType{Name: "UInt64"}
			paramNames[placeholder] = "Offset"
		}
	}
	if err := applyNamedParamNames(query, placeholders, paramNames, inferredTypes); err != nil {
		return err
	}
	return finalizeParams(query, placeholders, inferredTypes, paramNames, collectionParams)
}

func applyNamedParamNames(
	query *Query,
	placeholders []*clickhouse.PlaceHolder,
	paramNames map[*clickhouse.PlaceHolder]string,
	inferredTypes map[*clickhouse.PlaceHolder]CHType,
) error {
	if len(query.NamedParamNames) == 0 {
		return nil
	}
	if len(query.NamedParamNames) != len(placeholders) {
		return fmt.Errorf("found %d chgen.arg names for %d positional placeholders", len(query.NamedParamNames), len(placeholders))
	}
	namedTypes := make(map[string]CHType)
	namedTypePlaceholders := make(map[string][]*clickhouse.PlaceHolder)
	for index, name := range query.NamedParamNames {
		placeholder := placeholders[index]
		paramNames[placeholder] = name
		namedTypePlaceholders[name] = append(namedTypePlaceholders[name], placeholder)
		inferred, ok := inferredTypes[placeholder]
		if !ok {
			continue
		}
		previous, exists := namedTypes[name]
		if exists && !compatibleNamedArgTypes(previous, inferred) {
			return fmt.Errorf("chgen.arg(%q) has incompatible inferred types %s and %s", name, previous.String(), inferred.String())
		}
		if !exists {
			namedTypes[name] = inferred
		}
	}
	for name, typeInfo := range namedTypes {
		for _, placeholder := range namedTypePlaceholders[name] {
			if _, exists := inferredTypes[placeholder]; !exists {
				inferredTypes[placeholder] = typeInfo
			}
		}
	}
	return nil
}

// compatibleNamedArgTypes reports two types that one chgen.arg parameter can
// hold at the same time.
//
// The unwrap here is deliberately NOT splitCHWrappers. The check is
// RECURSIVE and unwraps ONE side at a time, and the structural name and
// parameter comparison sits BETWEEN the LowCardinality step and the Nullable
// step. That order lets Nullable(X) match Nullable(X) as a whole, and it
// applies to a nested parameter such as the element of an Array. A flat
// one-level split of both sides gives neither property.
func compatibleNamedArgTypes(left, right CHType) bool {
	if strings.EqualFold(left.Name, "LowCardinality") && len(left.Params) == 1 {
		return compatibleNamedArgTypes(left.Params[0], right)
	}
	if strings.EqualFold(right.Name, "LowCardinality") && len(right.Params) == 1 {
		return compatibleNamedArgTypes(left, right.Params[0])
	}
	if strings.EqualFold(left.Name, right.Name) &&
		len(left.LiteralParams) == len(right.LiteralParams) &&
		len(left.Params) == len(right.Params) {
		for index := range left.LiteralParams {
			if left.LiteralParams[index] != right.LiteralParams[index] {
				return false
			}
		}
		for index := range left.Params {
			if !compatibleNamedArgTypes(left.Params[index], right.Params[index]) {
				return false
			}
		}
		return true
	}
	if strings.EqualFold(left.Name, "Nullable") && len(left.Params) == 1 {
		return compatibleNamedArgTypes(left.Params[0], right)
	}
	if strings.EqualFold(right.Name, "Nullable") && len(right.Params) == 1 {
		return compatibleNamedArgTypes(left, right.Params[0])
	}
	return false
}

func numericCHType(value CHType) bool {
	switch strings.ToLower(value.Name) {
	case "uint8", "uint16", "uint32", "uint64", "uint128", "uint256",
		"int8", "int16", "int32", "int64", "int", "int128", "int256",
		"float32", "float64", "decimal", "decimal32", "decimal64", "decimal128", "decimal256":
		return len(value.Params) == 0
	default:
		return false
	}
}

// wideIntegerCHType reports whether the type is a 128 bit or 256 bit integer.
// These sizes follow the common integer rules, except against a float, where
// the server refuses the plus, minus and multiply operators.
func wideIntegerCHType(value CHType) bool {
	switch strings.ToLower(value.Name) {
	case "uint128", "uint256", "int128", "int256":
		return len(value.Params) == 0
	default:
		return false
	}
}

func pluralizeIdentifier(name string) string {
	if strings.HasSuffix(name, "IDs") || strings.HasSuffix(name, "Keys") || strings.HasSuffix(name, "s") {
		return name
	}
	return name + "s"
}

func resolveExecParams(query *Query, statement clickhouse.Expr, schema *Schema) error {
	placeholders := make([]*clickhouse.PlaceHolder, 0)
	inferredTypes := make(map[*clickhouse.PlaceHolder]CHType)
	paramNames := make(map[*clickhouse.PlaceHolder]string)
	collectionParams := make(map[*clickhouse.PlaceHolder]bool)
	clickhouse.Walk(statement, func(node clickhouse.Expr) bool {
		if placeholder, ok := node.(*clickhouse.PlaceHolder); ok {
			placeholders = append(placeholders, placeholder)
		}
		return true
	})

	switch exec := statement.(type) {
	case *clickhouse.InsertStmt:
		// clickhouse.Walk descends into InsertStmt.Values as of
		// clickhouse-sql-parser v0.5.2 (PR #284), so the top-level Walk above
		// already collected the fixed VALUES-tuple placeholders in AST order.
		// INSERT ... SELECT is walked through SelectExpr below, where the normal
		// SELECT resolver can infer predicates and named parameters against the
		// source tables.
		table, err := schemaTableForIdentifier(exec.Table, schema)
		if err != nil {
			return err
		}
		if exec.SelectExpr != nil {
			if len(exec.Values) != 0 {
				return fmt.Errorf("INSERT cannot contain both VALUES and SELECT")
			}
			targetColumns, err := insertSelectTargetColumns(table, exec.ColumnNames)
			if err != nil {
				return err
			}
			// INSERT SELECT has no VALUES tuple to use as a parameter type
			// context. Resolve the SELECT as its own lexical scope instead. The
			// source columns then provide types for predicates such as
			// `occurred_at >= ?`; callers can add an explicit -- param type when a
			// conversion function intentionally hides that context (for example
			// toDateTime(?)).
			selectScope, selectScopes, err := resolveScope(exec.SelectExpr, schema)
			if err != nil {
				return fmt.Errorf("INSERT SELECT source: %w", err)
			}
			if err := resolveParams(query, exec.SelectExpr, selectScope, selectScopes); err != nil {
				return err
			}
			if err := ensureTopLevelQueryResults(exec.SelectExpr, selectScope, selectScopes, nil, false); err != nil {
				return fmt.Errorf("INSERT SELECT source: %w", err)
			}
			if len(selectScopes.queryResults) != len(targetColumns) {
				return fmt.Errorf("INSERT SELECT has %d expressions for %d insertable columns", len(selectScopes.queryResults), len(targetColumns))
			}
			for index, result := range selectScopes.queryResults {
				if err := validateInsertSelectType(targetColumns[index], result.typeOf); err != nil {
					return err
				}
			}
			return nil
		}
		if exec.ColumnNames == nil || len(exec.ColumnNames.ColumnNames) == 0 {
			return fmt.Errorf("INSERT VALUES must specify an explicit column list")
		}
		if len(exec.Values) != 1 || len(exec.Values[0].Values) != len(exec.ColumnNames.ColumnNames) {
			return fmt.Errorf("fixed INSERT requires one VALUES row matching its column list")
		}
		batchEligible := true
		for index, value := range exec.Values[0].Values {
			columnName := nestedIdentifierName(&exec.ColumnNames.ColumnNames[index])
			column, ok := table.Columns[columnName]
			if !ok {
				return fmt.Errorf("table %s has no column %q", table.Name, columnName)
			}
			if _, bare := value.(*clickhouse.PlaceHolder); !bare {
				// A VALUES entry that wraps the placeholder in an expression,
				// for example toUInt32(?), has no column to append into, so the
				// native batch path cannot carry it.
				batchEligible = false
			}
			clickhouse.Walk(value, func(node clickhouse.Expr) bool {
				if placeholder, ok := node.(*clickhouse.PlaceHolder); ok {
					inferredTypes[placeholder] = column.Type
					paramNames[placeholder] = columnName
				}
				return true
			})
		}
		// The native PrepareBatch/Append path avoids the client-side text
		// interpolation that silently drops the sub-second fraction of every
		// time.Time and truncates a Map temporal to the Go default format.
		// It is available only when the whole VALUES tuple is bare
		// placeholders, one per named column.
		if batchEligible && len(placeholders) == len(exec.ColumnNames.ColumnNames) {
			query.batchInsert = true
			query.batchInsertTable = insertBatchTarget(exec)
		}
	case *clickhouse.AlterTable:
		if len(exec.AlterExprs) != 1 {
			return fmt.Errorf("fixed ALTER TABLE exec requires exactly one clause")
		}
		table, err := schemaTableForIdentifier(exec.TableIdentifier, schema)
		if err != nil {
			return err
		}
		scope := queryScope{tables: []scopedTable{{table: table, alias: table.Name}}}
		switch alter := exec.AlterExprs[0].(type) {
		case *clickhouse.AlterTableUpdate:
			for _, assignment := range alter.Assignments {
				columnName := nestedIdentifierName(assignment.Column)
				column, ok := table.Columns[columnName]
				if !ok {
					return fmt.Errorf("table %s has no column %q", table.Name, columnName)
				}
				clickhouse.Walk(assignment.Expr, func(node clickhouse.Expr) bool {
					if placeholder, ok := node.(*clickhouse.PlaceHolder); ok {
						inferredTypes[placeholder] = column.Type
						paramNames[placeholder] = columnName
					}
					return true
				})
			}
			inferPredicateParams(alter.WhereClause, scope, inferredTypes, paramNames, collectionParams)
		case *clickhouse.AlterTableDelete:
			inferPredicateParams(alter.WhereClause, scope, inferredTypes, paramNames, collectionParams)
		case *clickhouse.AlterTableDropPartition:
			// DROP PARTITION is metadata-only: it unlinks whole parts instead of
			// rewriting them like an ALTER ... DELETE mutation does. The partition
			// expression addresses a partition VALUE, not a table column, so there
			// is no column to infer a placeholder type from - the type must come
			// from a `-- param:` annotation in the query source.
			if alter.Partition == nil {
				return errors.New("ALTER TABLE DROP PARTITION requires a partition clause")
			}

			clickhouse.Walk(alter.Partition.Expr, func(node clickhouse.Expr) bool {
				if placeholder, ok := node.(*clickhouse.PlaceHolder); ok {
					paramNames[placeholder] = "partition"
				}

				return true
			})
		default:
			return fmt.Errorf("exec query supports INSERT, ALTER TABLE UPDATE, ALTER TABLE DELETE, or ALTER TABLE DROP PARTITION, got %T", exec.AlterExprs[0])
		}
	default:
		return fmt.Errorf("exec query supports INSERT, ALTER TABLE UPDATE, ALTER TABLE DELETE, or ALTER TABLE DROP PARTITION, got %T", statement)
	}

	if err := applyNamedParamNames(query, placeholders, paramNames, inferredTypes); err != nil {
		return err
	}
	return finalizeParams(query, placeholders, inferredTypes, paramNames, collectionParams)
}

func insertSelectTargetColumns(table Table, names *clickhouse.ColumnNamesExpr) ([]Column, error) {
	if names != nil {
		targets := make([]Column, 0, len(names.ColumnNames))
		for index := range names.ColumnNames {
			columnName := nestedIdentifierName(&names.ColumnNames[index])
			column, ok := table.Columns[columnName]
			if !ok {
				return nil, fmt.Errorf("table %s has no column %q", table.Name, columnName)
			}
			if !column.Insertable {
				return nil, fmt.Errorf("table %s column %q is not insertable", table.Name, columnName)
			}
			targets = append(targets, column)
		}
		return targets, nil
	}

	targets := make([]Column, 0, len(table.ColumnOrder))
	for _, columnName := range table.ColumnOrder {
		column := table.Columns[columnName]
		if column.Insertable {
			targets = append(targets, column)
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("table %s has no insertable columns", table.Name)
	}
	return targets, nil
}

func validateInsertSelectType(target Column, source CHType) error {
	if !insertTypesCompatible(target.Type, source) {
		return fmt.Errorf("INSERT SELECT target column %s has type %s, but SELECT expression has type %s", target.Name, target.Type.String(), source.String())
	}
	return nil
}

// insertTypesCompatible reports a SELECT expression type that an INSERT
// target column accepts.
//
// The unwrap here is deliberately NOT splitCHWrappers. The Nullable rule is
// ASYMMETRIC: a Nullable target accepts a non-Nullable source, but a
// non-Nullable target refuses a Nullable source. A flat split that reports
// only "was nullable" for each side cannot express that direction.
func insertTypesCompatible(target, source CHType) bool {
	target = unwrapLowCardinality(target)
	source = unwrapLowCardinality(source)

	if strings.EqualFold(target.Name, "Nullable") {
		if len(target.Params) != 1 {
			return false
		}
		if strings.EqualFold(source.Name, "Nullable") {
			if len(source.Params) != 1 {
				return false
			}
			source = source.Params[0]
		}
		return insertTypesCompatible(target.Params[0], source)
	}
	if strings.EqualFold(source.Name, "Nullable") {
		return false
	}
	// DateTime64 precision/timezone are conversion metadata rather than a
	// distinct storage family. ClickHouse accepts DateTime64 expressions with
	// a different scale or timezone when inserting into a DateTime64 column.
	if strings.EqualFold(target.Name, "DateTime64") && strings.EqualFold(source.Name, "DateTime64") {
		return true
	}
	if insertAcceptsAsString(target) && insertAcceptsAsString(source) {
		return true
	}
	if compatibleNamedArgTypes(target, source) {
		return true
	}
	if common, err := commonCHType(target, source); err == nil {
		return compatibleNamedArgTypes(common, target)
	}
	if strings.EqualFold(target.Name, "DateTime64") && strings.EqualFold(source.Name, "DateTime") {
		return true
	}
	if strings.EqualFold(target.Name, "Date32") && strings.EqualFold(source.Name, "Date") {
		return true
	}
	return false
}

// insertAcceptsAsString reports a type that an INSERT accepts through the
// String text form. The family is wider than the String supertype family:
// it also holds Enum8, Enum16, UUID, IPv4 and IPv6, because ClickHouse
// parses each of those from a String value on the insert path.
//
// Use this ONLY for insert compatibility. For the supertype lattice, which
// joins String and FixedString alone, use isStringSupertypeFamily.
func insertAcceptsAsString(value CHType) bool {
	switch strings.ToLower(value.Name) {
	case "string", "fixedstring", "enum8", "enum16", "uuid", "ipv4", "ipv6":
		return true
	default:
		return false
	}
}

// recordNamedArgGoName rejects two different chgen.arg source names that
// export to one Go field. Sharing one parameter is intended only for a
// repeated identical name; a silent merge of two different names would bind
// one value where the author wrote two.
func recordNamedArgGoName(sourceNames map[string]string, sourceName, goName string) error {
	previous, exists := sourceNames[goName]
	if exists && previous != sourceName {
		return fmt.Errorf(
			"chgen.arg names %q and %q both generate Go parameter %s",
			previous, sourceName, goName,
		)
	}
	sourceNames[goName] = sourceName
	return nil
}

func finalizeParams(
	query *Query,
	placeholders []*clickhouse.PlaceHolder,
	inferredTypes map[*clickhouse.PlaceHolder]CHType,
	paramNames map[*clickhouse.PlaceHolder]string,
	collectionParams map[*clickhouse.PlaceHolder]bool,
) error {
	if len(placeholders) == 0 {
		query.ParamIndexes = nil
		return nil
	}

	var indexes []int
	var params []Param
	switch {
	case len(query.NamedParamNames) > 0 && len(query.Params) > 0:
		// Named arguments normally infer one parameter per unique source name.
		// An annotation may override just one of those parameters (for example a
		// nullable LIMIT), so merge annotations by name instead of requiring one
		// annotation for every placeholder occurrence.
		annotations := make(map[string]Param, len(query.Params))
		for _, annotated := range query.Params {
			if _, exists := annotations[annotated.GoName]; exists {
				return fmt.Errorf("duplicate -- param annotation for %s", annotated.GoName)
			}
			annotations[annotated.GoName] = annotated
		}
		params = make([]Param, 0, len(query.NamedParamNames))
		indexes = make([]int, 0, len(placeholders))
		byName := make(map[string]int)
		sourceNames := make(map[string]string)
		usedAnnotations := make(map[string]bool)
		for _, sourceName := range query.NamedParamNames {
			name := exportedIdentifier(sourceName)
			if err := recordNamedArgGoName(sourceNames, sourceName, name); err != nil {
				return err
			}
			index, exists := byName[name]
			if !exists {
				param := Param{GoName: name}
				if annotated, ok := annotations[name]; ok {
					param = annotated
					usedAnnotations[name] = true
				}
				index = len(params)
				byName[name] = index
				params = append(params, param)
			}
			indexes = append(indexes, index)
		}
		for name := range annotations {
			if !usedAnnotations[name] {
				return fmt.Errorf("parameter %s is not mapped to a named SQL argument", name)
			}
		}
	case len(query.Params) == 0:
		// A repeated chgen.arg name means one value bound at every occurrence,
		// so named placeholders share one parameter. An anonymous placeholder
		// carries no such intent: a repeated column hint does not mean a
		// repeated value, so each anonymous placeholder gets its own parameter
		// and a repeated hint name gets a numeric suffix.
		named := len(query.NamedParamNames) > 0
		params = make([]Param, 0, len(placeholders))
		indexes = make([]int, 0, len(placeholders))
		byName := make(map[string]int)
		sourceNames := make(map[string]string)
		usedNames := make(map[string]int)
		for placeholderIndex, placeholder := range placeholders {
			name := parameterName(query, placeholderIndex, placeholder, paramNames, collectionParams)
			if named {
				if err := recordNamedArgGoName(sourceNames, query.NamedParamNames[placeholderIndex], name); err != nil {
					return err
				}
				if index, exists := byName[name]; exists {
					indexes = append(indexes, index)
					continue
				}
				byName[name] = len(params)
			} else {
				usedNames[name]++
				if usedNames[name] > 1 {
					name = fmt.Sprintf("%s%d", name, usedNames[name])
				}
			}
			indexes = append(indexes, len(params))
			params = append(params, Param{GoName: name})
		}
	case len(query.Params) == len(placeholders):
		params = make([]Param, 0, len(query.Params))
		indexes = make([]int, 0, len(placeholders))
		byName := make(map[string]int)
		for _, annotated := range query.Params {
			index, exists := byName[annotated.GoName]
			if !exists {
				index = len(params)
				byName[annotated.GoName] = index
				params = append(params, annotated)
			}
			indexes = append(indexes, index)
		}
	default:
		if len(query.Params) > len(placeholders) {
			return fmt.Errorf("SQL has %d positional placeholders but %d -- param annotations", len(placeholders), len(query.Params))
		}
		params = append([]Param(nil), query.Params...)
		indexes = make([]int, 0, len(placeholders))
		used := make([]bool, len(params))
		assignedHints := make(map[string]int)
		next := 0
		for placeholderIndex, placeholder := range placeholders {
			hint := parameterName(query, placeholderIndex, placeholder, paramNames, collectionParams)
			index, exists := assignedHints[hint]
			if hint == "Arg" {
				exists = false
			}
			if !exists {
				for candidate, param := range params {
					if !used[candidate] && param.GoName == hint {
						index = candidate
						exists = true
						break
					}
				}
			}
			if !exists {
				for next < len(params) && used[next] {
					next++
				}
				if next >= len(params) {
					return fmt.Errorf("could not map placeholder to a -- param annotation")
				}
				index = next
				next++
			}
			used[index] = true
			if hint != "Arg" {
				assignedHints[hint] = index
			}
			indexes = append(indexes, index)
		}
		for index, wasUsed := range used {
			if !wasUsed {
				return fmt.Errorf("parameter %s is not mapped to a SQL placeholder", params[index].GoName)
			}
		}
	}

	firstPlaceholder := make([]int, len(params))
	for index := range firstPlaceholder {
		firstPlaceholder[index] = -1
	}
	for placeholderIndex, paramIndex := range indexes {
		if firstPlaceholder[paramIndex] == -1 {
			firstPlaceholder[paramIndex] = placeholderIndex
		}
	}
	for index, param := range params {
		placeholderIndex := firstPlaceholder[index]
		inferred, hasInferred := inferredTypes[placeholders[placeholderIndex]]
		// Keep the ClickHouse type whenever the resolver knows it, also for a
		// parameter whose Go type came from an explicit -- param annotation.
		// The generator derives the range-guard walk plan from it, and the range
		// limit belongs to the column, not to the Go spelling of the value. A
		// parameter with no inferred type keeps the zero CHType, which holds no
		// temporal value and thus gets no guard.
		if hasInferred {
			params[index].CHType = inferred
		}
		if param.GoType != "" {
			continue
		}
		if !hasInferred {
			return fmt.Errorf("parameter %s has no inferable ClickHouse type; add an explicit GoType", param.GoName)
		}
		inferredGoType, err := goType(inferred)
		if err != nil {
			return fmt.Errorf("parameter %s: %w", param.GoName, err)
		}
		params[index].GoType = inferredGoType
	}
	query.Params = params
	query.ParamIndexes = indexes
	return nil
}

// inferPredicateParams derives placeholder hints from a DELETE/UPDATE WHERE
// clause. SELECT parameters use the richer scope-aware resolver above; exec
// predicates have a single target-table scope and need only the same
// column-vs-placeholder relation. Explicit -- param types remain the escape
// hatch for wrapped values such as toDateTime(?).
func inferPredicateParams(
	expression clickhouse.Expr,
	scope queryScope,
	inferredTypes map[*clickhouse.PlaceHolder]CHType,
	paramNames map[*clickhouse.PlaceHolder]string,
	collectionParams map[*clickhouse.PlaceHolder]bool,
) {
	clickhouse.Walk(expression, func(node clickhouse.Expr) bool {
		binary, ok := node.(*clickhouse.BinaryOperation)
		if !ok {
			return true
		}
		if placeholder, ok := binary.LeftExpr.(*clickhouse.PlaceHolder); ok {
			if inferred, err := inferExprType(binary.RightExpr, scope); err == nil {
				inferredTypes[placeholder] = inferred
			}
			if name, ok := directColumnName(binary.RightExpr); ok {
				paramNames[placeholder] = name
			}
		}
		if placeholder, ok := binary.RightExpr.(*clickhouse.PlaceHolder); ok {
			if inferred, err := inferExprType(binary.LeftExpr, scope); err == nil {
				inferredTypes[placeholder] = inferred
			}
			if name, ok := directColumnName(binary.LeftExpr); ok {
				paramNames[placeholder] = name
			}
		}
		if strings.EqualFold(string(binary.Operation), "IN") || strings.EqualFold(string(binary.Operation), "NOT IN") {
			leftType, err := inferExprType(binary.LeftExpr, scope)
			if err == nil {
				leftName, hasLeftName := directColumnName(binary.LeftExpr)
				clickhouse.Walk(binary.RightExpr, func(rightNode clickhouse.Expr) bool {
					placeholder, ok := rightNode.(*clickhouse.PlaceHolder)
					if ok {
						inferredTypes[placeholder] = CHType{Name: "Array", Params: []CHType{leftType}}
						collectionParams[placeholder] = true
						if hasLeftName {
							paramNames[placeholder] = leftName
						}
					}
					return true
				})
			}
		}
		return true
	})
}

func parameterName(
	query *Query,
	placeholderIndex int,
	placeholder *clickhouse.PlaceHolder,
	paramNames map[*clickhouse.PlaceHolder]string,
	collectionParams map[*clickhouse.PlaceHolder]bool,
) string {
	hint := paramNames[placeholder]
	if hint == "" {
		return "Arg"
	}
	name := exportedIdentifier(hint)
	if collectionParams[placeholder] && (len(query.NamedParamNames) == 0 || placeholderIndex >= len(query.NamedParamNames) || query.NamedParamNames[placeholderIndex] == "") {
		name = pluralizeIdentifier(name)
	}
	return name
}

func schemaTableForIdentifier(expression clickhouse.Expr, schema *Schema) (Table, error) {
	tableIdentifier, ok := expression.(*clickhouse.TableIdentifier)
	if !ok || tableIdentifier.Table == nil {
		return Table{}, fmt.Errorf("exec target is not a catalog table: %T", expression)
	}
	table, ok := schema.Tables[tableIdentifier.Table.Name]
	if !ok {
		message := fmt.Sprintf("table %q is not present in the schema or the query scope", tableIdentifier.Table.Name)
		if suggestion := suggestName(tableIdentifier.Table.Name, tableCandidates(schema, nil)); suggestion != "" {
			message += fmt.Sprintf("; did you mean %q?", suggestion)
		}
		return Table{}, fmt.Errorf("%s", message)
	}
	return table, nil
}

// insertBatchTarget builds the statement that PrepareBatch needs. The driver
// takes only the "INSERT INTO <table> (<columns>)" prefix: it appends the rows
// itself over the native protocol, so the VALUES tuple is left out.
func insertBatchTarget(insert *clickhouse.InsertStmt) string {
	table := ""
	if identifier, ok := insert.Table.(*clickhouse.TableIdentifier); ok && identifier.Table != nil {
		if identifier.Database != nil {
			table = identifier.Database.Name + "."
		}
		table += identifier.Table.Name
	}
	names := make([]string, 0, len(insert.ColumnNames.ColumnNames))
	for index := range insert.ColumnNames.ColumnNames {
		names = append(names, nestedIdentifierName(&insert.ColumnNames.ColumnNames[index]))
	}
	return fmt.Sprintf("INSERT INTO %s (%s)", table, strings.Join(names, ", "))
}

func nestedIdentifierName(identifier *clickhouse.NestedIdentifier) string {
	if identifier == nil || identifier.Ident == nil {
		return ""
	}
	name := identifier.Ident.Name
	if identifier.DotIdent != nil {
		name += "." + identifier.DotIdent.Name
	}
	return name
}

func resolveScope(selectQuery *clickhouse.SelectQuery, schema *Schema) (queryScope, *scopeIndex, error) {
	scopes := &scopeIndex{
		byNode:              make(map[clickhouse.Expr]queryScope),
		selects:             make(map[*clickhouse.SelectQuery]queryScope),
		scalarSubqueries:    make(map[*clickhouse.SelectQuery]CHType),
		resolvingScalar:     make(map[*clickhouse.SelectQuery]bool),
		expressionContracts: make(map[*clickhouse.FunctionExpr]bool),
	}
	if !selectQueryHasSetOperation(selectQuery) {
		scope, err := resolveSelectScope(selectQuery, schema, nil, scopes)
		if err != nil {
			return queryScope{}, nil, err
		}
		return scope, scopes, nil
	}
	scope, results, err := resolveSetQuery(selectQuery, schema, nil, scopes, false)
	if err != nil {
		return queryScope{}, nil, err
	}
	scopes.queryResults = results
	return scope, scopes, nil
}

func ensureTopLevelQueryResults(selectQuery *clickhouse.SelectQuery, scope queryScope, scopes *scopeIndex, overrides map[string]Result, requireNames bool) error {
	if len(scopes.queryResults) == 0 {
		results := make([]scopedQueryResult, 0, len(selectQuery.SelectItems))
		for _, item := range selectQuery.SelectItems {
			name, nameErr := selectItemSQLName(item)
			if nameErr != nil {
				if requireNames {
					return nameErr
				}
				name = fmt.Sprintf("_chgen_insert_column_%d", len(results)+1)
			}
			inferred, err := inferSelectItemType(item, scope)
			if err != nil {
				var missing *unregisteredFunctionError
				if item.Alias != nil && errors.As(err, &missing) && validateAssertedFunction(item.Expr, scope) == nil {
					err = fmt.Errorf("%w; this output can declare a runtime-checked -- result-chtype: %s ClickHouseType contract", err, item.Alias.Name)
				}
				result, ok := overrides[name]
				if !ok {
					result = Result{GoName: exportedIdentifier(name), SQLName: name}
				}
				return fmt.Errorf("result %s (%s): %w", result.GoName, result.SQLName, err)
			}
			results = append(results, scopedQueryResult{name: name, typeOf: inferred})
		}
		scopes.queryResults = results
		return nil
	}
	if !requireNames {
		return nil
	}
	return nameTopLevelSetResults(selectQuery, scopes.queryResults)
}

func nameTopLevelSetResults(selectQuery *clickhouse.SelectQuery, results []scopedQueryResult) error {
	leaves, err := setQueryLeaves(selectQuery)
	if err != nil {
		return err
	}
	if len(leaves) == 0 || len(leaves[0].SelectItems) != len(results) {
		return fmt.Errorf("set result name vector does not match the first branch")
	}
	for index, item := range leaves[0].SelectItems {
		name, err := selectItemSQLName(item)
		if err != nil {
			return err
		}
		results[index].name = name
	}
	return nil
}

func resolveSetQuery(
	selectQuery *clickhouse.SelectQuery,
	schema *Schema,
	parent *queryScope,
	scopes *scopeIndex,
	requireNames bool,
) (queryScope, []scopedQueryResult, error) {
	leaves, err := setQueryLeaves(selectQuery)
	if err != nil {
		return queryScope{}, nil, err
	}
	if len(leaves) == 0 {
		return queryScope{}, nil, fmt.Errorf("set query has no SELECT branch")
	}

	firstScope, err := resolveSelectScope(leaves[0], schema, parent, scopes)
	if err != nil {
		if len(leaves) == 1 {
			return queryScope{}, nil, err
		}
		return queryScope{}, nil, fmt.Errorf("set branch 1: %w", err)
	}
	branchScopes := make([]queryScope, 0, len(leaves))
	branchScopes = append(branchScopes, firstScope)
	shared := cteOnlyScope(firstScope, parent)
	if selectQuery.InnerQuery != nil {
		shared = cteOnlyScope(queryScope{scalarSubqueries: scopes.scalarSubqueries}, parent)
	}
	for index := 1; index < len(leaves); index++ {
		branchScope, branchErr := resolveSelectScope(leaves[index], schema, &shared, scopes)
		if branchErr != nil {
			return queryScope{}, nil, fmt.Errorf("set branch %d: %w", index+1, branchErr)
		}
		branchScopes = append(branchScopes, branchScope)
	}

	width := len(leaves[0].SelectItems)
	if width == 0 {
		return queryScope{}, nil, fmt.Errorf("set branch 1 has no result expressions")
	}
	for index := 1; index < len(leaves); index++ {
		if len(leaves[index].SelectItems) != width {
			return queryScope{}, nil, fmt.Errorf("set branch %d has %d result columns, want column count %d", index+1, len(leaves[index].SelectItems), width)
		}
	}
	results := make([]scopedQueryResult, width)
	for column := 0; column < width; column++ {
		name, nameErr := selectItemSQLName(leaves[0].SelectItems[column])
		if nameErr != nil {
			if requireNames {
				return queryScope{}, nil, fmt.Errorf("set result column %d name: %w", column+1, nameErr)
			}
			name = fmt.Sprintf("_chgen_set_column_%d", column+1)
		}
		types := make([]CHType, 0, len(leaves))
		for branch := range leaves {
			inferred, inferErr := inferSelectItemType(leaves[branch].SelectItems[column], branchScopes[branch])
			if inferErr != nil {
				if len(leaves) == 1 {
					return queryScope{}, nil, inferErr
				}
				return queryScope{}, nil, fmt.Errorf("set branch %d result column %d: %w", branch+1, column+1, inferErr)
			}
			types = append(types, inferred)
		}
		joined := types[0]
		if len(types) > 1 {
			var joinErr error
			joined, joinErr = commonSetOperationTypes(types)
			if joinErr != nil {
				return queryScope{}, nil, fmt.Errorf("set result column %d has no common type: %w", column+1, joinErr)
			}
		}
		results[column] = scopedQueryResult{name: name, typeOf: joined}
	}
	return firstScope, results, nil
}

func commonSetOperationTypes(types []CHType) (CHType, error) {
	if len(types) == 0 {
		return CHType{}, fmt.Errorf("set result has no branch types")
	}
	first := types[0].String()
	allExact := true
	for _, value := range types[1:] {
		if value.String() != first {
			allExact = false
			break
		}
	}
	if allExact {
		return types[0], nil
	}
	return commonCHTypes(types)
}

func setQueryLeaves(selectQuery *clickhouse.SelectQuery) ([]*clickhouse.SelectQuery, error) {
	if selectQuery == nil {
		return nil, fmt.Errorf("SELECT query is empty")
	}
	operations := []*clickhouse.SelectQuery{selectQuery.UnionAll, selectQuery.UnionDistinct, selectQuery.Except, selectQuery.Intersect}
	operationCount := 0
	var right *clickhouse.SelectQuery
	for _, operation := range operations {
		if operation != nil {
			operationCount++
			right = operation
		}
	}
	if operationCount > 1 {
		return nil, fmt.Errorf("SELECT query has more than one set-operation edge")
	}
	var leaves []*clickhouse.SelectQuery
	if selectQuery.InnerQuery != nil {
		inner, err := setQueryLeaves(selectQuery.InnerQuery)
		if err != nil {
			return nil, err
		}
		leaves = append(leaves, inner...)
	} else {
		leaves = append(leaves, selectQuery)
	}
	if right != nil {
		tail, err := setQueryLeaves(right)
		if err != nil {
			return nil, err
		}
		leaves = append(leaves, tail...)
	}
	return leaves, nil
}

func cteOnlyScope(scope queryScope, parent *queryScope) queryScope {
	return queryScope{
		expressionContracts: scope.expressionContracts,
		parent:              parent,
		scalars:             cloneScalarTypes(scope.scalars),
		exactScalarNames:    true,
		projectionAliases:   make(map[string]CHType),
		projectionExprs:     make(map[string]clickhouse.Expr),
		aliasExpansion:      make(map[string]bool),
		windows:             make(map[string]*clickhouse.WindowExpr),
		scalarSubqueries:    scope.scalarSubqueries,
		relations:           cloneRelations(scope.relations),
		reservedRelations:   cloneNames(scope.reservedRelations),
		reservedScalars:     cloneNames(scope.reservedScalars),
		usingTypes:          make(map[string]CHType),
		usingQualified:      make(map[string]CHType),
		arrayJoinTypes:      make(map[string]CHType),
		arrayJoinQualified:  make(map[string]CHType),
	}
}

func cloneScalarTypes(values map[string]CHType) map[string]CHType {
	copy := make(map[string]CHType, len(values))
	for name, value := range values {
		copy[name] = value
	}
	return copy
}

func cloneRelations(values map[string]Table) map[string]Table {
	copy := make(map[string]Table, len(values))
	for name, value := range values {
		copy[name] = value
	}
	return copy
}

func cloneNames(values map[string]bool) map[string]bool {
	copy := make(map[string]bool, len(values))
	for name, value := range values {
		copy[name] = value
	}
	return copy
}

type pendingCTE struct {
	name     string
	relation *clickhouse.SelectQuery
	scalar   clickhouse.Expr
}

func resolveWithClause(with *clickhouse.WithClause, schema *Schema, scope *queryScope, scopes *scopeIndex) error {
	entries := make([]pendingCTE, 0, len(with.CTEs))
	relationNames := make(map[string]bool)
	scalarNames := make(map[string]bool)
	for _, cte := range with.CTEs {
		if cte == nil {
			return fmt.Errorf("WITH clause has an empty CTE")
		}
		if cteQuery, ok := cte.Alias.(*clickhouse.SelectQuery); ok {
			name, err := relationName(cte.Expr)
			if err != nil {
				return fmt.Errorf("CTE name: %w", err)
			}
			if relationNames[name] {
				return fmt.Errorf("relation CTE %q is defined more than once", name)
			}
			relationNames[name] = true
			entries = append(entries, pendingCTE{name: name, relation: cteQuery})
			continue
		}
		name, err := relationName(cte.Alias)
		if err != nil {
			return fmt.Errorf("scalar CTE name: %w", err)
		}
		if scalarNames[name] {
			return fmt.Errorf("scalar CTE %q is defined more than once", name)
		}
		scalarNames[name] = true
		entries = append(entries, pendingCTE{name: name, scalar: cte.Expr})
	}
	for name := range relationNames {
		delete(scope.relations, name)
	}
	for name := range scalarNames {
		delete(scope.scalars, name)
	}
	scope.reservedRelations = cloneNames(relationNames)
	scope.reservedScalars = cloneNames(scalarNames)

	pending := make([]bool, len(entries))
	for index := range pending {
		pending[index] = true
	}
	remaining := len(entries)
	var firstErr error
	for remaining > 0 {
		progress := false
		firstErr = nil
		for index, entry := range entries {
			if !pending[index] {
				continue
			}
			trialScope := cloneQueryScope(*scope)
			trialScopes := cloneScopeIndex(scopes)
			if err := resolveOneCTE(entry, schema, &trialScope, trialScopes); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			*scope = trialScope
			*scopes = *trialScopes
			scope.scalarSubqueries = scopes.scalarSubqueries
			pending[index] = false
			remaining--
			progress = true
		}
		if !progress {
			return firstErr
		}
	}
	return nil
}

func resolveOneCTE(entry pendingCTE, schema *Schema, scope *queryScope, scopes *scopeIndex) error {
	if entry.relation != nil {
		table, err := resolveDerivedTable(entry.relation, entry.name, schema, scope, scopes, true)
		if err != nil {
			return fmt.Errorf("CTE %s: %w", entry.name, err)
		}
		scope.relations[entry.name] = table
		return nil
	}
	if entry.scalar == nil {
		return fmt.Errorf("scalar CTE %s has no expression", entry.name)
	}
	var scalarType CHType
	var err error
	if scalarQuery, ok := entry.scalar.(*clickhouse.SubQuery); ok {
		if scalarQuery.Select == nil {
			return fmt.Errorf("scalar CTE %s has no SELECT", entry.name)
		}
		scalarType, err = resolveScalarSubqueryType(scalarQuery, schema, scope, scopes)
	} else {
		if err = resolveScalarSubqueriesInExpr(entry.scalar, schema, scope, scopes); err == nil {
			scalarType, err = inferExprType(entry.scalar, *scope)
		}
	}
	if err != nil {
		return fmt.Errorf("scalar CTE %s: %w", entry.name, err)
	}
	scope.scalars[entry.name] = scalarType
	return nil
}

func cloneQueryScope(scope queryScope) queryScope {
	scope.scalars = cloneScalarTypes(scope.scalars)
	scope.projectionAliases = cloneScalarTypes(scope.projectionAliases)
	scope.projectionExprs = cloneProjectionExprs(scope.projectionExprs)
	scope.aliasExpansion = cloneAliasExpansion(scope.aliasExpansion)
	scope.windows = cloneWindows(scope.windows)
	scope.relations = cloneRelations(scope.relations)
	scope.reservedRelations = cloneNames(scope.reservedRelations)
	scope.reservedScalars = cloneNames(scope.reservedScalars)
	scope.usingTypes = cloneScalarTypes(scope.usingTypes)
	scope.usingQualified = cloneScalarTypes(scope.usingQualified)
	scope.arrayJoinTypes = cloneScalarTypes(scope.arrayJoinTypes)
	scope.arrayJoinQualified = cloneScalarTypes(scope.arrayJoinQualified)
	scope.tables = append([]scopedTable(nil), scope.tables...)
	return scope
}

func cloneProjectionExprs(values map[string]clickhouse.Expr) map[string]clickhouse.Expr {
	copy := make(map[string]clickhouse.Expr, len(values))
	for name, value := range values {
		copy[name] = value
	}
	return copy
}

func cloneWindows(values map[string]*clickhouse.WindowExpr) map[string]*clickhouse.WindowExpr {
	copy := make(map[string]*clickhouse.WindowExpr, len(values))
	for name, value := range values {
		copy[name] = value
	}
	return copy
}

func cloneScopeIndex(scopes *scopeIndex) *scopeIndex {
	copy := &scopeIndex{
		byNode:           make(map[clickhouse.Expr]queryScope, len(scopes.byNode)),
		selects:          make(map[*clickhouse.SelectQuery]queryScope, len(scopes.selects)),
		scalarSubqueries: make(map[*clickhouse.SelectQuery]CHType, len(scopes.scalarSubqueries)),
		resolvingScalar:  make(map[*clickhouse.SelectQuery]bool, len(scopes.resolvingScalar)),
		queryResults:     append([]scopedQueryResult(nil), scopes.queryResults...),
	}
	for node, scope := range scopes.byNode {
		copy.byNode[node] = scope
	}
	for query, scope := range scopes.selects {
		copy.selects[query] = scope
	}
	for query, value := range scopes.scalarSubqueries {
		copy.scalarSubqueries[query] = value
	}
	for query, value := range scopes.resolvingScalar {
		copy.resolvingScalar[query] = value
	}
	return copy
}

func resolveSelectScope(
	selectQuery *clickhouse.SelectQuery,
	schema *Schema,
	parent *queryScope,
	scopes *scopeIndex,
) (queryScope, error) {
	scope := queryScope{
		expressionContracts: scopes.expressionContracts,
		parent:              parent,
		scalars:             make(map[string]CHType),
		projectionAliases:   make(map[string]CHType),
		projectionExprs:     make(map[string]clickhouse.Expr),
		aliasExpansion:      make(map[string]bool),
		windows:             make(map[string]*clickhouse.WindowExpr),
		scalarSubqueries:    scopes.scalarSubqueries,
		exactScalarNames:    true,
		relations:           make(map[string]Table),
		reservedRelations:   make(map[string]bool),
		reservedScalars:     make(map[string]bool),
		usingTypes:          make(map[string]CHType),
		usingQualified:      make(map[string]CHType),
		arrayJoinTypes:      make(map[string]CHType),
		arrayJoinQualified:  make(map[string]CHType),
	}
	if parent != nil {
		for name, table := range parent.relations {
			scope.relations[name] = table
		}
	}
	if err := addProjectionAliasExpressions(selectQuery, &scope); err != nil {
		return queryScope{}, err
	}
	if selectQuery.With != nil {
		if err := resolveWithClause(selectQuery.With, schema, &scope, scopes); err != nil {
			return queryScope{}, err
		}
	}
	if selectQuery.From != nil {
		scope.fromBindingStart = len(scope.tables)
		if err := collectTables(selectQuery.From.Expr, schema, &scope, scopes); err != nil {
			return queryScope{}, err
		}
		if len(scope.tables) == 0 {
			return queryScope{}, fmt.Errorf("FROM clause contains no catalog table")
		}
		if err := recordJoinUsingTypes(selectQuery.From.Expr, &scope); err != nil {
			return queryScope{}, err
		}
	}
	for name, expression := range scope.projectionExprs {
		if err := resolveScalarSubqueriesInExpr(expression, schema, &scope, scopes); err != nil {
			return queryScope{}, fmt.Errorf("projection alias %s: %w", name, err)
		}
	}
	if selectQuery.Window != nil {
		for _, definition := range selectQuery.Window.Windows {
			if definition == nil || definition.Name == nil || definition.Expr == nil {
				return queryScope{}, fmt.Errorf("WINDOW clause has an empty definition")
			}
			name := definition.Name.Name
			if _, exists := scope.windows[name]; exists {
				return queryScope{}, fmt.Errorf("WINDOW clause defines %s more than once", definition.Name.Name)
			}
			scope.windows[name] = definition.Expr
		}
		if err := validateNamedWindowDefinitions(scope); err != nil {
			return queryScope{}, err
		}
	}
	if err := validateExecutableSelectClauses(selectQuery, schema, &scope, scopes); err != nil {
		return queryScope{}, err
	}
	for _, item := range selectQuery.SelectItems {
		if err := resolveScalarSubqueriesInExpr(item.Expr, schema, &scope, scopes); err != nil {
			return queryScope{}, err
		}
	}
	scopes.selects[selectQuery] = scope
	markSelectScope(selectQuery, scope, scopes)
	return scope, nil
}

func validateExecutableSelectClauses(
	selectQuery *clickhouse.SelectQuery,
	schema *Schema,
	scope *queryScope,
	scopes *scopeIndex,
) error {
	if selectQuery.Prewhere != nil {
		if expressionUsesArrayJoinOutput(selectQuery.Prewhere.Expr, *scope, make(map[string]bool)) {
			return fmt.Errorf("PREWHERE cannot use an ARRAY JOIN output")
		}
		if err := validateClauseFunctionPlacement("PREWHERE", selectQuery.Prewhere.Expr, false, scope); err != nil {
			return err
		}
		if err := validateClauseCondition("PREWHERE", selectQuery.Prewhere.Expr, schema, scope, scopes); err != nil {
			return err
		}
	}
	if selectQuery.Where != nil {
		if err := validateClauseFunctionPlacement("WHERE", selectQuery.Where.Expr, false, scope); err != nil {
			return err
		}
		if err := validateClauseCondition("WHERE", selectQuery.Where.Expr, schema, scope, scopes); err != nil {
			return err
		}
	}
	if selectQuery.From != nil {
		if err := validateJoinConditions(selectQuery.From.Expr, schema, scope, scopes); err != nil {
			return err
		}
	}
	if selectQuery.DistinctOn != nil {
		if err := validateDistinctOn(selectQuery.DistinctOn, *scope); err != nil {
			return err
		}
	}
	if selectQuery.Top != nil {
		if err := validateLimitExpression("TOP", selectQuery.Top.Number, schema, scope, scopes); err != nil {
			return err
		}
		if selectQuery.Top.WithTies && selectQuery.OrderBy == nil {
			return fmt.Errorf("TOP WITH TIES requires ORDER BY")
		}
	}
	if selectQuery.GroupBy != nil {
		if err := validateGroupByClause(selectQuery, schema, scope, scopes); err != nil {
			return err
		}
	}
	hasAggregation := selectQueryHasAggregate(selectQuery)
	if selectQuery.GroupBy == nil && hasAggregation {
		for _, item := range selectQuery.SelectItems {
			if err := validateGroupedExpression(item.Expr, nil, *scope); err != nil {
				return fmt.Errorf("aggregate projection: %w", err)
			}
		}
	}
	for _, item := range selectQuery.SelectItems {
		if err := resolveScalarSubqueriesInExpr(item.Expr, schema, scope, scopes); err != nil {
			return err
		}
	}
	if selectQuery.Having != nil || selectQuery.OrderBy != nil || selectQuery.LimitBy != nil {
		if err := addProjectionAliases(selectQuery, scope); err != nil {
			return err
		}
	}
	if hasAggregation {
		if err := validateAggregateClauseCoverage(selectQuery, *scope); err != nil {
			return err
		}
	}
	if selectQuery.Having != nil {
		if err := validateClauseFunctionPlacement("HAVING", selectQuery.Having.Expr, true, scope); err != nil {
			return err
		}
		if err := validateClauseCondition("HAVING", selectQuery.Having.Expr, schema, scope, scopes); err != nil {
			return err
		}
	}
	if selectQuery.Settings != nil {
		if err := validateSettingsClause(selectQuery.Settings, schema, scope, scopes); err != nil {
			return err
		}
	}
	if selectQuery.OrderBy != nil {
		for _, item := range selectQuery.OrderBy.Items {
			order, ok := item.(*clickhouse.OrderExpr)
			if !ok || order.Expr == nil {
				return fmt.Errorf("ORDER BY item %T is not supported", item)
			}
			if err := validateClauseExpression("ORDER BY", order.Expr, schema, scope, scopes); err != nil {
				return err
			}
			orderType, err := inferExprType(order.Expr, *scope)
			if err != nil {
				return fmt.Errorf("ORDER BY expression: %w", err)
			}
			if order.Fill != nil {
				for _, fill := range []struct {
					role string
					expr clickhouse.Expr
				}{
					{role: "ORDER BY WITH FILL FROM", expr: order.Fill.From},
					{role: "ORDER BY WITH FILL TO", expr: order.Fill.To},
					{role: "ORDER BY WITH FILL STEP", expr: order.Fill.Step},
					{role: "ORDER BY WITH FILL STALENESS", expr: order.Fill.Staleness},
				} {
					if fill.expr != nil {
						if err := validateOrderFillExpression(fill.role, fill.expr, orderType, schema, scope, scopes); err != nil {
							return err
						}
					}
				}
			}
		}
		if selectQuery.OrderBy.Interpolate != nil {
			return fmt.Errorf("ORDER BY INTERPOLATE semantic validation is not supported")
		}
	}
	if selectQuery.LimitBy != nil {
		if err := validateLimitClause("LIMIT BY", selectQuery.LimitBy.Limit, schema, scope, scopes); err != nil {
			return err
		}
		if selectQuery.LimitBy.ByExpr == nil || len(selectQuery.LimitBy.ByExpr.Items) == 0 {
			return fmt.Errorf("LIMIT BY has no key expression")
		}
		for _, expression := range selectQuery.LimitBy.ByExpr.Items {
			if err := validateClauseExpression("LIMIT BY key", expression, schema, scope, scopes); err != nil {
				return err
			}
		}
	}
	if selectQuery.Limit != nil {
		if err := validateLimitClause("LIMIT", selectQuery.Limit, schema, scope, scopes); err != nil {
			return err
		}
	}
	return nil
}

func expressionUsesArrayJoinOutput(expression clickhouse.Expr, scope queryScope, activeAliases map[string]bool) bool {
	found := false
	clickhouse.Walk(expression, func(node clickhouse.Expr) bool {
		if found {
			return false
		}
		switch value := node.(type) {
		case *clickhouse.SubQuery, *clickhouse.SelectQuery:
			return false
		case *clickhouse.Ident:
			if alias, ok := scope.projectionExprs[value.Name]; ok && !activeAliases[value.Name] {
				activeAliases[value.Name] = true
				found = expressionUsesArrayJoinOutput(alias, scope, activeAliases)
				delete(activeAliases, value.Name)
				return false
			}
			if _, scalar := scope.lookupLocalScalar(value.Name); !scalar {
				_, found = scope.arrayJoinTypes[value.Name]
			}
		case *clickhouse.Path:
			if len(value.Fields) >= 2 {
				qualifier := value.Fields[len(value.Fields)-2].Name
				name := value.Fields[len(value.Fields)-1].Name
				_, found = scope.arrayJoinQualified[qualifier+"."+name]
				if !found {
					_, found = scope.arrayJoinTypes[qualifier]
				}
			}
		}
		return !found
	})
	return found
}

func validateOrderFillExpression(
	role string,
	expression clickhouse.Expr,
	orderType CHType,
	schema *Schema,
	scope *queryScope,
	scopes *scopeIndex,
) error {
	if err := validateClauseExpression(role, expression, schema, scope, scopes); err != nil {
		return err
	}
	valueType, err := inferExprType(expression, *scope)
	if err != nil {
		return fmt.Errorf("%s expression: %w", role, err)
	}
	if strings.HasSuffix(role, "FROM") || strings.HasSuffix(role, "TO") {
		if _, err := commonCHTypes([]CHType{orderType, valueType}); err != nil {
			return fmt.Errorf("%s expression has type %s incompatible with order key %s: %w", role, valueType.String(), orderType.String(), err)
		}
		if _, ok := unwrapColumnExpression(expression).(*clickhouse.NumberLiteral); !ok {
			return fmt.Errorf("%s must be a measured constant literal", role)
		}
		return nil
	}
	literal, ok := unwrapColumnExpression(expression).(*clickhouse.NumberLiteral)
	if !ok {
		return fmt.Errorf("%s must be a positive numeric literal", role)
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(literal.Literal), 64)
	if err != nil || value <= 0 {
		return fmt.Errorf("%s must be a positive numeric literal", role)
	}
	return nil
}

func validateDistinctOn(distinct *clickhouse.DistinctOn, scope queryScope) error {
	if distinct == nil || len(distinct.Idents) == 0 {
		return fmt.Errorf("DISTINCT ON has no identifier")
	}
	for _, identifier := range distinct.Idents {
		if identifier == nil || identifier.Ident == nil {
			return fmt.Errorf("DISTINCT ON has an empty identifier")
		}
		qualifier := ""
		name := identifier.Ident.Name
		if identifier.DotIdent != nil {
			qualifier = name
			name = identifier.DotIdent.Name
		}
		if qualifier == "" {
			if _, exists := scope.projectionExprs[name]; exists {
				if _, err := inferExprType(identifier.Ident, scope); err != nil {
					return fmt.Errorf("DISTINCT ON alias: %w", err)
				}
				continue
			}
		}
		if _, err := scope.lookupColumn(qualifier, name); err != nil {
			return fmt.Errorf("DISTINCT ON identifier: %w", err)
		}
	}
	return nil
}

func validateGroupByClause(selectQuery *clickhouse.SelectQuery, schema *Schema, scope *queryScope, scopes *scopeIndex) error {
	group := selectQuery.GroupBy
	if group.Expr == nil || group.AggregateType != "" || group.WithCube || group.WithRollup || group.WithTotals {
		return fmt.Errorf("GROUP BY shape is not supported")
	}
	expressions := []clickhouse.Expr{group.Expr}
	if list, ok := group.Expr.(*clickhouse.ColumnExprList); ok {
		expressions = list.Items
	}
	groupKeys := make(map[string]bool, len(expressions))
	for _, expression := range expressions {
		if err := validateClauseFunctionPlacement("GROUP BY", expression, false, scope); err != nil {
			return err
		}
		if err := validateClauseExpression("GROUP BY", expression, schema, scope, scopes); err != nil {
			return err
		}
		groupKeys[clickhouse.Format(expression)] = true
		if identifier, ok := unwrapColumnExpression(expression).(*clickhouse.Ident); ok {
			if aliasExpr, exists := scope.projectionExprs[identifier.Name]; exists {
				groupKeys[clickhouse.Format(aliasExpr)] = true
			}
		}
	}
	for _, item := range selectQuery.SelectItems {
		if err := validateGroupedExpression(item.Expr, groupKeys, *scope); err != nil {
			return err
		}
	}
	return nil
}

func validateClauseFunctionPlacement(role string, expression clickhouse.Expr, allowAggregate bool, scope *queryScope) error {
	var validationErr error
	activeAliases := make(map[string]bool)
	var validateExpression func(clickhouse.Expr)
	validateExpression = func(current clickhouse.Expr) {
		clickhouse.Walk(current, func(node clickhouse.Expr) bool {
			if validationErr != nil {
				return false
			}
			if _, ok := node.(*clickhouse.WindowFunctionExpr); ok {
				validationErr = fmt.Errorf("%s cannot contain a window function", role)
				return false
			}
			switch node.(type) {
			case *clickhouse.SubQuery, *clickhouse.SelectQuery:
				return false
			}
			if identifier, ok := node.(*clickhouse.Ident); ok && scope != nil {
				if aliasExpr, exists := scope.projectionExprs[identifier.Name]; exists && !activeAliases[identifier.Name] {
					activeAliases[identifier.Name] = true
					validateExpression(aliasExpr)
					delete(activeAliases, identifier.Name)
					return false
				}
			}
			function, ok := node.(*clickhouse.FunctionExpr)
			if ok && function.Name != nil && isAggregateCallName(strings.ToLower(function.Name.Name)) && !allowAggregate {
				validationErr = fmt.Errorf("%s cannot contain an aggregate function", role)
				return false
			}
			return true
		})
	}
	validateExpression(expression)
	return validationErr
}

func selectQueryHasAggregate(selectQuery *clickhouse.SelectQuery) bool {
	for _, item := range selectQuery.SelectItems {
		if containsAggregateCall(item.Expr) {
			return true
		}
	}
	if selectQuery.Having != nil && containsAggregateCall(selectQuery.Having.Expr) {
		return true
	}
	if selectQuery.OrderBy != nil {
		for _, item := range selectQuery.OrderBy.Items {
			if order, ok := item.(*clickhouse.OrderExpr); ok && containsAggregateCall(order.Expr) {
				return true
			}
		}
	}
	if selectQuery.LimitBy != nil && selectQuery.LimitBy.ByExpr != nil {
		for _, expression := range selectQuery.LimitBy.ByExpr.Items {
			if containsAggregateCall(expression) {
				return true
			}
		}
	}
	if selectQuery.Window != nil {
		for _, window := range selectQuery.Window.Windows {
			if window != nil && window.Expr != nil && containsAggregateCall(window.Expr) {
				return true
			}
		}
	}
	return false
}

func validateAggregateClauseCoverage(selectQuery *clickhouse.SelectQuery, scope queryScope) error {
	groupKeys := make(map[string]bool)
	if selectQuery.GroupBy != nil && selectQuery.GroupBy.Expr != nil {
		expressions := []clickhouse.Expr{selectQuery.GroupBy.Expr}
		if list, ok := selectQuery.GroupBy.Expr.(*clickhouse.ColumnExprList); ok {
			expressions = list.Items
		}
		for _, expression := range expressions {
			groupKeys[clickhouse.Format(expression)] = true
		}
	}
	checks := make([]struct {
		role string
		expr clickhouse.Expr
	}, 0)
	if selectQuery.Having != nil {
		checks = append(checks, struct {
			role string
			expr clickhouse.Expr
		}{role: "HAVING", expr: selectQuery.Having.Expr})
	}
	if selectQuery.OrderBy != nil {
		for _, item := range selectQuery.OrderBy.Items {
			if order, ok := item.(*clickhouse.OrderExpr); ok {
				checks = append(checks, struct {
					role string
					expr clickhouse.Expr
				}{role: "ORDER BY", expr: order.Expr})
			}
		}
	}
	if selectQuery.LimitBy != nil && selectQuery.LimitBy.ByExpr != nil {
		for _, expression := range selectQuery.LimitBy.ByExpr.Items {
			checks = append(checks, struct {
				role string
				expr clickhouse.Expr
			}{role: "LIMIT BY", expr: expression})
		}
	}
	if selectQuery.Window != nil {
		for _, window := range selectQuery.Window.Windows {
			if window != nil && window.Expr != nil {
				checks = append(checks, struct {
					role string
					expr clickhouse.Expr
				}{role: "WINDOW", expr: window.Expr})
			}
		}
	}
	for _, check := range checks {
		if err := validateGroupedExpression(check.expr, groupKeys, scope); err != nil {
			return fmt.Errorf("%s aggregate coverage: %w", check.role, err)
		}
	}
	return nil
}

func validateGroupedExpression(expression clickhouse.Expr, groupKeys map[string]bool, scope queryScope) error {
	var validationErr error
	clickhouse.Walk(expression, func(node clickhouse.Expr) bool {
		if validationErr != nil {
			return false
		}
		if groupKeys[clickhouse.Format(node)] {
			return false
		}
		if identifier, ok := node.(*clickhouse.Ident); ok {
			if _, isProjectionAlias := scope.projectionAliases[identifier.Name]; isProjectionAlias {
				return false
			}
		}
		if _, ok := node.(*clickhouse.SubQuery); ok {
			return false
		}
		if window, ok := node.(*clickhouse.WindowFunctionExpr); ok {
			if window.Function != nil && window.Function.Params != nil && window.Function.Params.Items != nil {
				for _, argument := range window.Function.Params.Items.Items {
					if err := validateGroupedExpression(argument, groupKeys, scope); err != nil {
						validationErr = err
						return false
					}
				}
			}
			if window.OverExpr != nil {
				validationErr = validateGroupedExpression(window.OverExpr, groupKeys, scope)
			}
			return false
		}
		if function, ok := node.(*clickhouse.FunctionExpr); ok && function.Name != nil && isAggregateCallName(strings.ToLower(function.Name.Name)) {
			return false
		}
		switch value := node.(type) {
		case *clickhouse.Path:
			if len(value.Fields) >= 2 {
				qualifier := value.Fields[len(value.Fields)-2].Name
				name := value.Fields[len(value.Fields)-1].Name
				if _, err := scope.lookupColumn(qualifier, name); err == nil {
					validationErr = fmt.Errorf("GROUP BY does not cover selected column %s", clickhouse.Format(value))
				}
			}
			return false
		case *clickhouse.Ident:
			if _, err := scope.lookupColumn("", value.Name); err == nil {
				validationErr = fmt.Errorf("GROUP BY does not cover selected column %s", value.Name)
			}
		}
		return true
	})
	return validationErr
}

func containsAggregateCall(expression clickhouse.Expr) bool {
	found := false
	clickhouse.Walk(expression, func(node clickhouse.Expr) bool {
		switch value := node.(type) {
		case *clickhouse.SubQuery:
			return false
		case *clickhouse.WindowFunctionExpr:
			if value.OverExpr != nil && containsAggregateCall(value.OverExpr) {
				found = true
			}
			return false
		}
		function, ok := node.(*clickhouse.FunctionExpr)
		if ok && function.Name != nil && isAggregateCallName(strings.ToLower(function.Name.Name)) {
			found = true
			return false
		}
		return !found
	})
	return found
}

func expressionUsesTableColumn(expression clickhouse.Expr, scope queryScope) bool {
	found := false
	clickhouse.Walk(expression, func(node clickhouse.Expr) bool {
		switch value := node.(type) {
		case *clickhouse.Ident:
			if _, err := scope.lookupColumn("", value.Name); err == nil {
				found = true
			}
		case *clickhouse.Path:
			if len(value.Fields) >= 2 {
				if _, err := scope.lookupColumn(value.Fields[len(value.Fields)-2].Name, value.Fields[len(value.Fields)-1].Name); err == nil {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

func addProjectionAliases(selectQuery *clickhouse.SelectQuery, scope *queryScope) error {
	for _, item := range selectQuery.SelectItems {
		name, err := selectItemSQLName(item)
		if err != nil {
			continue
		}
		inferred, err := inferSelectItemType(item, *scope)
		if err != nil {
			var missing *unregisteredFunctionError
			if errors.As(err, &missing) && validateAssertedFunction(item.Expr, *scope) == nil {
				// Leave the expression available for normal alias expansion.
				// A later clause using it must still resolve its own type;
				// only the final output may have a result contract.
				continue
			}
			return fmt.Errorf("selected expression %s: %w", name, err)
		}
		scope.projectionAliases[name] = inferred
	}
	return nil
}

func addProjectionAliasExpressions(selectQuery *clickhouse.SelectQuery, scope *queryScope) error {
	for _, item := range selectQuery.SelectItems {
		if item.Alias == nil {
			continue
		}
		name := item.Alias.Name
		if previous, exists := scope.projectionExprs[name]; exists {
			if clickhouse.Format(previous) != clickhouse.Format(item.Expr) {
				return fmt.Errorf("projection alias %q has different expressions", name)
			}
			continue
		}
		scope.projectionExprs[name] = item.Expr
	}
	return nil
}

func inferSelectItemType(item *clickhouse.SelectItem, scope queryScope) (CHType, error) {
	if item == nil {
		return CHType{}, fmt.Errorf("SELECT item is empty")
	}
	if item.Alias != nil {
		scope.aliasExpansion = cloneAliasExpansion(scope.aliasExpansion)
		scope.aliasExpansion[item.Alias.Name] = true
	}
	return inferExprType(item.Expr, scope)
}

type selectSettingValueKind uint8

const (
	selectSettingUnsignedLiteral selectSettingValueKind = iota + 1
	selectSettingStringLiteral
	selectSettingBooleanLiteral
)

type selectSettingRule struct {
	kind    selectSettingValueKind
	nonZero bool
	hasMax  bool
	max     uint64
}

const maxExecutionTimeWholeSeconds uint64 = 9_223_372_036_854

var selectSettingRoster = map[string]selectSettingRule{
	"do_not_merge_across_partitions_select_final": {kind: selectSettingBooleanLiteral},
	"log_comment":                                  {kind: selectSettingStringLiteral},
	"max_block_size":                               {kind: selectSettingUnsignedLiteral, nonZero: true},
	"max_bytes_before_external_group_by":           {kind: selectSettingUnsignedLiteral},
	"max_bytes_before_external_sort":               {kind: selectSettingUnsignedLiteral},
	"max_execution_time":                           {kind: selectSettingUnsignedLiteral, hasMax: true, max: maxExecutionTimeWholeSeconds},
	"max_memory_usage":                             {kind: selectSettingUnsignedLiteral},
	"max_threads":                                  {kind: selectSettingUnsignedLiteral},
	"memory_overcommit_ratio_denominator":          {kind: selectSettingUnsignedLiteral},
	"memory_overcommit_ratio_denominator_for_user": {kind: selectSettingUnsignedLiteral},
	"optimize_read_in_order":                       {kind: selectSettingBooleanLiteral},
	"preferred_block_size_bytes":                   {kind: selectSettingUnsignedLiteral},
	"use_uncompressed_cache":                       {kind: selectSettingBooleanLiteral},
}

func validateSettingsClause(settings *clickhouse.SettingsClause, _ *Schema, _ *queryScope, _ *scopeIndex) error {
	return validateSettingsClauseWithRoster(settings, selectSettingRoster)
}

func validateSettingsClauseWithRoster(settings *clickhouse.SettingsClause, roster map[string]selectSettingRule) error {
	for _, item := range settings.Items {
		if item == nil || item.Name == nil || item.Expr == nil {
			return fmt.Errorf("SETTINGS item is empty")
		}
		rule, ok := roster[item.Name.Name]
		if !ok {
			return diagnostic.With(fmt.Errorf("SETTINGS name %q is not in the measured resolver roster; after verifying its effect on result types, opt in with %s %s in the query header", item.Name.Name, uncheckedSettingDirective, item.Name.Name), diagnostic.Detail{
				Code: "setting-unmeasured", Status: diagnostic.Unknown, Stage: "settings",
				Hint: fmt.Sprintf("Verify this setting's effect on result types, then opt in with %s %s. Known setting rules still apply; this is not a global bypass.", uncheckedSettingDirective, item.Name.Name),
			})
		}
		switch rule.kind {
		case selectSettingUnsignedLiteral:
			literal, ok := item.Expr.(*clickhouse.NumberLiteral)
			if !ok {
				if rule.nonZero {
					return fmt.Errorf("SETTINGS %s must be a positive unsigned integer literal", item.Name.Name)
				}
				return fmt.Errorf("SETTINGS %s must be an unsigned integer literal", item.Name.Name)
			}
			value, err := strconv.ParseUint(strings.TrimSpace(literal.Literal), 10, 64)
			if err != nil || rule.nonZero && value == 0 {
				if rule.nonZero {
					return fmt.Errorf("SETTINGS %s must be a positive unsigned integer literal", item.Name.Name)
				}
				return fmt.Errorf("SETTINGS %s must be an unsigned integer literal", item.Name.Name)
			}
			if rule.hasMax && value > rule.max {
				return fmt.Errorf("SETTINGS %s must be at most %d", item.Name.Name, rule.max)
			}
		case selectSettingStringLiteral:
			if _, ok := item.Expr.(*clickhouse.StringLiteral); !ok {
				return fmt.Errorf("SETTINGS %s must be a string literal", item.Name.Name)
			}
		case selectSettingBooleanLiteral:
			switch literal := item.Expr.(type) {
			case *clickhouse.BoolLiteral:
			case *clickhouse.NumberLiteral:
				if literal.Literal != "0" && literal.Literal != "1" {
					return fmt.Errorf("SETTINGS %s must be 0, 1, true, or false", item.Name.Name)
				}
			default:
				return fmt.Errorf("SETTINGS %s must be 0, 1, true, or false", item.Name.Name)
			}
		default:
			return fmt.Errorf("SETTINGS name %q has no measured value rule", item.Name.Name)
		}
	}
	return nil
}

func validateClauseExpression(role string, expression clickhouse.Expr, schema *Schema, scope *queryScope, scopes *scopeIndex) error {
	if expression == nil {
		return fmt.Errorf("%s expression is empty", role)
	}
	if err := resolveScalarSubqueriesInExpr(expression, schema, scope, scopes); err != nil {
		return fmt.Errorf("%s expression: %w", role, err)
	}
	if _, err := inferExprType(expression, *scope); err != nil {
		return fmt.Errorf("%s expression: %w", role, err)
	}
	return nil
}

func validateClauseCondition(role string, expression clickhouse.Expr, schema *Schema, scope *queryScope, scopes *scopeIndex) error {
	if err := resolveScalarSubqueriesInExpr(expression, schema, scope, scopes); err != nil {
		return fmt.Errorf("%s condition: %w", role, err)
	}
	condition, err := inferExprType(expression, *scope)
	if err != nil {
		return fmt.Errorf("%s condition: %w", role, err)
	}
	base := domainBaseType(condition)
	if len(base.Params) != 0 || base.normalizedName() != "uint8" && base.normalizedName() != "bool" && base.normalizedName() != "boolean" {
		return fmt.Errorf("%s condition has type %s, want UInt8 or Bool", role, condition.String())
	}
	return nil
}

func validateJoinConditions(expression clickhouse.Expr, schema *Schema, scope *queryScope, scopes *scopeIndex) error {
	prefix := *scope
	prefix.tables = nil
	_, err := validateJoinConditionsFromLeft(expression, schema, *scope, prefix, scopes)
	return err
}

func validateJoinConditionsFromLeft(
	expression clickhouse.Expr,
	schema *Schema,
	full queryScope,
	prefix queryScope,
	scopes *scopeIndex,
) (queryScope, error) {
	join, ok := expression.(*clickhouse.JoinExpr)
	if !ok {
		return appendJoinBoundary(prefix, expression, full), nil
	}
	boundary, err := validateJoinConditionsFromLeft(join.Left, schema, full, prefix, scopes)
	if err != nil {
		return prefix, err
	}
	if join.Constraints != nil {
		var conditions *clickhouse.ColumnExprList
		switch constraint := join.Constraints.(type) {
		case *clickhouse.OnClause:
			conditions = constraint.On
		case *clickhouse.UsingClause:
			if err := validateJoinUsing(constraint.Using, boundary); err != nil {
				return prefix, err
			}
		case *clickhouse.JoinConstraintClause:
			if constraint.Using != nil {
				if err := validateJoinUsing(constraint.Using, boundary); err != nil {
					return prefix, err
				}
			} else {
				conditions = constraint.On
			}
		default:
			return prefix, fmt.Errorf("JOIN constraint %T is not supported", join.Constraints)
		}
		if conditions != nil {
			if len(conditions.Items) == 0 {
				return prefix, fmt.Errorf("JOIN ON has no condition")
			}
			for _, condition := range conditions.Items {
				if err := validateClauseFunctionPlacement("JOIN ON", condition, false, &boundary); err != nil {
					return prefix, err
				}
				if err := validateClauseCondition("JOIN ON", condition, schema, &boundary, scopes); err != nil {
					return prefix, err
				}
			}
			if joinHasModifier(join, "ASOF") {
				if err := validateASOFJoinConditions(conditions, prefix, boundary); err != nil {
					return prefix, err
				}
			}
		}
	}
	if join.Right == nil {
		return boundary, nil
	}
	return validateJoinConditionsFromLeft(join.Right, schema, full, boundary, scopes)
}

func appendJoinBoundary(prefix queryScope, expression clickhouse.Expr, full queryScope) queryScope {
	aliases := make(map[string]bool)
	collectJoinBoundaryAliases(expression, aliases)
	for _, table := range full.tables {
		if aliases[table.alias] {
			found := false
			for _, existing := range prefix.tables {
				found = found || existing.alias == table.alias
			}
			if !found {
				prefix.tables = append(prefix.tables, table)
			}
		}
	}
	return prefix
}

func collectJoinBoundaryAliases(expression clickhouse.Expr, aliases map[string]bool) {
	switch value := expression.(type) {
	case *clickhouse.JoinExpr:
		collectJoinBoundaryAliases(value.Left, aliases)
		collectJoinBoundaryAliases(value.Right, aliases)
	case *clickhouse.JoinTableExpr:
		collectJoinBoundaryAliases(value.Table, aliases)
	case *clickhouse.TableExpr:
		source, alias, err := tableSource(value)
		if err != nil {
			return
		}
		if alias == "" {
			if name, nameErr := relationName(source); nameErr == nil {
				alias = name
			}
		}
		if alias != "" {
			aliases[alias] = true
		}
	}
}

func joinHasModifier(join *clickhouse.JoinExpr, want string) bool {
	for _, modifier := range join.Modifiers {
		if strings.EqualFold(modifier, want) {
			return true
		}
	}
	return false
}

func validateASOFJoinConditions(conditions *clickhouse.ColumnExprList, left queryScope, boundary queryScope) error {
	leftAliases := make(map[string]bool, len(left.tables))
	rightAliases := make(map[string]bool, len(boundary.tables))
	for _, table := range left.tables {
		leftAliases[table.alias] = true
	}
	for _, table := range boundary.tables {
		if !leftAliases[table.alias] {
			rightAliases[table.alias] = true
		}
	}
	count := 0
	clickhouse.Walk(conditions, func(node clickhouse.Expr) bool {
		operation, ok := node.(*clickhouse.BinaryOperation)
		if ok && (operation.Operation == clickhouse.TokenKindLT || operation.Operation == clickhouse.TokenKindLE ||
			operation.Operation == clickhouse.TokenKindGT || operation.Operation == clickhouse.TokenKindGE) {
			leftQualifier, leftOK := directQualifiedColumn(operation.LeftExpr)
			rightQualifier, rightOK := directQualifiedColumn(operation.RightExpr)
			if leftOK && rightOK && (leftAliases[leftQualifier] && rightAliases[rightQualifier] ||
				rightAliases[leftQualifier] && leftAliases[rightQualifier]) {
				count++
			}
		}
		return true
	})
	if count != 1 {
		return fmt.Errorf("ASOF JOIN ON requires exactly one cross-input inequality condition")
	}
	return nil
}

func directQualifiedColumn(expression clickhouse.Expr) (string, bool) {
	expression = unwrapColumnExpression(expression)
	path, ok := expression.(*clickhouse.Path)
	if !ok || len(path.Fields) != 2 {
		return "", false
	}
	return path.Fields[0].Name, true
}

func validateJoinUsing(expressions *clickhouse.ColumnExprList, scope queryScope) error {
	if expressions == nil || len(expressions.Items) == 0 {
		return fmt.Errorf("JOIN USING has no key")
	}
	if len(scope.tables) > 2 {
		return fmt.Errorf("multi-input JOIN USING name resolution is not supported")
	}
	for _, expression := range expressions.Items {
		column, ok := expression.(*clickhouse.ColumnExpr)
		if !ok || column.Expr == nil {
			return fmt.Errorf("JOIN USING key %T is not a bare column name", expression)
		}
		identifier, ok := column.Expr.(*clickhouse.Ident)
		if !ok {
			return fmt.Errorf("JOIN USING key %s is not a bare column name", clickhouse.Format(expression))
		}
		name := identifier.Name
		types := make([]CHType, 0, len(scope.tables))
		for _, table := range scope.tables {
			if column, ok := table.table.Columns[name]; ok {
				types = append(types, column.Type)
			}
		}
		if len(types) < 2 {
			return fmt.Errorf("JOIN USING key %q is not present on two input tables", name)
		}
		if _, err := commonCHTypes(types); err != nil {
			return fmt.Errorf("JOIN USING key %q has no common type: %w", name, err)
		}
	}
	return nil
}

func recordJoinUsingTypes(expression clickhouse.Expr, scope *queryScope) error {
	if len(scope.tables) != 2 {
		return nil
	}
	var recordErr error
	clickhouse.Walk(expression, func(node clickhouse.Expr) bool {
		join, ok := node.(*clickhouse.JoinExpr)
		if !ok || join.Constraints == nil {
			return recordErr == nil
		}
		var using *clickhouse.ColumnExprList
		switch constraint := join.Constraints.(type) {
		case *clickhouse.UsingClause:
			using = constraint.Using
		case *clickhouse.JoinConstraintClause:
			using = constraint.Using
		}
		if using == nil {
			return recordErr == nil
		}
		for _, expression := range using.Items {
			identifier, ok := unwrapColumnExpression(expression).(*clickhouse.Ident)
			if !ok {
				recordErr = fmt.Errorf("JOIN USING key %s is not a bare column name", clickhouse.Format(expression))
				return false
			}
			types := make([]CHType, 0, len(scope.tables))
			aliases := make([]string, 0, len(scope.tables))
			for _, table := range scope.tables {
				if column, exists := table.table.Columns[identifier.Name]; exists {
					types = append(types, column.Type)
					aliases = append(aliases, table.alias)
				}
			}
			if len(types) < 2 {
				continue
			}
			common, err := commonCHTypes(types)
			if err != nil {
				recordErr = fmt.Errorf("JOIN USING key %q has no common type: %w", identifier.Name, err)
				return false
			}
			scope.usingTypes[identifier.Name] = common
			for _, alias := range aliases {
				scope.usingQualified[alias+"."+identifier.Name] = common
			}
		}
		return recordErr == nil
	})
	return recordErr
}

func validateLimitClause(role string, limit *clickhouse.LimitClause, schema *Schema, scope *queryScope, scopes *scopeIndex) error {
	if limit == nil || limit.Limit == nil {
		return fmt.Errorf("%s has no limit expression", role)
	}
	if err := validateLimitExpression(role, limit.Limit, schema, scope, scopes); err != nil {
		return err
	}
	if limit.Offset != nil {
		if err := validateLimitExpression(role+" OFFSET", limit.Offset, schema, scope, scopes); err != nil {
			return err
		}
	}
	return nil
}

func validateLimitExpression(role string, expression clickhouse.Expr, schema *Schema, scope *queryScope, scopes *scopeIndex) error {
	if _, ok := expression.(*clickhouse.PlaceHolder); ok {
		return nil
	}
	hasPlaceholder := containsPlaceholder(expression)
	if hasPlaceholder {
		if err := validateMeasuredLimitPlaceholder(expression, *scope); err != nil {
			return fmt.Errorf("%s expression: %w", role, err)
		}
		return nil
	}
	if err := resolveScalarSubqueriesInExpr(expression, schema, scope, scopes); err != nil {
		return fmt.Errorf("%s expression: %w", role, err)
	}
	valueType, err := inferExprType(expression, *scope)
	if err != nil {
		return fmt.Errorf("%s expression: %w", role, err)
	}
	base := domainBaseType(valueType)
	if len(base.Params) != 0 || !strings.HasPrefix(base.normalizedName(), "uint") {
		return fmt.Errorf("%s expression has type %s, want an unsigned integer", role, valueType.String())
	}
	switch expression.(type) {
	case *clickhouse.NumberLiteral:
		return nil
	default:
		return fmt.Errorf("%s expression must be a measured numeric literal", role)
	}
}

func validateMeasuredLimitPlaceholder(expression clickhouse.Expr, scope queryScope) error {
	expression = unwrapColumnExpression(expression)
	function, ok := expression.(*clickhouse.FunctionExpr)
	if !ok || function.Name == nil || !strings.EqualFold(function.Name.Name, "ifNull") ||
		function.Params == nil || function.Params.Items == nil || len(function.Params.Items.Items) != 2 {
		return fmt.Errorf("composite placeholder shape is not in the measured LIMIT roster")
	}
	first := unwrapColumnExpression(function.Params.Items.Items[0])
	if _, ok := first.(*clickhouse.PlaceHolder); !ok {
		return fmt.Errorf("ifNull first argument is not a direct placeholder")
	}
	fallback := unwrapColumnExpression(function.Params.Items.Items[1])
	if containsPlaceholder(fallback) {
		return fmt.Errorf("ifNull fallback contains a placeholder")
	}
	fallbackType, err := inferExprType(fallback, scope)
	if err != nil {
		return fmt.Errorf("ifNull fallback: %w", err)
	}
	base := domainBaseType(fallbackType)
	if len(base.Params) != 0 || !strings.HasPrefix(base.normalizedName(), "uint") {
		return fmt.Errorf("ifNull fallback has type %s, want an unsigned integer", fallbackType.String())
	}
	return nil
}

func unwrapColumnExpression(expression clickhouse.Expr) clickhouse.Expr {
	for {
		column, ok := expression.(*clickhouse.ColumnExpr)
		if !ok || column.Expr == nil {
			return expression
		}
		expression = column.Expr
	}
}

func containsPlaceholder(expression clickhouse.Expr) bool {
	found := false
	clickhouse.Walk(expression, func(node clickhouse.Expr) bool {
		if _, ok := node.(*clickhouse.PlaceHolder); ok {
			found = true
		}
		return !found
	})
	return found
}

func resolveScalarSubqueriesInExpr(
	expression clickhouse.Expr,
	schema *Schema,
	parent *queryScope,
	scopes *scopeIndex,
) error {
	resolved := make(map[*clickhouse.SubQuery]bool)
	var resolveErr error
	clickhouse.Walk(expression, func(node clickhouse.Expr) bool {
		if resolveErr != nil {
			return false
		}
		if function, ok := node.(*clickhouse.FunctionExpr); ok && function.Name != nil && strings.EqualFold(function.Name.Name, "exists") {
			if function.Params == nil || function.Params.Items == nil || len(function.Params.Items.Items) != 1 {
				resolveErr = fmt.Errorf("EXISTS must contain exactly one subquery")
				return false
			}
			var selectQuery *clickhouse.SelectQuery
			clickhouse.Walk(function.Params.Items.Items[0], func(argument clickhouse.Expr) bool {
				switch value := argument.(type) {
				case *clickhouse.SubQuery:
					selectQuery = value.Select
					return false
				case *clickhouse.SelectQuery:
					selectQuery = value
					return false
				}
				return selectQuery == nil
			})
			if selectQuery == nil {
				resolveErr = fmt.Errorf("EXISTS argument must be a subquery, got %T", function.Params.Items.Items[0])
				return false
			}
			if _, _, err := resolveSetQuery(selectQuery, schema, parent, scopes, false); err != nil {
				resolveErr = fmt.Errorf("EXISTS subquery: %w", err)
			}
			return false
		}
		if operation, ok := node.(*clickhouse.BinaryOperation); ok && isInOperation(string(operation.Operation)) {
			if subquery, ok := unwrapColumnExpression(operation.RightExpr).(*clickhouse.SubQuery); ok {
				if err := resolveSetSubquery(operation.LeftExpr, subquery, schema, parent, scopes); err != nil {
					resolveErr = err
				}
				resolved[subquery] = true
			}
			return true
		}
		if subquery, ok := node.(*clickhouse.SubQuery); ok {
			if resolved[subquery] {
				return false
			}
			if _, err := resolveScalarSubqueryType(subquery, schema, parent, scopes); err != nil {
				resolveErr = err
			}
			return false
		}
		return true
	})
	return resolveErr
}

func isInOperation(operation string) bool {
	operation = strings.ToUpper(strings.TrimSpace(operation))
	return operation == "IN" || operation == "NOT IN"
}

func resolveSetSubquery(left clickhouse.Expr, subquery *clickhouse.SubQuery, schema *Schema, parent *queryScope, scopes *scopeIndex) error {
	if subquery == nil || subquery.Select == nil {
		return fmt.Errorf("IN set subquery has no SELECT")
	}
	innerScope, results, err := resolveSetQuery(subquery.Select, schema, scopeWithoutRowBindings(parent), scopes, false)
	if err != nil && parent != nil {
		if _, _, correlatedErr := resolveSetQuery(subquery.Select, schema, parent, scopes, false); correlatedErr == nil {
			return fmt.Errorf("correlated IN subquery is not supported")
		}
	}
	if err != nil {
		return fmt.Errorf("IN set subquery: %w", err)
	}
	if len(results) == 0 {
		return fmt.Errorf("IN set subquery must select at least one expression")
	}
	leftType, err := inferExprType(left, *parent)
	if err != nil {
		return fmt.Errorf("IN left expression: %w", err)
	}
	wantArity := 1
	if leftType.normalizedName() == "tuple" {
		wantArity = len(leftType.Params)
	}
	keyArity := len(results)
	// IN consumes set keys, not SELECT columns: one Tuple-valued column
	// supplies the same key components as a multi-column projection.
	// Unwrap only this outer key; nested tuples remain components.
	if keyArity == 1 && results[0].typeOf.normalizedName() == "tuple" {
		keyArity = len(results[0].typeOf.Params)
		if keyArity != wantArity {
			return fmt.Errorf("IN set tuple has %d components, want %d", keyArity, wantArity)
		}
	}
	if keyArity != wantArity {
		return fmt.Errorf("IN set projection has %d expressions, want %d", len(results), wantArity)
	}
	_ = innerScope
	return nil
}

func resolveScalarSubqueryType(
	subquery *clickhouse.SubQuery,
	schema *Schema,
	parent *queryScope,
	scopes *scopeIndex,
) (CHType, error) {
	if subquery == nil || subquery.Select == nil {
		return CHType{}, fmt.Errorf("scalar subquery has no SELECT")
	}
	if scalarType, ok := scopes.scalarSubqueries[subquery.Select]; ok {
		return scalarType, nil
	}
	if scopes.resolvingScalar[subquery.Select] {
		return CHType{}, fmt.Errorf("recursive scalar subquery is not supported")
	}
	scopes.resolvingScalar[subquery.Select] = true
	defer delete(scopes.resolvingScalar, subquery.Select)

	var innerScope queryScope
	var results []scopedQueryResult
	var err error
	if subquery.Select.Limit != nil && parent != nil {
		innerScope, results, err = resolveSetQuery(subquery.Select, schema, scopeWithoutRowBindings(parent), scopes, false)
		if err != nil {
			if _, _, correlatedErr := resolveSetQuery(subquery.Select, schema, parent, scopes, false); correlatedErr == nil {
				return CHType{}, fmt.Errorf("correlated scalar subquery with LIMIT is not supported")
			}
		}
	} else {
		innerScope, results, err = resolveSetQuery(subquery.Select, schema, parent, scopes, false)
	}
	if err != nil {
		return CHType{}, err
	}
	if len(results) != 1 {
		return CHType{}, fmt.Errorf("scalar subquery must select exactly one expression")
	}
	if !scalarSubqueryHasAtMostOneRow(subquery) {
		return CHType{}, fmt.Errorf("scalar subquery row cardinality is not statically bounded")
	}
	_ = innerScope
	scalarType := results[0].typeOf
	if scalarType.normalizedName() != "nullable" {
		if !scalarSubqueryTypeCanBeNullable(scalarType) {
			if !scalarSubqueryHasGuaranteedRow(subquery) {
				return CHType{}, fmt.Errorf("scalar subquery can be empty and cannot put %s inside Nullable", scalarType.String())
			}
		} else {
			scalarType = wrapNullable(scalarType)
		}
	}
	scopes.scalarSubqueries[subquery.Select] = scalarType
	return scalarType, nil
}

func scalarSubqueryTypeCanBeNullable(scalarType CHType) bool {
	if scalarType.normalizedName() == "lowcardinality" {
		return false
	}
	return canBeInsideNullable(scalarType)
}

func scopeWithoutRowBindings(scope *queryScope) *queryScope {
	if scope == nil {
		return nil
	}
	copy := *scope
	copy.tables = nil
	copy.usingTypes = nil
	copy.usingQualified = nil
	copy.parent = scopeWithoutRowBindings(scope.parent)
	return &copy
}

func resolveDerivedTable(
	selectQuery *clickhouse.SelectQuery,
	name string,
	schema *Schema,
	parent *queryScope,
	scopes *scopeIndex,
	allowRelationParent bool,
) (Table, error) {
	var relationParent *queryScope
	if parent != nil {
		relationParent = scopeWithoutRowBindings(parent)
		if allowRelationParent {
			relationParent.scalars = parent.scalars
		}
	}
	innerScope, results, err := resolveSetQuery(selectQuery, schema, relationParent, scopes, true)
	if err != nil {
		if parent != nil && !allowRelationParent {
			if _, _, correlatedErr := resolveSetQuery(selectQuery, schema, parent, scopes, true); correlatedErr == nil {
				return Table{}, fmt.Errorf("correlated derived table is not supported")
			}
		}
		return Table{}, err
	}
	_ = innerScope
	table := Table{Name: name, Columns: make(map[string]Column, len(results))}
	for _, result := range results {
		table.Columns[result.name] = Column{Name: result.name, Type: result.typeOf, Insertable: true}
		table.ColumnOrder = append(table.ColumnOrder, result.name)
	}
	return table, nil
}

func markSelectScope(selectQuery *clickhouse.SelectQuery, scope queryScope, scopes *scopeIndex) {
	clickhouse.Walk(selectQuery, func(node clickhouse.Expr) bool {
		if nested, ok := node.(*clickhouse.SelectQuery); ok && nested != selectQuery {
			return false
		}
		scopes.byNode[node] = scope
		return true
	})
}

func collectTables(expression clickhouse.Expr, schema *Schema, scope *queryScope, scopes *scopeIndex) error {
	return collectTablesWithFinal(expression, schema, scope, scopes, false)
}

func collectTablesWithFinal(
	expression clickhouse.Expr,
	schema *Schema,
	scope *queryScope,
	scopes *scopeIndex,
	final bool,
) error {
	switch expr := expression.(type) {
	case *clickhouse.JoinTableExpr:
		if expr.SampleRatio != nil {
			return fmt.Errorf("FROM SAMPLE capability validation is not supported")
		}
		return collectTablesWithFinal(expr.Table, schema, scope, scopes, final || expr.HasFinal)
	case *clickhouse.JoinExpr:
		if isArrayJoin(expr) {
			if err := collectArrayJoin(expr.Left, schema, scope, scopes); err != nil {
				return err
			}
			if expr.Right != nil {
				return collectTablesWithFinal(expr.Right, schema, scope, scopes, false)
			}
			return nil
		}
		if err := collectTablesWithFinal(expr.Left, schema, scope, scopes, false); err != nil {
			return err
		}
		if expr.Right != nil {
			return collectTablesWithFinal(expr.Right, schema, scope, scopes, false)
		}
		return nil
	case *clickhouse.TableExpr:
		final = final || expr.HasFinal
		source, alias, err := tableSource(expr)
		if err != nil {
			return err
		}
		switch source := source.(type) {
		case *clickhouse.TableIdentifier:
			if source.Table == nil {
				return fmt.Errorf("FROM table has no table name")
			}
			tableName := source.Table.Name
			var table Table
			var found bool
			if source.Database == nil {
				table, found = scope.lookupTable(tableName)
			}
			if !found && !scope.hasReservedRelation(tableName) {
				table, found = schema.Tables[tableName]
			}
			if !found {
				message := fmt.Sprintf("table %q is not present in the schema or the query scope", tableName)
				if suggestion := suggestName(tableName, tableCandidates(schema, scope)); suggestion != "" {
					message += fmt.Sprintf("; did you mean %q?", suggestion)
				}
				return fmt.Errorf("%s", message)
			}
			if final {
				if err := validateFinalEngine(table); err != nil {
					return err
				}
			}
			if alias == "" {
				alias = tableName
			}
			if scope.hasFromAlias(alias) {
				return fmt.Errorf("table alias %q is defined more than once", alias)
			}
			scope.tables = append(scope.tables, scopedTable{table: table, alias: alias})
			return nil
		case *clickhouse.SubQuery:
			if final {
				return fmt.Errorf("FINAL is not supported for a derived table")
			}
			if alias == "" {
				// ClickHouse accepts anonymous derived tables. They cannot be
				// referenced by name, but they still need an internal catalog
				// name so their projected columns can participate in outer-scope
				// resolution (notably INSERT ... SELECT rollups).
				alias = fmt.Sprintf("_chgen_derived_%d", len(scope.tables))
			}
			if scope.hasFromAlias(alias) {
				return fmt.Errorf("table alias %q is defined more than once", alias)
			}
			table, err := resolveDerivedTable(source.Select, alias, schema, scope, scopes, false)
			if err != nil {
				return fmt.Errorf("derived table %s: %w", alias, err)
			}
			scope.tables = append(scope.tables, scopedTable{table: table, alias: alias})
			return nil
		default:
			return fmt.Errorf("unsupported FROM expression %T", source)
		}
	default:
		return fmt.Errorf("unsupported FROM expression %T", expression)
	}
}

func validateFinalEngine(table Table) error {
	if table.Engine == nil {
		return fmt.Errorf("FINAL requires a measured collapsing MergeTree-family engine; table %s has no engine", table.Name)
	}
	switch strings.ToLower(table.Engine.Name) {
	case "replacingmergetree", "summingmergetree", "aggregatingmergetree", "collapsingmergetree", "versionedcollapsingmergetree":
		return nil
	default:
		return fmt.Errorf("FINAL is not supported for table %s with engine %s", table.Name, table.Engine.Name)
	}
}

func isArrayJoin(join *clickhouse.JoinExpr) bool {
	return join != nil && joinHasModifier(join, "ARRAY") && joinHasModifier(join, "JOIN")
}

func collectArrayJoin(
	expression clickhouse.Expr,
	schema *Schema,
	scope *queryScope,
	scopes *scopeIndex,
) error {
	list, ok := expression.(*clickhouse.ColumnExprList)
	if !ok || len(list.Items) == 0 {
		return fmt.Errorf("ARRAY JOIN requires an expression list")
	}
	type binding struct {
		name      string
		qualified []string
		element   CHType
	}
	beforeNode := cloneQueryScope(*scope)
	bindings := make([]binding, 0, len(list.Items))
	names := make(map[string]bool, len(list.Items))
	for _, item := range list.Items {
		column, ok := item.(*clickhouse.ColumnExpr)
		if !ok || column.Expr == nil {
			return fmt.Errorf("ARRAY JOIN item %T is not an expression", item)
		}
		itemScope := cloneQueryScope(beforeNode)
		if err := resolveScalarSubqueriesInExpr(column.Expr, schema, &itemScope, scopes); err != nil {
			return fmt.Errorf("ARRAY JOIN expression: %w", err)
		}
		container, err := inferExprType(column.Expr, itemScope)
		if err != nil {
			if column.Alias == nil && isEmptyArrayJoinLiteral(column.Expr) {
				// ClickHouse accepts an anonymous empty array and returns no
				// rows. It exposes no Nothing value to generated Go code.
				continue
			}
			return fmt.Errorf("ARRAY JOIN expression: %w", err)
		}
		element, err := arrayJoinElementType(container)
		if err != nil {
			return err
		}
		name, qualified, err := arrayJoinBinding(column, beforeNode)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		if _, exists := beforeNode.arrayJoinTypes[name]; exists || names[name] {
			return fmt.Errorf("ARRAY JOIN alias %q is defined more than once", name)
		}
		names[name] = true
		bindings = append(bindings, binding{name: name, qualified: qualified, element: element})
	}
	for _, value := range bindings {
		scope.arrayJoinTypes[value.name] = value.element
		for _, key := range value.qualified {
			scope.arrayJoinQualified[key] = value.element
		}
	}
	return nil
}

func isEmptyArrayJoinLiteral(expression clickhouse.Expr) bool {
	array, ok := unwrapColumnExpression(expression).(*clickhouse.ArrayParamList)
	return ok && (array.Items == nil || len(array.Items.Items) == 0)
}

func arrayJoinElementType(container CHType) (CHType, error) {
	switch {
	case strings.EqualFold(container.Name, "Array") && len(container.Params) == 1:
		return container.Params[0], nil
	case strings.EqualFold(container.Name, "Map") && len(container.Params) == 2:
		return CHType{Name: "Tuple", Params: append([]CHType(nil), container.Params...), ParamNames: []string{"keys", "values"}}, nil
	default:
		return CHType{}, fmt.Errorf("ARRAY JOIN requires an Array or Map expression, got %s", container.String())
	}
}

func arrayJoinBinding(column *clickhouse.ColumnExpr, scope queryScope) (string, []string, error) {
	if column.Alias != nil {
		if column.Alias.Name == "" {
			return "", nil, fmt.Errorf("ARRAY JOIN alias is empty")
		}
		return column.Alias.Name, nil, nil
	}
	expression := unwrapColumnExpression(column.Expr)
	switch value := expression.(type) {
	case *clickhouse.Ident:
		var qualified []string
		for _, table := range scope.tables {
			if _, ok := table.table.Columns[value.Name]; ok {
				qualified = append(qualified, table.alias+"."+value.Name)
				if table.table.Name != table.alias {
					qualified = append(qualified, table.table.Name+"."+value.Name)
				}
			}
		}
		return value.Name, qualified, nil
	case *clickhouse.Path:
		if len(value.Fields) < 2 {
			return "", nil, fmt.Errorf("ARRAY JOIN expression %s requires an alias", clickhouse.Format(column.Expr))
		}
		qualifier := value.Fields[len(value.Fields)-2].Name
		name := value.Fields[len(value.Fields)-1].Name
		return name, []string{qualifier + "." + name}, nil
	default:
		return "", nil, nil
	}
}

func tableSource(tableExpr *clickhouse.TableExpr) (clickhouse.Expr, string, error) {
	source := tableExpr.Expr
	if aliasExpr, ok := source.(*clickhouse.AliasExpr); ok {
		alias, ok := aliasExpr.Alias.(*clickhouse.Ident)
		if !ok {
			return nil, "", fmt.Errorf("unsupported table alias %T", aliasExpr.Alias)
		}
		return aliasExpr.Expr, alias.Name, nil
	}
	return source, "", nil
}

func relationName(expression clickhouse.Expr) (string, error) {
	switch expr := expression.(type) {
	case *clickhouse.Ident:
		return expr.Name, nil
	case *clickhouse.TableIdentifier:
		if expr.Table == nil {
			return "", fmt.Errorf("table identifier has no table name")
		}
		return expr.Table.Name, nil
	default:
		return "", fmt.Errorf("unsupported relation name expression %T", expression)
	}
}
