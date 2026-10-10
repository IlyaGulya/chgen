package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type summary struct {
	Version       int               `json:"version"`
	Profile       string            `json:"profile"`
	Status        string            `json:"status"`
	Configuration map[string]string `json:"configuration"`
	Suites        []suiteResult     `json:"suites"`
}

type suiteResult struct {
	ID            string            `json:"id"`
	Status        string            `json:"status"`
	Checks        []checkResult     `json:"checks"`
	Artifacts     []string          `json:"artifacts,omitempty"`
	Configuration map[string]string `json:"configuration,omitempty"`
}

type checkResult struct {
	ID             string            `json:"id"`
	Status         string            `json:"status"`
	Command        []string          `json:"command,omitempty"`
	Environment    map[string]string `json:"environment,omitempty"`
	ExitCode       int               `json:"exit_code"`
	DurationMillis int64             `json:"duration_ms"`
	Log            string            `json:"log,omitempty"`
	Error          string            `json:"error,omitempty"`
	OptionalSkips  []string          `json:"optional_skips,omitempty"`
}

type runner struct {
	ctx       context.Context
	root, out string
	report    summary
	current   *suiteResult
	tools     map[string]string
}

func (r *runner) command(id string, env map[string]string, args ...string) bool {
	return r.execute(id, env, "", false, []int{0}, args...)
}

func (r *runner) execute(id string, env map[string]string, output string, gate bool, accepted []int, args ...string) bool {
	result := checkResult{ID: id, Status: "passed", Command: args, Environment: env, Log: r.current.ID + "-" + id + ".log"}
	log, err := os.Create(filepath.Join(r.out, result.Log))
	if err != nil {
		r.refuse(id, "cannot create command log")
		return false
	}
	command := exec.CommandContext(r.ctx, args[0], args[1:]...)
	command.Dir, command.Env = r.root, os.Environ()
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	command.Stdout, command.Stderr = log, log
	command.WaitDelay = 5 * time.Second
	var stdout *os.File
	if output != "" {
		stdout, err = os.Create(filepath.Join(r.out, output))
		if err != nil {
			log.Close()
			r.refuse(id, "cannot create command artifact")
			return false
		}
		command.Stdout = stdout
	}
	fmt.Printf("[%s] %s\n", r.current.ID, id)
	start := time.Now()
	err = command.Run()
	result.DurationMillis = time.Since(start).Milliseconds()
	if err != nil {
		fmt.Fprintln(log, err)
	}
	if command.ProcessState == nil || r.ctx.Err() != nil {
		result.Status, result.ExitCode = "refused", 2
	} else {
		result.ExitCode = command.ProcessState.ExitCode()
		result.Status = "failed"
		for _, code := range accepted {
			if result.ExitCode == code {
				result.Status = "passed"
			}
		}
		if result.Status == "failed" && gate && result.ExitCode >= 2 {
			result.Status = "refused"
		}
		if err != nil && result.ExitCode == 0 {
			result.Status = "refused"
		}
	}
	if stdout != nil {
		if err := stdout.Close(); err != nil {
			result.Status = "refused"
		}
	}
	if err := log.Close(); err != nil {
		result.Status = "refused"
	}
	r.current.Checks = append(r.current.Checks, result)
	if result.Status != "passed" {
		r.current.Status = combineStatus(r.current.Status, result.Status)
	}
	if result.Status == "passed" && output != "" {
		return r.artifact(output)
	}
	return result.Status == "passed"
}

func (r *runner) output(id, filename string, env map[string]string, args ...string) bool {
	return r.execute(id, env, filename, false, []int{0}, args...)
}

func (r *runner) gate(id, tool string, args ...string) bool {
	return r.execute(id, nil, "", true, []int{0}, append([]string{tool}, args...)...)
}

func (r *runner) observation(id, tool string, args ...string) {
	if r.execute(id, nil, "", true, []int{0, 1}, append([]string{tool}, args...)...) {
		if last := &r.current.Checks[len(r.current.Checks)-1]; last.ExitCode == 1 {
			last.Status = "observed"
		}
	}
}

func (r *runner) expectedDifference(id, tool string, args ...string) {
	if r.execute(id, nil, "", true, []int{1}, append([]string{tool}, args...)...) {
		r.current.Checks[len(r.current.Checks)-1].Status = "observed"
	}
}

func (r *runner) refuse(id, message string) {
	r.current.Status = "refused"
	r.current.Checks = append(r.current.Checks, checkResult{ID: id, Status: "refused", ExitCode: 2, Error: message})
	fmt.Fprintln(os.Stderr, message)
}

func (r *runner) blocked(id, message string) {
	r.current.Checks = append(r.current.Checks, checkResult{ID: id, Status: "blocked", Error: message})
	if r.current.Status == "passed" {
		r.current.Status = "refused"
	}
}

func (r *runner) tool(name string) string {
	if r.tools == nil {
		r.tools = make(map[string]string)
	}
	if path, ok := r.tools[name]; ok {
		if path == "" {
			r.blocked("build-"+name, "tool unavailable because its earlier build failed")
		}
		return path
	}
	path := filepath.Join(r.out, "tools", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		r.refuse("build-"+name, "cannot create tool directory")
		return ""
	}
	r.tools[name] = ""
	packagePath := "./internal/tooling/cmd/" + name
	if name == "chgen" {
		packagePath = "./cmd/chgen"
	}
	if r.command("build-"+name, nil, "go", "build", "-o", path, packagePath) {
		r.tools[name] = path
	}
	return r.tools[name]
}

func (r *runner) artifact(name string) bool {
	info, err := os.Stat(filepath.Join(r.out, name))
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		r.refuse("artifact-"+name, "required artifact absent or empty: "+name)
		return false
	}
	r.current.Artifacts = append(r.current.Artifacts, name)
	return true
}

func (r *runner) test(id string, env map[string]string, tags, pattern string, packages ...string) bool {
	return r.testRequired(id, env, tags, pattern, nil, packages...)
}

func (r *runner) testRequired(id string, env map[string]string, tags, pattern string, required []string, packages ...string) bool {
	args := []string{"go", "test", "-count=1", "-timeout", "40m", "-v"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	if pattern != "" {
		args = append(args, "-run", pattern)
	}
	if !r.command(id, env, append(args, packages...)...) {
		return false
	}
	data, err := os.ReadFile(filepath.Join(r.out, r.current.Checks[len(r.current.Checks)-1].Log))
	if err != nil || !strings.Contains(string(data), "--- PASS:") {
		r.refuse(id+"-ran", "required live checks did not run; inspect the test log")
		return false
	}
	if len(required) == 0 {
		if strings.Contains(string(data), "--- SKIP:") {
			r.refuse(id+"-ran", "required live checks were skipped; inspect the test log")
			return false
		}
		return true
	}
	for _, name := range required {
		if !strings.Contains(string(data), "--- PASS: "+name+" (") {
			r.refuse(id+"-ran", "required live test did not pass: "+name)
			return false
		}
	}
	last := &r.current.Checks[len(r.current.Checks)-1]
	for line := range strings.SplitSeq(string(data), "\n") {
		if tail, ok := strings.CutPrefix(strings.TrimSpace(line), "--- SKIP: "); ok {
			name, _, _ := strings.Cut(tail, " ")
			last.OptionalSkips = append(last.OptionalSkips, name)
		}
	}
	return true
}

func (r *runner) server(endpoint, version string) bool {
	data, ok := r.request("server", endpoint, "SELECT version()")
	if !ok {
		return false
	}
	if strings.TrimSpace(string(data)) != version {
		r.refuse("server", "ClickHouse must answer pinned version "+version)
		return false
	}
	r.current.Checks = append(r.current.Checks, checkResult{ID: "server", Status: "passed", Command: []string{"ClickHouse", "SELECT version()"}, Environment: map[string]string{"endpoint": endpoint, "version": version}})
	return true
}

func (r *runner) sql(id, endpoint, sql string) bool {
	if _, ok := r.request(id, endpoint, sql); !ok {
		return false
	}
	r.current.Checks = append(r.current.Checks, checkResult{ID: id, Status: "passed", Command: []string{"ClickHouse", sql}})
	return true
}

func (r *runner) request(id, endpoint, sql string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(sql))
	if err != nil {
		r.refuse(id, "invalid ClickHouse HTTP endpoint")
		return nil, false
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		r.refuse(id, "cannot connect to ClickHouse HTTP endpoint")
		return nil, false
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1024))
	if err != nil || response.StatusCode != http.StatusOK {
		r.refuse(id, "ClickHouse setup request was refused")
		return nil, false
	}
	return data, true
}

func combineStatus(a, b string) string {
	if a == "refused" || b == "refused" {
		return "refused"
	}
	if a == "failed" || b == "failed" {
		return "failed"
	}
	return "passed"
}

func reportExit(report summary) int {
	switch report.Status {
	case "passed":
		return 0
	case "failed":
		return 1
	default:
		return 2
	}
}

func (r *runner) save() error {
	r.report.Status = "passed"
	var human strings.Builder
	human.WriteString("# Verification\n\nA passing run is not a claim of complete API coverage. `observed` means an expected cross-version difference, not a defect.\n\n| Suite | Status |\n|---|---|\n")
	for _, suite := range r.report.Suites {
		r.report.Status = combineStatus(r.report.Status, suite.Status)
		fmt.Fprintf(&human, "| %s | %s |\n", suiteKey(suite), suite.Status)
	}
	for _, suite := range r.report.Suites {
		fmt.Fprintf(&human, "\n## %s\n\n", suiteKey(suite))
		for _, check := range suite.Checks {
			fmt.Fprintf(&human, "- %s: %s (exit %d, %d ms)", check.ID, check.Status, check.ExitCode, check.DurationMillis)
			if check.Log != "" {
				fmt.Fprintf(&human, " — [log](%s)", filepath.ToSlash(check.Log))
			}
			if check.Error != "" {
				fmt.Fprintf(&human, " — %s", check.Error)
			}
			if len(check.OptionalSkips) != 0 {
				fmt.Fprintf(&human, " — optional skips: %s", strings.Join(check.OptionalSkips, ", "))
			}
			human.WriteByte('\n')
		}
		for _, artifact := range suite.Artifacts {
			fmt.Fprintf(&human, "- [Evidence: %s](%s)\n", filepath.Base(artifact), filepath.ToSlash(artifact))
		}
	}
	data, err := json.MarshalIndent(r.report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(r.out, "summary.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.out, "summary.md"), []byte(human.String()), 0o600)
}
