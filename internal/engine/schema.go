package engine

import (
	"fmt"
	"strconv"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// Schema is a catalog of ClickHouse tables.
type Schema struct {
	Tables map[string]Table
}

// Table is a ClickHouse table known to the generator.
type Table struct {
	Name string
	// File and Line locate the CREATE TABLE statement for this table.
	File        string
	Line        int
	Columns     map[string]Column
	ColumnOrder []string
	// Engine is the ENGINE clause. It is nil when the DDL has no engine.
	Engine *TableEngine
}

// TableEngine is the part of an ENGINE clause that controls row replacement.
type TableEngine struct {
	// Name is the engine name, such as ReplacingMergeTree.
	Name string
	// Params contains the engine arguments as written.
	Params []string
	// OrderBy contains the ORDER BY expressions as written.
	OrderBy []string
}

// Column is a ClickHouse column and its parsed type.
type Column struct {
	Name       string
	Type       CHType
	Insertable bool
}

// CHType is one ClickHouse type and its nested arguments.
type CHType struct {
	Name          string
	Params        []CHType
	LiteralParams []string

	// ParamNames contains Tuple element names in the order of Params.
	// An unnamed element has an empty name.
	ParamNames []string
}

// stripUnsupportedTTLRollup removes the `GROUP BY ... SET ...` tail of an
// aggregate-rollup TTL (e.g. v2_progress_advance_log) from CREATE TABLE DDL
// before parsing. The upstream clickhouse-sql-parser does not accept the
// `TTL <expr> GROUP BY ... SET ...` form and fails the whole file. chgen only
// needs the column catalog, and a TTL never contributes columns, so dropping
// the rollup tail is semantically inert for the generator while keeping the
// migration file itself the single source of truth. Only the GROUP BY/SET tail
// is removed; the leading `TTL <expr>` is left intact and harmlessly parses.
func stripUnsupportedTTLRollup(input string) string {
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "TTL ") {
			continue
		}
		idx := strings.Index(line, " GROUP BY ")
		if idx == -1 {
			continue
		}
		lines[i] = line[:idx] + strings.Repeat(" ", len(line)-idx)
	}
	return strings.Join(lines, "\n")
}

func parseCreateTable(createTable *clickhouse.CreateTable) (Table, error) {
	if createTable.Name == nil || createTable.Name.Table == nil {
		return Table{}, fmt.Errorf("CREATE TABLE has no table name")
	}
	if createTable.TableSchema == nil {
		return Table{}, fmt.Errorf("CREATE TABLE %s has no column list", createTable.Name.Table.Name)
	}

	tableName := createTable.Name.Table.Name
	table := Table{Name: tableName, Columns: make(map[string]Column)}
	for _, expression := range createTable.TableSchema.Columns {
		columnDef, ok := expression.(*clickhouse.ColumnDef)
		if !ok {
			// Indexes, constraints, projections, and other table clauses do
			// not contribute to a query column catalog.
			continue
		}
		column, err := parseColumnDef(tableName, columnDef)
		if err != nil {
			return Table{}, err
		}
		if _, exists := table.Columns[column.Name]; exists {
			return Table{}, fmt.Errorf("duplicate column %s.%s", tableName, column.Name)
		}
		table.Columns[column.Name] = column
		table.ColumnOrder = append(table.ColumnOrder, column.Name)
	}
	table.Engine = parseTableEngine(createTable.Engine)
	return table, nil
}

// parseTableEngine keeps the part of the ENGINE clause that decides which rows
// collapse into one. It returns nil when the DDL declares no engine.
func parseTableEngine(engine *clickhouse.EngineExpr) *TableEngine {
	if engine == nil {
		return nil
	}
	parsed := &TableEngine{Name: engine.Name}
	if engine.Params != nil && engine.Params.Items != nil {
		for _, item := range engine.Params.Items.Items {
			parsed.Params = append(parsed.Params, engineExprText(item))
		}
	}
	// ORDER BY can hang off the engine clause or stand beside it, depending on
	// how the DDL is written, so read whichever one the parser filled.
	if engine.OrderBy != nil {
		for _, item := range engine.OrderBy.Items {
			parsed.OrderBy = append(parsed.OrderBy, engineSortKeyTerms(item)...)
		}
	}
	return parsed
}

// engineSortKeyTerms flattens one ORDER BY item into its terms. ClickHouse
// accepts both `ORDER BY (a, b)` and `ORDER BY a, b`, and the parser gives the
// first shape as one parenthesised list. A guard compares the terms one by one,
// thus both shapes must give the same slice.
func engineSortKeyTerms(item clickhouse.Expr) []string {
	switch item := item.(type) {
	case nil:
		return nil
	case *clickhouse.OrderExpr:
		return engineSortKeyTerms(item.Expr)
	case *clickhouse.ColumnExpr:
		return engineSortKeyTerms(item.Expr)
	case *clickhouse.ParamExprList:
		if item.Items == nil {
			return nil
		}
		return engineSortKeyTerms(item.Items)
	case *clickhouse.ColumnExprList:
		var terms []string
		for _, nested := range item.Items {
			terms = append(terms, engineSortKeyTerms(nested)...)
		}
		return terms
	default:
		return []string{engineExprText(item)}
	}
}

// engineExprText renders one engine or sort expression. A plain column name is
// the usual case and gives its own name; anything else keeps a rendering that a
// guard can compare, and never a blank string, so an unreadable expression
// cannot look like an absent one.
func engineExprText(expr clickhouse.Expr) string {
	switch expr := expr.(type) {
	case nil:
		return ""
	case *clickhouse.Ident:
		return expr.Name
	case *clickhouse.ColumnExpr:
		return engineExprText(expr.Expr)
	case *clickhouse.ParamExprList:
		if expr.Items == nil {
			return ""
		}
		return engineExprText(expr.Items)
	case *clickhouse.ColumnExprList:
		var parts []string
		for _, item := range expr.Items {
			parts = append(parts, engineExprText(item))
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprintf("%T", expr)
	}
}

type catalogAlterAction uint8

const (
	catalogAlterReject catalogAlterAction = iota
	catalogAlterApply
	catalogAlterIgnore
)

// classifyCatalogAlter defines the effect of one ALTER TABLE clause on the
// catalog. Ignored clauses are understood schema operations whose effects are
// outside the column and engine metadata that chgen models.
func classifyCatalogAlter(clause clickhouse.AlterTableClause) catalogAlterAction {
	switch clause.(type) {
	case *clickhouse.AlterTableAddColumn, *clickhouse.AlterTableModifyColumn, *clickhouse.AlterTableDropColumn:
		return catalogAlterApply
	case *clickhouse.AlterTableAddProjection,
		*clickhouse.AlterTableMaterializeProjection,
		*clickhouse.AlterTableDropProjection,
		*clickhouse.AlterTableClearProjection:
		return catalogAlterIgnore
	// Data-skipping indexes do not change columns or the engine metadata we
	// model. Ignore only these clauses so column changes in the same migration
	// still reach the catalog.
	case *clickhouse.AlterTableAddIndex,
		*clickhouse.AlterTableMaterializeIndex,
		*clickhouse.AlterTableDropIndex,
		*clickhouse.AlterTableClearIndex:
		return catalogAlterIgnore
	default:
		return catalogAlterReject
	}
}

func applyAlterTable(schema *Schema, alterTable *clickhouse.AlterTable) error {
	if alterTable.TableIdentifier == nil || alterTable.TableIdentifier.Table == nil {
		return fmt.Errorf("ALTER TABLE has no table name")
	}
	tableName := alterTable.TableIdentifier.Table.Name
	table, ok := schema.Tables[tableName]
	if !ok {
		return fmt.Errorf("ALTER TABLE %s targets an unknown table", tableName)
	}
	for _, expression := range alterTable.AlterExprs {
		switch alterExpr := expression.(type) {
		case *clickhouse.AlterTableAddColumn:
			addColumn := alterExpr
			if addColumn.Column == nil {
				return fmt.Errorf("ALTER TABLE %s ADD COLUMN has no definition", tableName)
			}
			column, err := parseColumnDef(tableName, addColumn.Column)
			if err != nil {
				return err
			}
			if _, exists := table.Columns[column.Name]; exists {
				if addColumn.IfNotExists {
					continue
				}
				return fmt.Errorf("duplicate column %s.%s", tableName, column.Name)
			}
			table.Columns[column.Name] = column
			if addColumn.After == nil {
				table.ColumnOrder = append(table.ColumnOrder, column.Name)
			} else {
				afterName := addColumn.After.Ident.Name
				afterIndex := -1
				for index, existingName := range table.ColumnOrder {
					if existingName == afterName {
						afterIndex = index
						break
					}
				}
				if afterIndex == -1 {
					return fmt.Errorf("ALTER TABLE %s ADD COLUMN %s AFTER unknown column %s", tableName, column.Name, afterName)
				}
				table.ColumnOrder = append(table.ColumnOrder, "")
				copy(table.ColumnOrder[afterIndex+2:], table.ColumnOrder[afterIndex+1:])
				table.ColumnOrder[afterIndex+1] = column.Name
			}
		case *clickhouse.AlterTableModifyColumn:
			modifyColumn := alterExpr
			if modifyColumn.Column == nil {
				return fmt.Errorf("ALTER TABLE %s MODIFY COLUMN has no definition", tableName)
			}
			column, err := parseColumnDef(tableName, modifyColumn.Column)
			if err != nil {
				return err
			}
			if _, exists := table.Columns[column.Name]; !exists {
				if modifyColumn.IfExists {
					continue
				}
				return fmt.Errorf("ALTER TABLE %s MODIFY COLUMN targets unknown column %s", tableName, column.Name)
			}
			// Retype in place; ColumnOrder already has this column's position
			// from the CREATE TABLE or a prior ADD COLUMN.
			table.Columns[column.Name] = column
		case *clickhouse.AlterTableDropColumn:
			dropColumn := alterExpr
			if dropColumn.ColumnName == nil {
				return fmt.Errorf("ALTER TABLE %s DROP COLUMN has no column name", tableName)
			}
			columnName := nestedIdentifierName(dropColumn.ColumnName)
			if _, exists := table.Columns[columnName]; !exists {
				if dropColumn.IfExists {
					continue
				}
				return fmt.Errorf("ALTER TABLE %s DROP COLUMN targets unknown column %s", tableName, columnName)
			}
			delete(table.Columns, columnName)
			for index, existingName := range table.ColumnOrder {
				if existingName == columnName {
					table.ColumnOrder = append(table.ColumnOrder[:index], table.ColumnOrder[index+1:]...)
					break
				}
			}
		default:
			if classifyCatalogAlter(expression) == catalogAlterIgnore {
				continue
			}
			return fmt.Errorf("unsupported ALTER TABLE %s clause %T", tableName, expression)
		}
	}
	schema.Tables[tableName] = table
	return nil
}

func parseColumnDef(tableName string, columnDef *clickhouse.ColumnDef) (Column, error) {
	if columnDef.Name == nil || columnDef.Name.Ident == nil {
		return Column{}, fmt.Errorf("table %s contains a column without a name", tableName)
	}
	if columnDef.Type == nil {
		return Column{}, fmt.Errorf("column %s.%s has no type", tableName, columnDef.Name.Ident.Name)
	}
	columnName := columnDef.Name.Ident.Name
	if columnDef.Name.DotIdent != nil {
		columnName += "." + columnDef.Name.DotIdent.Name
	}
	columnType, err := parseCHType(columnDef.Type)
	if err != nil {
		return Column{}, fmt.Errorf("column %s.%s: %w", tableName, columnName, err)
	}
	return Column{
		Name:       columnName,
		Type:       columnType,
		Insertable: columnDef.MaterializedExpr == nil && columnDef.AliasExpr == nil,
	}, nil
}

func parseCHType(columnType clickhouse.ColumnType) (CHType, error) {
	switch typeExpr := columnType.(type) {
	case *clickhouse.ScalarType:
		if err := validateTypeFamilySpelling(typeExpr.Name.Name); err != nil {
			return CHType{}, err
		}
		name := canonicalCHTypeName(typeExpr.Name.Name)
		if name == "Decimal" {
			return CHType{Name: name, LiteralParams: []string{"10", "0"}}, nil
		}
		if strings.EqualFold(name, "DateTime64") {
			return CHType{Name: "DateTime64", LiteralParams: []string{"3"}}, nil
		}
		return CHType{Name: name}, nil
	case *clickhouse.TypeWithParams:
		if err := validateTypeFamilySpelling(typeExpr.Name.Name); err != nil {
			return CHType{}, err
		}
		literalParams := make([]string, 0, len(typeExpr.Params))
		for _, param := range typeExpr.Params {
			literalParams = append(literalParams, clickhouse.Format(param))
		}
		// clickhouse-sql-parser sends an Enum whose elements carry no
		// number to this branch, not to the EnumType branch, because
		// 'a', 'b' looks like a plain parameter list. Number the
		// elements here as ClickHouse reports them, so that both
		// declaration forms give the same canonical type.
		if strings.EqualFold(typeExpr.Name.Name, "Enum") {
			return CHType{}, fmt.Errorf("Enum has width-dependent semantics; use Enum8 or Enum16")
		}
		if isEnumTypeName(typeExpr.Name.Name) {
			if len(literalParams) == 0 {
				return CHType{}, fmt.Errorf("%s expects at least one element", typeExpr.Name.Name)
			}
			seenNames := make(map[string]struct{}, len(literalParams))
			for index := range literalParams {
				if _, exists := seenNames[literalParams[index]]; exists {
					return CHType{}, fmt.Errorf("%s has a duplicate element name", typeExpr.Name.Name)
				}
				seenNames[literalParams[index]] = struct{}{}
				literalParams[index] = fmt.Sprintf("%s = %d", literalParams[index], index+1)
			}
			if err := validateEnumNumbers(typeExpr.Name.Name, literalParams); err != nil {
				return CHType{}, err
			}
			return CHType{Name: canonicalCHTypeName(typeExpr.Name.Name), LiteralParams: literalParams}, nil
		}
		if strings.EqualFold(typeExpr.Name.Name, "DateTime") || strings.EqualFold(typeExpr.Name.Name, "DateTime64") {
			return canonicalParameterizedDateTime(typeExpr.Name.Name, literalParams)
		}
		if canonical, ok := floatAliasCanonicalName(typeExpr.Name.Name); ok {
			if len(literalParams) > 2 {
				return CHType{}, fmt.Errorf("%s accepts at most two ignored arguments", typeExpr.Name.Name)
			}
			return CHType{Name: canonical}, nil
		}
		canonicalName := canonicalCHTypeName(typeExpr.Name.Name)
		if canonicalName == "Decimal" {
			switch len(literalParams) {
			case 1:
				literalParams = append(literalParams, "0")
			case 2:
			default:
				return CHType{}, fmt.Errorf("Decimal expects one or two arguments")
			}
			precision, err := strconv.Atoi(literalParams[0])
			if err != nil || precision < 1 || precision > 76 {
				return CHType{}, fmt.Errorf("Decimal precision must be from 1 through 76")
			}
			scale, err := strconv.Atoi(literalParams[1])
			if err != nil || scale < 0 || scale > precision {
				return CHType{}, fmt.Errorf("Decimal scale must be from 0 through its precision")
			}
			return CHType{Name: canonicalName, LiteralParams: literalParams}, nil
		}
		if canonicalName == "Bool" {
			if len(literalParams) == 0 {
				return CHType{Name: canonicalName}, nil
			}
			return CHType{}, fmt.Errorf("Bool does not accept type arguments")
		}
		if canonicalName == "FixedString" {
			if len(literalParams) != 1 {
				return CHType{}, fmt.Errorf("FixedString expects one length argument")
			}
			length, err := strconv.Atoi(literalParams[0])
			if err != nil || length < 1 || length > 1<<24-1 {
				return CHType{}, fmt.Errorf("FixedString length must be from 1 through 16777215")
			}
			return CHType{Name: canonicalName, LiteralParams: literalParams}, nil
		}
		if isIntegerTypeAlias(typeExpr.Name.Name) {
			if len(literalParams) != 1 {
				return CHType{}, fmt.Errorf("integer display width expects one argument")
			}
			return CHType{Name: canonicalName}, nil
		}
		if isZeroArgumentSupportedType(canonicalName) {
			return CHType{}, fmt.Errorf("%s does not accept type arguments", canonicalName)
		}
		return CHType{Name: canonicalName, LiteralParams: literalParams}, nil
	case *clickhouse.EnumType:
		if err := validateTypeFamilySpelling(typeExpr.Name.Name); err != nil {
			return CHType{}, err
		}
		return parseEnumCHType(typeExpr)
	case *clickhouse.JSONType:
		if err := validateTypeFamilySpelling(typeExpr.Name.Name); err != nil {
			return CHType{}, err
		}
		return CHType{Name: canonicalCHTypeName(typeExpr.Name.Name)}, nil
	case *clickhouse.PropertyType:
		if err := validateTypeFamilySpelling(typeExpr.Name.Name); err != nil {
			return CHType{}, err
		}
		return CHType{Name: canonicalCHTypeName(typeExpr.Name.Name)}, nil
	case *clickhouse.ComplexType:
		if err := validateTypeFamilySpelling(typeExpr.Name.Name); err != nil {
			return CHType{}, err
		}
		return parseCHTypeParams(typeExpr.Name.Name, typeExpr.Params)
	case *clickhouse.NestedType:
		if err := validateTypeFamilySpelling(typeExpr.Name.Name); err != nil {
			return CHType{}, err
		}
		// The parser gives Tuple(...) and Nested(...) the same AST node,
		// with the same element list. Tuple keeps that list as its own
		// parameters. Nested is the ARRAY of such a tuple: the server
		// reports the runtime type of a Nested column reference as
		// Array(Tuple(...)), thus that is the type chgen must hold.
		//
		// Measured on ClickHouse 25.8.29.51 with real columns in a real
		// table, never over literals, because the server folds
		// constants. The table holds nst_a Nested(a Int32, b String)
		// and at Array(Tuple(a Int32, b String)). Every cell was paired
		// with ignore(...), so the answer is the EXECUTION type and not
		// an analysis-time guess:
		//
		//	toTypeName(nst_a)              Array(Tuple(a Int32, b String))
		//	toTypeName(at)                 Array(Tuple(a Int32, b String))
		//	toTypeName(argMin(nst_a, d))   Array(Tuple(a Int32, b String))
		//	toTypeName(min(nst_a))         Array(Tuple(a Int32, b String))
		//	toTypeName(groupArray(nst_a))  Array(Array(Tuple(a Int32, b String)))
		//	toTypeName(array(nst_a))       Array(Array(Tuple(a Int32, b String)))
		//	toTypeName(arrayElement(nst_a, 1))   Tuple(a Int32, b String)
		//	toTypeName(length(nst_a))            UInt64
		//	first_value/last_value/lagInFrame/leadInFrame(nst_a)
		//	                               Array(Tuple(a Int32, b String))
		//
		// The Nested column and the explicit Array(Tuple(...)) column
		// answer the SAME type in every one of those cells. The
		// wrapper-passthrough functions were therefore never wrong:
		// they echo the argument type, and the argument type was wrong
		// here, one level earlier. Modelling Nested as its true
		// Array(Tuple(...)) makes every one of those cells correct at
		// once, and it also lets a caller READ a Nested column, which
		// the parameter-less form could not do because gotype.go
		// refuses to map the bare name "nested" to a Go type.
		if !strings.EqualFold(typeExpr.Name.Name, "Nested") {
			return parseTupleType(typeExpr)
		}
		element, err := parseTupleType(typeExpr)
		if err != nil {
			return CHType{}, err
		}
		element.Name = "Tuple"
		return CHType{Name: "Array", Params: []CHType{element}}, nil
	default:
		return CHType{}, fmt.Errorf("unsupported ClickHouse type AST %T", columnType)
	}
}

// isEnumTypeName reports whether name is an Enum constructor.
func isEnumTypeName(name string) bool {
	switch strings.ToLower(name) {
	case "enum", "enum8", "enum16":
		return true
	default:
		return false
	}
}

// parseEnumCHType keeps the value set of an Enum column. The bare
// constructor name alone is not the type: a consumer cannot map an enum
// without the names and their numbers (type-oracle finding N5).
//
// The rendering follows ClickHouse, measured on 25.8.29.51. The server
// always reports the canonical form 'name' = number, and it fills in a
// number that the declaration leaves out:
//
//	Enum8('a', 'b')      is reported as Enum8('a' = 1, 'b' = 2)
//	Enum8('a' = -3, 'b') is refused with code 223
//
// A number is thus given for every element or for none. When none is
// given, the numbers start at 1 and count up.
func parseEnumCHType(typeExpr *clickhouse.EnumType) (CHType, error) {
	if strings.EqualFold(typeExpr.Name.Name, "Enum") {
		return CHType{}, fmt.Errorf("Enum has width-dependent semantics; use Enum8 or Enum16")
	}
	if len(typeExpr.Values) == 0 {
		return CHType{}, fmt.Errorf("%s expects at least one element", typeExpr.Name.Name)
	}
	literalParams := make([]string, 0, len(typeExpr.Values))
	seenNames := make(map[string]struct{}, len(typeExpr.Values))
	for index, value := range typeExpr.Values {
		if value.Name == nil {
			return CHType{}, fmt.Errorf("Enum element %d has no name", index)
		}
		name := clickhouse.Format(value.Name)
		if _, exists := seenNames[name]; exists {
			return CHType{}, fmt.Errorf("%s has a duplicate element name", typeExpr.Name.Name)
		}
		seenNames[name] = struct{}{}
		if value.Value == nil {
			// The declaration omits every number, thus the numbering
			// starts at 1 and counts up, as ClickHouse reports it.
			literalParams = append(literalParams, fmt.Sprintf("%s = %d", name, index+1))
			continue
		}
		literalParams = append(literalParams,
			fmt.Sprintf("%s = %s", name, clickhouse.Format(value.Value)))
	}
	if err := validateEnumNumbers(typeExpr.Name.Name, literalParams); err != nil {
		return CHType{}, err
	}
	return CHType{Name: canonicalCHTypeName(typeExpr.Name.Name), LiteralParams: literalParams}, nil
}

func validateEnumNumbers(name string, params []string) error {
	minimum, maximum := -128, 127
	if strings.EqualFold(name, "Enum16") {
		minimum, maximum = -32768, 32767
	}
	seen := make(map[int]struct{}, len(params))
	for _, param := range params {
		separator := strings.LastIndex(param, " = ")
		if separator < 0 {
			return fmt.Errorf("%s element has no number", name)
		}
		number, err := strconv.Atoi(strings.TrimSpace(param[separator+3:]))
		if err != nil || number < minimum || number > maximum {
			return fmt.Errorf("%s element number must be from %d through %d", name, minimum, maximum)
		}
		if _, exists := seen[number]; exists {
			return fmt.Errorf("%s has a duplicate element number", name)
		}
		seen[number] = struct{}{}
	}
	return nil
}

func parseCHTypeName(typeName string) (CHType, error) {
	statements, err := clickhouse.NewParser("CREATE TABLE __chgen_cast_type (value " + typeName + ")").ParseStmts()
	if err != nil {
		return CHType{}, err
	}
	if len(statements) != 1 {
		return CHType{}, fmt.Errorf("expected one type declaration, got %d statements", len(statements))
	}
	createTable, ok := statements[0].(*clickhouse.CreateTable)
	if !ok || createTable.TableSchema == nil || len(createTable.TableSchema.Columns) != 1 {
		return CHType{}, fmt.Errorf("could not parse ClickHouse type %q", typeName)
	}
	column, ok := createTable.TableSchema.Columns[0].(*clickhouse.ColumnDef)
	if !ok || column.Type == nil {
		return CHType{}, fmt.Errorf("could not parse ClickHouse type %q", typeName)
	}
	return parseCHType(column.Type)
}

// parseTupleType reads the element types, and the element names when the
// declaration gives them, out of a Tuple(...) declaration. Measured on
// ClickHouse 25.8.29.51: Tuple(Int32, String) has unnamed elements, and
// Tuple(a Int32, b String) has the names a and b.
func parseTupleType(typeExpr *clickhouse.NestedType) (CHType, error) {
	result := CHType{Name: typeExpr.Name.Name}
	names := make([]string, 0, len(typeExpr.Columns))
	named := false
	for _, element := range typeExpr.Columns {
		switch value := element.(type) {
		case *clickhouse.ColumnDef:
			if value.Name == nil || value.Name.Ident == nil || value.Type == nil {
				return CHType{}, fmt.Errorf("Tuple element has no name or no type")
			}
			elementType, err := parseCHType(value.Type)
			if err != nil {
				return CHType{}, err
			}
			result.Params = append(result.Params, elementType)
			names = append(names, value.Name.Ident.Name)
			named = true
		case clickhouse.ColumnType:
			elementType, err := parseCHType(value)
			if err != nil {
				return CHType{}, err
			}
			result.Params = append(result.Params, elementType)
			names = append(names, "")
		default:
			return CHType{}, fmt.Errorf("unsupported Tuple element AST %T", element)
		}
	}
	if len(result.Params) == 0 {
		return CHType{}, fmt.Errorf("Tuple has no elements")
	}
	if named {
		result.ParamNames = names
	}
	return result, nil
}

func parseCHTypeParams(name string, params []clickhouse.ColumnType) (CHType, error) {
	result := CHType{Name: canonicalCHTypeName(name), Params: make([]CHType, 0, len(params))}
	for _, param := range params {
		parsed, err := parseCHType(param)
		if err != nil {
			return CHType{}, err
		}
		result.Params = append(result.Params, parsed)
	}
	if strings.EqualFold(result.Name, "SimpleAggregateFunction") {
		if err := validateSimpleAggregateFunction(result); err != nil {
			return CHType{}, err
		}
	}
	return result, nil
}

func validateSimpleAggregateFunction(value CHType) error {
	if len(value.Params) != 2 {
		return fmt.Errorf("SimpleAggregateFunction expects a function and a type")
	}
	function := value.Params[0].Name
	inner := value.Params[1]
	switch {
	case function == "any", function == "anyLast", strings.EqualFold(function, "min"), strings.EqualFold(function, "max"):
		return nil
	case strings.EqualFold(function, "sum"):
		base := inner
		if base.normalizedName() == "nullable" && len(base.Params) == 1 {
			base = base.Params[0]
		}
		name := base.normalizedName()
		switch name {
		case "int64", "uint64", "int128", "uint128", "int256", "uint256", "float64":
			return nil
		}
		if name == "decimal" && len(base.LiteralParams) == 2 && base.LiteralParams[0] == "38" {
			return nil
		}
		return fmt.Errorf("SimpleAggregateFunction(sum, T) requires a T that sum does not widen")
	case function == "groupArrayArray":
		if inner.normalizedName() == "array" && len(inner.Params) == 1 &&
			inner.Params[0].normalizedName() != "nullable" && inner.Params[0].normalizedName() != "lowcardinality" {
			return nil
		}
		return fmt.Errorf("SimpleAggregateFunction(groupArrayArray, T) requires an Array T that the aggregate does not change")
	default:
		return fmt.Errorf("SimpleAggregateFunction function %s has no measured constructor rule", value.Params[0].String())
	}
}

func (t CHType) normalizedName() string {
	return strings.ToLower(canonicalCHTypeName(t.Name))
}

func canonicalCHTypeName(name string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(name), " "))
	aliases := map[string]string{
		"int": "Int32", "int signed": "Int32", "integer": "Int32", "integer signed": "Int32",
		"mediumint": "Int32", "mediumint signed": "Int32",
		"int unsigned": "UInt32", "integer unsigned": "UInt32", "mediumint unsigned": "UInt32",
		"bigint": "Int64", "bigint signed": "Int64", "signed": "Int64",
		"bigint unsigned": "UInt64", "unsigned": "UInt64", "bit": "UInt64",
		"smallint": "Int16", "smallint signed": "Int16", "smallint unsigned": "UInt16",
		"tinyint": "Int8", "tinyint signed": "Int8", "int1": "Int8", "int1 signed": "Int8", "byte": "Int8",
		"tinyint unsigned": "UInt8", "int1 unsigned": "UInt8",
		"float": "Float32", "real": "Float32", "single": "Float32",
		"double": "Float64", "double precision": "Float64",
		"year": "UInt16", "bool": "Bool", "boolean": "Bool",
		"dec": "Decimal", "fixed": "Decimal", "numeric": "Decimal",
	}
	if canonical, ok := aliases[normalized]; ok {
		return canonical
	}
	canonicalNames := map[string]string{
		"array": "Array", "map": "Map", "nullable": "Nullable", "lowcardinality": "LowCardinality",
		"string": "String", "fixedstring": "FixedString", "bool": "Bool",
		"date": "Date", "date32": "Date32", "datetime": "DateTime", "datetime64": "DateTime64",
		"uuid": "UUID", "ipv4": "IPv4", "ipv6": "IPv6",
		"decimal": "Decimal", "decimal32": "Decimal32", "decimal64": "Decimal64",
		"decimal128": "Decimal128", "decimal256": "Decimal256",
		"enum": "Enum", "enum8": "Enum8", "enum16": "Enum16",
	}
	if canonical, ok := canonicalNames[normalized]; ok {
		return canonical
	}
	return name
}

func validateTypeFamilySpelling(name string) error {
	normalized := strings.ToLower(strings.Join(strings.Fields(name), " "))
	if isMeasuredCaseInsensitiveTypeAlias(normalized) {
		return nil
	}
	canonical := map[string]string{
		"array": "Array", "map": "Map", "nullable": "Nullable", "lowcardinality": "LowCardinality",
		"simpleaggregatefunction": "SimpleAggregateFunction", "aggregatefunction": "AggregateFunction",
		"tuple": "Tuple", "nested": "Nested", "variant": "Variant", "dynamic": "Dynamic", "json": "JSON",
		"string": "String", "fixedstring": "FixedString",
		"date": "Date", "date32": "Date32", "uuid": "UUID", "ipv4": "IPv4", "ipv6": "IPv6",
		"int8": "Int8", "int16": "Int16", "int32": "Int32", "int64": "Int64",
		"int128": "Int128", "int256": "Int256", "uint8": "UInt8", "uint16": "UInt16",
		"uint32": "UInt32", "uint64": "UInt64", "uint128": "UInt128", "uint256": "UInt256",
		"float32": "Float32", "float64": "Float64", "decimal32": "Decimal32", "decimal64": "Decimal64",
		"decimal128": "Decimal128", "decimal256": "Decimal256", "enum": "Enum", "enum8": "Enum8", "enum16": "Enum16",
		"point": "Point", "ring": "Ring", "linestring": "LineString", "polygon": "Polygon",
		"multilinestring": "MultiLineString", "multipolygon": "MultiPolygon",
	}
	if expected, exists := canonical[normalized]; exists && name != expected {
		return fmt.Errorf("ClickHouse type family %s is case-sensitive; use %s", name, expected)
	}
	return nil
}

func isMeasuredCaseInsensitiveTypeAlias(name string) bool {
	switch name {
	case "int", "int signed", "integer", "integer signed", "mediumint", "mediumint signed",
		"int unsigned", "integer unsigned", "mediumint unsigned", "bigint", "bigint signed",
		"signed", "bigint unsigned", "unsigned", "bit", "smallint", "smallint signed",
		"smallint unsigned", "tinyint", "tinyint signed", "int1", "int1 signed", "byte",
		"tinyint unsigned", "int1 unsigned", "year", "float", "real", "single",
		"double", "double precision", "bool", "boolean", "dec", "fixed", "numeric",
		"decimal", "datetime", "datetime64":
		return true
	default:
		return false
	}
}

func isZeroArgumentSupportedType(name string) bool {
	switch strings.ToLower(name) {
	case "string", "bool", "date", "date32", "uuid", "ipv4", "ipv6",
		"int8", "int16", "int32", "int64", "uint8", "uint16", "uint32", "uint64",
		"float32", "float64":
		return true
	default:
		return false
	}
}

func isIntegerTypeAlias(name string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(name), " "))
	switch normalized {
	case "int", "int signed", "integer", "integer signed", "mediumint", "mediumint signed",
		"int unsigned", "integer unsigned", "mediumint unsigned", "bigint", "bigint signed",
		"signed", "bigint unsigned", "unsigned", "bit", "smallint", "smallint signed",
		"smallint unsigned", "tinyint", "tinyint signed", "int1", "int1 signed", "byte",
		"tinyint unsigned", "int1 unsigned", "year":
		return true
	default:
		return false
	}
}

func floatAliasCanonicalName(name string) (string, bool) {
	switch strings.ToLower(strings.Join(strings.Fields(name), " ")) {
	case "float", "real", "single":
		return "Float32", true
	case "double", "double precision":
		return "Float64", true
	default:
		return "", false
	}
}

func canonicalParameterizedDateTime(name string, params []string) (CHType, error) {
	isDateTime64 := strings.EqualFold(name, "DateTime64")
	if len(params) == 0 {
		if isDateTime64 {
			return CHType{Name: "DateTime64", LiteralParams: []string{"3"}}, nil
		}
		return CHType{Name: "DateTime"}, nil
	}
	scale, scaleErr := strconv.Atoi(params[0])
	if scaleErr != nil {
		if !isDateTime64 && isQuotedTypeString(params[0]) {
			if err := validateMeasuredTimezone(params[0]); err != nil {
				return CHType{}, err
			}
			return CHType{Name: "DateTime", LiteralParams: params[:1]}, nil
		}
		return CHType{}, fmt.Errorf("%s scale must be from 0 through 9", name)
	}
	if len(params) > 2 {
		return CHType{}, fmt.Errorf("%s accepts at most scale and timezone", name)
	}
	if scale < 0 || scale > 9 {
		return CHType{}, fmt.Errorf("%s scale must be from 0 through 9", name)
	}
	if isDateTime64 {
		if len(params) == 2 {
			if !isQuotedTypeString(params[1]) {
				params = params[:1]
			} else if err := validateMeasuredTimezone(params[1]); err != nil {
				return CHType{}, err
			}
		}
		return CHType{Name: "DateTime64", LiteralParams: params}, nil
	}
	if len(params) == 2 {
		if !isQuotedTypeString(params[1]) {
			params = params[:1]
		} else if err := validateMeasuredTimezone(params[1]); err != nil {
			return CHType{}, err
		}
	}
	if scale == 0 {
		if len(params) == 2 {
			return CHType{Name: "DateTime", LiteralParams: params[1:]}, nil
		}
		return CHType{Name: "DateTime"}, nil
	}
	return CHType{Name: "DateTime64", LiteralParams: params}, nil
}

func isQuotedTypeString(value string) bool {
	return len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\''
}

func validateMeasuredTimezone(value string) error {
	switch value {
	case "'UTC'", "'Asia/Tokyo'", "'Europe/Berlin'", "'Europe/Moscow'":
		return nil
	default:
		return fmt.Errorf("timezone %s has no pinned ClickHouse constructor evidence", value)
	}
}

func (t CHType) String() string {
	if len(t.Params) == 0 && len(t.LiteralParams) == 0 {
		return t.Name
	}
	params := make([]string, 0, len(t.LiteralParams)+len(t.Params))
	params = append(params, t.LiteralParams...)
	for index, param := range t.Params {
		if index < len(t.ParamNames) && t.ParamNames[index] != "" {
			params = append(params, t.ParamNames[index]+" "+param.String())
			continue
		}
		params = append(params, param.String())
	}
	return fmt.Sprintf("%s(%s)", t.Name, strings.Join(params, ", "))
}
