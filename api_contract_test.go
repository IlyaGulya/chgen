package chgen_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen"
)

var (
	_ func(string) error                                           = chgen.Run
	_ func(string) (*chgen.Config, error)                          = chgen.LoadConfig
	_ func([]string) (*chgen.SchemaCatalogs, error)                = chgen.ParseSchemaCatalogs
	_ func([]string, *chgen.SchemaCatalogs) ([]chgen.Query, error) = chgen.ParseQueryFiles
	_ func(string, []chgen.Query) ([]byte, error)                  = chgen.Generate
	_ func(string, string, string) (chgen.CHType, error)           = chgen.InferExpressionType
)

// TestSupportedPipeline uses only the supported external API. This test must
// compile and run as a package that cannot read unexported chgen names.
func TestSupportedPipeline(t *testing.T) {
	directory := t.TempDir()
	schemaPath := filepath.Join(directory, "schema.sql")
	queryPath := filepath.Join(directory, "queries.sql")
	if err := os.WriteFile(schemaPath, []byte("CREATE TABLE events (id UInt64) ENGINE = Memory"), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	if err := os.WriteFile(queryPath, []byte("-- name: ListEvents :many\nSELECT id FROM events\n"), 0o644); err != nil {
		t.Fatalf("write query: %v", err)
	}

	catalogs, err := chgen.ParseSchemaCatalogs([]string{schemaPath})
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	queries, err := chgen.ParseQueryFiles([]string{queryPath}, catalogs)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	source, err := chgen.Generate("eventquery", queries)
	if err != nil {
		t.Fatalf("generate source: %v", err)
	}
	if len(source) == 0 {
		t.Fatal("generated source is empty")
	}

	inferred, err := chgen.InferExpressionType(
		"CREATE TABLE events (id UInt64) ENGINE = Memory",
		"events",
		"id",
	)
	if err != nil {
		t.Fatalf("infer expression: %v", err)
	}
	if inferred.String() != "UInt64" {
		t.Fatalf("inferred type = %s, want UInt64", inferred)
	}
}

func TestFacadePreservesBatchStateAndCallerMutations(t *testing.T) {
	directory := t.TempDir()
	schemaPath := filepath.Join(directory, "schema.sql")
	queryPath := filepath.Join(directory, "queries.sql")
	if err := os.WriteFile(schemaPath, []byte("CREATE TABLE events (id UInt64, label String) ENGINE = Memory"), 0o644); err != nil {
		t.Fatal(err)
	}
	querySQL := "-- name: InsertEvent :exec\nINSERT INTO events (id, label) VALUES (chgen.arg('ID'), chgen.arg('Label'))\n"
	if err := os.WriteFile(queryPath, []byte(querySQL), 0o644); err != nil {
		t.Fatal(err)
	}
	catalogs, err := chgen.ParseSchemaCatalogs([]string{schemaPath})
	if err != nil {
		t.Fatal(err)
	}
	table := catalogs.Physical.Tables["events"]
	column := table.Columns["id"]
	column.Type = chgen.CHType{Name: "UInt32"}
	table.Columns["id"] = column
	catalogs.Physical.Tables["events"] = table

	queries, err := chgen.ParseQueryFiles([]string{queryPath}, catalogs)
	if err != nil {
		t.Fatal(err)
	}
	queries[0].Params[0].GoName = "ChangedID"
	source, err := chgen.Generate("eventquery", queries)
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "PrepareBatch") {
		t.Fatal("the facade dropped the parsed INSERT VALUES batch state")
	}
	if !strings.Contains(text, "ChangedID uint32") {
		t.Fatalf("the facade dropped a caller mutation of a parameter or schema:\n%s", text)
	}
}

func TestFacadeConcreteDTOsAndNestedTypeStrings(t *testing.T) {
	types := []chgen.CHType{
		{Name: "Nullable", Params: []chgen.CHType{{Name: "String"}}},
		{Name: "LowCardinality", Params: []chgen.CHType{{Name: "String"}}},
		{Name: "Array", Params: []chgen.CHType{{Name: "UInt16"}}},
		{Name: "Map", Params: []chgen.CHType{{Name: "String"}, {Name: "Nullable", Params: []chgen.CHType{{Name: "UInt64"}}}}},
		{Name: "Tuple", Params: []chgen.CHType{{Name: "UInt8"}, {Name: "String"}}, ParamNames: []string{"id", "name"}},
		{Name: "DateTime64", LiteralParams: []string{"3", "'UTC'"}},
		{Name: "AggregateFunction", Params: []chgen.CHType{{Name: "UInt64"}}, LiteralParams: []string{"sum"}},
	}
	want := []string{
		"Nullable(String)", "LowCardinality(String)", "Array(UInt16)",
		"Map(String, Nullable(UInt64))", "Tuple(id UInt8, name String)",
		"DateTime64(3, 'UTC')", "AggregateFunction(sum, UInt64)",
	}
	for index, value := range types {
		if got := value.String(); got != want[index] {
			t.Errorf("type %d = %s, want %s", index, got, want[index])
		}
	}

	query := chgen.Query{
		Name:            "List",
		File:            "queries.sql",
		Line:            1,
		Command:         chgen.CommandMany,
		Params:          []chgen.Param{{GoName: "ID", GoType: "uint64", CHType: chgen.CHType{Name: "UInt64"}}},
		ExternalParams:  []chgen.ExternalParam{{GoName: "Rows", SchemaName: "row", WireName: "rows", RowType: "Row", Columns: []chgen.ExternalColumn{{GoName: "ID", SQLName: "id", GoType: "uint64", ClickHouseType: "UInt64"}}}},
		ParamIndexes:    []int{0},
		Results:         []chgen.Result{{GoName: "ID", SQLName: "id", GoType: "uint64", CHType: chgen.CHType{Name: "UInt64"}}},
		ResultCapacity:  "1",
		SQL:             "SELECT ? AS id",
		NamedParamNames: []string{"ID"},
	}
	config := chgen.Config{Path: "chgen.yaml", Version: 1, Packages: []chgen.PackageConfig{{Name: "query", Output: "query.go", Queries: []chgen.InputEntry{{Entry: "queries.sql", Path: "/queries.sql"}}, Schema: []chgen.InputEntry{{Entry: "schema.sql", Path: "/schema.sql"}}}}}
	schema := chgen.Schema{Tables: map[string]chgen.Table{"events": {Name: "events", File: "schema.sql", Line: 1, Columns: map[string]chgen.Column{"id": {Name: "id", Type: chgen.CHType{Name: "UInt64"}, Insertable: true}}, ColumnOrder: []string{"id"}, Engine: &chgen.TableEngine{Name: "Memory", Params: []string{}, OrderBy: []string{}}}}}
	catalogs := chgen.SchemaCatalogs{Physical: &schema, External: &chgen.Schema{Tables: map[string]chgen.Table{}}}
	query.Results[0].GoName = "Changed"
	config.Packages[0].Queries[0].Entry = "changed.sql"
	catalogs.Physical.Tables["events"] = schema.Tables["events"]
	if query.Results[0].GoName != "Changed" || config.Packages[0].Queries[0].Entry != "changed.sql" || catalogs.Physical.Tables["events"].Name != "events" {
		t.Fatal("a public DTO mutation did not persist")
	}
	if _, err := chgen.InferQueryResultType("CREATE TABLE events (id UInt64) ENGINE = Memory", "SELECT id FROM events"); err != nil {
		t.Fatal(err)
	}
}

// TestSupportedPackageSurface prevents an internal helper from becoming a
// public API by accident. A deliberate API change must update this list and
// the external contract tests in the same change.
func TestSupportedPackageSurface(t *testing.T) {
	want := []string{
		"CHType",
		"Check",
		"CheckReport",
		"Column",
		"Command",
		"CommandExec",
		"CommandMany",
		"CommandOne",
		"Config",
		"Diagnostic",
		"ExplainError",
		"ExternalColumn",
		"ExternalParam",
		"Generate",
		"InferExpressionType",
		"InferQueryResultType",
		"InputEntry",
		"LoadConfig",
		"MeasuredCHVersion",
		"PackageConfig",
		"Param",
		"ParseQueryFiles",
		"ParseSchemaCatalogs",
		"Query",
		"Result",
		"Run",
		"Schema",
		"SchemaCatalogs",
		"Table",
		"TableEngine",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	set := token.NewFileSet()
	seen := make(map[string]bool)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(set, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if declaration.Recv == nil && declaration.Name.IsExported() {
					seen[declaration.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, specification := range declaration.Specs {
					switch specification := specification.(type) {
					case *ast.TypeSpec:
						if specification.Name.IsExported() {
							seen[specification.Name.Name] = true
						}
					case *ast.ValueSpec:
						for _, name := range specification.Names {
							if name.IsExported() {
								seen[name.Name] = true
							}
						}
					}
				}
			}
		}
	}
	got := make([]string, 0, len(seen))
	for name := range seen {
		got = append(got, name)
	}
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("exported package names = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("exported package names = %v, want %v", got, want)
		}
	}

	var _ string = chgen.MeasuredCHVersion
	var _ = chgen.CHType{}.String
}
