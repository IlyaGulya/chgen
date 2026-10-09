package engine

import (
	"fmt"
	"os"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// ParseServerExternalSchemas reads only marked external declarations. Physical
// migrations are neither replayed nor executed in server-analysis mode.
func ParseServerExternalSchemas(paths []string) (*Schema, error) {
	catalogs := &SchemaCatalogs{Physical: &Schema{Tables: map[string]Table{}}, External: &Schema{Tables: map[string]Table{}}}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		content := string(data)
		markers, err := externalMarkerTargets(path, content)
		if err != nil {
			return nil, err
		}
		for _, marker := range markers {
			tokens, err := scanSQLBoundary(content[marker.target:])
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %w", path, marker.line, err)
			}
			end := len(content)
			for _, token := range tokens {
				if !token.quoted && !token.comment && token.text == ";" {
					end = marker.target + token.end
					break
				}
			}
			statements, err := clickhouse.NewParser(content[marker.target:end] + "\n;").ParseStmts()
			if err != nil {
				return nil, fmt.Errorf("%s:%d: parse external schema: %w", path, marker.line, err)
			}
			if len(statements) != 1 {
				return nil, fmt.Errorf("%s:%d: external marker requires one CREATE TABLE", path, marker.line)
			}
			create, ok := statements[0].(*clickhouse.CreateTable)
			if !ok {
				return nil, fmt.Errorf("%s:%d: external marker requires CREATE TABLE", path, marker.line)
			}
			if err := checkQualifiedReferences(create); err != nil {
				return nil, fmt.Errorf("%s:%d: %w", path, marker.line, err)
			}
			parseTable := func(create *clickhouse.CreateTable) (Table, error) {
				return parseCreateTableWithType(create, parseServerCHType)
			}
			if err := applyExternalCreateWithParser(catalogs, path, marker.line, create, parseTable); err != nil {
				return nil, err
			}
		}
	}
	return catalogs.External, nil
}
