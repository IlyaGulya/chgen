package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "chgen.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadConfigParsesPackages(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
packages:
  - name: querygen
    output: out/queries.sql.go
    queries: queries.sql
    schema:
      - migrations
      - external_tables.sql
`)
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(config.Packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(config.Packages))
	}
	pkg := config.Packages[0]
	if pkg.Name != "querygen" {
		t.Errorf("name: got %q", pkg.Name)
	}
	if pkg.Output != filepath.Join(dir, "out/queries.sql.go") {
		t.Errorf("output not resolved against config dir: %q", pkg.Output)
	}
	if len(pkg.Queries) != 1 || pkg.Queries[0].Path != filepath.Join(dir, "queries.sql") {
		t.Errorf("queries: %+v", pkg.Queries)
	}
	if len(pkg.Schema) != 2 || pkg.Schema[0].Path != filepath.Join(dir, "migrations") {
		t.Errorf("schema: %+v", pkg.Schema)
	}
	if pkg.Schema[1].Entry != "external_tables.sql" {
		t.Errorf("original entry text lost: %+v", pkg.Schema[1])
	}
}

func TestLoadConfigResolvesOutputPaths(t *testing.T) {
	configDir := t.TempDir()
	absDir := t.TempDir()
	tests := map[string]struct {
		output string
		want   string
	}{
		"relative":  {output: "out/queries.sql.go", want: filepath.Join(configDir, "out", "queries.sql.go")},
		"absolute":  {output: filepath.Join(absDir, "queries.sql.go"), want: filepath.Join(absDir, "queries.sql.go")},
		"clean":     {output: "out/../queries.sql.go", want: filepath.Join(configDir, "queries.sql.go")},
		"traversal": {output: "../queries.sql.go", want: filepath.Join(filepath.Dir(configDir), "queries.sql.go")},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			content := "version: 1\npackages:\n  - name: querygen\n    output: " + test.output + "\n    queries: queries.sql\n    schema: schema.sql\n"
			config, err := LoadConfig(writeConfig(t, configDir, content))
			if err != nil {
				t.Fatal(err)
			}
			if got := config.Packages[0].Output; got != test.want {
				t.Fatalf("output = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLoadConfigRejectsTheAbsoluteOutputJoinMutation(t *testing.T) {
	configDir := t.TempDir()
	absOutput := filepath.Join(t.TempDir(), "queries.sql.go")
	content := "version: 1\npackages:\n  - name: querygen\n    output: " + absOutput + "\n    queries: queries.sql\n    schema: schema.sql\n"
	config, err := LoadConfig(writeConfig(t, configDir, content))
	if err != nil {
		t.Fatal(err)
	}
	mutated := filepath.Join(configDir, absOutput)
	if config.Packages[0].Output == mutated {
		t.Fatalf("the output contract accepted the old join mutation %q", mutated)
	}
}

func loadConfigError(t *testing.T, content string) string {
	t.Helper()
	path := writeConfig(t, t.TempDir(), content)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatalf("expected an error")
	}
	return err.Error()
}

func TestLoadConfigUnsupportedVersion(t *testing.T) {
	got := loadConfigError(t, "version: 2\npackages:\n  - name: a\n    output: o.go\n    queries: q.sql\n    schema: s.sql\n")
	if !strings.Contains(got, "unsupported config version 2; this chgen supports version 1") {
		t.Fatalf("got %q", got)
	}
}

func TestLoadConfigMissingRequiredKeys(t *testing.T) {
	cases := map[string]string{
		`missing required key "name"`:    "version: 1\npackages:\n  - output: o.go\n    queries: q.sql\n    schema: s.sql\n",
		`missing required key "output"`:  "version: 1\npackages:\n  - name: a\n    queries: q.sql\n    schema: s.sql\n",
		`missing required key "queries"`: "version: 1\npackages:\n  - name: a\n    output: o.go\n    schema: s.sql\n",
		`missing required key "schema"`:  "version: 1\npackages:\n  - name: a\n    output: o.go\n    queries: q.sql\n",
	}
	for want, content := range cases {
		got := loadConfigError(t, content)
		if !strings.Contains(got, "package 1: "+want) {
			t.Errorf("want %q in %q", want, got)
		}
	}
}

func TestLoadConfigUnknownKey(t *testing.T) {
	got := loadConfigError(t, "version: 1\npackages:\n  - name: a\n    output: o.go\n    queries: q.sql\n    schema: s.sql\n    extern: x\n")
	want := `unknown key "extern"; supported keys: version, packages, name, output, queries, schema`
	if !strings.Contains(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLoadConfigUnknownTopLevelKey(t *testing.T) {
	got := loadConfigError(t, "version: 1\nplugins: []\npackages:\n  - name: a\n    output: o.go\n    queries: q.sql\n    schema: s.sql\n")
	if !strings.Contains(got, `unknown key "plugins"`) {
		t.Fatalf("got %q", got)
	}
}

func TestLoadConfigDuplicateOutput(t *testing.T) {
	got := loadConfigError(t, `
version: 1
packages:
  - name: a
    output: same.go
    queries: q.sql
    schema: s.sql
  - name: b
    output: same.go
    queries: q2.sql
    schema: s.sql
`)
	if !strings.Contains(got, `packages "a" and "b" declare the same output same.go`) {
		t.Fatalf("got %q", got)
	}
}

func TestLoadConfigRequiresPackages(t *testing.T) {
	got := loadConfigError(t, "version: 1\npackages: []\n")
	if !strings.Contains(got, "packages") {
		t.Fatalf("got %q", got)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadConfig(filepath.Join(dir, "chgen.yaml"))
	if err == nil {
		t.Fatalf("expected an error")
	}
	want := "chgen.yaml not found in " + dir + "; create one or pass -f <path>"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}
