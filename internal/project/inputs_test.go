package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("-- sql"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func TestExpandInputsFileUsedAsIs(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "queries.sql")
	entry := InputEntry{Entry: "queries.sql", Path: filepath.Join(dir, "queries.sql")}
	got, err := expandInputEntries("queries", []InputEntry{entry})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(got) != 1 || got[0] != entry.Path {
		t.Fatalf("got %v", got)
	}
}

func TestExpandInputsMissingEntry(t *testing.T) {
	entry := InputEntry{Entry: "missing.sql", Path: filepath.Join(t.TempDir(), "missing.sql")}
	_, err := expandInputEntries("schema", []InputEntry{entry})
	want := `schema entry "missing.sql": no such file or directory`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestExpandInputsDirectoryRules(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir,
		"m/000002_b.up.sql",
		"m/000001_a.up.sql",
		"m/000001_a.down.sql",
		"m/readme.md",
		"m/nested/000003_c.up.sql",
	)
	entry := InputEntry{Entry: "m", Path: filepath.Join(dir, "m")}
	got, err := expandInputEntries("schema", []InputEntry{entry})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	want := []string{
		filepath.Join(dir, "m/000001_a.up.sql"),
		filepath.Join(dir, "m/000002_b.up.sql"),
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandInputsEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "m/only.down.sql")
	entry := InputEntry{Entry: "m", Path: filepath.Join(dir, "m")}
	_, err := expandInputEntries("schema", []InputEntry{entry})
	want := `schema directory "m" contains no .sql files (after ignoring .down.sql)`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestExpandInputsGlob(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "m/b.sql", "m/a.sql", "m/a.down.sql", "m/c.txt")
	entry := InputEntry{Entry: "m/*.sql", Path: filepath.Join(dir, "m/*.sql")}
	got, err := expandInputEntries("queries", []InputEntry{entry})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(got) != 2 || !strings.HasSuffix(got[0], "a.sql") || !strings.HasSuffix(got[1], "b.sql") {
		t.Fatalf("got %v", got)
	}
}

func TestExpandInputsGlobNoMatches(t *testing.T) {
	dir := t.TempDir()
	entry := InputEntry{Entry: "m/*.sql", Path: filepath.Join(dir, "m/*.sql")}
	_, err := expandInputEntries("queries", []InputEntry{entry})
	want := `queries glob "m/*.sql" matches no files`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestExpandInputsGlobMatchesDirectoriesAreSkipped(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "m/sub.sql/inner.sql", "m/a.sql")
	entry := InputEntry{Entry: "m/*.sql", Path: filepath.Join(dir, "m/*.sql")}
	got, err := expandInputEntries("queries", []InputEntry{entry})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(got) != 1 || !strings.HasSuffix(got[0], "a.sql") {
		t.Fatalf("got %v", got)
	}
}
