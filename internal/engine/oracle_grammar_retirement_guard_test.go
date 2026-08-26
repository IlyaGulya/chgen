package engine

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var historicalOracleEvidenceSHA256 = map[string]string{
	"docs/binary-operator-fallback-survey.md": "dcfb3d9fb2e395292ac0c2d0e789a3a0909c613c738b035b50ed33e897da7ee1",
}

func TestHistoricalOracleEvidenceIsImmutable(t *testing.T) {
	for path, want := range historicalOracleEvidenceSHA256 {
		data, err := os.ReadFile(moduleRootPath(filepath.FromSlash(path)))
		if err != nil {
			t.Errorf("read historical evidence %s: %v", path, err)
			continue
		}
		got := fmt.Sprintf("%x", sha256.Sum256(data))
		if got != want {
			t.Errorf("historical evidence %s changed: got SHA-256 %s, want %s", path, got, want)
		}
	}
}

func TestRepositoryHasNoActiveVersionedGrammarAxis(t *testing.T) {
	retiredEnvironmentKey := strings.Join([]string{"CHGEN", "ORACLE", "GRAMMAR"}, "_")
	allowedRetirementReferences := map[string]bool{
		"README.md": true,
		"internal/engine/ci_profile_contract_test.go":             true,
		"docs/ci-and-the-oracle-baseline.md":                      true,
		"docs/historical-oracle-evidence.md":                      true,
		"internal/engine/oracle_grammar_retirement_guard_test.go": true,
	}
	allowedHistoricalKindReferences := map[string]bool{
		"internal/engine/argument_domain.go":           true,
		"internal/tooling/cmd/oraclegate/main.go":      true,
		"internal/tooling/cmd/oraclegate/main_test.go": true,
		"internal/engine/comparability_domain_test.go": true,
		"internal/oraclereport/baseline.go":            true,
		"internal/oraclereport/baseline_test.go":       true,
		"internal/oraclereport/legacy_test.go":         true,
		"testdata/oracle-baseline.json":                true,
		"internal/engine/union_retirement_test.go":     true,
	}
	historical := map[string]bool{}
	for path := range historicalOracleEvidenceSHA256 {
		historical[path] = true
	}
	versionedHelper := regexp.MustCompile(`\b(?:v2|v3)[A-Z][A-Za-z0-9_]*\b`)
	versionedGrammar := regexp.MustCompile(`(?i)\bgrammar v[123]\b`)
	versionedKind := regexp.MustCompile(`\bv[23]-(?:comb|fn|window|mismatch)-?[A-Za-z0-9_]*\b`)
	forbidden := []string{
		"oracleSchemaDDL" + "V2",
		"oracleSchemaDDL" + "V3",
		"oracleSeedRow" + "V2",
		"oracleSeedRow" + "V3",
		"current-" + "v3-fixture",
		"g." + "v2",
		"g." + "v3",
	}
	root := moduleRootPath()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relativeErr := filepath.Rel(root, path)
		if relativeErr != nil {
			return relativeErr
		}
		path = filepath.ToSlash(relative)
		if entry.IsDir() {
			if path == ".git" || path == "vendor" || strings.HasPrefix(path, ".") && path != ".github" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".go" && ext != ".md" && ext != ".json" && ext != ".yml" && ext != ".yaml" {
			return nil
		}
		if historical[path] {
			return nil
		}
		data, err := os.ReadFile(moduleRootPath(filepath.FromSlash(path)))
		if err != nil {
			return err
		}
		text := string(data)
		if strings.Contains(text, retiredEnvironmentKey) && !allowedRetirementReferences[path] {
			t.Errorf("%s contains the retired grammar environment key outside the refusal allowlist", path)
		}
		for _, token := range forbidden {
			if strings.Contains(text, token) {
				t.Errorf("%s contains retired active token %q", path, token)
			}
		}
		if versionedHelper.MatchString(text) {
			t.Errorf("%s contains an active versioned helper: %q", path, versionedHelper.FindString(text))
		}
		if versionedGrammar.MatchString(text) {
			t.Errorf("%s contains an active versioned grammar statement: %q", path, versionedGrammar.FindString(text))
		}
		if versionedKind.MatchString(text) && !allowedHistoricalKindReferences[path] {
			t.Errorf("%s contains an active versioned report kind: %q", path, versionedKind.FindString(text))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
