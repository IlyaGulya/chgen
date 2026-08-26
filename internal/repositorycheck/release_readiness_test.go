package repositorycheck_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const publicModulePath = "github.com/IlyaGulya/chgen"

func TestReleaseMetadataIsComplete(t *testing.T) {
	goMod := readReleaseFile(t, "go.mod")
	if !strings.HasPrefix(goMod, "module "+publicModulePath+"\n") {
		t.Fatalf("go.mod does not declare %s", publicModulePath)
	}
	for _, line := range strings.Split(goMod, "\n") {
		if line == "replace (" || strings.HasPrefix(line, "replace ") {
			t.Fatalf("go.mod has a replace directive: %s", line)
		}
	}

	license := readReleaseFile(t, "LICENSE")
	for _, text := range []string{"MIT License", "Permission is hereby granted", "THE SOFTWARE IS PROVIDED \"AS IS\""} {
		if !strings.Contains(license, text) {
			t.Fatalf("LICENSE does not contain %q", text)
		}
	}

	readme := readReleaseFile(t, "README.md")
	install := "go install " + publicModulePath + "/cmd/chgen@latest"
	if !strings.Contains(readme, install) {
		t.Fatalf("README does not contain the install command %q", install)
	}
	if !strings.Contains(readme, "MIT. See [LICENSE](LICENSE).") {
		t.Fatal("README does not link to the license")
	}
	if !strings.Contains(readme, "[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)") {
		t.Fatal("README does not link to the third-party notices")
	}

	notices := readReleaseFile(t, "THIRD_PARTY_NOTICES.md")
	for _, text := range []string{
		"ClickHouse 25.8.29.51",
		"Apache License 2.0",
		"https://github.com/ClickHouse/ClickHouse/blob/master/LICENSE",
	} {
		if !strings.Contains(notices, text) {
			t.Fatalf("THIRD_PARTY_NOTICES.md does not contain %q", text)
		}
	}

	exampleConfig := readReleaseFile(t, filepath.Join("examples", "chgen.yaml"))
	if !strings.Contains(exampleConfig, "output: internal/examplequeries/queries.sql.go") {
		t.Fatal("the generated example package is not under examples/internal")
	}
}

func TestReleaseDependenciesHaveNoLocalReplacement(t *testing.T) {
	command := exec.Command("go", "list", "-mod=readonly", "-m", "-json", "all")
	command.Dir = moduleRootPath()
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list module dependencies: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var module struct {
			Path    string
			Main    bool
			Replace *json.RawMessage
		}
		if err := decoder.Decode(&module); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode module dependency: %v", err)
		}
		if module.Main && module.Path != publicModulePath {
			t.Fatalf("main module = %s, want %s", module.Path, publicModulePath)
		}
		if module.Replace != nil {
			t.Fatalf("module %s has a replacement", module.Path)
		}
		first := module.Path
		if slash := strings.IndexByte(first, '/'); slash >= 0 {
			first = first[:slash]
		}
		if !strings.Contains(first, ".") {
			t.Fatalf("module %s does not have a public module path", module.Path)
		}
	}
}

func TestReleaseCIRunsPinnedLiveGates(t *testing.T) {
	workflow := readReleaseFile(t, filepath.Join(".github", "workflows", "ci.yml"))
	checks := []string{
		"image: clickhouse/clickhouse-server:25.8.29.51",
		"go test -tags fuzzoracle -run TestTypeOracle",
		"go test -tags execoracle",
		"testdata/probe-baseline-25.8.29.51.json",
		"run: ./scripts/verify.sh",
	}
	for _, check := range checks {
		if !strings.Contains(workflow, check) {
			t.Fatalf("CI workflow does not contain %q", check)
		}
	}
}

func TestReleaseVerificationRunsTheOfflineOracle(t *testing.T) {
	verification := readReleaseFile(t, filepath.Join("scripts", "verify.sh"))
	if !strings.Contains(verification, "go test -tags fuzzoracle ./internal/engine") {
		t.Fatal("the release verification does not run the complete offline type-oracle package")
	}
}

func readReleaseFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(moduleRootPath(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
