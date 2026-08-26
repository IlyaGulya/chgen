package quickstart_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/IlyaGulya/chgen"
)

func TestGeneratedOutputIsCurrent(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate the quickstart test")
	}
	quickstartRoot := filepath.Dir(sourceFile)
	committedPath := filepath.Join(quickstartRoot, "internal", "gen", "queries.sql.go")
	want, err := os.ReadFile(committedPath)
	if err != nil {
		t.Fatalf("read committed output: %v", err)
	}

	temporaryRoot := t.TempDir()
	for _, name := range []string{"chgen.yaml", "queries.sql", "schema.sql"} {
		data, err := os.ReadFile(filepath.Join(quickstartRoot, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(temporaryRoot, name), data, 0o644); err != nil {
			t.Fatalf("copy %s: %v", name, err)
		}
	}
	if err := chgen.Run(filepath.Join(temporaryRoot, "chgen.yaml")); err != nil {
		t.Fatalf("regenerate the quickstart: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(temporaryRoot, "internal", "gen", "queries.sql.go"))
	if err != nil {
		t.Fatalf("read regenerated output: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("examples/quickstart/internal/gen/queries.sql.go is not current")
	}
}
