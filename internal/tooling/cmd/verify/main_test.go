package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var binary string

func TestVerifierMergesSilentSuccessfulCommands(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input")
	if err := os.MkdirAll(input, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(input, "silent.log"), "")
	writeFile(t, filepath.Join(input, "summary.json"), `{"version":1,"profile":"full","status":"passed","suites":[{"id":"offline","status":"passed","checks":[{"id":"build","status":"passed","command":["go","build"],"exit_code":0,"log":"silent.log"}]}]}`)
	output := filepath.Join(directory, "merged")
	command := exec.CommandContext(t.Context(), binary, "-merge", input, "-expect", "offline", "-report", output)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("silent successful command must merge: %v\n%s", err, data)
	}
	data, err := os.ReadFile(filepath.Join(output, "runs", "0", "silent.log"))
	if err != nil || len(data) != 0 {
		t.Fatalf("empty log was not preserved: %v, %q", err, data)
	}
}

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "chgen-verify-test-")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(directory, "verify")
	command := exec.Command("go", "build", "-o", binary, ".")
	if data, err := command.CombinedOutput(); err != nil {
		panic(string(data) + err.Error())
	}
	status := m.Run()
	if err := os.RemoveAll(directory); err != nil {
		panic(err)
	}
	os.Exit(status)
}

func TestVerifierListsTheSameSuitesForLocalAndCIUse(t *testing.T) {
	command := exec.CommandContext(t.Context(), binary, "-list")
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("list verification suites: %v\n%s", err, data)
	}
	var suites []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &suites); err != nil {
		t.Fatal(err)
	}
	want := []string{"offline", "minimum", "compatibility", "fuzz", "types", "execution", "boundary", "versions"}
	if len(suites) != len(want) {
		t.Fatalf("suite count = %d, want %d", len(suites), len(want))
	}
	for i, id := range want {
		if suites[i].ID != id {
			t.Fatalf("suite %d = %q, want %q", i, suites[i].ID, id)
		}
	}
}

func TestVerifierRunsRealFuzzCommandAndReportsItsResult(t *testing.T) {
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "internal/functionrules"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(directory, "go.mod"), "module verificationfixture\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(directory, "pipeline_test.go"), `package verificationfixture
import "testing"
func FuzzPublicSQLPipeline(f *testing.F) {
 f.Add("SELECT 1")
 f.Fuzz(func(t *testing.T, sql string) {})
}`)
	writeFile(t, filepath.Join(directory, "internal/functionrules/evidence_test.go"), `package evidencefixture
import "testing"
func FuzzCompactEvidence(f *testing.F) {
 f.Add([]byte("{}"))
 f.Fuzz(func(t *testing.T, data []byte) {})
}`)
	report := filepath.Join(directory, "report")
	command := exec.CommandContext(t.Context(), binary, "-root", directory, "-suite", "fuzz", "-fuzz-time", "1x", "-report", report)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run verification: %v\n%s", err, data)
	}
	data, err := os.ReadFile(filepath.Join(report, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Status string `json:"status"`
		Suites []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Checks []struct {
				ID      string   `json:"id"`
				Status  string   `json:"status"`
				Command []string `json:"command"`
				Log     string   `json:"log"`
			} `json:"checks"`
		} `json:"suites"`
	}
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Status != "passed" || len(summary.Suites) != 1 || summary.Suites[0].ID != "fuzz" || summary.Suites[0].Status != "passed" {
		t.Fatalf("unexpected summary: %s", data)
	}
	checks := summary.Suites[0].Checks
	if len(checks) != 2 || checks[0].ID != "public-pipeline" || checks[1].ID != "function-evidence" {
		t.Fatalf("missing executed command: %s", data)
	}
	for _, check := range checks {
		if check.Status != "passed" || len(check.Command) == 0 || check.Command[0] != "go" || !slices.Contains(check.Command, "1x") {
			t.Fatalf("explicit fuzz command or budget was ignored: %+v", check)
		}
		if log, err := os.ReadFile(filepath.Join(report, check.Log)); err != nil || len(log) == 0 {
			t.Fatalf("missing command evidence: %v", err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(report, "summary.md")); err != nil || len(data) == 0 {
		t.Fatalf("missing human summary: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestVerifierRefusesMissingServerInsteadOfReportingSkippedTestsAsSuccess(t *testing.T) {
	directory := t.TempDir()
	report := filepath.Join(directory, "report")
	command := exec.CommandContext(t.Context(), binary, "-suite", "types", "-report", report)
	command.Env = append(os.Environ(), "CHGEN_ORACLE_URL=", "CHGEN_CONTRACT_NATIVE=", "CHGEN_EXEC_HTTP=", "CHGEN_EXEC_NATIVE=")
	data, err := command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
		t.Fatalf("missing server should refuse (2), got %v\n%s", err, data)
	}
	data, err = os.ReadFile(filepath.Join(report, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Status string `json:"status"`
		Suites []struct {
			Status string `json:"status"`
			Checks []struct {
				Error string `json:"error"`
			} `json:"checks"`
		} `json:"suites"`
	}
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Status != "refused" || len(summary.Suites) != 1 || summary.Suites[0].Status != "refused" || len(summary.Suites[0].Checks) != 1 || summary.Suites[0].Checks[0].Error == "" {
		t.Fatalf("missing refusal evidence: %s", data)
	}
}

func TestVerifierMergesReportsAndRefusesMissingRequiredSuites(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "inputs", "fuzz")
	if err := os.MkdirAll(input, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(input, "summary.json"), `{"version":1,"profile":"quick","status":"passed","configuration":{},"suites":[{"id":"fuzz","status":"passed","checks":[{"id":"public-pipeline","status":"passed","exit_code":0,"log":"fuzz.log"}]}]}`)
	writeFile(t, filepath.Join(input, "fuzz.log"), "PASS\n")
	report := filepath.Join(directory, "combined")
	command := exec.CommandContext(t.Context(), binary, "-merge", filepath.Dir(input), "-expect", "fuzz,types", "-report", report)
	data, err := command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
		t.Fatalf("missing types must refuse merge: %v\n%s", err, data)
	}
	data, err = os.ReadFile(filepath.Join(report, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Status string `json:"status"`
		Suites []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Checks []struct {
				Log string `json:"log"`
			} `json:"checks"`
		} `json:"suites"`
	}
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Status != "refused" || len(summary.Suites) != 2 || summary.Suites[0].Status != "passed" || summary.Suites[1].ID != "types" || summary.Suites[1].Status != "refused" {
		t.Fatalf("unexpected merge: %s", data)
	}
	log, err := os.ReadFile(filepath.Join(report, summary.Suites[0].Checks[0].Log))
	if err != nil || string(log) != "PASS\n" {
		t.Fatalf("merged report lost evidence: %q, %v", log, err)
	}
}

func TestVerifierKeepsFailedCommandEvidenceAndExitStatus(t *testing.T) {
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "internal/functionrules"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(directory, "go.mod"), "module failingfixture\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(directory, "pipeline_test.go"), `package failingfixture
import "testing"
func FuzzPublicSQLPipeline(f *testing.F) {
 f.Add("known regression")
 f.Fuzz(func(t *testing.T, sql string) { t.Fatal("fixture regression detected") })
}`)
	writeFile(t, filepath.Join(directory, "internal/functionrules/evidence_test.go"), `package evidencefixture
import "testing"
func FuzzCompactEvidence(f *testing.F) {
 f.Add([]byte("{}"))
 f.Fuzz(func(t *testing.T, data []byte) {})
}`)
	report := filepath.Join(directory, "report")
	command := exec.CommandContext(t.Context(), binary, "-root", directory, "-suite", "fuzz", "-fuzz-time", "1x", "-report", report)
	data, err := command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("test failure must exit 1: %v\n%s", err, data)
	}
	data, err = os.ReadFile(filepath.Join(report, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var reportData struct {
		Status string `json:"status"`
		Suites []struct {
			Checks []struct {
				Status   string `json:"status"`
				ExitCode int    `json:"exit_code"`
				Log      string `json:"log"`
			} `json:"checks"`
		} `json:"suites"`
	}
	if err := json.Unmarshal(data, &reportData); err != nil {
		t.Fatal(err)
	}
	if reportData.Status != "failed" || len(reportData.Suites) != 1 || len(reportData.Suites[0].Checks) != 2 || reportData.Suites[0].Checks[0].ExitCode != 1 || reportData.Suites[0].Checks[0].Status != "failed" || reportData.Suites[0].Checks[1].Status != "passed" {
		t.Fatalf("failure was hidden: %s", data)
	}
	log, err := os.ReadFile(filepath.Join(report, reportData.Suites[0].Checks[0].Log))
	if err != nil || !strings.Contains(string(log), "fixture regression detected") {
		t.Fatalf("failure evidence missing: %s, %v", log, err)
	}
}

func TestVerifierCannotMergeGreenReportsWithMissingEvidence(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input")
	if err := os.MkdirAll(input, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(input, "summary.json"), `{"version":1,"status":"passed","suites":[{"id":"fuzz","status":"passed","checks":[{"id":"public-pipeline","status":"passed","exit_code":0,"log":"missing.log"}]}]}`)
	command := exec.CommandContext(t.Context(), binary, "-merge", input, "-expect", "fuzz", "-report", filepath.Join(directory, "report"))
	data, err := command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
		t.Fatalf("missing log must refuse: %v\n%s", err, data)
	}
}

func TestVerifierCompilesSupportedPairWithTheRequestedCompiler(t *testing.T) {
	report := filepath.Join(t.TempDir(), "report")
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), binary, "-root", root, "-suite", "compatibility", "-go", "1.24.0", "-driver", "v2.42.0", "-report", report)
	if data, err := command.CombinedOutput(); err != nil {
		entries, _ := os.ReadDir(report)
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".log") {
				log, _ := os.ReadFile(filepath.Join(report, entry.Name()))
				t.Logf("%s:\n%s", entry.Name(), log)
			}
		}
		t.Fatalf("supported pair should compile: %v\n%s", err, data)
	}
	data, err := os.ReadFile(filepath.Join(report, "compatibility-go1.24.0-driverv2.42.0.log"))
	if err != nil || !strings.Contains(string(data), "Generated code compiles with Go 1.24.0 and clickhouse-go v2.42.0.") {
		t.Fatalf("real supported compiler/driver check did not complete: %v\n%s", err, data)
	}
}

func TestVerifierKeepsCompilerDownloadNoticeOutOfTheCompilerPath(t *testing.T) {
	// Simulate only the external Go command's download notice. The separate
	// supported-pair test above still compiles every golden with the real Go.
	directory := t.TempDir()
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	goCommand, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	compilerRoot := filepath.Join(directory, "compiler with spaces")
	for _, path := range []string{filepath.Join(compilerRoot, "bin"), filepath.Join(directory, "commands")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(compilerRoot, "bin", "go"), "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(compilerRoot, "bin", "go"), 0o700); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(directory, "commands", "go")
	writeFile(t, shim, `#!/bin/sh
if [ "$1" = env ] && [ "$2" = GOROOT ]; then
  printf '%s\n' 'go: downloading go1.24.0 (test download notice)' >&2
  printf '%s\n' "$COMPAT_TEST_GOROOT"
  exit 0
fi
exec "$COMPAT_TEST_GO" "$@"
`)
	if err := os.Chmod(shim, 0o700); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(directory, "report")
	command := exec.CommandContext(t.Context(), binary, "-root", root, "-suite", "compatibility", "-go", "1.24.0", "-driver", "v2.42.0", "-report", report)
	command.Env = append(os.Environ(), "PATH="+filepath.Dir(shim)+string(os.PathListSeparator)+os.Getenv("PATH"), "COMPAT_TEST_GOROOT="+compilerRoot, "COMPAT_TEST_GO="+goCommand)
	data, err := command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("the deliberately failing compiler must fail the suite: %v\n%s", err, data)
	}
	data, err = os.ReadFile(filepath.Join(report, "compatibility-go1.24.0-driverv2.42.0-goroot.txt"))
	if err != nil || strings.TrimSpace(string(data)) != compilerRoot {
		t.Fatalf("compiler path contains stderr or lost spaces: %v\n%s", err, data)
	}
	data, err = os.ReadFile(filepath.Join(report, "compatibility-go1.24.0-driverv2.42.0-compiler.log"))
	if err != nil || !strings.Contains(string(data), "test download notice") {
		t.Fatalf("compiler download diagnostic was lost: %v\n%s", err, data)
	}
	data, err = os.ReadFile(filepath.Join(report, "compatibility-go1.24.0-driverv2.42.0.log"))
	if err != nil || !strings.Contains(string(data), "read Go version: exit status 1") {
		t.Fatalf("compiler execution failure was not preserved: %v\n%s", err, data)
	}
}

func TestVerifierDefaultCreatesFreshReportsWithoutOverwritingPreviousRun(t *testing.T) {
	directory := t.TempDir()
	for range 2 {
		command := exec.CommandContext(t.Context(), binary, "-suite", "types")
		command.Dir = directory
		command.Env = append(os.Environ(), "CHGEN_ORACLE_URL=", "CHGEN_CONTRACT_NATIVE=")
		if data, err := command.CombinedOutput(); err == nil {
			t.Fatalf("missing server unexpectedly passed: %s", data)
		}
	}
	entries, err := os.ReadDir(filepath.Join(directory, "verify-report"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || !entries[0].IsDir() || !entries[1].IsDir() {
		t.Fatalf("expected two independent run bundles, got %v", entries)
	}
	for _, entry := range entries {
		if _, err := os.Stat(filepath.Join(directory, "verify-report", entry.Name(), "summary.json")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVerifierCombinesEveryCICellIncludingExpectedVersionDifferences(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input")
	cells := []struct{ id, goVersion, driver string }{
		{"offline", "", ""}, {"minimum", "", ""},
		{"compatibility", "1.24.0", "v2.42.0"}, {"compatibility", "1.25.0", "v2.47.0"},
		{"fuzz", "", ""}, {"types", "", ""}, {"execution", "", ""}, {"boundary", "", ""}, {"versions", "", ""},
	}
	for i, cell := range cells {
		bundle := filepath.Join(input, fmt.Sprint(i))
		if err := os.MkdirAll(bundle, 0o700); err != nil {
			t.Fatal(err)
		}
		status, code := "passed", 0
		if cell.id == "versions" {
			status, code = "observed", 1
		}
		data, err := json.Marshal(map[string]any{
			"version": 1, "status": "passed", "configuration": map[string]string{"go": cell.goVersion, "driver": cell.driver},
			"suites": []any{map[string]any{"id": cell.id, "status": "passed", "checks": []any{map[string]any{"id": "check", "status": status, "exit_code": code, "log": "check.log"}}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(bundle, "summary.json"), string(data))
		writeFile(t, filepath.Join(bundle, "check.log"), "saved evidence\n")
	}
	report := filepath.Join(directory, "combined")
	command := exec.CommandContext(t.Context(), binary, "-merge", input, "-expect", "ci", "-report", report)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("complete CI matrix must pass: %v\n%s", err, data)
	}
	data, err := os.ReadFile(filepath.Join(report, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Status string            `json:"status"`
		Suites []json.RawMessage `json:"suites"`
	}
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Status != "passed" || len(summary.Suites) != 9 {
		t.Fatalf("unexpected complete report: %s", data)
	}
	human, err := os.ReadFile(filepath.Join(report, "summary.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, cell := range []string{"compatibility/1.24.0/v2.42.0", "compatibility/1.25.0/v2.47.0"} {
		if !strings.Contains(string(human), cell) {
			t.Fatalf("human report hides matrix cell %s:\n%s", cell, human)
		}
	}
}
