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
	if config.Path != path {
		t.Fatalf("config path = %q, want original path %q", config.Path, path)
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

func TestLoadConfigRejectsDuplicateKeysAndDocuments(t *testing.T) {
	tests := map[string]struct {
		content string
		want    string
	}{
		"duplicate top-level key": {
			content: "version: 1\nversion: 1\npackages: []\n",
			want:    `top level has duplicate key "version"`,
		},
		"duplicate package key": {
			content: "version: 1\npackages:\n  - name: a\n    name: b\n    output: o.go\n    queries: q.sql\n    schema: s.sql\n",
			want:    `package 1 has duplicate key "name"`,
		},
		"second document": {
			content: "version: 1\npackages:\n  - name: a\n    output: o.go\n    queries: q.sql\n    schema: s.sql\n---\nversion: 1\npackages: []\n",
			want:    "exactly one YAML document",
		},
		"empty second document": {
			content: "version: 1\npackages:\n  - name: a\n    output: o.go\n    queries: q.sql\n    schema: s.sql\n---\n",
			want:    "exactly one YAML document",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := loadConfigError(t, test.content)
			if !strings.Contains(got, test.want) || !strings.Contains(got, "line ") && strings.Contains(name, "duplicate") {
				t.Fatalf("got %q, want a line-aware error that contains %q", got, test.want)
			}
		})
	}
}

func TestLoadConfigRequiresStrictYAMLStrings(t *testing.T) {
	tests := map[string]struct {
		content string
		want    string
	}{
		"string version": {
			content: "version: \"1\"\npackages: []\n",
			want:    "must be an integer",
		},
		"mapping name": {
			content: "version: 1\npackages:\n  - name: {value: a}\n    output: o.go\n    queries: q.sql\n    schema: s.sql\n",
			want:    "must be a string",
		},
		"integer output": {
			content: "version: 1\npackages:\n  - name: a\n    output: 7\n    queries: q.sql\n    schema: s.sql\n",
			want:    "must be a string",
		},
		"boolean query": {
			content: "version: 1\npackages:\n  - name: a\n    output: o.go\n    queries: true\n    schema: s.sql\n",
			want:    "must be a string",
		},
		"integer query list entry": {
			content: "version: 1\npackages:\n  - name: a\n    output: o.go\n    queries: [q.sql, 3]\n    schema: s.sql\n",
			want:    "queries entry 2 must be a string",
		},
		"mapping schema": {
			content: "version: 1\npackages:\n  - name: a\n    output: o.go\n    queries: q.sql\n    schema: {path: s.sql}\n",
			want:    "must be a string or a list of strings",
		},
		"null packages": {
			content: "version: 1\npackages: null\n",
			want:    "packages must be a list",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := loadConfigError(t, test.content)
			if !strings.Contains(got, test.want) {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestLoadConfigReportsScopedValueErrors(t *testing.T) {
	tests := map[string]struct {
		content string
		want    string
	}{
		"name": {
			content: "version: 1\npackages:\n  - name: {value: a}\n    output: o.go\n    queries: q.sql\n    schema: s.sql\n",
			want:    "line 3: package 1: name must be a string",
		},
		"output": {
			content: "version: 1\npackages:\n  - name: a\n    output: 7\n    queries: q.sql\n    schema: s.sql\n",
			want:    "line 4: package 1: output must be a string",
		},
		"queries list entry": {
			content: "version: 1\npackages:\n  - name: a\n    output: o.go\n    queries:\n      - q.sql\n      - false\n    schema: s.sql\n",
			want:    "line 7: package 1: queries entry 2 must be a string",
		},
		"schema": {
			content: "version: 1\npackages:\n  - name: a\n    output: o.go\n    queries: q.sql\n    schema: {path: s.sql}\n",
			want:    "line 6: package 1: schema must be a string or a list of strings",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeConfig(t, dir, test.content)
			_, err := LoadConfig(path)
			if err == nil {
				t.Fatal("expected an error")
			}
			want := path + ": " + test.want
			if err.Error() != want {
				t.Fatalf("error = %q, want %q", err, want)
			}
		})
	}
}

func TestLoadConfigKeepsQuotedMergeTextAsAPath(t *testing.T) {
	content := "version: 1\npackages:\n  - name: a\n    output: \"<<\"\n    queries: \"<<\"\n    schema: s.sql\n"
	config, err := LoadConfig(writeConfig(t, t.TempDir(), content))
	if err != nil {
		t.Fatal(err)
	}
	pkg := config.Packages[0]
	if filepath.Base(pkg.Output) != "<<" || pkg.Queries[0].Entry != "<<" {
		t.Fatalf("quoted merge text changed: output %q, query entry %q", pkg.Output, pkg.Queries[0].Entry)
	}
}

func TestLoadConfigRejectsEmptyNameAndOutput(t *testing.T) {
	tests := map[string]struct {
		content string
		want    string
	}{
		"empty name": {
			content: "version: 1\npackages:\n  - name: \"\"\n    output: o.go\n    queries: q.sql\n    schema: s.sql\n",
			want:    "name must be a non-empty Go package identifier",
		},
		"empty output": {
			content: "version: 1\npackages:\n  - name: a\n    output: \"\"\n    queries: q.sql\n    schema: s.sql\n",
			want:    "output must be a non-empty path",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := loadConfigError(t, test.content)
			if !strings.Contains(got, test.want) {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestPackageNameUsesTheGoIdentifierContract(t *testing.T) {
	for _, name := range []string{"querygen", "_querygen", "данные"} {
		t.Run("valid_"+name, func(t *testing.T) {
			if !validPackageName(name) {
				t.Fatalf("validPackageName(%q) = false", name)
			}
		})
	}
	for _, name := range []string{"_", "for", "7query", "query-name", "query.name", ""} {
		t.Run("invalid_"+name, func(t *testing.T) {
			if validPackageName(name) {
				t.Fatalf("validPackageName(%q) = true", name)
			}
		})
	}
}

func TestLoadConfigRejectsInvalidPackageNames(t *testing.T) {
	for _, name := range []string{"_", "for", "7query", "query-name", "query.name"} {
		t.Run(name, func(t *testing.T) {
			content := "version: 1\npackages:\n  - name: " + name + "\n    output: o.go\n    queries: q.sql\n    schema: s.sql\n"
			got := loadConfigError(t, content)
			if !strings.Contains(got, `invalid package name "`+name+`"`) {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestLoadConfigAcceptsUnicodePackageName(t *testing.T) {
	content := "version: 1\npackages:\n  - name: данные\n    output: o.go\n    queries: q.sql\n    schema: s.sql\n"
	config, err := LoadConfig(writeConfig(t, t.TempDir(), content))
	if err != nil {
		t.Fatal(err)
	}
	if got := config.Packages[0].Name; got != "данные" {
		t.Fatalf("name = %q", got)
	}
}

func TestLoadConfigRejectsYAMLAliasesAndMergeKeys(t *testing.T) {
	tests := map[string]struct {
		content string
		want    string
	}{
		"alias": {
			content: "version: 1\npackages:\n  - name: a\n    output: &output o.go\n    queries: q.sql\n    schema: *output\n",
			want:    "YAML aliases are not supported",
		},
		"merge": {
			content: "version: 1\npackages:\n  - <<: &defaults\n      name: a\n      output: o.go\n      queries: q.sql\n      schema: s.sql\n",
			want:    "YAML merge keys are not supported",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := loadConfigError(t, test.content)
			if !strings.Contains(got, test.want) || !strings.Contains(got, "line ") {
				t.Fatalf("got %q, want a line-aware error that contains %q", got, test.want)
			}
		})
	}
}
