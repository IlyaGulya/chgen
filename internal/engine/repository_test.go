package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func moduleRootPath(parts ...string) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("find the module root: caller information is not available")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return filepath.Join(append([]string{root}, parts...)...)
}

func TestModuleRootPath(t *testing.T) {
	data, err := os.ReadFile(moduleRootPath("go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "module github.com/IlyaGulya/chgen\n") {
		t.Fatal("the module root helper found a different Go module")
	}
	if _, err := os.Stat(moduleRootPath("internal", "engine", "repository_test.go")); err != nil {
		t.Fatal(err)
	}
}
