package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseSchemaFilesAppliesAddColumnMigration(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.sql")
	delta := filepath.Join(dir, "delta.sql")
	if err := os.WriteFile(base, []byte("CREATE TABLE events (id UInt64);"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(delta, []byte("ALTER TABLE events ADD COLUMN IF NOT EXISTS payload Nullable(String);"), 0o600); err != nil {
		t.Fatal(err)
	}

	schema, err := schemasFromFilesErr(t, []string{base, delta})
	if err != nil {
		t.Fatalf("schemasFromFilesErr() error = %v", err)
	}
	if _, ok := schema.Tables["events"].Columns["payload"]; !ok {
		t.Fatal("schema is missing the ADD COLUMN migration")
	}
}

func TestParseSchemaFilesPreservesAddColumnAfterOrder(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.sql")
	delta := filepath.Join(dir, "delta.sql")
	if err := os.WriteFile(base, []byte("CREATE TABLE events (id UInt64, name String, created_at DateTime);"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(delta, []byte("ALTER TABLE events ADD COLUMN repository_key String AFTER name;"), 0o600); err != nil {
		t.Fatal(err)
	}

	schema, err := schemasFromFilesErr(t, []string{base, delta})
	if err != nil {
		t.Fatalf("schemasFromFilesErr() error = %v", err)
	}
	if got, want := schema.Tables["events"].ColumnOrder, []string{"id", "name", "repository_key", "created_at"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("column order = %v, want %v", got, want)
	}
}

func TestParseSchemaFilesAppliesModifyColumnMigration(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.sql")
	delta := filepath.Join(dir, "delta.sql")
	if err := os.WriteFile(base, []byte("CREATE TABLE rollups (id UInt64, duration_p50_seconds Float64);"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(delta, []byte("ALTER TABLE rollups MODIFY COLUMN duration_p50_seconds AggregateFunction(quantile(0.50), Float64);"), 0o600); err != nil {
		t.Fatal(err)
	}

	schema, err := schemasFromFilesErr(t, []string{base, delta})
	if err != nil {
		t.Fatalf("schemasFromFilesErr() error = %v", err)
	}
	column, ok := schema.Tables["rollups"].Columns["duration_p50_seconds"]
	if !ok {
		t.Fatal("schema is missing duration_p50_seconds after MODIFY COLUMN")
	}
	if got, want := column.Type.String(), "AggregateFunction(quantile(0.50), Float64)"; got != want {
		t.Fatalf("column type = %q, want %q", got, want)
	}
	if got, want := schema.Tables["rollups"].ColumnOrder, []string{"id", "duration_p50_seconds"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("MODIFY COLUMN changed column order: got %v, want %v", got, want)
	}
}

func TestParseSchemaFilesRejectsModifyColumnOnUnknownColumn(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.sql")
	delta := filepath.Join(dir, "delta.sql")
	if err := os.WriteFile(base, []byte("CREATE TABLE rollups (id UInt64);"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(delta, []byte("ALTER TABLE rollups MODIFY COLUMN missing_col Float64;"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := schemasFromFilesErr(t, []string{base, delta}); err == nil {
		t.Fatal("schemasFromFilesErr() accepted MODIFY COLUMN on an unknown column")
	}
}

func TestParseSchemaFilesAppliesDropColumnMigration(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.sql")
	delta := filepath.Join(dir, "delta.sql")
	if err := os.WriteFile(base, []byte("CREATE TABLE rollups (id UInt64, name String, duration_p50_seconds Float64);"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(delta, []byte("ALTER TABLE rollups DROP COLUMN duration_p50_seconds;"), 0o600); err != nil {
		t.Fatal(err)
	}

	schema, err := schemasFromFilesErr(t, []string{base, delta})
	if err != nil {
		t.Fatalf("schemasFromFilesErr() error = %v", err)
	}
	if _, ok := schema.Tables["rollups"].Columns["duration_p50_seconds"]; ok {
		t.Fatal("schema still has duration_p50_seconds after DROP COLUMN")
	}
	if got, want := schema.Tables["rollups"].ColumnOrder, []string{"id", "name"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("column order = %v, want %v", got, want)
	}
}

func TestParseSchemaFilesRejectsDropColumnOnUnknownColumn(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.sql")
	delta := filepath.Join(dir, "delta.sql")
	if err := os.WriteFile(base, []byte("CREATE TABLE rollups (id UInt64);"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(delta, []byte("ALTER TABLE rollups DROP COLUMN missing_col;"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := schemasFromFilesErr(t, []string{base, delta}); err == nil {
		t.Fatal("schemasFromFilesErr() accepted DROP COLUMN on an unknown column")
	}
}

// TestParseSchemaFilesAppliesDropAddColumnRetype mirrors the real 000027
// migration pattern: ClickHouse cannot MODIFY COLUMN a Float64 into an
// AggregateFunction state in place (CANNOT_CONVERT_TYPE), so a genuine type
// change is a DROP COLUMN followed by an ADD COLUMN of the same name.
func TestParseSchemaFilesAppliesDropAddColumnRetype(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.sql")
	delta := filepath.Join(dir, "delta.sql")
	if err := os.WriteFile(base, []byte("CREATE TABLE rollups (id UInt64, duration_sum_seconds Float64, duration_p50_seconds Float64, duration_max_seconds Float64);"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(delta, []byte(
		"ALTER TABLE rollups DROP COLUMN duration_p50_seconds;\n"+
			"ALTER TABLE rollups ADD COLUMN duration_p50_seconds AggregateFunction(quantile(0.50), Float64) AFTER duration_sum_seconds;",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	schema, err := schemasFromFilesErr(t, []string{base, delta})
	if err != nil {
		t.Fatalf("schemasFromFilesErr() error = %v", err)
	}
	column, ok := schema.Tables["rollups"].Columns["duration_p50_seconds"]
	if !ok {
		t.Fatal("schema is missing duration_p50_seconds after DROP+ADD retype")
	}
	if got, want := column.Type.String(), "AggregateFunction(quantile(0.50), Float64)"; got != want {
		t.Fatalf("column type = %q, want %q", got, want)
	}
	if got, want := schema.Tables["rollups"].ColumnOrder, []string{"id", "duration_sum_seconds", "duration_p50_seconds", "duration_max_seconds"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DROP+ADD retype changed column order unexpectedly: got %v, want %v", got, want)
	}
}

func TestParseSchemaKeepsEngineNameParamsAndSortKey(t *testing.T) {
	schema, err := schemaFromDDLErr(t,
		"CREATE TABLE facts (id UInt64, version DateTime64(3), part String) "+
			"ENGINE = ReplacingMergeTree(version) ORDER BY (part, id);",
	)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	engine := schema.Tables["facts"].Engine
	if engine == nil {
		t.Fatal("engine clause was dropped")
	}
	if got, want := engine.Name, "ReplacingMergeTree"; got != want {
		t.Fatalf("engine name = %q, want %q", got, want)
	}
	if got, want := engine.Params, []string{"version"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("engine params = %v, want %v", got, want)
	}
	if got, want := engine.OrderBy, []string{"part", "id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sort key = %v, want %v", got, want)
	}
}

// A sort key of several terms needs the parentheses. Measured on ClickHouse
// 25.8.29.51: ORDER BY (part, id) creates the table with the sorting key
// "part, id", while ORDER BY part, id is a syntax error, code 62, at the
// comma. An earlier version of this test claimed that the server takes both
// spellings and pinned the bare one, which the server never accepted. The
// front end took the bare form until v0.5.4 and refuses it from v0.5.5, thus
// the front end moved towards the server, not away from it.
func TestParseSchemaSortKeySpellingsAgree(t *testing.T) {
	parenthesised, err := schemaFromDDLErr(t,
		"CREATE TABLE facts (id UInt64, part String) ENGINE = MergeTree ORDER BY (part, id);",
	)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() parenthesised error = %v", err)
	}
	if got, want := parenthesised.Tables["facts"].Engine.OrderBy, []string{"part", "id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parenthesised sort key = %v, want %v", got, want)
	}
	if _, err := schemaFromDDLErr(t,
		"CREATE TABLE facts (id UInt64, part String) ENGINE = MergeTree ORDER BY part, id;",
	); err == nil {
		t.Fatal("a bare sort key of several terms was accepted, but the server refuses it")
	}
}

// An engine with no arguments must give an empty parameter list and not a list
// that holds one blank string, because a guard reads the first parameter as the
// version column and a blank string would read as a declared column.
func TestParseSchemaEngineWithoutParams(t *testing.T) {
	schema, err := schemaFromDDLErr(t,
		"CREATE TABLE facts (id UInt64) ENGINE = MergeTree ORDER BY id;",
	)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	engine := schema.Tables["facts"].Engine
	if engine == nil {
		t.Fatal("engine clause was dropped")
	}
	if len(engine.Params) != 0 {
		t.Fatalf("engine params = %v, want none", engine.Params)
	}
}

// The four v2 fact tables are the reason this metadata is kept. Their engine
// decides which rows collapse into one, thus a guard must be able to read the
// name, the version column and the sort key of each from the shipped DDL.
func TestParseSchemaFilesKeepsFactTableEngines(t *testing.T) {
	schema, err := schemasFromFilesErr(t, []string{moduleRootPath("testdata", "fact_schema.sql")})
	if err != nil {
		t.Fatalf("schemasFromFilesErr() error = %v", err)
	}
	for _, table := range []string{
		"fact_events_v1",
		"fact_sessions_v1",
	} {
		engine := schema.Tables[table].Engine
		if engine == nil {
			t.Errorf("%s: engine clause was dropped", table)
			continue
		}
		if got, want := engine.Name, "ReplacingMergeTree"; got != want {
			t.Errorf("%s: engine = %q, want %q", table, got, want)
		}
		if got, want := engine.Params, []string{"projected_at"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: version column = %v, want %v", table, got, want)
		}
		if len(engine.OrderBy) == 0 {
			t.Errorf("%s: sort key is empty", table)
			continue
		}
		if got, want := engine.OrderBy[len(engine.OrderBy)-1], "event_id"; got != want {
			t.Errorf("%s: sort key ends with %q, want %q", table, got, want)
		}
	}
}
