package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestResolvedVersionUsesLinkedVersionFirst(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v2.0.0"}}
	if got := resolvedVersion("v1.2.3", info, true); got != "v1.2.3" {
		t.Fatalf("build version = %q, want v1.2.3", got)
	}
}

func TestExecuteRoutesInit(t *testing.T) {
	dir := t.TempDir()
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	var stdout, stderr bytes.Buffer
	if code := run([]string{"init"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run init code = %d, stderr = %q", code, stderr.String())
	}
	for _, name := range []string{"chgen.yaml", "schema.sql", "queries.sql"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("stat %s: %v", name, err)
		}
	}
}

func TestExecuteInitRefusesUnexpectedArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"init", "target"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no output", stdout.String())
	}
	if !strings.Contains(stderr.String(), `unexpected argument "target"`) || strings.Count(stderr.String(), "Usage:") != 1 {
		t.Fatalf("stderr does not contain one diagnostic and usage:\n%s", stderr.String())
	}
}

func TestExecuteKeepsVersionRouting(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"-version", "old-positional-argument"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run version code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "chgen ") {
		t.Fatalf("version output = %q", stdout.String())
	}
}

func TestExecuteHelpShowsInit(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run help code = %d", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "chgen init") || strings.Count(stderr.String(), "Usage:") != 1 {
		t.Fatalf("help streams are wrong: stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}

func TestRunFlagErrorsUseStderrAndCodeTwo(t *testing.T) {
	for _, args := range [][]string{{"-unknown"}, {"-f"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 2 {
				t.Fatalf("run code = %d, want 2", code)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want no output", stdout.String())
			}
			if strings.Count(stderr.String(), "Usage:") != 1 {
				t.Fatalf("stderr must contain one usage block:\n%s", stderr.String())
			}
		})
	}
}

func TestRunTopLevelPositionalErrorsKeepCodeOneWithoutUsage(t *testing.T) {
	for _, args := range [][]string{{"junk"}, {"-f", "config.yaml", "junk"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 1 {
				t.Fatalf("run code = %d, want 1", code)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want no output", stdout.String())
			}
			if !strings.Contains(stderr.String(), `unexpected argument "junk"`) || strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr must contain one diagnostic without usage:\n%s", stderr.String())
			}
			if strings.Count(stderr.String(), "unexpected argument") != 1 {
				t.Fatalf("stderr must contain one diagnostic:\n%s", stderr.String())
			}
		})
	}
}

func TestRunInitHelpUsesStderrAndCodeZero(t *testing.T) {
	for _, help := range []string{"-h", "--help"} {
		t.Run(help, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run([]string{"init", help}, &stdout, &stderr); code != 0 {
				t.Fatalf("run code = %d, want 0", code)
			}
			if stdout.Len() != 0 || strings.Count(stderr.String(), "Usage: chgen init") != 1 {
				t.Fatalf("help streams are wrong: stdout %q, stderr %q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunScaffoldErrorUsesCodeOne(t *testing.T) {
	dir := t.TempDir()
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	if err := os.WriteFile("chgen.yaml", []byte("sentinel\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"init"}, &stdout, &stderr); code != 1 {
		t.Fatalf("run code = %d, want 1", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "already exists") || strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("runtime-error streams are wrong: stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}

func TestResolvedVersionUsesModuleVersion(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}
	if got := resolvedVersion("dev", info, true); got != "v1.2.3" {
		t.Fatalf("build version = %q, want v1.2.3", got)
	}
}

func TestResolvedVersionHasDevelopmentFallback(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}
	if got := resolvedVersion("dev", info, true); got != "dev" {
		t.Fatalf("build version = %q, want dev", got)
	}
}
