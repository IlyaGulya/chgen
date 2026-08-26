package scaffold

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/project"
)

func TestInitCreatesDeterministicProjectThatGenerates(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	for _, dir := range []string{first, second} {
		if err := Init(dir); err != nil {
			t.Fatalf("Init(%q): %v", dir, err)
		}
	}

	wantNames := []string{"chgen.yaml", "queries.sql", "schema.sql"}
	for _, dir := range []string{first, second} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		gotNames := make([]string, 0, len(entries))
		for _, entry := range entries {
			gotNames = append(gotNames, entry.Name())
		}
		if !reflect.DeepEqual(gotNames, wantNames) {
			t.Fatalf("initial files = %q, want %q", gotNames, wantNames)
		}
	}

	for _, name := range wantNames {
		left, err := os.ReadFile(filepath.Join(first, name))
		if err != nil {
			t.Fatal(err)
		}
		right, err := os.ReadFile(filepath.Join(second, name))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(left, right) {
			t.Errorf("%s differs between runs", name)
		}
	}

	if err := project.Run(filepath.Join(first, "chgen.yaml")); err != nil {
		t.Fatalf("generate initialized project: %v", err)
	}
	generated, err := os.ReadFile(filepath.Join(first, "internal/querygen/queries.sql.go"))
	if err != nil {
		t.Fatalf("read generated output: %v", err)
	}
	if !strings.Contains(string(generated), "package querygen") || !strings.Contains(string(generated), "GetUser") {
		t.Fatalf("generated output does not contain the initialized query:\n%s", generated)
	}
}

func TestInitRefusesEveryExistingTargetBeforeItWrites(t *testing.T) {
	for _, existing := range []string{"chgen.yaml", "schema.sql", "queries.sql"} {
		t.Run(existing, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, existing)
			if err := os.WriteFile(path, []byte("sentinel\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			err := Init(dir)
			if err == nil || !strings.Contains(err.Error(), existing+" already exists") {
				t.Fatalf("Init error = %v, want an existing-file refusal", err)
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 1 || entries[0].Name() != existing {
				t.Fatalf("files after refusal = %v, want only %s", entries, existing)
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(content) != "sentinel\n" {
				t.Fatalf("existing file changed to %q", content)
			}
		})
	}
}

func TestInitDoesNotRemoveAFileCreatedDuringInitialization(t *testing.T) {
	dir := t.TempDir()
	operations := osFileOperations
	operations.create = func(path string) (io.WriteCloser, error) {
		if filepath.Base(path) == "schema.sql" {
			if err := os.WriteFile(path, []byte("foreign\n"), 0o600); err != nil {
				t.Fatalf("write foreign file: %v", err)
			}
		}
		return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	}

	err := initWithOperations(dir, operations)
	if err == nil {
		t.Fatal("initWithOperations succeeded after a target appeared")
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, "chgen.yaml")) ||
		!strings.Contains(err.Error(), "files created before failure") {
		t.Fatalf("error does not report partial state: %v", err)
	}
	foreign, readErr := os.ReadFile(filepath.Join(dir, "schema.sql"))
	if readErr != nil {
		t.Fatalf("read foreign file: %v", readErr)
	}
	if string(foreign) != "foreign\n" {
		t.Fatalf("foreign file changed to %q", foreign)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "chgen.yaml")); statErr != nil {
		t.Fatalf("the reported partial file does not exist: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "queries.sql")); !os.IsNotExist(statErr) {
		t.Fatalf("queries.sql state = %v, want no file", statErr)
	}
}
