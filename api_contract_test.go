package chgen_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

func TestSchemaCatalogsAcceptWaitViewWithoutDefiningTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.sql")
	if err := os.WriteFile(path, []byte("SYSTEM WAIT VIEW v2_runner_group_snapshot_refresh;"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalogs, err := chgen.ParseSchemaCatalogs([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalogs.Physical.Tables) != 0 || len(catalogs.External.Tables) != 0 {
		t.Fatalf("waiting for a view changed the catalogs: %+v", catalogs)
	}
}

func TestSchemaCatalogsReplayChangesAroundWaitView(t *testing.T) {
	for _, wait := range []string{
		"SYSTEM WAIT VIEW refresh",
		"system wait view analytics.refresh",
		"SYSTEM WAIT VIEW `view with spaces`",
		"SYSTEM WAIT VIEW `database name`.`refresh;view`",
		`SYSTEM WAIT VIEW "database name"."refresh;view"`,
		"SYSTEM WAIT VIEW `refresh\\`view`",
		`SYSTEM WAIT VIEW "refresh\"view"`,
		"SYSTEM WAIT VIEW `refresh``view`",
		`SYSTEM WAIT VIEW "refresh""view"`,
		"SYSTEM /* ; */ WAIT\nVIEW analytics /* qualifier */ . refresh",
		"/* outer /* nested */ comment */ SYSTEM WAIT VIEW refresh /* outer /* nested */ comment */",
	} {
		t.Run(wait, func(t *testing.T) {
			ddl := "CREATE TABLE events (id UInt64) ENGINE=Memory;\n" + wait + ";\n" +
				"ALTER TABLE events ADD COLUMN label String;\n" +
				"-- chgen:external\nCREATE TABLE requested (id UInt64);\n" + wait
			config, _ := writeCheckProject(t, ddl, "-- name: Read :many\nSELECT id, label FROM events;")
			path := filepath.Join(filepath.Dir(config), "schema.sql")
			catalogs, err := chgen.ParseSchemaCatalogs([]string{path})
			if err != nil {
				t.Fatal(err)
			}
			table := catalogs.Physical.Tables["events"]
			if len(catalogs.Physical.Tables) != 1 || len(catalogs.External.Tables) != 1 ||
				catalogs.External.Tables["requested"].Columns["id"].Type.Name != "UInt64" ||
				!reflect.DeepEqual(table.ColumnOrder, []string{"id", "label"}) ||
				table.Columns["id"].Type.Name != "UInt64" || table.Columns["label"].Type.Name != "String" ||
				table.Engine == nil || table.Engine.Name != "Memory" || table.Line != 1 {
				t.Fatalf("wait changed or hid catalog definitions: %+v; %+v", table, catalogs.External.Tables)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != ddl {
				t.Fatalf("schema input was modified: %v", err)
			}
		})
	}
}

func TestSchemaCatalogsDoNotHideInvalidStatementsAroundWaitView(t *testing.T) {
	for _, sql := range []string{
		"SYSTEM WAIT VIEW;",
		"SYSTEM WAIT VIEW db.;",
		"SYSTEM WAIT VIEW a.b.c;",
		"SYSTEM WAIT VIEW 'refresh';",
		"SYSTEM WAIT VIEW 123;",
		"SYSTEM WAIT VIEW `unclosed;",
		"SYSTEM WAIT VIEW `escaped\\`",
		"SYSTEM WAIT VIEW ``;",
		"SYSTEM WAIT VIEW refresh /* unclosed",
		"SYSTEM /* outer /* inner */ WAIT VIEW refresh;",
		"SYSTEM WAIT VIEW refresh SYNC;",
		"SYSTEM WAIT VIEW IF EXISTS refresh;",
		"SYSTEM WAIT VIEW a, b;",
		"SYSTEM WAIT TABLE refresh;",
		"SYSTEM RELOAD CONFIG;",
		"SYSTEM WAIT VIEW refresh ALTER TABLE events ADD COLUMN label String;",
		"SYSTEM WAIT VIEW refresh;\nALTER TABLE events ADD COLUMN;",
		"ALTER TABLE events ADD COLUMN;\nSYSTEM WAIT VIEW refresh;",
		"SYSTEM WAIT VIEW refresh;\nALTER TABLE events DROP COLUMN missing;",
		"SYSTEM WAIT VIEW refresh;\nCREATE DATABASE hidden;",
		"SYSTEM WAIT VIEW refresh;\nINVALID",
		"SYSTEM WAIT VIEW refresh;\nALTER",
		"-- chgen:external\nSYSTEM WAIT VIEW refresh;\nCREATE TABLE requested (id UInt64);",
	} {
		t.Run(sql, func(t *testing.T) {
			config, _ := writeCheckProject(t, "CREATE TABLE events (id UInt64) ENGINE=Memory;\n"+sql,
				"-- name: Read :many\nSELECT id FROM events;")
			path := filepath.Join(filepath.Dir(config), "schema.sql")
			catalogs, err := chgen.ParseSchemaCatalogs([]string{path})
			if err == nil || catalogs != nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("invalid migration must fail with its source path: catalogs=%+v, err=%v", catalogs, err)
			}
		})
	}
}

func TestSchemaWaitViewPreservesSourceLocationsAndQuotedText(t *testing.T) {
	const ddl = "-- SYSTEM WAIT VIEW not_a_statement;\n" +
		"SYSTEM WAIT\nVIEW `refresh;view`;\n" +
		"CREATE TABLE events (id UInt64, label String DEFAULT 'SYSTEM WAIT VIEW text;') ENGINE=Memory;\n" +
		"SYSTEM WAIT VIEW refresh;\n"
	config, _ := writeCheckProject(t, ddl, "-- name: Read :many\nSELECT id FROM events;")
	path := filepath.Join(filepath.Dir(config), "schema.sql")
	catalogs, err := chgen.ParseSchemaCatalogs([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if table := catalogs.Physical.Tables["events"]; table.Line != 4 || table.File != path {
		t.Fatalf("CREATE TABLE location changed: %+v", table)
	}
	if err := os.WriteFile(path, []byte(ddl+"ALTER TABLE events DROP COLUMN missing;"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := chgen.ParseSchemaCatalogs([]string{path}); err == nil || !strings.Contains(err.Error(), path+":6:") {
		t.Fatalf("later ALTER lost its original location: %v", err)
	}
}

func TestSchemaCatalogsIgnoreTableSettingsWithoutChangingModeledMetadata(t *testing.T) {
	const ddl = `CREATE TABLE events (id UInt64, value String)
ENGINE=ReplacingMergeTree(value) ORDER BY id;
ALTER TABLE events MODIFY SETTING max_parts_to_merge_at_once = 4;
ALTER TABLE events RESET SETTING max_parts_to_merge_at_once;`
	config, _ := writeCheckProject(t, ddl, "-- name: Read :many\nSELECT id FROM events;")
	path := filepath.Join(filepath.Dir(config), "schema.sql")
	catalogs, err := chgen.ParseSchemaCatalogs([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	want := chgen.Table{
		Name: "events", File: path, Line: 1,
		Columns: map[string]chgen.Column{
			"id":    {Name: "id", Type: chgen.CHType{Name: "UInt64"}, Insertable: true},
			"value": {Name: "value", Type: chgen.CHType{Name: "String"}, Insertable: true},
		},
		ColumnOrder: []string{"id", "value"},
		Engine:      &chgen.TableEngine{Name: "ReplacingMergeTree", Params: []string{"value"}, OrderBy: []string{"id"}},
	}
	if got := catalogs.Physical.Tables["events"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("settings changed modeled catalog metadata:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestSchemaCatalogsApplyColumnChangesBesideIgnoredTableSettings(t *testing.T) {
	const ddl = `CREATE TABLE events (id UInt64, label String, obsolete String)
ENGINE=ReplacingMergeTree(id) ORDER BY id;
ALTER TABLE events
	ADD COLUMN created_at DateTime,
	MODIFY COLUMN label Nullable(String),
	DROP COLUMN obsolete,
	MODIFY SETTING max_threads = 4;
ALTER TABLE events RESET SETTING max_threads;`
	config, _ := writeCheckProject(t, ddl, "-- name: Read :many\nSELECT id, label, created_at FROM events;")
	catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(filepath.Dir(config), "schema.sql")})
	if err != nil {
		t.Fatal(err)
	}
	table := catalogs.Physical.Tables["events"]
	if !reflect.DeepEqual(table.ColumnOrder, []string{"id", "label", "created_at"}) ||
		table.Columns["label"].Type.String() != "Nullable(String)" ||
		table.Columns["created_at"].Type.Name != "DateTime" {
		t.Fatalf("column operations were lost beside settings: %+v", table)
	}
	if table.Engine == nil || table.Engine.Name != "ReplacingMergeTree" ||
		!reflect.DeepEqual(table.Engine.Params, []string{"id"}) ||
		!reflect.DeepEqual(table.Engine.OrderBy, []string{"id"}) {
		t.Fatalf("settings or column operations changed engine metadata: %+v", table.Engine)
	}
}

func TestSchemaSettingsCLIGeneratesFromTheWholeMigration(t *testing.T) {
	const ddl = `CREATE TABLE events (id UInt64) ENGINE=Memory;
ALTER TABLE events MODIFY SETTING max_threads = 4;
ALTER TABLE events RESET SETTING max_threads;
ALTER TABLE events ADD COLUMN created_at DateTime;`
	config, output := writeCheckProject(t, ddl, "-- name: Read :many\nSELECT id, created_at FROM events;")
	cli := buildPublicCLI(t)
	cmd := exec.CommandContext(t.Context(), cli, "-f", config)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v\n%s", err, out)
	}
	generated, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "CreatedAt time.Time") {
		t.Fatalf("generated output missed the column after settings: %s", generated)
	}
}

func TestSystemCatalogReadBoundaries(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT name FROM system.tables", ""},
		{"SELECT name FROM system.columns", ""},
		{"SELECT name FROM system.functions", ""},
		{"SELECT active FROM system.parts", ""},
		{"SELECT p.rows FROM system.parts AS p", ""},
		{"SELECT parts.rows FROM system.parts", ""},
		{"SELECT name FROM system.no_such_table", "system.no_such_table"},
		{"SELECT no_such_column FROM system.parts", "no_such_column"},
		{"SELECT name FROM other_db.parts", "qualified reference other_db.parts is not allowed"},
		{"SELECT database FROM parts", "database"},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			config, _ := writeCheckProject(t, "CREATE TABLE parts (name String) ENGINE=MergeTree ORDER BY name;", "-- name: Read :many\n"+tc.sql+";")
			dir := filepath.Dir(config)
			catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(dir, "schema.sql")})
			if err != nil {
				t.Fatal(err)
			}
			queries, err := chgen.ParseQueryFiles([]string{filepath.Join(dir, "queries.sql")}, catalogs)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %q, got %v", tc.want, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := chgen.Generate("querygen", queries); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSystemCatalogCannotBeChangedByMigrations(t *testing.T) {
	for _, ddl := range []string{
		"CREATE TABLE system.parts (id UInt64) ENGINE=MergeTree ORDER BY id;",
		"ALTER TABLE system.parts ADD COLUMN id UInt64;",
		"DROP TABLE system.parts;",
	} {
		t.Run(ddl, func(t *testing.T) {
			config, _ := writeCheckProject(t, ddl, "-- name: Read :one\nSELECT 1;")
			_, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(filepath.Dir(config), "schema.sql")})
			if err == nil || !strings.Contains(err.Error(), "system tables are read-only") {
				t.Fatalf("expected read-only diagnostic, got %v", err)
			}
		})
	}
}

func TestSystemCatalogRejectsExecWrites(t *testing.T) {
	config, _ := writeCheckProject(t, "CREATE TABLE parts (name String) ENGINE=MergeTree ORDER BY name;", "-- name: Remove :exec\nDELETE FROM system.parts WHERE name = 'x';")
	dir := filepath.Dir(config)
	catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(dir, "schema.sql")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = chgen.ParseQueryFiles([]string{filepath.Join(dir, "queries.sql")}, catalogs)
	if err == nil || !strings.Contains(err.Error(), "system tables are read-only") {
		t.Fatalf("want read-only diagnostic, got %v", err)
	}
}

func TestSystemPartsSignatureGeneratesWithoutAnApplicationDeclaration(t *testing.T) {
	catalogs, err := chgen.ParseSchemaCatalogs([]string{"testdata/golden/system_parts/schema.sql"})
	if err != nil {
		t.Fatal(err)
	}
	queries, err := chgen.ParseQueryFiles([]string{"testdata/golden/system_parts/queries.sql"}, catalogs)
	if err != nil {
		t.Fatal(err)
	}
	q := queries[0]
	if len(q.Params) != 1 || q.Params[0].GoName != "Table" || q.Params[0].GoType != "string" ||
		len(q.Results) != 1 || q.Results[0].GoType != "uint64" {
		t.Fatalf("system.parts query contract: %+v", q)
	}
	if _, err := chgen.Generate("querygen", queries); err != nil {
		t.Fatal(err)
	}
}

func TestCreateAsExchangeRenameKeepsFactDefinition(t *testing.T) {
	const ddl = `CREATE TABLE fact_ci_spans_v3 (
		fact_id String, repository_key String, span_type String, span_id String,
		event_date Date, projected_at DateTime64(3, 'UTC')
	) ENGINE=ReplacingMergeTree(projected_at) PARTITION BY toYYYYMM(event_date)
	ORDER BY (repository_key, span_type, fact_id);
	CREATE TABLE fact_ci_spans_v3_sort_key AS fact_ci_spans_v3
	ENGINE=ReplacingMergeTree(projected_at) PARTITION BY toYYYYMM(event_date)
	ORDER BY (repository_key, span_type, span_id, fact_id);
	CREATE MATERIALIZED VIEW mv TO fact_ci_spans_v3_sort_key AS SELECT * FROM fact_ci_spans_v3;
	INSERT INTO fact_ci_spans_v3_sort_key SELECT * FROM fact_ci_spans_v3;
	EXCHANGE TABLES fact_ci_spans_v3 AND fact_ci_spans_v3_sort_key;
	RENAME TABLE fact_ci_spans_v3_sort_key TO fact_ci_spans_v3_old;`
	config, _ := writeCheckProject(t, ddl, "-- name: Fingerprint :many\nSELECT fact_id FROM fact_ci_spans_v3;")
	dir := filepath.Dir(config)
	catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(dir, "schema.sql")})
	if err != nil {
		t.Fatal(err)
	}
	queries, err := chgen.ParseQueryFiles([]string{filepath.Join(dir, "queries.sql")}, catalogs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chgen.Generate("querygen", queries); err != nil {
		t.Fatal(err)
	}
	current := catalogs.Physical.Tables["fact_ci_spans_v3"]
	old := catalogs.Physical.Tables["fact_ci_spans_v3_old"]
	if !reflect.DeepEqual(current.Engine.OrderBy, []string{"repository_key", "span_type", "span_id", "fact_id"}) ||
		!reflect.DeepEqual(old.Engine.OrderBy, []string{"repository_key", "span_type", "fact_id"}) ||
		len(current.Columns) != 6 || !reflect.DeepEqual(current.Columns, old.Columns) {
		t.Fatalf("swap lost definitions: current=%+v old=%+v", current, old)
	}
}

func TestCreateAsCopiesColumnAndStorageMetadata(t *testing.T) {
	const ddl = `CREATE TABLE source (
		id UInt64, event_date Date, version UInt64,
		x UInt64 DEFAULT id + 1, m UInt64 MATERIALIZED id + 2,
		a UInt64 ALIAS id + 3, label String COMMENT 'hello' CODEC(ZSTD)
	) ENGINE=ReplacingMergeTree(version) PARTITION BY toYYYYMM(event_date)
	ORDER BY (id, event_date) PRIMARY KEY id SAMPLE BY id
	TTL event_date + INTERVAL 1 MONTH SETTINGS index_granularity=4096;
	CREATE TABLE copied AS source;
	CREATE TABLE changed AS source ENGINE=ReplacingMergeTree(version) ORDER BY (id, event_date, version);
	CREATE TABLE memory_copy AS source ENGINE=Memory;`
	config, _ := writeCheckProject(t, ddl, "-- name: Read :many\nSELECT id FROM copied;")
	catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(filepath.Dir(config), "schema.sql")})
	if err != nil {
		t.Fatal(err)
	}
	source := catalogs.Physical.Tables["source"]
	copied := catalogs.Physical.Tables["copied"]
	if !reflect.DeepEqual(source.Columns, copied.Columns) || !reflect.DeepEqual(source.Engine, copied.Engine) {
		t.Fatalf("plain CREATE AS did not copy the definition: source=%+v copy=%+v", source, copied)
	}
	if copied.Columns["x"].DefaultExpr != "id + 1" ||
		copied.Columns["m"].MaterializedExpr != "id + 2" || copied.Columns["m"].Insertable ||
		copied.Columns["a"].AliasExpr != "id + 3" || copied.Columns["a"].Insertable ||
		copied.Columns["label"].Comment != "hello" || copied.Columns["label"].Codec != "CODEC(ZSTD)" {
		t.Fatalf("column modifiers were lost: %+v", copied.Columns)
	}
	engine := copied.Engine
	if engine.PartitionBy != "toYYYYMM(event_date)" || engine.PrimaryKey != "id" || engine.SampleBy != "id" || engine.TTL == "" || engine.Settings["index_granularity"] != "4096" {
		t.Fatalf("storage clauses were lost: %+v", engine)
	}
	changed := catalogs.Physical.Tables["changed"].Engine
	if changed.PartitionBy != engine.PartitionBy || changed.PrimaryKey != engine.PrimaryKey || changed.SampleBy != engine.SampleBy || changed.TTL != "" || len(changed.Settings) != 0 {
		t.Fatalf("new ENGINE inheritance differs from ClickHouse 25.8: %+v", changed)
	}
	if got := catalogs.Physical.Tables["memory_copy"].Engine; got.Name != "Memory" || len(got.OrderBy) != 0 || got.PartitionBy != "" {
		t.Fatalf("Memory inherited MergeTree clauses: %+v", got)
	}
}

func TestCreateAsCopiesCurrentDefinitionAndIsIndependent(t *testing.T) {
	const ddl = `CREATE TABLE source (id UInt64, label String DEFAULT 'old', occurred_at Date)
	ENGINE=MergeTree ORDER BY id TTL occurred_at + INTERVAL 1 MONTH
	SETTINGS index_granularity=4096, max_parts_to_merge_at_once=4;
	ALTER TABLE source REMOVE TTL;
	ALTER TABLE source MODIFY SETTING index_granularity=2048;
	ALTER TABLE source RESET SETTING max_parts_to_merge_at_once;
	CREATE TABLE copied AS source;
	ALTER TABLE copied MODIFY COLUMN label String DEFAULT 'new';
	ALTER TABLE copied ADD COLUMN extra UInt8;
	ALTER TABLE copied MODIFY SETTING index_granularity=1024;`
	config, _ := writeCheckProject(t, ddl, "-- name: Read :many\nSELECT * FROM copied;")
	catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(filepath.Dir(config), "schema.sql")})
	if err != nil {
		t.Fatal(err)
	}
	source, copied := catalogs.Physical.Tables["source"], catalogs.Physical.Tables["copied"]
	if source.Engine.TTL != "" || copied.Engine.TTL != "" ||
		source.Engine.Settings["index_granularity"] != "2048" || copied.Engine.Settings["index_granularity"] != "1024" ||
		len(source.Engine.Settings) != 1 || len(copied.Engine.Settings) != 1 ||
		len(source.Columns) != 3 || len(copied.Columns) != 4 ||
		source.Columns["label"].DefaultExpr != "'old'" || copied.Columns["label"].DefaultExpr != "'new'" {
		t.Fatalf("CREATE AS copied stale or shared metadata: source=%+v copy=%+v", source, copied)
	}
}

func TestCreateAsRejectsMissingOrQualifiedSources(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"CREATE TABLE copied AS missing_table;", `unknown source table "missing_table"`},
		{"CREATE TABLE copied AS db.source;", "qualified reference db.source is not allowed; the database comes from the connection; use the unqualified name source"},
		{"-- chgen:external\nCREATE TABLE ext (id UInt64);\nCREATE TABLE copied AS ext;", `unknown source table "ext"`},
		{"-- chgen:external\nCREATE TABLE ext AS source;", "external schema must declare an explicit column list"},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			config, _ := writeCheckProject(t, "CREATE TABLE source (id UInt64) ENGINE=Memory;\n"+tc.sql, "-- name: Read :many\nSELECT id FROM source;")
			path := filepath.Join(filepath.Dir(config), "schema.sql")
			_, err := chgen.ParseSchemaCatalogs([]string{path})
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), path+":") {
				t.Fatalf("source diagnostic = %v, want %q with file location", err, tc.want)
			}
		})
	}
}

func TestCreateAsIfNotExistsPreservesExistingDefinition(t *testing.T) {
	const ddl = `CREATE TABLE existing (id UInt64) ENGINE=MergeTree ORDER BY id;
	CREATE TABLE source (other String) ENGINE=Memory;
	CREATE TABLE IF NOT EXISTS existing AS source;
	CREATE TABLE IF NOT EXISTS existing AS missing_table;`
	config, _ := writeCheckProject(t, ddl, "-- name: Read :many\nSELECT id FROM existing;")
	catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(filepath.Dir(config), "schema.sql")})
	if err != nil {
		t.Fatal(err)
	}
	table := catalogs.Physical.Tables["existing"]
	if len(table.Columns) != 1 || table.Columns["id"].Type.Name != "UInt64" || table.Line != 1 || table.Engine.Name != "MergeTree" {
		t.Fatalf("IF NOT EXISTS replaced its target: %+v", table)
	}
}

func TestCreateAsRetainsModifiersAfterColumnRetype(t *testing.T) {
	const ddl = `CREATE TABLE source (id UInt64,
		computed UInt64 MATERIALIZED id + 1,
		label String DEFAULT 'unknown' COMMENT 'label' CODEC(ZSTD)
	) ENGINE=Memory;
	ALTER TABLE source MODIFY COLUMN computed UInt32;
	ALTER TABLE source MODIFY COLUMN label LowCardinality(String);
	CREATE TABLE copied AS source;`
	config, _ := writeCheckProject(t, ddl, "-- name: Read :many\nSELECT label FROM copied;")
	catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(filepath.Dir(config), "schema.sql")})
	if err != nil {
		t.Fatal(err)
	}
	columns := catalogs.Physical.Tables["copied"].Columns
	if columns["computed"].Insertable || columns["computed"].MaterializedExpr != "id + 1" || columns["computed"].Type.Name != "UInt32" ||
		columns["label"].DefaultExpr != "'unknown'" || columns["label"].Comment != "label" || columns["label"].Codec != "CODEC(ZSTD)" {
		t.Fatalf("column retype discarded inherited modifiers: %+v", columns)
	}
}

func TestLightweightDeleteExecPublicContract(t *testing.T) {
	const ddl = `CREATE TABLE fact_job_metrics_v3 (
		repository_owner String,
		repository String,
		github_workflow_run_id UInt64,
		github_workflow_run_attempt UInt32,
		projected_at DateTime64(3, 'UTC')
	) ENGINE = ReplacingMergeTree ORDER BY (repository_owner, repository, github_workflow_run_id);`
	const sql = `-- name: DeleteStaleRunJobMetricFacts :exec
-- param: RunKeys []string
-- param: RefoldStartedAtUnixMilli int64
DELETE FROM fact_job_metrics_v3
WHERE has(chgen.arg('RunKeys'), concat(repository_owner, '/', repository, '#', toString(github_workflow_run_id), '.', toString(github_workflow_run_attempt)))
  AND toUnixTimestamp64Milli(projected_at) < chgen.arg('RefoldStartedAtUnixMilli')
SETTINGS lightweight_deletes_sync = 2;`
	config, _ := writeCheckProject(t, ddl, sql)
	directory := filepath.Dir(config)
	catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(directory, "schema.sql")})
	if err != nil {
		t.Fatal(err)
	}
	queries, err := chgen.ParseQueryFiles([]string{filepath.Join(directory, "queries.sql")}, catalogs)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 || len(queries[0].Params) != 2 ||
		queries[0].Params[0].GoName != "RunKeys" || queries[0].Params[0].GoType != "[]string" ||
		queries[0].Params[1].GoName != "RefoldStartedAtUnixMilli" || queries[0].Params[1].GoType != "int64" {
		t.Fatalf("lightweight delete parameters: %+v", queries)
	}
	generated, err := chgen.Generate("querygen", queries)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"DELETE FROM fact_job_metrics_v3",
		"WHERE has(?, concat(repository_owner",
		"SETTINGS lightweight_deletes_sync = 2",
		"DeleteStaleRunJobMetricFacts(ctx context.Context, arg DeleteStaleRunJobMetricFactsParams) error",
	} {
		if !strings.Contains(string(generated), want) {
			t.Errorf("generated lightweight delete is missing %q:\n%s", want, generated)
		}
	}
}

func TestLightweightDeleteRejectsUnknownTargetOrPredicateColumn(t *testing.T) {
	const ddl = `CREATE TABLE facts (id UInt64, repository String) ENGINE = MergeTree ORDER BY id;`
	for _, test := range []struct {
		name  string
		param string
		query string
		want  string
	}{
		{
			name:  "target",
			param: "-- param: ID uint64\n",
			query: "DELETE FROM missing WHERE id = chgen.arg('ID')",
			want:  `table "missing" is not present`,
		},
		{
			name:  "nested predicate column",
			param: "-- param: Keys []string\n",
			query: "DELETE FROM facts WHERE has(chgen.arg('Keys'), concat(repository, missing))",
			want:  `column "missing"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, _ := writeCheckProject(t, ddl, "-- name: Delete :exec\n"+test.param+test.query)
			directory := filepath.Dir(config)
			catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(directory, "schema.sql")})
			if err != nil {
				t.Fatal(err)
			}
			_, err = chgen.ParseQueryFiles([]string{filepath.Join(directory, "queries.sql")}, catalogs)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse query error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLightweightDeleteExecAcceptsPositionalParameters(t *testing.T) {
	const ddl = `CREATE TABLE events (id UInt64, code String) ENGINE = MergeTree ORDER BY id;`
	for _, test := range []struct {
		name   string
		source string
		param  chgen.Param
	}{
		{
			name:   "inferred scalar",
			source: "-- name: Delete :exec\nDELETE FROM events WHERE id = ?",
			param:  chgen.Param{GoName: "ID", GoType: "uint64"},
		},
		{
			name:   "annotated collection",
			source: "-- name: Delete :exec\n-- param: Keys []string\nDELETE FROM events WHERE has(?, code)",
			param:  chgen.Param{GoName: "Keys", GoType: "[]string"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, _ := writeCheckProject(t, ddl, test.source)
			directory := filepath.Dir(config)
			catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(directory, "schema.sql")})
			if err != nil {
				t.Fatal(err)
			}
			queries, err := chgen.ParseQueryFiles([]string{filepath.Join(directory, "queries.sql")}, catalogs)
			if err != nil {
				t.Fatal(err)
			}
			if len(queries) != 1 || len(queries[0].Params) != 1 ||
				queries[0].Params[0].GoName != test.param.GoName ||
				queries[0].Params[0].GoType != test.param.GoType {
				t.Fatalf("positional parameter: %+v, want %+v", queries, test.param)
			}
		})
	}
}

func TestLightweightDeleteDoesNotLoseSettingsPlaceholders(t *testing.T) {
	config, _ := writeCheckProject(t,
		"CREATE TABLE events (id UInt64) ENGINE = MergeTree ORDER BY id;",
		"-- name: Delete :exec\nDELETE FROM events WHERE id = ? SETTINGS lightweight_deletes_sync = ?;",
	)
	directory := filepath.Dir(config)
	catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(directory, "schema.sql")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = chgen.ParseQueryFiles([]string{filepath.Join(directory, "queries.sql")}, catalogs)
	if err == nil || !strings.Contains(err.Error(), "2 positional placeholders") {
		t.Fatalf("SETTINGS placeholder was silently omitted: %v", err)
	}
}

func TestUncheckedExecSupportsNewClickHouseCommandsAndReportsTrust(t *testing.T) {
	config, _ := writeCheckProject(t,
		"CREATE TABLE events (id UInt64) ENGINE = Memory;",
		"-- name: WaitRefresh :exec\n-- chgen:unchecked-exec\nSYSTEM WAIT VIEW refresh_view;",
	)
	directory := filepath.Dir(config)
	catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(directory, "schema.sql")})
	if err != nil {
		t.Fatal(err)
	}
	queries, err := chgen.ParseQueryFiles([]string{filepath.Join(directory, "queries.sql")}, catalogs)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := chgen.Generate("querygen", queries)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "SYSTEM WAIT VIEW refresh_view;") ||
		!strings.Contains(string(generated), "WaitRefresh(ctx context.Context, arg WaitRefreshParams) error") {
		t.Fatalf("unchecked command was not generated as :exec:\n%s", generated)
	}
	report, err := chgen.Check(config)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "unknown" || len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != "exec-unchecked" {
		t.Fatalf("unchecked execution is invisible to check: %+v", report)
	}
}

func TestUncheckedExecBindsExplicitParameters(t *testing.T) {
	config, _ := writeCheckProject(t,
		"CREATE TABLE events (id UInt64) ENGINE = MergeTree ORDER BY id;",
		"-- name: DeleteKnown :exec\n-- chgen:unchecked-exec\n-- param: ID uint64\nDELETE FROM events WHERE id = chgen.arg('ID') OR id = chgen.arg('ID');",
	)
	directory := filepath.Dir(config)
	catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(directory, "schema.sql")})
	if err != nil {
		t.Fatal(err)
	}
	queries, err := chgen.ParseQueryFiles([]string{filepath.Join(directory, "queries.sql")}, catalogs)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 || len(queries[0].Params) != 1 || queries[0].Params[0].GoType != "uint64" ||
		!reflect.DeepEqual(queries[0].ParamIndexes, []int{0, 0}) {
		t.Fatalf("unchecked parameter mapping: %+v", queries)
	}
	generated, err := chgen.Generate("querygen", queries)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "id = ? OR id = ?") ||
		!strings.Contains(string(generated), "arg.ID, arg.ID") {
		t.Fatalf("unchecked SQL lost its parameter binding:\n%s", generated)
	}
}

func TestUncheckedExecRejectsAmbiguousOrMultipleStatements(t *testing.T) {
	const ddl = "CREATE TABLE events (id UInt64) ENGINE = MergeTree ORDER BY id;"
	for _, test := range []struct {
		name string
		sql  string
		want string
	}{
		{"multiple statements", "-- name: Delete :exec\n-- chgen:unchecked-exec\nDELETE FROM events WHERE id = 1; DROP TABLE events", "exactly one SQL statement"},
		{"untyped parameter", "-- name: Delete :exec\n-- chgen:unchecked-exec\nDELETE FROM events WHERE id = chgen.arg('ID')", "no inferable ClickHouse type"},
		{"mixed markers", "-- name: Delete :exec\n-- chgen:unchecked-exec\n-- param: ID uint64\nDELETE FROM events WHERE id = chgen.arg('ID') OR id = ?", "cannot mix"},
		{"read command", "-- name: Read :many\n-- chgen:unchecked-exec\nSELECT id FROM events", "valid once in a :exec query header"},
		{"duplicate directive", "-- name: Delete :exec\n-- chgen:unchecked-exec\n-- chgen:unchecked-exec\nDELETE FROM events WHERE id = 1", "valid once in a :exec query header"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, _ := writeCheckProject(t, ddl, test.sql)
			directory := filepath.Dir(config)
			catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(directory, "schema.sql")})
			if err != nil {
				t.Fatal(err)
			}
			_, err = chgen.ParseQueryFiles([]string{filepath.Join(directory, "queries.sql")}, catalogs)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSchemaCatalogsRemoveTTLStillValidatesTheWholeAlter(t *testing.T) {
	for _, ddl := range []string{
		"ALTER TABLE missing REMOVE TTL;",
		"-- chgen:external\nCREATE TABLE requested (id UInt64);\nALTER TABLE requested REMOVE TTL;",
		"CREATE TABLE events (id UInt64) ENGINE=Memory;\nALTER TABLE events REMOVE TTL, DROP COLUMN missing;",
		"CREATE TABLE events (id UInt64) ENGINE=Memory;\nALTER TABLE events REMOVE TTL, RENAME COLUMN id TO other;",
		"CREATE TABLE events (id UInt64) ENGINE=Memory;\nALTER TABLE events REMOVE;",
		"CREATE TABLE events (id UInt64) ENGINE=Memory;\nALTER TABLE events REMOVE TTL garbage;",
	} {
		t.Run(ddl, func(t *testing.T) {
			config, _ := writeCheckProject(t, ddl, "-- name: Read :many\nSELECT id FROM events;")
			path := filepath.Join(filepath.Dir(config), "schema.sql")
			if catalogs, err := chgen.ParseSchemaCatalogs([]string{path}); err == nil || catalogs != nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("REMOVE TTL hid an invalid ALTER: %+v, %v", catalogs, err)
			}
		})
	}
}

func TestSchemaWaitViewCLIUsesTheWholeMigration(t *testing.T) {
	cli := buildPublicCLI(t)
	config, output := writeCheckProject(t, `CREATE TABLE events (id UInt64) ENGINE=Memory;
SYSTEM WAIT VIEW "analytics"."refresh;view";
ALTER TABLE events ADD COLUMN label String;`, "-- name: Read :many\nSELECT id, label FROM events;")
	cmd := exec.CommandContext(t.Context(), cli, "-f", config)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v\n%s", err, out)
	}
	generated, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "Label string") || !strings.Contains(string(generated), "uint64") {
		t.Fatalf("generated result lost a column: %s", generated)
	}
	path := filepath.Join(filepath.Dir(config), "schema.sql")
	if err := os.WriteFile(path, []byte("SYSTEM WAIT VIEW refresh;\nALTER TABLE events ADD COLUMN;"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.CommandContext(t.Context(), cli, "-f", config)
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "schema.sql") {
		t.Fatalf("invalid migration generated successfully or lost its location: %v\n%s", err, out)
	}
	if after, err := os.ReadFile(output); err != nil || string(after) != string(generated) {
		t.Fatalf("failed generation overwrote the last valid output: %v", err)
	}
}

func TestSchemaCatalogsRemoveTTLWithoutLosingColumnsOrEngine(t *testing.T) {
	const ddl = `CREATE TABLE events (id UInt64, occurred_at DateTime)
ENGINE=ReplacingMergeTree(id) ORDER BY (id, occurred_at)
TTL occurred_at + INTERVAL 1 DAY;
ALTER TABLE events REMOVE TTL, ADD COLUMN label String;
SYSTEM WAIT VIEW refresh;
ALTER TABLE events MODIFY COLUMN label Nullable(String);`
	config, output := writeCheckProject(t, ddl, "-- name: Read :many\nSELECT id, label FROM events;")
	catalogs, err := chgen.ParseSchemaCatalogs([]string{filepath.Join(filepath.Dir(config), "schema.sql")})
	if err != nil {
		t.Fatal(err)
	}
	table := catalogs.Physical.Tables["events"]
	wantEngine := &chgen.TableEngine{Name: "ReplacingMergeTree", Params: []string{"id"}, OrderBy: []string{"id", "occurred_at"}}
	if !reflect.DeepEqual(table.Engine, wantEngine) ||
		!reflect.DeepEqual(table.ColumnOrder, []string{"id", "occurred_at", "label"}) ||
		table.Columns["label"].Type.Name != "Nullable" || table.Columns["label"].Type.Params[0].Name != "String" {
		t.Fatalf("REMOVE TTL hid modeled metadata or later ALTER clauses: %+v", table)
	}
	cli := buildPublicCLI(t)
	cmd := exec.CommandContext(t.Context(), cli, "-f", config)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v\n%s", err, out)
	}
	if generated, err := os.ReadFile(output); err != nil || !strings.Contains(string(generated), "Label *string") {
		t.Fatalf("generated result lost the post-TTL column type: %v\n%s", err, generated)
	}
}
