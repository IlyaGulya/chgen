package project

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/engine"
)

func writeProjectText(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func validProjectSQL(t *testing.T, dir, prefix string) {
	t.Helper()
	writeProjectText(t, dir, prefix+"/schema.sql", "CREATE TABLE values (id UInt64) ENGINE = Memory;\n")
	writeProjectText(t, dir, prefix+"/queries.sql", "-- name: ListValues :many\nSELECT id FROM values;\n")
}

func TestRunGeneratesEveryPackage(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(name, content string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("migrations/000001_init.up.sql", `
CREATE TABLE orders (order_id String, customer_id String) ENGINE = MergeTree ORDER BY order_id;
`)
	mustWrite("migrations/000001_init.down.sql", `DROP TABLE orders;`)
	mustWrite("querygen/external_tables.sql", `-- chgen:external
CREATE TABLE order_keys (order_id String);
`)
	mustWrite("querygen/queries.sql", `-- name: ListOrders :many
SELECT order_id, customer_id FROM orders
WHERE order_id IN (SELECT order_id FROM chgen.external(order_keys));
`)
	mustWrite("servinggen/queries.sql", `-- name: CountOrders :one
SELECT count() AS total FROM orders;
`)
	mustWrite("chgen.yaml", `
version: 1
packages:
  - name: querygen
    output: querygen/queries.sql.go
    queries: querygen/queries.sql
    schema:
      - migrations
      - querygen/external_tables.sql
  - name: servinggen
    output: servinggen/queries.sql.go
    queries: servinggen/queries.sql
    schema: migrations
`)
	if err := Run(filepath.Join(dir, "chgen.yaml")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "querygen/queries.sql.go"))
	if err != nil {
		t.Fatalf("read querygen output: %v", err)
	}
	if !strings.Contains(string(first), "package querygen") || !strings.Contains(string(first), "ListOrders") {
		t.Errorf("querygen output is wrong:\n%s", first)
	}
	if strings.Contains(string(first), "000001_init.down.sql") {
		t.Errorf("down migration must not be read")
	}
	second, err := os.ReadFile(filepath.Join(dir, "servinggen/queries.sql.go"))
	if err != nil {
		t.Fatalf("read servinggen output: %v", err)
	}
	if !strings.Contains(string(second), "package servinggen") {
		t.Errorf("servinggen output is wrong")
	}
}

func TestRunReadsIndexAndColumnChangesFromSchemaDirectory(t *testing.T) {
	dir := t.TempDir()
	writeProjectText(t, dir, "migrations/001.sql", "CREATE TABLE orders (order_id String) ENGINE = MergeTree ORDER BY order_id;\n")
	writeProjectText(t, dir, "migrations/002.sql", `
ALTER TABLE orders ADD COLUMN channel String,
    ADD INDEX idx_channel channel TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE orders MATERIALIZE INDEX idx_channel;
ALTER TABLE orders CLEAR INDEX idx_channel;
ALTER TABLE orders DROP INDEX idx_channel;
ALTER TABLE orders ADD COLUMN version UInt64;
`)
	writeProjectText(t, dir, "queries.sql", "-- name: ListOrders :many\nSELECT channel, version FROM orders;\n")
	writeProjectText(t, dir, "chgen.yaml", `
version: 1
packages:
  - name: querygen
    output: generated/queries.sql.go
    queries: queries.sql
    schema: migrations
`)
	if err := Run(filepath.Join(dir, "chgen.yaml")); err != nil {
		t.Fatalf("generate with index migration in schema directory: %v", err)
	}
	generated, err := os.ReadFile(filepath.Join(dir, "generated/queries.sql.go"))
	if err != nil {
		t.Fatal(err)
	}
	// Both fields originate in the same file as the index operations. Hiding
	// that migration or skipping it wholesale must not satisfy this test.
	fields := strings.Fields(string(generated))
	normalized := strings.Join(fields, " ")
	if !strings.Contains(normalized, "Channel string") || !strings.Contains(normalized, "Version uint64") {
		t.Fatalf("column changes in index migration missing from generated code:\n%s", generated)
	}
}

func TestRunReadsRenameFromSchemaDirectory(t *testing.T) {
	dir := t.TempDir()
	writeProjectText(t, dir, "migrations/001.sql", "CREATE TABLE serving (id UInt64) ENGINE = MergeTree ORDER BY id;\n")
	writeProjectText(t, dir, "migrations/002.sql", `
CREATE TABLE staged (id UInt64, workflow_path String) ENGINE = MergeTree ORDER BY (workflow_path, id);
INSERT INTO staged SELECT id, '' FROM serving;
RENAME TABLE serving TO archived, staged TO serving;
ALTER TABLE serving ADD COLUMN label String;
DROP TABLE archived;
`)
	writeProjectText(t, dir, "queries.sql", "-- name: Read :many\nSELECT id, workflow_path, label FROM serving;\n")
	writeProjectText(t, dir, "chgen.yaml", "version: 1\npackages:\n  - name: querygen\n    output: generated/queries.sql.go\n    queries: queries.sql\n    schema: migrations\n")
	if err := Run(filepath.Join(dir, "chgen.yaml")); err != nil {
		t.Fatal(err)
	}
	generated, err := os.ReadFile(filepath.Join(dir, "generated/queries.sql.go"))
	if err != nil {
		t.Fatal(err)
	}
	if code := strings.Join(strings.Fields(string(generated)), " "); !strings.Contains(code, "WorkflowPath string") || !strings.Contains(code, "Label string") {
		t.Fatalf("renamed definition missing from generated code:\n%s", generated)
	}
}

func TestRunReadsExchangeFromSchemaDirectory(t *testing.T) {
	dir := t.TempDir()
	writeProjectText(t, dir, "migrations/001.sql", "CREATE TABLE serving (id UInt64) ENGINE=MergeTree ORDER BY id;")
	writeProjectText(t, dir, "migrations/002.sql", "CREATE TABLE staged (id String, workflow_path String) ENGINE=MergeTree ORDER BY (workflow_path, id); EXCHANGE TABLES serving AND staged; DROP TABLE staged; ALTER TABLE serving ADD COLUMN label String;")
	writeProjectText(t, dir, "queries.sql", "-- name: Read :many\nSELECT id, workflow_path, label FROM serving;")
	writeProjectText(t, dir, "chgen.yaml", "version: 1\npackages:\n  - name: querygen\n    output: generated/queries.sql.go\n    queries: queries.sql\n    schema: migrations\n")
	if err := Run(filepath.Join(dir, "chgen.yaml")); err != nil {
		t.Fatal(err)
	}
	generated, err := os.ReadFile(filepath.Join(dir, "generated/queries.sql.go"))
	if err != nil {
		t.Fatal(err)
	}
	code := strings.Join(strings.Fields(string(generated)), " ")
	for _, field := range []string{"ID string", "WorkflowPath string", "Label string"} {
		if !strings.Contains(code, field) {
			t.Fatalf("missing exchanged field %s: %s", field, code)
		}
	}
}

func TestRunWritesAnAbsoluteOutput(t *testing.T) {
	configDir := t.TempDir()
	output := filepath.Join(t.TempDir(), "generated", "queries.sql.go")
	if err := os.WriteFile(filepath.Join(configDir, "schema.sql"), []byte("CREATE TABLE values (id UInt64) ENGINE = Memory;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "queries.sql"), []byte("-- name: ListValues :many\nSELECT id FROM values;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := "version: 1\npackages:\n  - name: querygen\n    output: " + output + "\n    queries: queries.sql\n    schema: schema.sql\n"
	configPath := filepath.Join(configDir, "chgen.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(configPath); err != nil {
		t.Fatal(err)
	}
	generated, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read absolute output: %v", err)
	}
	if !strings.Contains(string(generated), "package querygen") || !strings.Contains(string(generated), "ListValues") {
		t.Fatalf("absolute output has unexpected content:\n%s", generated)
	}
	mutated := filepath.Join(configDir, output)
	if _, err := os.Stat(mutated); !os.IsNotExist(err) {
		t.Fatalf("the old join mutation wrote %q", mutated)
	}
}

func TestRunLateGenerationFailureChangesNoFile(t *testing.T) {
	dir := t.TempDir()
	validProjectSQL(t, dir, "first")
	writeProjectText(t, dir, "second/schema.sql", "CREATE TABLE values (id UInt64) ENGINE = Memory;\n")
	writeProjectText(t, dir, "second/queries.sql", "-- name: Broken :many\nSELECT FROM values;\n")
	writeProjectText(t, dir, "first/generated.go", "first sentinel\n")
	writeProjectText(t, dir, "second/generated.go", "second sentinel\n")
	config := `version: 1
packages:
  - name: firstgen
    output: first/generated.go
    queries: first/queries.sql
    schema: first/schema.sql
  - name: secondgen
    output: second/generated.go
    queries: second/queries.sql
    schema: second/schema.sql
`
	writeProjectText(t, dir, "chgen.yaml", config)

	tracked := []string{
		"chgen.yaml",
		"first/schema.sql", "first/queries.sql", "first/generated.go",
		"second/schema.sql", "second/queries.sql", "second/generated.go",
	}
	before := make(map[string][]byte, len(tracked))
	for _, name := range tracked {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = data
	}

	if err := Run(filepath.Join(dir, "chgen.yaml")); err == nil {
		t.Fatal("the invalid second package did not fail")
	}
	for _, name := range tracked {
		after, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before[name]) {
			t.Errorf("%s changed after a planning failure", name)
		}
	}
}

func TestRunLateExpansionFailureChangesNoFile(t *testing.T) {
	dir := t.TempDir()
	validProjectSQL(t, dir, "first")
	writeProjectText(t, dir, "first/generated.go", "first sentinel\n")
	writeProjectText(t, dir, "second/generated.go", "second sentinel\n")
	config := `version: 1
packages:
  - name: firstgen
    output: first/generated.go
    queries: first/queries.sql
    schema: first/schema.sql
  - name: secondgen
    output: second/generated.go
    queries: second/missing-queries.sql
    schema: second/missing-schema.sql
`
	writeProjectText(t, dir, "chgen.yaml", config)
	err := Run(filepath.Join(dir, "chgen.yaml"))
	if err == nil || !strings.Contains(err.Error(), `schema entry "second/missing-schema.sql": no such file or directory`) {
		t.Fatalf("got %v", err)
	}
	for name, want := range map[string]string{
		"first/generated.go":  "first sentinel\n",
		"second/generated.go": "second sentinel\n",
	} {
		got, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(got) != want {
			t.Errorf("%s changed after an expansion failure", name)
		}
	}
}

func TestRunRejectsOneFileAsSchemaAndQueries(t *testing.T) {
	dir := t.TempDir()
	writeProjectText(t, dir, "mixed.sql", "CREATE TABLE values (id UInt64) ENGINE = Memory;\n")
	config := `version: 1
packages:
  - name: gen
    output: generated/generated.go
    queries: mixed.sql
    schema: mixed.sql
`
	writeProjectText(t, dir, "chgen.yaml", config)
	err := Run(filepath.Join(dir, "chgen.yaml"))
	if err == nil || !strings.Contains(err.Error(), `schema entry "mixed.sql" and queries entry "mixed.sql" select the same file`) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "generated")); !os.IsNotExist(err) {
		t.Fatalf("planning created an output directory: %v", err)
	}
}

func TestRunRejectsOutputConfigCollision(t *testing.T) {
	dir := t.TempDir()
	validProjectSQL(t, dir, "input")
	config := `version: 1
packages:
  - name: gen
    output: project.go
    queries: input/queries.sql
    schema: input/schema.sql
`
	writeProjectText(t, dir, "project.go", config)
	before := []byte(config)
	err := Run(filepath.Join(dir, "project.go"))
	if err == nil || !strings.Contains(err.Error(), "is the configuration file") {
		t.Fatalf("got %v", err)
	}
	after, readErr := os.ReadFile(filepath.Join(dir, "project.go"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) {
		t.Fatal("the rejected output changed the configuration file")
	}
}

func TestRunRejectsOutputInputCollision(t *testing.T) {
	dir := t.TempDir()
	writeProjectText(t, dir, "schema.sql", "CREATE TABLE values (id UInt64) ENGINE = Memory;\n")
	query := "-- name: ListValues :many\nSELECT id FROM values;\n"
	writeProjectText(t, dir, "queries.go", query)
	config := `version: 1
packages:
  - name: gen
    output: queries.go
    queries: queries.go
    schema: schema.sql
`
	writeProjectText(t, dir, "chgen.yaml", config)
	err := Run(filepath.Join(dir, "chgen.yaml"))
	if err == nil || !strings.Contains(err.Error(), `output `+filepath.Join(dir, "queries.go")+` is also queries input "queries.go"`) {
		t.Fatalf("got %v", err)
	}
	after, readErr := os.ReadFile(filepath.Join(dir, "queries.go"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != query {
		t.Fatal("the rejected output changed the query input")
	}
}

func TestRunRejectsConfigInputCollision(t *testing.T) {
	dir := t.TempDir()
	writeProjectText(t, dir, "schema.sql", "CREATE TABLE values (id UInt64) ENGINE = Memory;\n")
	config := `version: 1
packages:
  - name: gen
    output: generated/generated.go
    queries: chgen.yaml
    schema: schema.sql
`
	writeProjectText(t, dir, "chgen.yaml", config)
	err := Run(filepath.Join(dir, "chgen.yaml"))
	if err == nil || !strings.Contains(err.Error(), `configuration file `+filepath.Join(dir, "chgen.yaml")+` is also queries input "chgen.yaml"`) {
		t.Fatalf("got %v", err)
	}
	after, readErr := os.ReadFile(filepath.Join(dir, "chgen.yaml"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != config {
		t.Fatal("the rejected input changed the configuration file")
	}
}

func TestRunRejectsConfigInputIdentityAlias(t *testing.T) {
	dir := t.TempDir()
	writeProjectText(t, dir, "schema.sql", "CREATE TABLE values (id UInt64) ENGINE = Memory;\n")
	config := `version: 1
packages:
  - name: gen
    output: generated/generated.go
    queries: query-source.sql
    schema: schema.sql
`
	configPath := filepath.Join(dir, "Config.yaml")
	writeProjectText(t, dir, "Config.yaml", config)
	inputPath := filepath.Join(dir, "query-source.sql")
	if err := os.Link(configPath, inputPath); err != nil {
		t.Skipf("cannot create a file identity alias: %v", err)
	}
	err := Run(configPath)
	if err == nil || !strings.Contains(err.Error(), `is also queries input "query-source.sql"`) {
		t.Fatalf("got %v", err)
	}
}

func TestRunRejectsOutputInputFileIdentityAlias(t *testing.T) {
	dir := t.TempDir()
	writeProjectText(t, dir, "schema.sql", "CREATE TABLE values (id UInt64) ENGINE = Memory;\n")
	writeProjectText(t, dir, "input.go", "-- name: ListValues :many\nSELECT id FROM values;\n")
	if err := os.Link(filepath.Join(dir, "input.go"), filepath.Join(dir, "generated.go")); err != nil {
		t.Skipf("cannot create a file identity alias: %v", err)
	}
	config := `version: 1
packages:
  - name: gen
    output: generated.go
    queries: input.go
    schema: schema.sql
`
	writeProjectText(t, dir, "chgen.yaml", config)
	err := Run(filepath.Join(dir, "chgen.yaml"))
	if err == nil || !strings.Contains(err.Error(), `is also queries input "input.go"`) {
		t.Fatalf("got %v", err)
	}
}

func TestRunRejectsPortableOutputAndInputAlias(t *testing.T) {
	dir := t.TempDir()
	writeProjectText(t, dir, "schema.sql", "CREATE TABLE values (id UInt64) ENGINE = Memory;\n")
	writeProjectText(t, dir, "Input.go", "-- name: ListValues :many\nSELECT id FROM values;\n")
	config := `version: 1
packages:
  - name: gen
    output: input.go
    queries: Input.go
    schema: schema.sql
`
	writeProjectText(t, dir, "chgen.yaml", config)
	err := Run(filepath.Join(dir, "chgen.yaml"))
	if err == nil || !strings.Contains(err.Error(), `is also queries input "Input.go"`) {
		t.Fatalf("got %v", err)
	}
}

func TestRunRejectsCanonicalOutputCollision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test needs symbolic-link creation without an extra privilege")
	}
	dir := t.TempDir()
	validProjectSQL(t, dir, "first")
	validProjectSQL(t, dir, "second")
	if err := os.Mkdir(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	config := `version: 1
packages:
  - name: firstgen
    output: real/generated.go
    queries: first/queries.sql
    schema: first/schema.sql
  - name: secondgen
    output: alias/generated.go
    queries: second/queries.sql
    schema: second/schema.sql
`
	writeProjectText(t, dir, "chgen.yaml", config)
	err := Run(filepath.Join(dir, "chgen.yaml"))
	if err == nil || !strings.Contains(err.Error(), `packages "firstgen" and "secondgen" resolve to the same output`) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "real/generated.go")); !os.IsNotExist(err) {
		t.Fatalf("the collision created an output: %v", err)
	}
}

func TestRunRejectsPortableOutputAliases(t *testing.T) {
	tests := []struct {
		name         string
		firstOutput  string
		secondOutput string
		want         string
	}{
		{
			name: "file case", firstOutput: "First/Generated.go", secondOutput: "first/generated.go",
			want: "resolve to the same output",
		},
		{
			name: "directory case", firstOutput: "Generated/first.go", secondOutput: "generated/second.go",
			want: "one output per directory is required",
		},
		{
			name: "directory Unicode normalization", firstOutput: "Caf\u00e9/first.go", secondOutput: "cafe\u0301/second.go",
			want: "one output per directory is required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			validProjectSQL(t, dir, "first")
			validProjectSQL(t, dir, "second")
			config := "version: 1\npackages:\n" +
				"  - name: firstgen\n    output: " + test.firstOutput + "\n    queries: first/queries.sql\n    schema: first/schema.sql\n" +
				"  - name: secondgen\n    output: " + test.secondOutput + "\n    queries: second/queries.sql\n    schema: second/schema.sql\n"
			writeProjectText(t, dir, "chgen.yaml", config)
			err := Run(filepath.Join(dir, "chgen.yaml"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunRejectsPortableConfigOutputAlias(t *testing.T) {
	dir := t.TempDir()
	validProjectSQL(t, dir, "input")
	config := `version: 1
packages:
  - name: gen
    output: config.go
    queries: input/queries.sql
    schema: input/schema.sql
`
	configPath := filepath.Join(dir, "Config.go")
	writeProjectText(t, dir, "Config.go", config)
	err := Run(configPath)
	if err == nil || !strings.Contains(err.Error(), "is the configuration file") {
		t.Fatalf("got %v", err)
	}
}

func TestRunRejectsConfigOutputFileIdentityAlias(t *testing.T) {
	dir := t.TempDir()
	validProjectSQL(t, dir, "input")
	config := `version: 1
packages:
  - name: gen
    output: generated.go
    queries: input/queries.sql
    schema: input/schema.sql
`
	configPath := filepath.Join(dir, "Config.go")
	writeProjectText(t, dir, "Config.go", config)
	if err := os.Link(configPath, filepath.Join(dir, "generated.go")); err != nil {
		t.Skipf("cannot create a file identity alias: %v", err)
	}
	err := Run(configPath)
	if err == nil || !strings.Contains(err.Error(), "is the configuration file") {
		t.Fatalf("got %v", err)
	}
}

func TestRunRejectsOutputFileIdentityAlias(t *testing.T) {
	dir := t.TempDir()
	validProjectSQL(t, dir, "first")
	validProjectSQL(t, dir, "second")
	if err := os.MkdirAll(filepath.Join(dir, "first-output"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "second-output"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProjectText(t, dir, "first-output/generated.go", "sentinel\n")
	if err := os.Link(filepath.Join(dir, "first-output/generated.go"), filepath.Join(dir, "second-output/generated.go")); err != nil {
		t.Skipf("cannot create a file identity alias: %v", err)
	}
	config := `version: 1
packages:
  - name: firstgen
    output: first-output/generated.go
    queries: first/queries.sql
    schema: first/schema.sql
  - name: secondgen
    output: second-output/generated.go
    queries: second/queries.sql
    schema: second/schema.sql
`
	writeProjectText(t, dir, "chgen.yaml", config)
	err := Run(filepath.Join(dir, "chgen.yaml"))
	if err == nil || !strings.Contains(err.Error(), "resolve to the same output") {
		t.Fatalf("got %v", err)
	}
}

func TestRunRejectsDifferentOutputsInOneDirectory(t *testing.T) {
	dir := t.TempDir()
	validProjectSQL(t, dir, "first")
	validProjectSQL(t, dir, "second")
	config := `version: 1
packages:
  - name: firstgen
    output: generated/first.go
    queries: first/queries.sql
    schema: first/schema.sql
  - name: secondgen
    output: generated/second.go
    queries: second/queries.sql
    schema: second/schema.sql
`
	writeProjectText(t, dir, "chgen.yaml", config)
	err := Run(filepath.Join(dir, "chgen.yaml"))
	if err == nil || !strings.Contains(err.Error(), "one output per directory is required") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "generated")); !os.IsNotExist(err) {
		t.Fatalf("planning created an output directory: %v", err)
	}
}

func TestRunRejectsDuplicateGeneratedDeclarationsInOneDirectory(t *testing.T) {
	dir := t.TempDir()
	validProjectSQL(t, dir, "first")
	validProjectSQL(t, dir, "second")
	config := `version: 1
packages:
  - name: gen
    output: generated/first.go
    queries: first/queries.sql
    schema: first/schema.sql
  - name: gen
    output: generated/second.go
    queries: second/queries.sql
    schema: second/schema.sql
`
	writeProjectText(t, dir, "chgen.yaml", config)
	err := Run(filepath.Join(dir, "chgen.yaml"))
	if err == nil || !strings.Contains(err.Error(), "one output per directory is required") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "generated")); !os.IsNotExist(err) {
		t.Fatalf("planning created an output directory: %v", err)
	}
}

func TestTwoGeneratedFilesRepeatSharedDeclarations(t *testing.T) {
	dir := t.TempDir()
	validProjectSQL(t, dir, "first")
	validProjectSQL(t, dir, "second")
	generate := func(prefix string) []byte {
		t.Helper()
		catalogs, err := engine.ParseSchemaCatalogs([]string{filepath.Join(dir, prefix, "schema.sql")})
		if err != nil {
			t.Fatal(err)
		}
		queries, err := engine.ParseQueryFiles([]string{filepath.Join(dir, prefix, "queries.sql")}, catalogs)
		if err != nil {
			t.Fatal(err)
		}
		source, err := engine.Generate("gen", queries)
		if err != nil {
			t.Fatal(err)
		}
		return source
	}
	declarations := func(source []byte) map[string]bool {
		t.Helper()
		file, err := parser.ParseFile(token.NewFileSet(), "generated.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		names := make(map[string]bool)
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				names[declaration.Name.Name] = true
			case *ast.GenDecl:
				for _, spec := range declaration.Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						names[spec.Name.Name] = true
					case *ast.ValueSpec:
						for _, name := range spec.Names {
							names[name.Name] = true
						}
					}
				}
			}
		}
		return names
	}
	first := declarations(generate("first"))
	second := declarations(generate("second"))
	for _, name := range []string{"Queries", "Querier", "MockQuerier", "New"} {
		if !first[name] || !second[name] {
			t.Fatalf("generated files did not both declare %s", name)
		}
	}
}

func TestRunRejectsUnsafeOutputNames(t *testing.T) {
	tests := []string{
		"generated.sql", ".generated.go", "_generated.go", "generated_test.go",
		"generated_linux.go", "generated_amd64.go", "generated_linux_arm64.go",
	}
	for _, output := range tests {
		t.Run(output, func(t *testing.T) {
			dir := t.TempDir()
			validProjectSQL(t, dir, "input")
			config := "version: 1\npackages:\n  - name: gen\n    output: " + output + "\n    queries: input/queries.sql\n    schema: input/schema.sql\n"
			writeProjectText(t, dir, "chgen.yaml", config)
			if err := Run(filepath.Join(dir, "chgen.yaml")); err == nil {
				t.Fatalf("unsafe output %q did not fail", output)
			}
			if _, err := os.Stat(filepath.Join(dir, output)); !os.IsNotExist(err) {
				t.Fatalf("unsafe output was created: %v", err)
			}
		})
	}
}

func TestCommitRefusesChangedOutputParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test needs symbolic-link creation without an extra privilege")
	}
	dir := t.TempDir()
	validProjectSQL(t, dir, "input")
	if err := os.Mkdir(filepath.Join(dir, "output"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `version: 1
packages:
  - name: gen
    output: output/generated.go
    queries: input/queries.sql
    schema: input/schema.sql
`
	configPath := filepath.Join(dir, "chgen.yaml")
	writeProjectText(t, dir, "chgen.yaml", config)
	loaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildExecutionPlan(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "output")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other", filepath.Join(dir, "output")); err != nil {
		t.Fatal(err)
	}
	err = commitExecutionPlan(plan)
	if err == nil || !strings.Contains(err.Error(), "changed after planning; refuse the commit") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "other/generated.go")); !os.IsNotExist(err) {
		t.Fatalf("the changed parent received an output: %v", err)
	}
}

func TestRunRejectsOutputSymbolicLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test needs symbolic-link creation without an extra privilege")
	}
	dir := t.TempDir()
	validProjectSQL(t, dir, "input")
	writeProjectText(t, dir, "target.go", "target sentinel\n")
	if err := os.Symlink("target.go", filepath.Join(dir, "generated.go")); err != nil {
		t.Fatal(err)
	}
	config := `version: 1
packages:
  - name: gen
    output: generated.go
    queries: input/queries.sql
    schema: input/schema.sql
`
	writeProjectText(t, dir, "chgen.yaml", config)
	err := Run(filepath.Join(dir, "chgen.yaml"))
	if err == nil || !strings.Contains(err.Error(), "output links are not supported") {
		t.Fatalf("got %v", err)
	}
	after, readErr := os.ReadFile(filepath.Join(dir, "target.go"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != "target sentinel\n" {
		t.Fatal("the rejected output link changed its target")
	}
}

func TestRunAtomicReplacementPreservesModeAndLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	validProjectSQL(t, dir, "input")
	output := filepath.Join(dir, "input/generated.go")
	writeProjectText(t, dir, "input/generated.go", "sentinel\n")
	if err := os.Chmod(output, 0o640); err != nil {
		t.Fatal(err)
	}
	config := `version: 1
packages:
  - name: gen
    output: input/generated.go
    queries: input/queries.sql
    schema: input/schema.sql
`
	writeProjectText(t, dir, "chgen.yaml", config)
	if err := Run(filepath.Join(dir, "chgen.yaml")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("output mode = %o, want 640", got)
	}
	listing, err := os.ReadDir(filepath.Dir(output))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range listing {
		if strings.HasPrefix(entry.Name(), ".chgen-") {
			t.Fatalf("temporary output remains: %s", entry.Name())
		}
	}
}

func TestRunNewOutputRespectsCallerUmask(t *testing.T) {
	if configPath := os.Getenv("CHGEN_TEST_UMASK_CONFIG"); configPath != "" {
		if err := Run(configPath); err != nil {
			t.Fatal(err)
		}
		return
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("the test needs a POSIX shell")
	}
	dir := t.TempDir()
	validProjectSQL(t, dir, "input")
	config := `version: 1
packages:
  - name: gen
    output: generated.go
    queries: input/queries.sql
    schema: input/schema.sql
`
	configPath := filepath.Join(dir, "chgen.yaml")
	writeProjectText(t, dir, "chgen.yaml", config)

	command := exec.Command(shell, "-c", `umask 077; exec "$1" -test.run '^TestRunNewOutputRespectsCallerUmask$'`, "sh", os.Args[0])
	command.Env = append(os.Environ(), "CHGEN_TEST_UMASK_CONFIG="+configPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run under umask 077: %v\n%s", err, output)
	}
	info, err := os.Stat(filepath.Join(dir, "generated.go"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("new output mode = %o, want 600 with umask 077", got)
	}
}

type recordingOutputFile struct {
	events *[]string
	name   string
	fail   string
}

func (file *recordingOutputFile) Name() string { return file.name }

func (file *recordingOutputFile) Chmod(fs.FileMode) error {
	*file.events = append(*file.events, "chmod")
	if file.fail == "chmod" {
		return errors.New("chmod failed")
	}
	return nil
}

func (file *recordingOutputFile) Write(data []byte) (int, error) {
	*file.events = append(*file.events, "write")
	if file.fail == "write" {
		return 0, errors.New("write failed")
	}
	return len(data), nil
}

func (file *recordingOutputFile) Sync() error {
	*file.events = append(*file.events, "sync")
	if file.fail == "sync" {
		return errors.New("sync failed")
	}
	return nil
}

func (file *recordingOutputFile) Close() error {
	*file.events = append(*file.events, "close")
	if file.fail == "close" {
		return errors.New("close failed")
	}
	return nil
}

func TestCommitOrdersOperationsAndCleansFailures(t *testing.T) {
	tests := []struct {
		name       string
		fail       string
		wantEvents []string
		wantError  bool
	}{
		{name: "success", wantEvents: []string{"mkdir", "canonical", "create", "chmod", "write", "sync", "close", "lstat", "rename"}},
		{name: "create", fail: "create", wantEvents: []string{"mkdir", "canonical", "create"}, wantError: true},
		{name: "chmod", fail: "chmod", wantEvents: []string{"mkdir", "canonical", "create", "chmod", "close", "remove"}, wantError: true},
		{name: "write", fail: "write", wantEvents: []string{"mkdir", "canonical", "create", "chmod", "write", "close", "remove"}, wantError: true},
		{name: "sync", fail: "sync", wantEvents: []string{"mkdir", "canonical", "create", "chmod", "write", "sync", "close", "remove"}, wantError: true},
		{name: "close", fail: "close", wantEvents: []string{"mkdir", "canonical", "create", "chmod", "write", "sync", "close", "remove"}, wantError: true},
		{name: "rename", fail: "rename", wantEvents: []string{"mkdir", "canonical", "create", "chmod", "write", "sync", "close", "lstat", "rename", "remove"}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join("root", "output")
			output := filepath.Join(directory, "generated.go")
			temp := filepath.Join(directory, ".chgen-temp")
			var events []string
			operations := outputOperations{
				mkdirAll: func(string, fs.FileMode) error {
					events = append(events, "mkdir")
					return nil
				},
				create: func(_ string, mode fs.FileMode) (outputFile, error) {
					events = append(events, "create")
					if mode != 0o666 {
						t.Fatalf("temporary mode = %o, want 666 before umask", mode)
					}
					if test.fail == "create" {
						return nil, errors.New("create failed")
					}
					return &recordingOutputFile{events: &events, name: temp, fail: test.fail}, nil
				},
				lstat: func(string) (fs.FileInfo, error) {
					events = append(events, "lstat")
					return nil, fs.ErrNotExist
				},
				rename: func(string, string) error {
					events = append(events, "rename")
					if test.fail == "rename" {
						return errors.New("rename failed")
					}
					return nil
				},
				remove: func(string) error {
					events = append(events, "remove")
					return nil
				},
				canonicalPotential: func(string) (string, error) {
					events = append(events, "canonical")
					return directory, nil
				},
			}
			existingInfo, err := os.Stat(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			plan := &executionPlan{packages: []plannedPackage{{
				name: "gen", output: output, outputDirectory: directory,
				outputInfo: existingInfo, mode: 0o640, generated: []byte("generated"),
			}}}
			err = commitExecutionPlanWithOperations(plan, operations)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError = %t", err, test.wantError)
			}
			if got, want := strings.Join(events, ","), strings.Join(test.wantEvents, ","); got != want {
				t.Fatalf("events = %q, want %q", got, want)
			}
		})
	}
}

func TestCommitCleansEarlierStageWhenLaterStageFails(t *testing.T) {
	var events []string
	createCount := 0
	operations := outputOperations{
		mkdirAll: func(string, fs.FileMode) error { return nil },
		create: func(directory string, _ fs.FileMode) (outputFile, error) {
			createCount++
			if createCount == 2 {
				return nil, errors.New("second create failed")
			}
			return &recordingOutputFile{events: &events, name: filepath.Join(directory, ".chgen-first")}, nil
		},
		lstat:  func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist },
		rename: func(string, string) error { return nil },
		remove: func(path string) error {
			events = append(events, "remove "+path)
			return nil
		},
		canonicalPotential: func(path string) (string, error) { return path, nil },
	}
	firstDirectory := filepath.Join("root", "first")
	secondDirectory := filepath.Join("root", "second")
	plan := &executionPlan{packages: []plannedPackage{
		{name: "first", output: filepath.Join(firstDirectory, "generated.go"), outputDirectory: firstDirectory, mode: 0o644, generated: []byte("first")},
		{name: "second", output: filepath.Join(secondDirectory, "generated.go"), outputDirectory: secondDirectory, mode: 0o644, generated: []byte("second")},
	}}
	if err := commitExecutionPlanWithOperations(plan, operations); err == nil {
		t.Fatal("the second CreateTemp failure did not fail")
	}
	wantRemove := "remove " + filepath.Join(firstDirectory, ".chgen-first")
	if !slicesContain(events, wantRemove) {
		t.Fatalf("events %v do not contain %q", events, wantRemove)
	}
}

func slicesContain(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
