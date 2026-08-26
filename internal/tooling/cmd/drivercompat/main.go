package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/IlyaGulya/chgen/internal/drivercompat"
)

var goRuntimePattern = regexp.MustCompile(`^go([1-9][0-9]*\.[0-9]+)(?:\.[0-9]+)?`)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("drivercompat", flag.ContinueOnError)
	flags.SetOutput(stderr)
	minimumGo := flags.String("go", "", "minimum Go version of one matrix cell")
	driverVersion := flags.String("driver", "", "clickhouse-go version of one matrix cell")
	goCommand := flags.String("go-command", "go", "Go command for the compile check")
	root := flags.String("root", "", "repository root")
	list := flags.Bool("list", false, "list the supported matrix")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	cells := drivercompat.Supported()
	if err := drivercompat.Validate(cells); err != nil {
		return err
	}
	if *list {
		if *minimumGo != "" || *driverVersion != "" {
			return fmt.Errorf("-list cannot be used with -go or -driver")
		}
		for _, cell := range cells {
			fmt.Fprintf(stdout, "%s\t%s\n", cell.MinimumGo, cell.DriverVersion)
		}
		return nil
	}
	if *minimumGo == "" || *driverVersion == "" {
		return fmt.Errorf("-go and -driver are required")
	}
	cell, err := drivercompat.Find(*minimumGo, *driverVersion)
	if err != nil {
		return err
	}
	if *root == "" {
		*root, err = findRoot()
		if err != nil {
			return err
		}
	}
	return compileCell(*root, *goCommand, cell, stdout, stderr)
}

func findRoot() (string, error) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("find repository root: caller information is not available")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", fmt.Errorf("find repository root: %w", err)
	}
	return root, nil
}

func compileCell(root, goCommand string, cell drivercompat.Cell, stdout, stderr io.Writer) error {
	gotVersion, err := commandOutput(goCommand, "env", "GOVERSION")
	if err != nil {
		return fmt.Errorf("read Go version: %w", err)
	}
	wantLine := strings.TrimSuffix(cell.MinimumGo, ".0")
	match := goRuntimePattern.FindStringSubmatch(strings.TrimSpace(gotVersion))
	if len(match) != 2 || match[1] != wantLine {
		return fmt.Errorf("matrix cell needs Go %s.x, but %s reports %s", wantLine, goCommand, strings.TrimSpace(gotVersion))
	}
	dir, err := os.MkdirTemp("", "chgen-driver-compat-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := writeModule(dir, cell); err != nil {
		return err
	}
	if err := copyGeneratedPackages(root, dir); err != nil {
		return err
	}
	for _, commandArgs := range [][]string{{"mod", "tidy"}, {"test", "./..."}} {
		command := exec.Command(goCommand, commandArgs...)
		command.Dir = dir
		command.Stdout = stdout
		command.Stderr = stderr
		command.Env = append(filteredEnvironment(os.Environ()), "GOTOOLCHAIN=local", "GOWORK=off")
		if err := command.Run(); err != nil {
			return fmt.Errorf("Go %s with driver %s: go %s: %w", cell.MinimumGo, cell.DriverVersion, strings.Join(commandArgs, " "), err)
		}
	}
	declaredGo, err := moduleGoVersion(dir, goCommand, cell.DriverVersion)
	if err != nil {
		return err
	}
	if declaredGo != cell.MinimumGo {
		return fmt.Errorf("clickhouse-go %s declares Go %s, but the matrix names Go %s", cell.DriverVersion, declaredGo, cell.MinimumGo)
	}
	goMod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return err
	}
	if !strings.Contains(string(goMod), "\ngo "+cell.MinimumGo+"\n") {
		return fmt.Errorf("dependency resolution changed the Go floor from %s", cell.MinimumGo)
	}
	fmt.Fprintf(stdout, "Generated code compiles with Go %s and clickhouse-go %s. This check does not run a query.\n", cell.MinimumGo, cell.DriverVersion)
	return nil
}

func moduleGoVersion(dir, goCommand, driverVersion string) (string, error) {
	command := exec.Command(goCommand, "list", "-m", "-json", "github.com/ClickHouse/clickhouse-go/v2@"+driverVersion)
	command.Dir = dir
	command.Env = append(filteredEnvironment(os.Environ()), "GOTOOLCHAIN=local", "GOWORK=off")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("read clickhouse-go module metadata: %w", err)
	}
	var module struct {
		GoVersion string
	}
	if err := json.Unmarshal(output, &module); err != nil {
		return "", fmt.Errorf("decode clickhouse-go module metadata: %w", err)
	}
	if module.GoVersion == "" {
		return "", fmt.Errorf("clickhouse-go %s does not declare a Go version", driverVersion)
	}
	return module.GoVersion, nil
}

func commandOutput(name string, args ...string) (string, error) {
	command := exec.Command(name, args...)
	command.Env = append(filteredEnvironment(os.Environ()), "GOTOOLCHAIN=local", "GOWORK=off")
	output, err := command.Output()
	return string(output), err
}

func filteredEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, value := range environment {
		if strings.HasPrefix(value, "GOTOOLCHAIN=") || strings.HasPrefix(value, "GOWORK=") {
			continue
		}
		result = append(result, value)
	}
	return result
}

func writeModule(dir string, cell drivercompat.Cell) error {
	content := fmt.Sprintf(`module example.com/chgen-driver-compat

go %s

require (
	github.com/ClickHouse/clickhouse-go/v2 %s
	github.com/google/uuid v1.6.0
	github.com/shopspring/decimal v1.4.0
)
`, cell.MinimumGo, cell.DriverVersion)
	return os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o600)
}

func copyGeneratedPackages(root, target string) error {
	matches, err := filepath.Glob(filepath.Join(root, "testdata", "golden", "*", "want.go"))
	if err != nil {
		return err
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		return fmt.Errorf("no generated golden packages found")
	}
	for _, source := range matches {
		name := filepath.Base(filepath.Dir(source))
		destinationDir := filepath.Join(target, name)
		if err := os.MkdirAll(destinationDir, 0o700); err != nil {
			return err
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(destinationDir, "generated.go"), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}
