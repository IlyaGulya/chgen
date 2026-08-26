package examples_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedExamplesAreUpToDate regenerates the example output in a copy
// of the example tree and fails if the committed file drifted from the SQL
// sources. This is the same drift guard that a real project puts in CI.
func TestGeneratedExamplesAreUpToDate(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}

	committed := filepath.Join(repoRoot, "examples", "internal", "examplequeries", "queries.sql.go")
	want, err := os.ReadFile(committed)
	if err != nil {
		t.Fatalf("read committed output: %v", err)
	}

	// Copy the example inputs so the run cannot touch the repository.
	tmp := t.TempDir()
	for _, name := range []string{
		"chgen.yaml",
		"queries.sql",
		"schema.sql",
		"external_tables.sql",
		"migrations/001_add_channel_column.sql",
	} {
		source := filepath.Join(repoRoot, "examples", name)
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		target := filepath.Join(tmp, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	cmd := exec.Command("go", "run", "./cmd/chgen", "-f", filepath.Join(tmp, "chgen.yaml"))
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run chgen: %v\n%s", err, out)
	}

	got, err := os.ReadFile(filepath.Join(tmp, "internal", "examplequeries", "queries.sql.go"))
	if err != nil {
		t.Fatalf("read regenerated output: %v", err)
	}

	if string(got) != string(want) {
		t.Fatalf("examples/internal/examplequeries/queries.sql.go is stale; regenerate it with:\n"+
			"  go run ./cmd/chgen -f examples/chgen.yaml\n"+
			"got %d bytes, want %d bytes", len(got), len(want))
	}
}

// TestGeneratedExamplePackageIsPrivate prevents an example-only package from
// becoming an importable public package.
func TestGeneratedExamplePackageIsPrivate(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	exampleRoot := filepath.Join(repoRoot, "examples")
	generated := filepath.Join(exampleRoot, "internal", "examplequeries", "queries.sql.go")
	relative, err := filepath.Rel(exampleRoot, generated)
	if err != nil {
		t.Fatalf("resolve generated package path: %v", err)
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) < 2 || parts[0] != "internal" {
		t.Fatalf("generated example package %q is not private", relative)
	}
	if _, err := os.Stat(filepath.Join(exampleRoot, "gen")); !os.IsNotExist(err) {
		t.Fatalf("examples/gen must not exist: %v", err)
	}
}
