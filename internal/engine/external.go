package engine

import (
	"fmt"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
	"strings"
)

// ExternalParam describes one request-scoped ClickHouse external table used by
// a generated query. SchemaName identifies the reusable row schema, while
// WireName is the table identifier embedded in the runtime SQL and sent with
// the native query protocol.
type ExternalParam struct {
	GoName     string
	SchemaName string
	WireName   string
	RowType    string
	Columns    []ExternalColumn
}

// ExternalColumn is one typed column in an external-table row.
type ExternalColumn struct {
	GoName         string
	SQLName        string
	GoType         string
	ClickHouseType string
}

// checkQualifiedReferences rejects every db.table reference. The database is
// a connection property; a name in the SQL text would silently disagree with
// the connection.
func checkQualifiedReferences(statement clickhouse.Expr) error {
	var refError error
	clickhouse.Walk(statement, func(node clickhouse.Expr) bool {
		identifier, ok := node.(*clickhouse.TableIdentifier)
		if !ok || identifier.Database == nil || identifier.Table == nil || refError != nil {
			return true
		}
		if identifier.Database.Name == "__DATABASE__" {
			refError = fmt.Errorf("__DATABASE__ was removed; the database comes from the connection (clickhouse Auth.Database); use the unqualified name %s", identifier.Table.Name)
		} else {
			refError = fmt.Errorf("qualified reference %s.%s is not allowed; the database comes from the connection; use the unqualified name %s", identifier.Database.Name, identifier.Table.Name, identifier.Table.Name)
		}
		return false
	})
	return refError
}

func normalizeExternalTables(sql string, externalSchema *Schema, physicalSchema *Schema) (string, []ExternalParam, error) {
	var normalized strings.Builder
	normalized.Grow(len(sql))
	paramsByName := make(map[string]int)
	wireNames := make(map[string]string)
	params := make([]ExternalParam, 0)

	for index := 0; index < len(sql); {
		if quote := sql[index]; quote == '\'' || quote == '"' || quote == '`' {
			end := skipSQLQuoted(sql, index, quote)
			normalized.WriteString(sql[index:end])
			index = end
			continue
		}
		if strings.HasPrefix(sql[index:], "--") {
			end := skipSQLLineComment(sql, index)
			normalized.WriteString(sql[index:end])
			index = end
			continue
		}
		if strings.HasPrefix(sql[index:], "/*") {
			end := skipSQLBlockComment(sql, index)
			normalized.WriteString(sql[index:end])
			index = end
			continue
		}

		goName, schemaName, end, matched, err := parseExternalAt(sql, index)
		if err != nil {
			return "", nil, err
		}
		if !matched {
			normalized.WriteByte(sql[index])
			index++
			continue
		}
		if externalSchema == nil {
			externalSchema = &Schema{Tables: map[string]Table{}}
		}
		table, exists := externalSchema.Tables[schemaName]
		if !exists {
			return "", nil, unknownExternalSchemaError(schemaName, externalSchema, physicalSchema)
		}
		if goName == "" {
			goName = exportedIdentifier(schemaName)
		}
		if !goIdentifierPattern.MatchString(goName) || goName[0] < 'A' || goName[0] > 'Z' {
			return "", nil, fmt.Errorf("chgen.external parameter name %q must be an exported Go identifier", goName)
		}
		wireName := schemaName
		if goName != exportedIdentifier(schemaName) {
			wireName = strings.ToLower(goName[:1]) + goName[1:]
		}

		columns := make([]ExternalColumn, 0, len(table.ColumnOrder))
		columnGoNames := make(map[string]string, len(table.ColumnOrder))
		for _, columnName := range table.ColumnOrder {
			column := table.Columns[columnName]
			if !column.Insertable {
				return "", nil, fmt.Errorf(
					"external schema %s column %s must be an ordinary insertable column",
					schemaName,
					columnName,
				)
			}
			columnGoType, err := goType(column.Type)
			if err != nil {
				return "", nil, fmt.Errorf(
					"external schema %s column %s: %w",
					schemaName,
					columnName,
					err,
				)
			}
			columnGoName := exportedIdentifier(columnName)
			if previous, exists := columnGoNames[columnGoName]; exists {
				return "", nil, fmt.Errorf(
					"external schema %s columns %q and %q both generate Go field %s",
					schemaName,
					previous,
					columnName,
					columnGoName,
				)
			}
			columnGoNames[columnGoName] = columnName
			columns = append(columns, ExternalColumn{
				GoName:         columnGoName,
				SQLName:        columnName,
				GoType:         columnGoType,
				ClickHouseType: column.Type.String(),
			})
		}
		if len(columns) == 0 {
			return "", nil, fmt.Errorf("external schema %s has no columns", schemaName)
		}
		param := ExternalParam{
			GoName:     goName,
			SchemaName: schemaName,
			WireName:   wireName,
			RowType:    exportedIdentifier(schemaName) + "Row",
			Columns:    columns,
		}
		if previousIndex, exists := paramsByName[goName]; exists {
			previous := params[previousIndex]
			if previous.SchemaName != schemaName || previous.WireName != wireName {
				return "", nil, fmt.Errorf(
					"chgen.external parameter %q is reused with incompatible schemas %q and %q",
					goName,
					previous.SchemaName,
					schemaName,
				)
			}
		} else {
			if previousGoName, exists := wireNames[wireName]; exists {
				return "", nil, fmt.Errorf(
					"chgen.external parameters %q and %q resolve to duplicate wire table %q",
					previousGoName,
					goName,
					wireName,
				)
			}
			paramsByName[goName] = len(params)
			wireNames[wireName] = goName
			params = append(params, param)
		}

		normalized.WriteString(wireName)
		index = end
	}
	return normalized.String(), params, nil
}

const externalTablePrefix = "chgen.external"

func parseExternalAt(sql string, start int) (string, string, int, bool, error) {
	if len(sql)-start < len(externalTablePrefix) ||
		!strings.EqualFold(sql[start:start+len(externalTablePrefix)], externalTablePrefix) {
		return "", "", start, false, nil
	}
	if start > 0 && (isSQLIdentifierByte(sql[start-1]) || sql[start-1] == '.') {
		return "", "", start, false, nil
	}

	index := skipSQLWhitespace(sql, start+len(externalTablePrefix))
	if index >= len(sql) || sql[index] != '(' {
		return "", "", start, true, fmt.Errorf(
			"invalid chgen.external at byte %d: expected opening parenthesis",
			start,
		)
	}
	index = skipSQLWhitespace(sql, index+1)
	var goName string
	if index < len(sql) && sql[index] == '\'' {
		nameStart := index + 1
		index++
		for index < len(sql) && isSQLIdentifierByte(sql[index]) {
			index++
		}
		goName = sql[nameStart:index]
		if index >= len(sql) || sql[index] != '\'' {
			return "", "", start, true, fmt.Errorf("invalid chgen.external parameter name at byte %d", start)
		}
		index = skipSQLWhitespace(sql, index+1)
		if index >= len(sql) || sql[index] != ',' {
			return "", "", start, true, fmt.Errorf(
				"invalid chgen.external(%q) at byte %d: expected schema after comma",
				goName,
				start,
			)
		}
		index = skipSQLWhitespace(sql, index+1)
	}

	schemaStart := index
	if index >= len(sql) || !(sql[index] == '_' || sql[index] >= 'a' && sql[index] <= 'z' || sql[index] >= 'A' && sql[index] <= 'Z') {
		return "", "", start, true, fmt.Errorf("invalid chgen.external at byte %d: expected schema identifier", start)
	}
	for index < len(sql) && isSQLIdentifierByte(sql[index]) {
		index++
	}
	schemaName := sql[schemaStart:index]
	index = skipSQLWhitespace(sql, index)
	if index >= len(sql) || sql[index] != ')' {
		return "", "", start, true, fmt.Errorf(
			"invalid chgen.external(%s) at byte %d: expected closing parenthesis",
			schemaName,
			start,
		)
	}
	return goName, schemaName, index + 1, true, nil
}

func schemaForQuery(physical *Schema, externalParams []ExternalParam, externalSchema *Schema) (*Schema, error) {
	merged := &Schema{Tables: make(map[string]Table, len(physical.Tables)+len(externalParams))}
	for name, table := range physical.Tables {
		merged.Tables[name] = table
	}
	for _, param := range externalParams {
		if _, exists := merged.Tables[param.WireName]; exists {
			return nil, fmt.Errorf(
				"external parameter %s wire table %q collides with a physical table",
				param.GoName,
				param.WireName,
			)
		}
		table := externalSchema.Tables[param.SchemaName]
		table.Name = param.WireName
		merged.Tables[param.WireName] = table
	}
	return merged, nil
}

// unknownExternalSchemaError builds the complete diagnostic for a
// chgen.external reference that names no marked schema.
func unknownExternalSchemaError(schemaName string, externalSchema, physicalSchema *Schema) error {
	message := fmt.Sprintf("chgen.external(%s): unknown external schema; declare it with \"-- chgen:external\" before its CREATE TABLE in a schema input", schemaName)
	if physicalSchema != nil {
		if _, exists := physicalSchema.Tables[schemaName]; exists {
			message += fmt.Sprintf("; a physical table %q exists; chgen.external only accepts schemas marked -- chgen:external", schemaName)
			return &externalReferenceError{message: message}
		}
	}
	candidates := make([]string, 0, len(externalSchema.Tables))
	for name := range externalSchema.Tables {
		candidates = append(candidates, name)
	}
	if suggestion := suggestName(schemaName, candidates); suggestion != "" {
		message += fmt.Sprintf("; did you mean %q?", suggestion)
	}
	return &externalReferenceError{message: message}
}
