package engine

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The golden corpus.
//
// Every other test in this package asserts FRAGMENTS of the generated
// text: it looks for a substring and it says nothing about the rest of
// the file. That is enough to prove one rule, but it cannot show that a
// merge removed a code path, because the removed path has no assertion
// that names it.
//
// A golden case pins the WHOLE output. If two branches generate
// different Go for the same input, the difference is a text difference
// in a committed file, and a merge that drops a path fails immediately.
//
// A case is one directory under testdata/golden. Its files are:
//
//	schema.sql     required   the CREATE TABLE catalog
//	queries.sql    required   the annotated queries
//	external.sql   optional   the external-table row catalog
//	want.go        expected   the complete generated Go source
//	want_error.txt expected   the exact error text, for a refusal case
//
// A case gives want.go OR want_error.txt, never both and never neither.
// The expected error text is pinned in full, because the refusal
// messages are a product of this project: they tell a user what
// ClickHouse accepts, thus a change to the wording is a change to the
// product and must be visible in a review.
//
// The corpus needs NO ClickHouse server. Generation is a pure function
// of the SQL text, thus `go test ./...` stays offline. The types that
// the corpus pins were measured against a real server by the tests that
// introduced each rule; this corpus locks the text, not the truth.
//
// TO REGENERATE, after an INTENDED behaviour change:
//
//	go test ./internal/engine -run TestGolden -update
//
// Then READ the diff. A change to a want file is a change to the code
// that users get. If the diff has a line that your change does not
// explain, the change did more than you intended.
//
// TO ADD A CASE: make the directory, write schema.sql and queries.sql,
// run the command above, and read the new want file before you commit
// it. The corpus is only worth its cost if a person checks what the
// update wrote.

var updateGolden = flag.Bool("update", false, "rewrite the golden files under testdata/golden from the current generator output")

// goldenPackageName is the package name for every generated case. It is
// fixed so that a diff between two cases shows a real difference and not
// a name.
const goldenPackageName = "golden"

func TestGolden(t *testing.T) {
	root := moduleRootPath("testdata", "golden")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read golden corpus: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("the golden corpus is empty")
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			runGoldenCase(t, filepath.Join(root, name))
		})
	}
}

func runGoldenCase(t *testing.T, dir string) {
	t.Helper()

	schemaPath := filepath.Join(dir, "schema.sql")
	queriesPath := filepath.Join(dir, "queries.sql")
	externalPath := filepath.Join(dir, "external.sql")
	wantSourcePath := filepath.Join(dir, "want.go")
	wantErrorPath := filepath.Join(dir, "want_error.txt")

	_, sourceExists := readOptionalGoldenFile(t, wantSourcePath)
	_, errorExists := readOptionalGoldenFile(t, wantErrorPath)
	if !*updateGolden {
		switch {
		case sourceExists && errorExists:
			t.Fatalf("%s has both want.go and want_error.txt; a case has exactly one expectation", dir)
		case !sourceExists && !errorExists:
			t.Fatalf("%s has neither want.go nor want_error.txt; run with -update to write one", dir)
		}
	}

	got, genErr := generateGoldenCase(t, schemaPath, queriesPath, externalPath)

	if *updateGolden {
		if genErr != nil {
			writeGoldenFile(t, wantErrorPath, goldenRefusalText(genErr))
			removeGoldenFile(t, wantSourcePath)
			return
		}
		writeGoldenFile(t, wantSourcePath, got)
		removeGoldenFile(t, wantErrorPath)
		return
	}

	if errorExists {
		want, _ := readOptionalGoldenFile(t, wantErrorPath)
		if genErr == nil {
			t.Fatalf("generation succeeded, want the refusal:\n%s", want)
		}
		if gotText, wantText := goldenRefusalText(genErr), want; gotText != wantText {
			t.Fatalf("refusal text differs\n got: %q\nwant: %q", gotText, wantText)
		}
		return
	}

	if genErr != nil {
		t.Fatalf("generation failed, want the golden source: %v", genErr)
	}
	want, _ := readOptionalGoldenFile(t, wantSourcePath)
	if got != want {
		t.Fatalf("generated source differs from %s\n%s\nregenerate with: go test ./internal/engine -run TestGolden -update",
			wantSourcePath, firstGoldenDifference(want, got))
	}
}

// generateGoldenCase runs the same pipeline that cmd/chgen runs, from the
// SQL files to the formatted Go source.
func generateGoldenCase(t *testing.T, schemaPath, queriesPath, externalPath string) (string, error) {
	t.Helper()
	schemaText, ok := readOptionalGoldenFile(t, schemaPath)
	if !ok {
		t.Fatalf("%s is missing; every case needs a schema", schemaPath)
	}
	queryText, ok := readOptionalGoldenFile(t, queriesPath)
	if !ok {
		t.Fatalf("%s is missing; every case needs queries", queriesPath)
	}

	// The legacy ParseSchema and ParseWithSchemas front doors are gone.
	// These helpers route through the catalog pipeline that cmd/chgen
	// uses, thus a golden file records the output of the real path.
	schema, err := schemaFromDDLErr(t, schemaText)
	if err != nil {
		return "", err
	}
	var externalSchema *Schema
	if externalText, ok := readOptionalGoldenFile(t, externalPath); ok {
		externalSchema, err = schemaFromDDLErr(t, externalText)
		if err != nil {
			return "", err
		}
	}
	queries, err := parseQueriesWithCatalogsErr(t, queryText, schema, externalSchema)
	if err != nil {
		return "", err
	}
	generated, err := Generate(goldenPackageName, queries)
	if err != nil {
		return "", err
	}
	return string(generated), nil
}

func readOptionalGoldenFile(t *testing.T, path string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data), true
}

func writeGoldenFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Logf("updated %s", path)
}

func removeGoldenFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove %s: %v", path, err)
	}
}

// firstGoldenDifference reports the first line that differs, with a small
// amount of context. A full diff of a generated file is too long to read
// in a test log, and the first difference is almost always the cause.
func firstGoldenDifference(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	limit := len(wantLines)
	if len(gotLines) < limit {
		limit = len(gotLines)
	}
	for index := 0; index < limit; index++ {
		if wantLines[index] != gotLines[index] {
			var builder strings.Builder
			builder.WriteString("first difference at line ")
			builder.WriteString(goldenLineNumber(index + 1))
			builder.WriteString("\n")
			for context := index - 2; context < index; context++ {
				if context >= 0 {
					builder.WriteString("  " + wantLines[context] + "\n")
				}
			}
			builder.WriteString("- want: " + wantLines[index] + "\n")
			builder.WriteString("+ got:  " + gotLines[index] + "\n")
			return builder.String()
		}
	}
	return "the files agree up to line " + goldenLineNumber(limit) +
		"; want has " + goldenLineNumber(len(wantLines)) +
		" lines and got has " + goldenLineNumber(len(gotLines)) + " lines"
}

func goldenLineNumber(value int) string {
	digits := ""
	if value == 0 {
		return "0"
	}
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// TestGoldenSourceCompiles type-checks every want.go with the real
// dependencies. A golden file that pins text which does not build would
// make the corpus worse than no corpus. It would let a broken generator
// stay green. The check uses an isolated module with pinned dependencies.
//
// Set CHGEN_SKIP_GOLDEN_BUILD=1 to skip it if the Go build cache is not
// usable in your environment.
func TestGoldenSourceCompiles(t *testing.T) {
	if os.Getenv("CHGEN_SKIP_GOLDEN_BUILD") != "" {
		t.Skip("CHGEN_SKIP_GOLDEN_BUILD is set")
	}
	if testing.Short() {
		t.Skip("the golden build check is slow; -short skips it")
	}

	root := moduleRootPath("testdata", "golden")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read golden corpus: %v", err)
	}

	buildRoot := newGeneratedCompileModule(t)

	packages := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		source, ok := readOptionalGoldenFile(t, filepath.Join(root, entry.Name(), "want.go"))
		if !ok {
			continue // a refusal case generates no source
		}
		caseDir := filepath.Join(buildRoot, entry.Name())
		if err := os.MkdirAll(caseDir, 0o755); err != nil {
			t.Fatalf("create %s: %v", caseDir, err)
		}
		if err := os.WriteFile(filepath.Join(caseDir, "queries.go"), []byte(source), 0o644); err != nil {
			t.Fatalf("write case source: %v", err)
		}
		packages = append(packages, "./"+entry.Name())
	}
	if len(packages) == 0 {
		t.Fatal("no golden source to build")
	}

	command := exec.Command("go", append([]string{"build"}, packages...)...)
	command.Dir = buildRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("the golden source does not build: %v\n%s", err, output)
	}
}

func newGeneratedCompileModule(t *testing.T) string {
	t.Helper()
	buildRoot := t.TempDir()
	fixtureRoot := moduleRootPath("internal", "engine", "testdata", "generatedcompile")
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(fixtureRoot, name))
		if err != nil {
			t.Fatalf("read generated compile fixture %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(buildRoot, name), data, 0o644); err != nil {
			t.Fatalf("write generated compile fixture %s: %v", name, err)
		}
	}
	return buildRoot
}

// goldenRefusalText makes the refusal text stable across machines.
//
// The catalog pipeline needs real files, thus a diagnostic carries the
// temporary path of the query file. That path changes with every run and
// with every machine, so a golden file that held it could never match.
// Remove the leading "<path>:<line>: " part and keep the line number,
// because the line is a product of the tool and worth pinning.
func goldenRefusalText(err error) string {
	text := err.Error()
	if index := strings.Index(text, "queries.sql:"); index >= 0 {
		text = text[index+len("queries.sql:"):]
		if colon := strings.Index(text, ": "); colon >= 0 {
			text = "line " + text[:colon] + ": " + text[colon+2:]
		}
	}
	return text + "\n"
}
