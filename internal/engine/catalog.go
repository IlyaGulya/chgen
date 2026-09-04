package engine

import (
	"fmt"
	"os"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

const externalMarker = "-- chgen:external"

// SchemaCatalogs holds the two catalogs that one ordered schema input stream
// produces. Physical tables answer FROM references; External tables answer
// chgen.external(...) references. The catalogs never mix.
type SchemaCatalogs struct {
	Physical *Schema
	External *Schema
}

// ParseSchemaCatalogs reads ordered schema files into a physical catalog and
// an external catalog. A CREATE TABLE directly after a "-- chgen:external"
// marker line declares an external row schema. Between the marker and the
// CREATE keyword only blank lines and comment lines are permitted.
func ParseSchemaCatalogs(paths []string) (*SchemaCatalogs, error) {
	catalogs := &SchemaCatalogs{
		Physical: &Schema{Tables: make(map[string]Table)},
		External: &Schema{Tables: make(map[string]Table)},
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read schema file %q: %w", path, err)
		}
		if err := applySchemaSource(catalogs, path, string(data)); err != nil {
			return nil, err
		}
	}
	return catalogs, nil
}

func applySchemaSource(catalogs *SchemaCatalogs, path, raw string) error {
	// The TTL preprocessor blanks the unsupported rollup tail in place, so
	// byte offsets and line numbers of the processed text match the file.
	content := stripUnsupportedTTLRollup(raw)
	statements, err := clickhouse.NewParser(content).ParseStmts()
	if err != nil {
		return fmt.Errorf("%s: parse schema SQL: %w", path, err)
	}

	markers, err := externalMarkerTargets(path, content)
	if err != nil {
		return err
	}

	createPositions := make(map[int]bool, len(statements))
	for _, statement := range statements {
		if create, ok := statement.(*clickhouse.CreateTable); ok {
			createPositions[int(create.CreatePos)] = true
		}
	}
	for _, marker := range markers {
		if !createPositions[marker.target] {
			return fmt.Errorf("%s:%d: %s marker is not followed by CREATE TABLE", path, marker.line, externalMarker)
		}
	}
	markedOffsets := make(map[int]bool, len(markers))
	for _, marker := range markers {
		markedOffsets[marker.target] = true
	}

	for _, statement := range statements {
		switch statement := statement.(type) {
		case *clickhouse.CreateTable:
			line := lineOfOffset(content, int(statement.CreatePos))
			if markedOffsets[int(statement.CreatePos)] {
				if err := applyExternalCreate(catalogs, path, line, statement); err != nil {
					return err
				}
				continue
			}
			if err := applyPhysicalCreate(catalogs, path, line, statement); err != nil {
				return err
			}
		case *clickhouse.AlterTable:
			line := lineOfOffset(content, int(statement.AlterPos))
			if err := applyCatalogAlter(catalogs, path, content, line, statement); err != nil {
				return err
			}
		case *clickhouse.DropStmt:
			line := lineOfOffset(content, int(statement.DropPos))
			if err := applyCatalogDrop(catalogs, path, line, statement); err != nil {
				return err
			}
		case *clickhouse.CreateView, *clickhouse.CreateMaterializedView, *clickhouse.InsertStmt:
			// Views and seed INSERT statements do not contribute physical
			// column definitions to this catalog.
		default:
			line := lineOfOffset(content, int(statement.Pos()))
			return fmt.Errorf("%s:%d: statement is not CREATE TABLE, DROP TABLE/VIEW, or a supported ALTER TABLE; move non-schema SQL out of the schema inputs", path, line)
		}
	}
	return nil
}

func applyExternalCreate(catalogs *SchemaCatalogs, path string, line int, statement *clickhouse.CreateTable) error {
	table, err := parseCreateTable(statement)
	if err != nil {
		return fmt.Errorf("%s:%d: %w", path, line, err)
	}
	if statement.Engine != nil {
		return fmt.Errorf("%s:%d: external schema %q must not declare ENGINE/ORDER BY/PARTITION BY/TTL; an external schema is a column list only", path, line, table.Name)
	}
	if physical, exists := catalogs.Physical.Tables[table.Name]; exists {
		return fmt.Errorf("%s:%d: external schema %q collides with physical table %q declared at %s:%d; rename one of them", path, line, table.Name, physical.Name, physical.File, physical.Line)
	}
	if previous, exists := catalogs.External.Tables[table.Name]; exists {
		return fmt.Errorf("%s:%d: duplicate CREATE TABLE %q; first declared at %s:%d", path, line, table.Name, previous.File, previous.Line)
	}
	table.File = path
	table.Line = line
	catalogs.External.Tables[table.Name] = table
	return nil
}

func applyPhysicalCreate(catalogs *SchemaCatalogs, path string, line int, statement *clickhouse.CreateTable) error {
	table, err := parseCreateTable(statement)
	if err != nil {
		return fmt.Errorf("%s:%d: %w", path, line, err)
	}
	if table.Name == migrationsTableName {
		return nil
	}
	if external, exists := catalogs.External.Tables[table.Name]; exists {
		return fmt.Errorf("%s:%d: external schema %q collides with physical table %q declared at %s:%d; rename one of them", external.File, external.Line, external.Name, table.Name, path, line)
	}
	if previous, exists := catalogs.Physical.Tables[table.Name]; exists {
		return fmt.Errorf("%s:%d: duplicate CREATE TABLE %q; first declared at %s:%d", path, line, table.Name, previous.File, previous.Line)
	}
	table.File = path
	table.Line = line
	catalogs.Physical.Tables[table.Name] = table
	return nil
}

func applyCatalogDrop(catalogs *SchemaCatalogs, path string, line int, statement *clickhouse.DropStmt) error {
	if statement.Name == nil || statement.Name.Table == nil {
		return fmt.Errorf("%s:%d: %s has no object name", path, line, statement.Type())
	}

	switch statement.DropTarget {
	case clickhouse.KeywordView:
		// Views never enter the catalog, so dropping one is a catalog no-op.
		// Accept it explicitly so migration replay does not mistake the absent
		// catalog entry for an unknown physical table.
		return nil
	case clickhouse.KeywordTable:
	default:
		return fmt.Errorf("%s:%d: %s is not supported; supported DROP operations: DROP TABLE, DROP VIEW", path, line, statement.Type())
	}

	tableName := statement.Name.Table.Name
	if tableName == migrationsTableName {
		return nil
	}
	if _, exists := catalogs.External.Tables[tableName]; exists {
		return fmt.Errorf("%s:%d: DROP TABLE %q targets an external schema; remove or edit its CREATE TABLE instead", path, line, tableName)
	}
	if _, exists := catalogs.Physical.Tables[tableName]; !exists {
		if statement.IfExists {
			return nil
		}
		return fmt.Errorf("%s:%d: DROP TABLE %q targets an unknown table; add IF EXISTS if the table may be absent", path, line, tableName)
	}
	delete(catalogs.Physical.Tables, tableName)
	return nil
}

const migrationsTableName = "schema_migrations"

var supportedAlterOperations = "supported ALTER TABLE operations: ADD COLUMN, MODIFY COLUMN, DROP COLUMN; projection operations ADD PROJECTION, MATERIALIZE PROJECTION, DROP PROJECTION, CLEAR PROJECTION and index operations ADD INDEX, MATERIALIZE INDEX, DROP INDEX, CLEAR INDEX are ignored"

func applyCatalogAlter(catalogs *SchemaCatalogs, path, content string, line int, statement *clickhouse.AlterTable) error {
	if statement.TableIdentifier == nil || statement.TableIdentifier.Table == nil {
		return fmt.Errorf("%s:%d: ALTER TABLE has no table name", path, line)
	}
	tableName := statement.TableIdentifier.Table.Name
	if tableName == migrationsTableName {
		return nil
	}
	if _, exists := catalogs.External.Tables[tableName]; exists {
		return fmt.Errorf("%s:%d: ALTER TABLE %q targets an external schema; edit its CREATE TABLE instead", path, line, tableName)
	}
	for _, clause := range statement.AlterExprs {
		switch classifyCatalogAlter(clause) {
		case catalogAlterApply, catalogAlterIgnore:
		case catalogAlterReject:
			clauseLine := lineOfOffset(content, int(clause.Pos()))
			return fmt.Errorf("%s:%d: %s is not supported; %s", path, clauseLine, alterClauseKeyword(clause), supportedAlterOperations)
		}
	}
	if err := applyAlterTable(catalogs.Physical, statement); err != nil {
		return fmt.Errorf("%s:%d: %w", path, line, err)
	}
	return nil
}

// alterClauseKeyword names an ALTER TABLE clause by its SQL keywords, never
// by its Go AST type name.
func alterClauseKeyword(clause clickhouse.AlterTableClause) string {
	name := fmt.Sprintf("%T", clause)
	name = strings.TrimPrefix(name, "*parser.AlterTable")
	var words []string
	start := 0
	for index := 1; index <= len(name); index++ {
		if index == len(name) || (name[index] >= 'A' && name[index] <= 'Z' && name[index-1] >= 'a' && name[index-1] <= 'z') {
			words = append(words, strings.ToUpper(name[start:index]))
			start = index
		}
	}
	return strings.Join(words, " ")
}

type externalMarkerRef struct {
	line   int
	target int
}

// externalMarkerTargets finds every marker line and the byte offset of the
// first content after it, skipping blank lines and comment lines.
func externalMarkerTargets(path, content string) ([]externalMarkerRef, error) {
	var markers []externalMarkerRef
	offset := 0
	line := 0
	for offset <= len(content) {
		line++
		end := strings.IndexByte(content[offset:], '\n')
		var next int
		var text string
		if end == -1 {
			text = content[offset:]
			next = len(content) + 1
		} else {
			text = content[offset : offset+end]
			next = offset + end + 1
		}
		if strings.TrimSpace(text) == externalMarker {
			target, found := nextContentOffset(content, next)
			if !found {
				return nil, fmt.Errorf("%s:%d: %s marker is not followed by CREATE TABLE", path, line, externalMarker)
			}
			markers = append(markers, externalMarkerRef{line: line, target: target})
		}
		offset = next
	}
	return markers, nil
}

// nextContentOffset returns the offset of the first byte after start that is
// not whitespace and not inside a -- line comment.
func nextContentOffset(content string, start int) (int, bool) {
	index := start
	for index < len(content) {
		switch {
		case content[index] == ' ' || content[index] == '\t' || content[index] == '\r' || content[index] == '\n':
			index++
		case strings.HasPrefix(content[index:], "--"):
			index = skipSQLLineComment(content, index)
		default:
			return index, true
		}
	}
	return 0, false
}

func lineOfOffset(content string, offset int) int {
	if offset > len(content) {
		offset = len(content)
	}
	return 1 + strings.Count(content[:offset], "\n")
}
