package repositorycheck_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreCommitChecksStagedFormatting(t *testing.T) {
	hook := moduleRootPath(".githooks", "pre-commit")
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(source string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "with space.go"), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runHook := func(wantOK bool) {
		t.Helper()
		cmd := exec.Command("git", "hook", "run", "pre-commit")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if (err == nil) != wantOK {
			t.Fatalf("hook error = %v, want success %v\n%s", err, wantOK, out)
		}
		if !wantOK && !strings.Contains(string(out), "needs gofmt") {
			t.Fatalf("missing formatting diagnostic: %s", out)
		}
	}
	git("init", "--quiet")
	git("config", "core.hooksPath", filepath.Dir(hook))
	runHook(true)

	const unformatted = "package fixture\nfunc f( ){ }\n"
	const formatted = "package fixture\n\nfunc f() {}\n"
	write(unformatted)
	git("add", "--", "with space.go")
	write(formatted)
	runHook(false) // A formatted working tree must not hide a bad staged blob.

	git("add", "--", "with space.go")
	write(unformatted)
	runHook(true) // Unstaged edits must not enter or block the formatted commit.
	got, err := os.ReadFile(filepath.Join(dir, "with space.go"))
	if err != nil || string(got) != unformatted {
		t.Fatalf("hook changed working tree: %q, %v", got, err)
	}
	git("rm", "--force", "--", "with space.go")
	runHook(true)
}
