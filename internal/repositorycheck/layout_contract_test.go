package repositorycheck_test

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen"
)

var testFunctionPattern = regexp.MustCompile(`^(Test|Fuzz|Benchmark)[A-Za-z0-9_]+$`)

type layoutSnapshot struct {
	rootProduction        []string
	rootTests             []string
	repositoryFiles       []string
	repositoryFunctions   []string
	apiInventoryFunctions []string
	apiInventoryFileTests []string
	engineFiles           []string
	engineFunctions       []string
	baselineFiles         []string
	baselineFunctions     []string
	tagged                map[string][]string
	live                  []string
	publicNames           []string
	publicShape           []string
	baselinePublicShape   []string
	hasPublicAlias        bool
	hasInternalSignature  bool
	hasInternalDocs       bool
	hasInternalReflection bool
	hasInternalGoDoc      bool
	hasInvalidRootTest    bool
	hasSilentCommand      bool
	hasUnsafeArtifactPath bool
	hasUnsafeGenerator    bool
	hasInvalidExtraction  bool
}

func TestStandardGoLayoutContract(t *testing.T) {
	snapshot, err := readLayoutSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateLayoutSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestPublicCommandSurface(t *testing.T) {
	root := moduleRootPath()
	publicCommands, err := commandDirectories(filepath.Join(root, "cmd"))
	if err != nil {
		t.Fatal(err)
	}
	toolingCommands, err := commandDirectories(filepath.Join(root, "internal", "tooling", "cmd"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCommandSurface(publicCommands, toolingCommands); err != nil {
		t.Fatal(err)
	}
}

func TestPublicCommandSurfaceRejectsMutations(t *testing.T) {
	publicCommands := []string{"chgen"}
	toolingCommands := expectedToolingCommands()
	mutations := map[string]func(*[]string, *[]string){
		"public maintainer command": func(public, _ *[]string) {
			*public = append(*public, "oraclegate")
		},
		"missing public command": func(public, _ *[]string) {
			*public = nil
		},
		"missing maintainer command": func(_, tooling *[]string) {
			*tooling = (*tooling)[1:]
		},
		"extra maintainer command": func(_, tooling *[]string) {
			*tooling = append(*tooling, "unknown")
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			public := append([]string(nil), publicCommands...)
			tooling := append([]string(nil), toolingCommands...)
			mutate(&public, &tooling)
			if err := validateCommandSurface(public, tooling); err == nil {
				t.Fatal("the command surface gate accepted the mutation")
			}
		})
	}
}

func commandDirectories(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	commands := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			return nil, fmt.Errorf("command root contains file %s", entry.Name())
		}
		mainPath := filepath.Join(root, entry.Name(), "main.go")
		file, err := parser.ParseFile(token.NewFileSet(), mainPath, nil, parser.PackageClauseOnly)
		if err != nil {
			return nil, fmt.Errorf("parse command %s: %w", entry.Name(), err)
		}
		if file.Name.Name != "main" {
			return nil, fmt.Errorf("command %s has package %s", entry.Name(), file.Name.Name)
		}
		commands = append(commands, entry.Name())
	}
	sort.Strings(commands)
	return commands, nil
}

func validateCommandSurface(publicCommands, toolingCommands []string) error {
	if !reflect.DeepEqual(publicCommands, []string{"chgen"}) {
		return fmt.Errorf("public commands = %v, want [chgen]", publicCommands)
	}
	wantTooling := expectedToolingCommands()
	if !reflect.DeepEqual(toolingCommands, wantTooling) {
		return fmt.Errorf("maintainer commands = %v, want %v", toolingCommands, wantTooling)
	}
	return nil
}

func expectedToolingCommands() []string {
	return []string{
		"apiinventory",
		"drivercompat",
		"execoraclegate",
		"gensemantics",
		"oraclediff",
		"oraclegate",
		"probe",
		"probegate",
		"supportmanifest",
		"typespecimens",
	}
}

func TestStandardGoLayoutContractRejectsMutations(t *testing.T) {
	base, err := readLayoutSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*layoutSnapshot){
		"extra root production file": func(value *layoutSnapshot) { value.rootProduction = append(value.rootProduction, "parser.go") },
		"repository test returned to root": func(value *layoutSnapshot) {
			value.rootTests = append(value.rootTests, "release_readiness_test.go")
		},
		"removed repository test file": func(value *layoutSnapshot) {
			value.repositoryFiles = value.repositoryFiles[1:]
		},
		"removed repository test function": func(value *layoutSnapshot) {
			value.repositoryFunctions = value.repositoryFunctions[1:]
		},
		"removed API inventory test function": func(value *layoutSnapshot) {
			value.apiInventoryFunctions = value.apiInventoryFunctions[1:]
		},
		"removed API inventory file test": func(value *layoutSnapshot) {
			value.apiInventoryFileTests = value.apiInventoryFileTests[1:]
		},
		"too many root test files": func(value *layoutSnapshot) {
			value.rootTests = append(value.rootTests, "one_test.go", "two_test.go", "three_test.go", "four_test.go", "five_test.go")
		},
		"removed engine file":      func(value *layoutSnapshot) { value.engineFiles = value.engineFiles[1:] },
		"removed test function":    func(value *layoutSnapshot) { value.engineFunctions = value.engineFunctions[1:] },
		"changed fuzz roster":      func(value *layoutSnapshot) { value.tagged["fuzzoracle"] = value.tagged["fuzzoracle"][:1] },
		"changed exec roster":      func(value *layoutSnapshot) { value.tagged["execoracle"] = nil },
		"changed axis roster":      func(value *layoutSnapshot) { value.tagged["axisaudit"] = nil },
		"changed live roster":      func(value *layoutSnapshot) { value.live = value.live[:1] },
		"untagged added live test": func(value *layoutSnapshot) { value.live = append(value.live, "new_live_test.go") },
		"widened build tag":        func(value *layoutSnapshot) { value.tagged["invalid"] = []string{"typeoracle_fuzz_test.go"} },
		"changed public API":       func(value *layoutSnapshot) { value.publicNames = append(value.publicNames, "ParseInternal") },
		"changed public API shape": func(value *layoutSnapshot) { value.publicShape = value.publicShape[1:] },
		"public alias":             func(value *layoutSnapshot) { value.hasPublicAlias = true },
		"internal signature":       func(value *layoutSnapshot) { value.hasInternalSignature = true },
		"internal documentation":   func(value *layoutSnapshot) { value.hasInternalDocs = true },
		"internal reflection":      func(value *layoutSnapshot) { value.hasInternalReflection = true },
		"internal go doc":          func(value *layoutSnapshot) { value.hasInternalGoDoc = true },
		"invalid root test":        func(value *layoutSnapshot) { value.hasInvalidRootTest = true },
		"silent tagged command":    func(value *layoutSnapshot) { value.hasSilentCommand = true },
		"unsafe artifact path":     func(value *layoutSnapshot) { value.hasUnsafeArtifactPath = true },
		"unsafe generator output":  func(value *layoutSnapshot) { value.hasUnsafeGenerator = true },
		"invalid leaf extraction":  func(value *layoutSnapshot) { value.hasInvalidExtraction = true },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := cloneLayoutSnapshot(base)
			mutate(&candidate)
			if err := validateLayoutSnapshot(candidate); err == nil {
				t.Fatal("the layout gate accepted the mutation")
			}
		})
	}
}

func TestStandardGoLayoutContractAllowsValidAdditions(t *testing.T) {
	base, err := readLayoutSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	base.engineFiles = append(base.engineFiles, "new_live_test.go")
	base.engineFunctions = append(base.engineFunctions, "new_live_test.go:TestNewLiveRule")
	base.tagged["fuzzoracle"] = append(base.tagged["fuzzoracle"], "new_live_test.go")
	base.live = append(base.live, "new_live_test.go")
	if err := validateLayoutSnapshot(base); err != nil {
		t.Fatalf("the layout gate refused valid additions: %v", err)
	}
}

func TestInternalProjectDependencyDirection(t *testing.T) {
	root := moduleRootPath()
	projectPath := "github.com/IlyaGulya/chgen/internal/project"
	enginePath := "github.com/IlyaGulya/chgen/internal/engine"
	engineImportsProject, err := packageImportsPath(filepath.Join(root, "internal", "engine"), projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if engineImportsProject {
		t.Fatal("the engine imports the project orchestration package")
	}
	projectImportsEngine, err := packageImportsPath(filepath.Join(root, "internal", "project"), enginePath)
	if err != nil {
		t.Fatal(err)
	}
	if !projectImportsEngine {
		t.Fatal("the project orchestration package does not use the engine leaf package")
	}
}

func TestInternalProjectDependencyDirectionRejectsMutation(t *testing.T) {
	directory := t.TempDir()
	source := "package engine\nimport _ \"github.com/IlyaGulya/chgen/internal/project\"\n"
	if err := os.WriteFile(filepath.Join(directory, "engine.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	importsProject, err := packageImportsPath(directory, "github.com/IlyaGulya/chgen/internal/project")
	if err != nil {
		t.Fatal(err)
	}
	if !importsProject {
		t.Fatal("the dependency gate accepted an engine-to-project import")
	}
}

func TestInternalPackageDocumentationMatchesOwnership(t *testing.T) {
	root := moduleRootPath()
	engineDoc := packageDocumentation(t, root, "./internal/engine")
	for _, stale := range []string{
		"Run reads a chgen configuration file",
		"LoadConfig reads the configuration",
	} {
		if strings.Contains(engineDoc, stale) {
			t.Fatalf("the engine documentation contains the old project claim %q", stale)
		}
	}
	projectDoc := packageDocumentation(t, root, "./internal/project")
	if !strings.Contains(projectDoc, "Package project loads chgen project configuration") {
		t.Fatal("the project package documentation does not state its ownership")
	}
}

func TestRootAPISurfaceScannerRejectsInternalAliases(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantAlias bool
	}{
		{name: "alias", body: "type PublicAlias = engine.CHType\n", wantAlias: true},
		{name: "method", body: "type Public struct{}\nfunc (Public) Leak(value engine.CHType) {}\n"},
		{name: "constant selector", body: "const PublicConstant = engine.CommandMany\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "facade.go")
			source := "package chgen\nimport engine \"github.com/IlyaGulya/chgen/internal/engine\"\n" + test.body
			if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
				t.Fatal(err)
			}
			_, alias, internalSignature, _, err := rootAPISurface(root, []string{"facade.go"})
			if err != nil {
				t.Fatal(err)
			}
			if alias != test.wantAlias || !internalSignature {
				t.Fatalf("alias = %v, internal signature = %v", alias, internalSignature)
			}
		})
	}
}

func TestPublicAPIShapeRejectsSourceMutations(t *testing.T) {
	root := moduleRootPath()
	source, err := os.ReadFile(filepath.Join(root, "facade.go"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := readLineBaseline(filepath.Join(root, "testdata", "public-api-baseline.txt"))
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(string) string{
		"extra method": func(value string) string {
			return value + "\nfunc (CHType) Extra() string { return \"\" }\n"
		},
		"extra field": func(value string) string {
			return strings.Replace(value, "type CHType struct {\n", "type CHType struct {\n\tExtra string\n", 1)
		},
		"embedded exported type": func(value string) string {
			return strings.Replace(value, "type CHType struct {\n", "type CHType struct {\n\t*TableEngine\n", 1)
		},
		"promoted method from private type": func(value string) string {
			value = strings.Replace(value, "type CHType struct {\n", "type CHType struct {\n\thelper\n", 1)
			return value + "\ntype helper struct{}\nfunc (helper) Extra() {}\n"
		},
		"changed signature": func(value string) string {
			return strings.Replace(value, "func InferQueryResultType(ddl, statement string) (CHType, error)", "func InferQueryResultType(ddl string, statement []byte) (CHType, error)", 1)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "facade.go"), []byte(mutate(string(source))), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := publicAPIShape(directory, []string{"facade.go"})
			if err != nil {
				t.Fatal(err)
			}
			if reflect.DeepEqual(got, want) {
				t.Fatal("the API shape gate accepted a public API mutation")
			}
		})
	}
}

func TestArtifactPathScannerRejectsRelativeRepositoryRead(t *testing.T) {
	tests := []string{
		`func readArtifact() { _, _ = os.Open("docs/artifact.md") }`,
		`func readArtifact() { path := filepath.Join("testdata", "artifact.json"); _, _ = os.ReadFile(path) }`,
		`func readArtifact() { path := filepath.Join(".", "testdata", "artifact.json"); _, _ = os.ReadFile(path) }`,
		`const artifactPath = ".github/workflows/ci.yml"; func readArtifact() { _, _ = os.Create(artifactPath) }`,
		`func readArtifact() { _, _ = filesystem.Open("docs/artifact.md") }`,
	}
	for index, body := range tests {
		directory := t.TempDir()
		source := "package engine\nimport (\"os\"; filesystem \"os\"; \"path/filepath\")\n" + body
		if err := os.WriteFile(filepath.Join(directory, "unsafe_test.go"), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "repository_test.go"), []byte("package engine\nfunc TestModuleRootPath() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !engineHasUnsafeArtifactPath(directory) {
			t.Fatalf("the artifact path scanner accepted mutation %d", index)
		}
	}
	directory := t.TempDir()
	safe := `package engine
import "os"
func moduleRootPath(parts ...string) string { return "" }
func readArtifact() { _, _ = os.Open(moduleRootPath("docs", "artifact.md")) }
`
	if err := os.WriteFile(filepath.Join(directory, "safe_test.go"), []byte(safe), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "repository_test.go"), []byte("package engine\nfunc TestModuleRootPath() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if engineHasUnsafeArtifactPath(directory) {
		t.Fatal("the artifact path scanner refused moduleRootPath")
	}
}

func TestCommandTargetScannerDistinguishesDotAndEngine(t *testing.T) {
	if !engineCommandIsSilent("go test -tags fuzzoracle -run TestTypeOracle -v .") {
		t.Fatal("the command scanner accepted the root package target")
	}
	if engineCommandIsSilent("go test -tags fuzzoracle -run TestTypeOracle -v ./internal/engine") {
		t.Fatal("the command scanner refused the engine package target")
	}
	if engineCommandIsSilent("go test -tags fuzzoracle -run TestTypeOracle -v /tmp/chgen/internal/engine") {
		t.Fatal("the command scanner refused an absolute engine package target")
	}
	if engineCommandIsSilent("go test -tags fuzzoracle -covermode atomic ./internal/engine") {
		t.Fatal("the command scanner treated a standard flag value as a package target")
	}
	if !engineCommandIsSilent("go test -tags fuzzoracle -run TestTypeOracle -v ${target}") {
		t.Fatal("the command scanner accepted a variable package target")
	}
	if engineCommandIsSilent(`example := "go test -tags fuzzoracle ."`) {
		t.Fatal("the command scanner treated a Go string as a command")
	}
	if !engineCommandIsSilent("go test -tags fuzzoracle -run TestTypeOracle -v") {
		t.Fatal("the command scanner accepted a missing package target")
	}
	if engineCommandIsSilent("The execoracle tag is not part of go test ./...") {
		t.Fatal("the command scanner refused a default-suite description")
	}
	multiline := shellCommandLines("go test -tags=fuzzoracle . \\\n+  -run TestTypeOracle -v\n")
	if len(multiline) != 1 || !engineCommandIsSilent(multiline[0]) {
		t.Fatal("the command scanner accepted a multiline root target")
	}
	if engineCommandIsSilent(`go test -tags "${tag_set}" -run '^$' ./...`) {
		t.Fatal("the command scanner refused an intentional compile-only command")
	}
}

func TestSourceBuildTagRejectsLegacyConstraint(t *testing.T) {
	if got := sourceBuildTag([]byte("// +build fuzzoracle\n\npackage chgen_test\n")); got != "// +build fuzzoracle" {
		t.Fatalf("legacy build tag = %q", got)
	}
}

func readLayoutSnapshot() (layoutSnapshot, error) {
	root := moduleRootPath()
	production, tests, err := rootGoFiles(root)
	if err != nil {
		return layoutSnapshot{}, err
	}
	repositoryFiles, repositoryFunctions, err := packageTestInventory(filepath.Join(root, "internal", "repositorycheck"), "repositorycheck_test")
	if err != nil {
		return layoutSnapshot{}, err
	}
	apiInventoryFunctions, err := testFileFunctions(filepath.Join(root, "internal", "apiinventory", "api_inventory_test.go"), "apiinventory_test")
	if err != nil {
		return layoutSnapshot{}, err
	}
	apiInventoryFileTests, err := testFileFunctions(filepath.Join(root, "internal", "apiinventory", "file_test.go"), "apiinventory")
	if err != nil {
		return layoutSnapshot{}, err
	}
	engineDir := filepath.Join(root, "internal", "engine")
	engineNames, functionNames, tagged, live, err := engineTestInventory(engineDir)
	if err != nil {
		return layoutSnapshot{}, err
	}
	projectNames, projectFunctions, err := packageTestInventory(filepath.Join(root, "internal", "project"), "project")
	if err != nil {
		return layoutSnapshot{}, err
	}
	engineNames, err = appendUniqueInventory(engineNames, projectNames)
	if err != nil {
		return layoutSnapshot{}, err
	}
	functionNames, err = appendUniqueInventory(functionNames, projectFunctions)
	if err != nil {
		return layoutSnapshot{}, err
	}
	baselineFiles, baselineFunctions, err := readEngineBaseline(filepath.Join(root, "testdata", "layout-engine-baseline.txt"))
	if err != nil {
		return layoutSnapshot{}, err
	}
	publicNames, publicAlias, internalSignature, internalDocs, err := rootAPISurface(root, production)
	if err != nil {
		return layoutSnapshot{}, err
	}
	publicShape, err := publicAPIShape(root, production)
	if err != nil {
		return layoutSnapshot{}, err
	}
	baselinePublicShape, err := readLineBaseline(filepath.Join(root, "testdata", "public-api-baseline.txt"))
	if err != nil {
		return layoutSnapshot{}, err
	}
	return layoutSnapshot{
		rootProduction:        production,
		rootTests:             tests,
		repositoryFiles:       repositoryFiles,
		repositoryFunctions:   repositoryFunctions,
		apiInventoryFunctions: apiInventoryFunctions,
		apiInventoryFileTests: apiInventoryFileTests,
		engineFiles:           engineNames,
		engineFunctions:       functionNames,
		baselineFiles:         baselineFiles,
		baselineFunctions:     baselineFunctions,
		tagged:                tagged,
		live:                  live,
		publicNames:           publicNames,
		publicShape:           publicShape,
		baselinePublicShape:   baselinePublicShape,
		hasPublicAlias:        publicAlias,
		hasInternalSignature:  internalSignature,
		hasInternalDocs:       internalDocs,
		hasInternalReflection: publicReflectionUsesInternalType(),
		hasInternalGoDoc:      publicGoDocUsesInternalType(root),
		hasInvalidRootTest:    rootTestsHaveInvalidPackageOrTag(root, tests),
		hasSilentCommand:      repositoryHasSilentEngineCommand(root),
		hasUnsafeArtifactPath: engineHasUnsafeArtifactPath(engineDir),
		hasUnsafeGenerator:    generatorsCanWriteRootGo(root),
		hasInvalidExtraction:  projectExtractionIsInvalid(root),
	}, nil
}

func validateLayoutSnapshot(value layoutSnapshot) error {
	wantProduction := []string{"doc.go", "facade.go"}
	wantTests := []string{
		"api_contract_test.go",
		"cursor_query_test.go",
		"oracle_regression_test.go",
		"facade_test.go",
	}
	if len(value.rootProduction) > 10 || !sameRequiredValues(value.rootProduction, wantProduction) || len(value.rootProduction) != len(wantProduction) {
		return fmt.Errorf("root production Go files = %v, want %v", value.rootProduction, wantProduction)
	}
	if len(value.rootTests) != len(wantTests) || !sameRequiredValues(value.rootTests, wantTests) {
		return fmt.Errorf("root test Go files = %v, want %v", value.rootTests, wantTests)
	}
	if value.hasInvalidRootTest {
		return fmt.Errorf("a root test uses a build tag or a package other than chgen_test")
	}
	wantRepositoryFiles := []string{
		"hooks_test.go",
		"layout_contract_test.go",
		"release_readiness_test.go",
		"repository_test.go",
	}
	if len(value.repositoryFiles) != len(wantRepositoryFiles) || !sameRequiredValues(value.repositoryFiles, wantRepositoryFiles) {
		return fmt.Errorf("repository check files = %v, want %v", value.repositoryFiles, wantRepositoryFiles)
	}
	if !sameRequiredValues(value.repositoryFunctions, expectedRepositoryTestFunctions()) {
		return fmt.Errorf("repository check test roster lost a required function")
	}
	if !sameRequiredValues(value.apiInventoryFunctions, expectedAPIInventoryTestFunctions()) {
		return fmt.Errorf("API inventory test roster lost a required function")
	}
	if !sameRequiredValues(value.apiInventoryFileTests, expectedAPIInventoryFileTestFunctions()) {
		return fmt.Errorf("API inventory file test roster lost a required function")
	}
	if len(value.baselineFiles) != 202 || !sameRequiredValues(value.engineFiles, value.baselineFiles) {
		return fmt.Errorf("engine test roster lost a required file")
	}
	if len(value.baselineFunctions) != 893 || !sameRequiredValues(value.engineFunctions, value.baselineFunctions) {
		return fmt.Errorf("engine test roster lost a required test function")
	}
	wantTagged := expectedTaggedRosters()
	for _, tag := range []string{"fuzzoracle", "execoracle", "axisaudit"} {
		want := wantTagged[tag]
		if !sameRequiredValues(value.tagged[tag], want) {
			return fmt.Errorf("%s test roster = %v, require %v", tag, value.tagged[tag], want)
		}
	}
	if len(value.tagged["invalid"]) != 0 {
		return fmt.Errorf("engine tests have invalid package or build tags: %v", value.tagged["invalid"])
	}
	if !sameRequiredValues(value.live, expectedLiveRoster()) {
		return fmt.Errorf("live test roster = %v, require %v", value.live, expectedLiveRoster())
	}
	if !sameRequiredValues(value.tagged["fuzzoracle"], value.live) {
		return fmt.Errorf("every live test must use the fuzzoracle build tag")
	}
	if !reflect.DeepEqual(value.publicNames, expectedPublicNames()) {
		return fmt.Errorf("public API = %v, want %v", value.publicNames, expectedPublicNames())
	}
	if !reflect.DeepEqual(value.publicShape, value.baselinePublicShape) {
		return fmt.Errorf("public API shape = %q, want %q", value.publicShape, value.baselinePublicShape)
	}
	if value.hasPublicAlias || value.hasInternalSignature || value.hasInternalDocs || value.hasInternalReflection || value.hasInternalGoDoc {
		return fmt.Errorf("the public facade exposes an alias or an internal implementation detail")
	}
	if value.hasSilentCommand {
		return fmt.Errorf("a current command can select engine tests while it targets the root package")
	}
	if value.hasUnsafeArtifactPath {
		return fmt.Errorf("an engine test uses a repository artifact without moduleRootPath")
	}
	if value.hasUnsafeGenerator {
		return fmt.Errorf("a generator can recreate a generated Go file in the module root")
	}
	if value.hasInvalidExtraction {
		return fmt.Errorf("the project leaf package or test-only semantic support has an invalid layout")
	}
	return nil
}

func rootGoFiles(root string) ([]string, []string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, err
	}
	var production, tests []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		if strings.HasSuffix(entry.Name(), "_test.go") {
			tests = append(tests, entry.Name())
		} else {
			production = append(production, entry.Name())
		}
	}
	sort.Strings(production)
	sort.Strings(tests)
	return production, tests, nil
}

func rootTestsHaveInvalidPackageOrTag(root string, names []string) bool {
	set := token.NewFileSet()
	for _, name := range names {
		path := filepath.Join(root, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return true
		}
		file, err := parser.ParseFile(set, path, data, parser.PackageClauseOnly)
		wantPackage := "chgen_test"
		if name == "facade_test.go" {
			wantPackage = "chgen"
		}
		if err != nil || file.Name.Name != wantPackage || sourceBuildTag(data) != "" {
			return true
		}
	}
	return false
}

func sourceBuildTag(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			return ""
		}
		if strings.HasPrefix(line, "//go:build ") {
			return line
		}
		if strings.HasPrefix(line, "// +build ") {
			return line
		}
	}
	return ""
}

func readEngineBaseline(path string) ([]string, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var files, functions []string
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "file "):
			files = append(files, strings.TrimPrefix(line, "file "))
		case strings.HasPrefix(line, "func "):
			functions = append(functions, strings.TrimPrefix(line, "func "))
		case line != "":
			return nil, nil, fmt.Errorf("invalid layout baseline line %q", line)
		}
	}
	sort.Strings(files)
	sort.Strings(functions)
	return files, functions, nil
}

func sameRequiredValues(got, required []string) bool {
	available := make(map[string]bool, len(got))
	for _, value := range got {
		available[value] = true
	}
	for _, value := range required {
		if !available[value] {
			return false
		}
	}
	return true
}

func engineTestInventory(directory string) ([]string, []string, map[string][]string, []string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var files, functions, live []string
	tagged := map[string][]string{"fuzzoracle": {}, "execoracle": {}, "axisaudit": {}, "invalid": {}}
	set := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, "_test.go") || name == "repository_test.go" {
			continue
		}
		files = append(files, name)
		path := filepath.Join(directory, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		buildLine := sourceBuildTag(data)
		for tag := range tagged {
			if buildLine == "//go:build "+tag {
				tagged[tag] = append(tagged[tag], name)
			}
		}
		if strings.HasSuffix(name, "_live_test.go") {
			live = append(live, name)
		}
		file, err := parser.ParseFile(set, path, data, 0)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if file.Name.Name != "engine" || buildLine != "" && buildLine != "//go:build fuzzoracle" &&
			buildLine != "//go:build execoracle" && buildLine != "//go:build axisaudit" {
			tagged["invalid"] = append(tagged["invalid"], name)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && testFunctionPattern.MatchString(function.Name.Name) {
				identity := name + ":" + function.Name.Name
				functions = append(functions, identity)
			}
		}
	}
	sort.Strings(files)
	sort.Strings(functions)
	sort.Strings(live)
	for tag := range tagged {
		sort.Strings(tagged[tag])
	}
	return files, functions, tagged, live, nil
}

func packageTestInventory(directory, packageName string) ([]string, []string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, nil, err
	}
	var files, functions []string
	set := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(directory, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		file, err := parser.ParseFile(set, path, data, 0)
		if err != nil {
			return nil, nil, err
		}
		if file.Name.Name != packageName || sourceBuildTag(data) != "" {
			return nil, nil, fmt.Errorf("%s has an invalid package or build tag", path)
		}
		files = append(files, name)
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && testFunctionPattern.MatchString(function.Name.Name) {
				functions = append(functions, name+":"+function.Name.Name)
			}
		}
	}
	sort.Strings(files)
	sort.Strings(functions)
	return files, functions, nil
}

func testFileFunctions(path, packageName string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, path, data, 0)
	if err != nil {
		return nil, err
	}
	if file.Name.Name != packageName || sourceBuildTag(data) != "" {
		return nil, fmt.Errorf("%s has an invalid package or build tag", path)
	}
	var functions []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Recv == nil && testFunctionPattern.MatchString(function.Name.Name) {
			functions = append(functions, filepath.Base(path)+":"+function.Name.Name)
		}
	}
	sort.Strings(functions)
	return functions, nil
}

func appendUniqueInventory(left, right []string) ([]string, error) {
	seen := make(map[string]bool, len(left)+len(right))
	result := make([]string, 0, len(left)+len(right))
	for _, values := range [][]string{left, right} {
		for _, value := range values {
			if seen[value] {
				return nil, fmt.Errorf("internal test inventory repeats %s", value)
			}
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func packageImportsPath(directory, importPath string) (bool, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false, err
	}
	set := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		file, err := parser.ParseFile(set, path, nil, parser.ImportsOnly)
		if err != nil {
			return false, err
		}
		for _, specification := range file.Imports {
			value, err := strconv.Unquote(specification.Path.Value)
			if err != nil {
				return false, err
			}
			if value == importPath {
				return true, nil
			}
		}
	}
	return false, nil
}

func rootAPISurface(root string, production []string) ([]string, bool, bool, bool, error) {
	set := token.NewFileSet()
	seen := make(map[string]bool)
	publicAlias := false
	internalSignature := false
	internalDocs := false
	for _, name := range production {
		path := filepath.Join(root, name)
		file, err := parser.ParseFile(set, path, nil, parser.ParseComments)
		if err != nil {
			return nil, false, false, false, err
		}
		internalImports := make(map[string]bool)
		for _, item := range file.Imports {
			path := strings.Trim(item.Path.Value, `"`)
			if !strings.Contains(path, "/internal/") {
				continue
			}
			name := filepath.Base(path)
			if item.Name != nil {
				name = item.Name.Name
			}
			internalImports[name] = true
		}
		for _, group := range file.Comments {
			if strings.Contains(group.Text(), "/internal/") || strings.Contains(group.Text(), "internal/engine") {
				internalDocs = true
			}
		}
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if declaration.Name.IsExported() {
					if declaration.Recv == nil {
						seen[declaration.Name.Name] = true
					}
					receiverUsesInternal := declaration.Recv != nil && nodeUsesImport(declaration.Recv, internalImports)
					if receiverUsesInternal || nodeUsesImport(declaration.Type, internalImports) {
						internalSignature = true
					}
				}
			case *ast.GenDecl:
				for _, specification := range declaration.Specs {
					switch specification := specification.(type) {
					case *ast.TypeSpec:
						if specification.Name.IsExported() {
							seen[specification.Name.Name] = true
							publicAlias = publicAlias || specification.Assign.IsValid()
							internalSignature = internalSignature || nodeUsesImport(specification.Type, internalImports)
						}
					case *ast.ValueSpec:
						for _, valueName := range specification.Names {
							if valueName.IsExported() {
								seen[valueName.Name] = true
								internalSignature = internalSignature || nodeUsesImport(specification.Type, internalImports)
								for _, value := range specification.Values {
									internalSignature = internalSignature || nodeUsesImport(value, internalImports)
								}
							}
						}
					}
				}
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, publicAlias, internalSignature, internalDocs, nil
}

func publicAPIShape(root string, production []string) ([]string, error) {
	set := token.NewFileSet()
	var shape []string
	for _, name := range production {
		path := filepath.Join(root, name)
		file, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			return nil, err
		}
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if !declaration.Name.IsExported() {
					continue
				}
				prefix := "func " + declaration.Name.Name
				if declaration.Recv != nil {
					prefix = "method " + renderNode(set, declaration.Recv.List[0].Type) + "." + declaration.Name.Name
				}
				shape = append(shape, prefix+strings.TrimPrefix(renderNode(set, declaration.Type), "func"))
			case *ast.GenDecl:
				for _, specification := range declaration.Specs {
					switch specification := specification.(type) {
					case *ast.TypeSpec:
						if !specification.Name.IsExported() {
							continue
						}
						structure, ok := specification.Type.(*ast.StructType)
						if !ok {
							shape = append(shape, "type "+specification.Name.Name+" "+renderNode(set, specification.Type))
							continue
						}
						shape = append(shape, "type "+specification.Name.Name+" struct")
						for _, field := range structure.Fields.List {
							if len(field.Names) == 0 {
								shape = append(shape, "embedded "+specification.Name.Name+"."+renderNode(set, field.Type))
							}
							for _, fieldName := range field.Names {
								if fieldName.IsExported() {
									shape = append(shape, "field "+specification.Name.Name+"."+fieldName.Name+" "+renderNode(set, field.Type))
								}
							}
						}
					case *ast.ValueSpec:
						for index, valueName := range specification.Names {
							if !valueName.IsExported() {
								continue
							}
							record := strings.ToLower(declaration.Tok.String()) + " " + valueName.Name
							if specification.Type != nil {
								record += " " + renderNode(set, specification.Type)
							}
							if len(specification.Values) != 0 {
								valueIndex := index
								if valueIndex >= len(specification.Values) {
									valueIndex = len(specification.Values) - 1
								}
								record += " = " + renderNode(set, specification.Values[valueIndex])
							}
							shape = append(shape, record)
						}
					}
				}
			}
		}
	}
	sort.Strings(shape)
	return shape, nil
}

func renderNode(set *token.FileSet, node any) string {
	var output bytes.Buffer
	if err := format.Node(&output, set, node); err != nil {
		panic(err)
	}
	return output.String()
}

func readLineBaseline(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			result = append(result, line)
		}
	}
	sort.Strings(result)
	return result, nil
}

func nodeUsesImport(node ast.Node, imports map[string]bool) bool {
	if node == nil {
		return false
	}
	found := false
	ast.Inspect(node, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		name, ok := selector.X.(*ast.Ident)
		if ok && imports[name.Name] {
			found = true
		}
		return !found
	})
	return found
}

func publicReflectionUsesInternalType() bool {
	types := []reflect.Type{
		reflect.TypeOf(chgen.CHType{}), reflect.TypeOf(chgen.Column{}), reflect.TypeOf(chgen.Config{}),
		reflect.TypeOf(chgen.ExternalColumn{}), reflect.TypeOf(chgen.ExternalParam{}), reflect.TypeOf(chgen.InputEntry{}),
		reflect.TypeOf(chgen.PackageConfig{}), reflect.TypeOf(chgen.Param{}), reflect.TypeOf(chgen.Query{}),
		reflect.TypeOf(chgen.Result{}), reflect.TypeOf(chgen.Schema{}), reflect.TypeOf(chgen.SchemaCatalogs{}),
		reflect.TypeOf(chgen.Table{}), reflect.TypeOf(chgen.TableEngine{}), reflect.TypeOf(chgen.Command("")),
	}
	seen := make(map[reflect.Type]bool)
	var visit func(reflect.Type) bool
	visit = func(value reflect.Type) bool {
		if value == nil || seen[value] {
			return false
		}
		seen[value] = true
		if strings.Contains(value.PkgPath(), "/internal/") || strings.Contains(value.String(), "/internal/") {
			return true
		}
		switch value.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			return visit(value.Elem()) || value.Kind() == reflect.Map && visit(value.Key())
		case reflect.Struct:
			for index := 0; index < value.NumField(); index++ {
				if visit(value.Field(index).Type) {
					return true
				}
			}
		}
		return false
	}
	for _, value := range types {
		if visit(value) {
			return true
		}
	}
	return false
}

func publicGoDocUsesInternalType(root string) bool {
	command := exec.Command("go", "doc", "-all", ".")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		return true
	}
	text := string(output)
	return strings.Contains(text, "/internal/") || strings.Contains(text, "internal/engine") ||
		strings.Contains(text, "engine.Facade")
}

func repositoryHasSilentEngineCommand(root string) bool {
	paths, err := activeCommandSourcePaths(root)
	if err != nil {
		return true
	}
	for _, name := range paths {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return true
		}
		for _, command := range shellCommandLines(string(data)) {
			if engineCommandIsSilent(command) {
				fmt.Fprintf(os.Stderr, "silent engine test command in %s: %s\n", name, command)
				return true
			}
		}
	}
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		return true
	}
	for _, marker := range []string{
		"go test -tags fuzzoracle -run TestTypeOracle -count=1 -timeout 40m -v ./internal/engine",
		"go test -tags execoracle -count=1 -timeout 40m -v ./internal/engine",
	} {
		if !strings.Contains(string(workflow), marker) {
			return true
		}
	}
	return false
}

func activeCommandSourcePaths(root string) ([]string, error) {
	historical := map[string]bool{
		"docs/binary-operator-fallback-survey.md": true,
	}
	paths := []string{"README.md"}
	for _, directory := range []string{"docs", "scripts", ".github/workflows", "internal/engine"} {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(directory)), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			extension := filepath.Ext(entry.Name())
			if extension != ".md" && extension != ".sh" && extension != ".yml" && extension != ".yaml" && extension != ".go" {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if !historical[relative] {
				paths = append(paths, relative)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func shellCommandLines(text string) []string {
	var commands []string
	var continued strings.Builder
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if continued.Len() != 0 {
			continued.WriteByte(' ')
		}
		continued.WriteString(strings.TrimSuffix(line, "\\"))
		if strings.HasSuffix(line, "\\") {
			continue
		}
		command := continued.String()
		continued.Reset()
		if strings.Contains(command, "go test") {
			commands = append(commands, command)
		}
	}
	if continued.Len() != 0 && strings.Contains(continued.String(), "go test") {
		commands = append(commands, continued.String())
	}
	return commands
}

func engineCommandIsSilent(command string) bool {
	arguments, ok := goTestArguments(command)
	if !ok {
		return false
	}
	joined := strings.Join(arguments, " ")
	sensitive := strings.Contains(joined, "fuzzoracle") || strings.Contains(joined, "execoracle") ||
		strings.Contains(joined, "axisaudit") || strings.Contains(joined, "${tag_set}") ||
		strings.Contains(joined, "TestGolden") || strings.Contains(joined, "TestFunctionProbe") ||
		strings.Contains(joined, "TestTypeOracle") || strings.Contains(joined, "TestExecOracle") ||
		strings.Contains(joined, "TestWrapperGrid") || strings.Contains(joined, "TestCombinatorGrid") ||
		strings.Contains(joined, "TestStatementOracle") || strings.Contains(joined, "TestSetOperationMatrix")
	if !sensitive {
		return false
	}
	targets, compileOnly := goTestTargets(arguments)
	if len(targets) == 0 {
		return true
	}
	for _, target := range targets {
		target = filepath.ToSlash(target)
		if compileOnly && target == "./..." {
			continue
		}
		if target == "./internal/engine" || target == "./internal/engine/..." || strings.HasSuffix(target, "/internal/engine") {
			continue
		}
		return true
	}
	return false
}

func goTestArguments(command string) ([]string, bool) {
	tokens := shellTokens(command)
	for index := 0; index+1 < len(tokens); index++ {
		if tokens[index] != "go" || tokens[index+1] != "test" {
			continue
		}
		validPrefix := true
		for _, prefix := range tokens[:index] {
			if prefix == "if" || prefix == "!" || prefix == "time" || prefix == "exec" || prefix == "run:" ||
				prefix == "-" || prefix == "$" || strings.Contains(prefix, "=") {
				continue
			}
			validPrefix = false
		}
		if !validPrefix {
			return nil, false
		}
		arguments := tokens[index+2:]
		for end, token := range arguments {
			if token == ";" || token == "&&" || token == "||" || token == "then" {
				arguments = arguments[:end]
				break
			}
		}
		return arguments, true
	}
	return nil, false
}

func shellTokens(command string) []string {
	var tokens []string
	var current strings.Builder
	quote := rune(0)
	escaped := false
	flush := func() {
		if current.Len() != 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}
	for _, character := range command {
		if escaped {
			current.WriteRune(character)
			escaped = false
			continue
		}
		if character == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				current.WriteRune(character)
			}
			continue
		}
		if character == '\'' || character == '"' || character == '`' {
			quote = character
			continue
		}
		if character == ' ' || character == '\t' || character == '\r' || character == '\n' {
			flush()
			continue
		}
		if character == ';' {
			flush()
			tokens = append(tokens, ";")
			continue
		}
		current.WriteRune(character)
	}
	flush()
	return tokens
}

func goTestTargets(arguments []string) ([]string, bool) {
	valueFlags := map[string]bool{
		"-bench": true, "-benchtime": true, "-count": true, "-covermode": true, "-coverpkg": true, "-coverprofile": true,
		"-cpu": true, "-exec": true, "-gcflags": true, "-ldflags": true, "-list": true, "-mod": true, "-p": true,
		"-parallel": true, "-pkgdir": true, "-run": true, "-shuffle": true, "-tags": true, "-timeout": true,
		"-toolexec": true, "-vet": true,
		"-chgen-grid-url": true,
	}
	var targets []string
	compileOnly := false
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if strings.HasPrefix(argument, "-") {
			name, value, hasValue := strings.Cut(argument, "=")
			if valueFlags[name] && !hasValue && index+1 < len(arguments) {
				index++
				value = arguments[index]
			}
			if name == "-run" && value == "^$" {
				compileOnly = true
			}
			continue
		}
		targets = append(targets, argument)
	}
	return targets, compileOnly
}

func engineHasUnsafeArtifactPath(directory string) bool {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return true
	}
	set := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(set, filepath.Join(directory, entry.Name()), nil, 0)
		if err != nil {
			return true
		}
		files = append(files, file)
	}
	bindings := make(map[string]ast.Expr)
	for _, file := range files {
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok {
				continue
			}
			collectValueBindings(general.Specs, bindings)
		}
	}
	for _, file := range files {
		imports := fileImportPaths(file)
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			local := cloneExprBindings(bindings)
			ast.Inspect(function.Body, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.AssignStmt:
					for index, left := range node.Lhs {
						name, ok := left.(*ast.Ident)
						if ok && index < len(node.Rhs) {
							local[name.Name] = node.Rhs[index]
						}
					}
				case *ast.DeclStmt:
					if general, ok := node.Decl.(*ast.GenDecl); ok {
						collectValueBindings(general.Specs, local)
					}
				}
				return true
			})
			unsafe := false
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 || !isArtifactIOCall(call.Fun, imports) {
					return true
				}
				if artifactExpressionIsUnsafe(call.Args[0], local, imports, make(map[string]bool)) {
					unsafe = true
					return false
				}
				return true
			})
			if unsafe {
				return true
			}
		}
	}
	helper, err := os.ReadFile(filepath.Join(directory, "repository_test.go"))
	return err != nil || !strings.Contains(string(helper), "func TestModuleRootPath")
}

func fileImportPaths(file *ast.File) map[string]string {
	result := make(map[string]string)
	for _, item := range file.Imports {
		path, err := strconv.Unquote(item.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if item.Name != nil {
			name = item.Name.Name
		}
		result[name] = path
	}
	return result
}

func collectValueBindings(specifications []ast.Spec, bindings map[string]ast.Expr) {
	for _, specification := range specifications {
		value, ok := specification.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for index, name := range value.Names {
			if index < len(value.Values) {
				bindings[name.Name] = value.Values[index]
			}
		}
	}
}

func cloneExprBindings(source map[string]ast.Expr) map[string]ast.Expr {
	result := make(map[string]ast.Expr, len(source))
	for name, value := range source {
		result[name] = value
	}
	return result
}

func isArtifactIOCall(function ast.Expr, imports map[string]string) bool {
	selector, ok := function.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok || imports[packageName.Name] != "os" {
		return false
	}
	switch selector.Sel.Name {
	case "Create", "Open", "OpenFile", "ReadDir", "ReadFile", "Remove", "Stat", "WriteFile":
		return true
	default:
		return false
	}
}

func artifactExpressionIsUnsafe(expression ast.Expr, bindings map[string]ast.Expr, imports map[string]string, visiting map[string]bool) bool {
	switch expression := expression.(type) {
	case *ast.BasicLit:
		if expression.Kind != token.STRING {
			return false
		}
		value, err := strconv.Unquote(expression.Value)
		return err == nil && repositoryRelativeArtifact(value)
	case *ast.Ident:
		if visiting[expression.Name] {
			return false
		}
		value, ok := bindings[expression.Name]
		if !ok {
			return false
		}
		visiting[expression.Name] = true
		defer delete(visiting, expression.Name)
		return artifactExpressionIsUnsafe(value, bindings, imports, visiting)
	case *ast.BinaryExpr:
		return artifactExpressionIsUnsafe(expression.X, bindings, imports, visiting) || artifactExpressionIsUnsafe(expression.Y, bindings, imports, visiting)
	case *ast.CallExpr:
		if name, ok := expression.Fun.(*ast.Ident); ok && name.Name == "moduleRootPath" {
			return false
		}
		if selector, ok := expression.Fun.(*ast.SelectorExpr); ok {
			if packageName, ok := selector.X.(*ast.Ident); ok && imports[packageName.Name] == "path/filepath" && len(expression.Args) != 0 {
				switch selector.Sel.Name {
				case "Join":
					if artifactExpressionIsUnsafe(expression.Args[0], bindings, imports, visiting) {
						return true
					}
					prefix, known := artifactLiteralString(expression.Args[0], bindings, make(map[string]bool))
					if !known || prefix != "." && prefix != "" {
						return false
					}
					for _, argument := range expression.Args[1:] {
						if artifactExpressionIsUnsafe(argument, bindings, imports, visiting) {
							return true
						}
					}
					return false
				case "Clean", "FromSlash":
					return artifactExpressionIsUnsafe(expression.Args[0], bindings, imports, visiting)
				}
			}
		}
		for _, argument := range expression.Args {
			if artifactExpressionIsUnsafe(argument, bindings, imports, visiting) {
				return true
			}
		}
	}
	return false
}

func artifactLiteralString(expression ast.Expr, bindings map[string]ast.Expr, visiting map[string]bool) (string, bool) {
	switch expression := expression.(type) {
	case *ast.BasicLit:
		if expression.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(expression.Value)
		return value, err == nil
	case *ast.Ident:
		if visiting[expression.Name] {
			return "", false
		}
		value, ok := bindings[expression.Name]
		if !ok {
			return "", false
		}
		visiting[expression.Name] = true
		defer delete(visiting, expression.Name)
		return artifactLiteralString(value, bindings, visiting)
	case *ast.BinaryExpr:
		if expression.Op != token.ADD {
			return "", false
		}
		left, leftOK := artifactLiteralString(expression.X, bindings, visiting)
		right, rightOK := artifactLiteralString(expression.Y, bindings, visiting)
		return left + right, leftOK && rightOK
	default:
		return "", false
	}
}

func repositoryRelativeArtifact(value string) bool {
	value = filepath.ToSlash(value)
	return value == "go.mod" || value == "go.sum" || value == "README.md" ||
		value == "testdata" || value == "docs" || value == ".github" || value == "scripts" ||
		strings.HasPrefix(value, "testdata/") || strings.HasPrefix(value, "docs/") ||
		strings.HasPrefix(value, ".github/") || strings.HasPrefix(value, "scripts/")
}

func generatorsCanWriteRootGo(root string) bool {
	typeSpecimenCommand, err := os.ReadFile(filepath.Join(root, "internal", "tooling", "cmd", "typespecimens", "main.go"))
	if err != nil || !strings.Contains(string(typeSpecimenCommand), `"internal/engine/type_specimen_catalog_generated_test.go"`) {
		return true
	}
	typeSpecimenGenerator, err := os.ReadFile(filepath.Join(root, "internal", "typespecimen", "file.go"))
	if err != nil || !strings.Contains(string(typeSpecimenGenerator), "func GeneratedGo(packageName string") {
		return true
	}
	semanticGenerator, err := os.ReadFile(filepath.Join(root, "internal", "tooling", "cmd", "gensemantics", "main.go"))
	if err != nil || !strings.Contains(string(semanticGenerator), `flag.String("root", "internal/engine"`) ||
		!strings.Contains(string(semanticGenerator), `filepath.Join(*root, "semantic_roster_generated_test.go")`) {
		return true
	}
	directive, err := os.ReadFile(filepath.Join(root, "internal", "engine", "registry.go"))
	if err != nil || !strings.Contains(string(directive), "//go:generate go run ../tooling/cmd/gensemantics -root .") {
		return true
	}
	verification, err := os.ReadFile(filepath.Join(root, "scripts", "verify.sh"))
	if err != nil {
		return true
	}
	for _, command := range []string{
		`"${repo_root}/scripts/verify-layout.sh"`,
		"go run ./internal/tooling/cmd/gensemantics -check",
		"go run ./internal/tooling/cmd/typespecimens -check",
	} {
		if !strings.Contains(string(verification), command) {
			return true
		}
	}
	layoutVerification, err := os.ReadFile(filepath.Join(root, "scripts", "verify-layout.sh"))
	if err != nil || !strings.Contains(string(layoutVerification), "--- PASS: TestStandardGoLayoutContract") {
		return true
	}
	return false
}

func projectExtractionIsInvalid(root string) bool {
	required := []string{
		"internal/tooling/cmd/gensemantics/main.go",
		"internal/apiinventory/api_inventory_test.go",
		"internal/engine/semantic_fact_support_test.go",
		"internal/engine/semantic_roster_generated_test.go",
		"internal/project/config.go",
		"internal/project/config_test.go",
		"internal/project/doc.go",
		"internal/project/inputs.go",
		"internal/project/inputs_test.go",
		"internal/project/run.go",
		"internal/project/run_test.go",
		"internal/repositorycheck/hooks_test.go",
		"internal/repositorycheck/layout_contract_test.go",
		"internal/repositorycheck/release_readiness_test.go",
		"internal/repositorycheck/repository_test.go",
	}
	for _, name := range required {
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil || info.IsDir() {
			return true
		}
	}
	forbidden := []string{
		"api_inventory_test.go",
		"internal/engine/config.go",
		"internal/engine/inputs.go",
		"internal/engine/run.go",
		"internal/engine/semantic_fact.go",
		"internal/engine/semantic_roster_generated.go",
		"internal/gensemantics/main.go",
		"layout_contract_test.go",
		"release_readiness_test.go",
	}
	for _, name := range forbidden {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err == nil || !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

func expectedRepositoryTestFunctions() []string {
	return strings.Fields(`
		hooks_test.go:TestPreCommitChecksStagedFormatting
		layout_contract_test.go:TestArtifactPathScannerRejectsRelativeRepositoryRead
		layout_contract_test.go:TestCommandTargetScannerDistinguishesDotAndEngine
		layout_contract_test.go:TestInternalPackageDocumentationMatchesOwnership
		layout_contract_test.go:TestInternalProjectDependencyDirection
		layout_contract_test.go:TestInternalProjectDependencyDirectionRejectsMutation
		layout_contract_test.go:TestPublicCommandSurface
		layout_contract_test.go:TestPublicCommandSurfaceRejectsMutations
		layout_contract_test.go:TestPublicAPIShapeRejectsSourceMutations
		layout_contract_test.go:TestRootAPISurfaceScannerRejectsInternalAliases
		layout_contract_test.go:TestSourceBuildTagRejectsLegacyConstraint
		layout_contract_test.go:TestStandardGoLayoutContract
		layout_contract_test.go:TestStandardGoLayoutContractAllowsValidAdditions
		layout_contract_test.go:TestStandardGoLayoutContractRejectsMutations
		release_readiness_test.go:TestReleaseCIRunsPinnedLiveGates
		release_readiness_test.go:TestReleaseDependenciesHaveNoLocalReplacement
		release_readiness_test.go:TestReleaseMetadataIsComplete
		release_readiness_test.go:TestReleaseVerificationRunsTheOfflineOracle
		repository_test.go:TestModuleRootPath
	`)
}

func expectedAPIInventoryTestFunctions() []string {
	return strings.Fields(`
		api_inventory_test.go:TestClickHouseAPIInventoryMatchesServer
		api_inventory_test.go:TestPinnedClickHouseAPIInventory
		api_inventory_test.go:TestPinnedClickHouseAPIInventoryRejectsMissingClasses
	`)
}

func expectedAPIInventoryFileTestFunctions() []string {
	return strings.Fields(`
		file_test.go:TestFunctionFactsRemainInTheInventoryFormat
		file_test.go:TestFunctionsQueryDoesNotCollectDescriptions
		file_test.go:TestLoadRejectsRetiredFunctionProse
	`)
}

func packageDocumentation(t *testing.T, root, packagePath string) string {
	t.Helper()
	command := exec.Command("go", "doc", packagePath)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("read documentation for %s: %v\n%s", packagePath, err, output)
	}
	return string(output)
}

func expectedPublicNames() []string {
	return []string{
		"CHType", "Column", "Command", "CommandExec", "CommandMany", "CommandOne", "Config",
		"ExternalColumn", "ExternalParam", "Generate", "InferExpressionType", "InferQueryResultType",
		"InputEntry", "LoadConfig", "MeasuredCHVersion", "PackageConfig", "Param", "ParseQueryFiles",
		"ParseSchemaCatalogs", "Query", "Result", "Run", "Schema", "SchemaCatalogs", "Table", "TableEngine",
	}
}

func expectedTaggedRosters() map[string][]string {
	return map[string][]string{
		"fuzzoracle": strings.Fields("aggregate_combinator_grid_test.go aggregate_geo_alias_live_test.go aggregate_signature_live_test.go array_join_matrix_live_test.go curated_signal_contract_test.go final_matrix_live_test.go function_class_sweep_test.go function_probe_live_test.go grid_version_gate_test.go higher_order_matrix_live_test.go is_null_expr_live_test.go lambda_boundary_live_test.go nested_scope_matrix_live_test.go null_literal_live_test.go oracle_fixture_schema_guard_test.go oracle_widened_fixture_reachability_test.go set_operation_matrix_live_test.go statementoracle_test.go temporal_param_live_test.go toyyyymmdd_live_test.go typeoracle_execwitness_test.go typeoracle_fuzz_test.go typeoracle_sampling_plan_test.go window_geo_alias_live_test.go window_matrix_live_test.go window_validation_live_test.go wrappergrid_test.go"),
		"execoracle": strings.Fields("array_join_value_probes_test.go execoracle_runner_test.go execoracle_test.go execoracle_value_test.go final_value_probes_test.go nested_scope_value_probes_test.go set_operation_value_probes_test.go"),
		"axisaudit":  {"axisaudit_test.go"},
	}
}

func expectedLiveRoster() []string {
	return strings.Fields("aggregate_geo_alias_live_test.go aggregate_signature_live_test.go array_join_matrix_live_test.go final_matrix_live_test.go function_probe_live_test.go higher_order_matrix_live_test.go is_null_expr_live_test.go lambda_boundary_live_test.go nested_scope_matrix_live_test.go null_literal_live_test.go set_operation_matrix_live_test.go temporal_param_live_test.go toyyyymmdd_live_test.go window_geo_alias_live_test.go window_matrix_live_test.go window_validation_live_test.go")
}

func cloneLayoutSnapshot(value layoutSnapshot) layoutSnapshot {
	result := value
	result.rootProduction = append([]string(nil), value.rootProduction...)
	result.rootTests = append([]string(nil), value.rootTests...)
	result.repositoryFiles = append([]string(nil), value.repositoryFiles...)
	result.repositoryFunctions = append([]string(nil), value.repositoryFunctions...)
	result.apiInventoryFunctions = append([]string(nil), value.apiInventoryFunctions...)
	result.apiInventoryFileTests = append([]string(nil), value.apiInventoryFileTests...)
	result.engineFiles = append([]string(nil), value.engineFiles...)
	result.engineFunctions = append([]string(nil), value.engineFunctions...)
	result.baselineFiles = append([]string(nil), value.baselineFiles...)
	result.baselineFunctions = append([]string(nil), value.baselineFunctions...)
	result.live = append([]string(nil), value.live...)
	result.publicNames = append([]string(nil), value.publicNames...)
	result.publicShape = append([]string(nil), value.publicShape...)
	result.baselinePublicShape = append([]string(nil), value.baselinePublicShape...)
	result.tagged = make(map[string][]string, len(value.tagged))
	for tag, files := range value.tagged {
		result.tagged[tag] = append([]string(nil), files...)
	}
	return result
}
