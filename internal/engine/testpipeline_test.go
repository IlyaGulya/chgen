package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// The helpers in this file give the tests the same string-in convenience that
// the removed legacy front doors gave, but they route every call through the
// catalog pipeline that the CLI uses: ParseSchemaCatalogs and ParseQueryFiles.
// A test thus measures the behaviour that a user gets, and no second loader
// with different semantics is necessary.

// catalogsFromDDL builds the physical and external catalogs from one DDL
// string. The string is written to a temporary file, because the catalog
// loader reads files and reports the file and the line in its diagnostics.
func catalogsFromDDL(t *testing.T, ddl string) *SchemaCatalogs {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.sql")
	if err := os.WriteFile(path, []byte(ddl), 0o644); err != nil {
		t.Fatalf("write schema file: %v", err)
	}
	catalogs, err := ParseSchemaCatalogs([]string{path})
	if err != nil {
		t.Fatalf("ParseSchemaCatalogs() error = %v", err)
	}
	return catalogs
}

// schemaFromDDL returns the physical catalog of one DDL string.
func schemaFromDDL(t *testing.T, ddl string) *Schema {
	t.Helper()
	return catalogsFromDDL(t, ddl).Physical
}

// catalogsFromDDLFiles builds the catalogs from ordered DDL files. Use it when
// a test must show that a later migration file changes an earlier table.
func catalogsFromDDLFiles(t *testing.T, paths []string) (*SchemaCatalogs, error) {
	t.Helper()
	return ParseSchemaCatalogs(paths)
}

// parseQueriesWithCatalogs parses annotated query text against catalogs. The
// text is written to a temporary file, so the query pipeline is the same one
// that the CLI runs.
func parseQueriesWithCatalogs(t *testing.T, input string, catalogs *SchemaCatalogs) ([]Query, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queries.sql")
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatalf("write query file: %v", err)
	}
	return ParseQueryFiles([]string{path}, catalogs)
}

// parseQueriesWithSchema parses annotated query text against one physical
// catalog and no external catalog.
func parseQueriesWithSchema(t *testing.T, input string, schema *Schema) ([]Query, error) {
	t.Helper()
	return parseQueriesWithCatalogs(t, input, &SchemaCatalogs{
		Physical: schema,
		External: &Schema{Tables: make(map[string]Table)},
	})
}

// parseQueriesWithDDL is the common case: one DDL string and one query string.
func parseQueriesWithDDL(t *testing.T, ddl, input string) ([]Query, error) {
	t.Helper()
	return parseQueriesWithCatalogs(t, input, catalogsFromDDL(t, ddl))
}

// schemaFromDDLErr is schemaFromDDL in the error-returning shape that the
// ported tests use. A test that must inspect a schema error keeps the error.
func schemaFromDDLErr(t *testing.T, ddl string) (*Schema, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.sql")
	if err := os.WriteFile(path, []byte(ddl), 0o644); err != nil {
		t.Fatalf("write schema file: %v", err)
	}
	catalogs, err := ParseSchemaCatalogs([]string{path})
	if err != nil {
		return nil, err
	}
	return catalogs.Physical, nil
}

// schemasFromFilesErr loads ordered DDL files into one physical catalog.
func schemasFromFilesErr(t *testing.T, paths []string) (*Schema, error) {
	t.Helper()
	catalogs, err := ParseSchemaCatalogs(paths)
	if err != nil {
		return nil, err
	}
	return catalogs.Physical, nil
}

// parseQueriesWithCatalogsErr parses query text against a physical catalog and
// an external catalog given separately, as the removed ParseWithSchemas did.
func parseQueriesWithCatalogsErr(t *testing.T, input string, physical, external *Schema) ([]Query, error) {
	t.Helper()
	if external == nil {
		external = &Schema{Tables: make(map[string]Table)}
	}
	return parseQueriesWithCatalogs(t, input, &SchemaCatalogs{Physical: physical, External: external})
}

// parseQueryFileWithSchema parses an existing query file against one physical
// catalog. It keeps the file path, thus the diagnostics keep the file and the
// line of the offending source.
func parseQueryFileWithSchema(t *testing.T, path string, schema *Schema) ([]Query, error) {
	t.Helper()
	return ParseQueryFiles([]string{path}, &SchemaCatalogs{
		Physical: schema,
		External: &Schema{Tables: make(map[string]Table)},
	})
}
