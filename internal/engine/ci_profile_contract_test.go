package engine

import (
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

var ciWorkflowPath = moduleRootPath(".github", "workflows", "ci.yml")

func readCIWorkflow(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(ciWorkflowPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var approvedCIActionUses = map[string]bool{
	"actions/checkout@v7":        true,
	"actions/setup-go@v7":        true,
	"actions/upload-artifact@v7": true,
}

func ciActionUses(workflow string) ([]string, error) {
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(workflow), &document); err != nil {
		return nil, fmt.Errorf("parse CI workflow: %w", err)
	}
	var result []string
	var visit func(*yaml.Node)
	visit = func(node *yaml.Node) {
		if node.Kind == yaml.MappingNode {
			for index := 0; index+1 < len(node.Content); index += 2 {
				key := node.Content[index]
				value := node.Content[index+1]
				if key.Value == "uses" && value.Kind == yaml.ScalarNode {
					reference := strings.TrimSpace(value.Value)
					result = append(result, reference)
				}
				visit(value)
			}
			return
		}
		for _, child := range node.Content {
			visit(child)
		}
	}
	visit(&document)
	return result, nil
}

func validateCIActionUses(workflow string) error {
	references, err := ciActionUses(workflow)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, reference := range references {
		if !approvedCIActionUses[reference] {
			return fmt.Errorf("CI uses an unapproved GitHub Action %q", reference)
		}
		seen[reference] = true
	}
	for reference := range approvedCIActionUses {
		if !seen[reference] {
			return fmt.Errorf("CI does not use required GitHub Action %q", reference)
		}
	}
	return nil
}

func TestCIUsesCurrentApprovedGitHubActions(t *testing.T) {
	workflow := readCIWorkflow(t)
	if err := validateCIActionUses(workflow); err != nil {
		t.Fatal(err)
	}
	wantCounts := map[string]int{
		"actions/checkout@v7":        7,
		"actions/setup-go@v7":        7,
		"actions/upload-artifact@v7": 4,
	}
	gotCounts := make(map[string]int)
	references, err := ciActionUses(workflow)
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range references {
		gotCounts[reference]++
	}
	for reference, want := range wantCounts {
		if got := gotCounts[reference]; got != want {
			t.Errorf("CI uses %s %d times, want %d", reference, got, want)
		}
	}
}

func TestCIActionVersionContractRejectsMutations(t *testing.T) {
	workflow := readCIWorkflow(t)
	mutations := map[string]string{
		"old checkout":            strings.Replace(workflow, "actions/checkout@v7", "actions/checkout@v4", 1),
		"old setup go":            strings.Replace(workflow, "actions/setup-go@v7", "actions/setup-go@v5", 1),
		"old upload":              strings.Replace(workflow, "actions/upload-artifact@v7", "actions/upload-artifact@v4", 1),
		"floating ref":            strings.Replace(workflow, "actions/checkout@v7", "actions/checkout@main", 1),
		"unknown action":          strings.Replace(workflow, "actions/checkout@v7", "actions/cache@v4", 1),
		"vendor action":           strings.Replace(workflow, "actions/checkout@v7", "vendor/action@v7", 1),
		"local action":            strings.Replace(workflow, "actions/checkout@v7", "./.github/actions/setup", 1),
		"reusable workflow":       strings.Replace(workflow, "jobs:\n", "jobs:\n  delegated:\n    uses: vendor/automation/.github/workflows/build.yml@v7\n", 1),
		"missing required action": strings.ReplaceAll(workflow, "actions/upload-artifact@v7", "example/upload-artifact@v7"),
	}
	for name, mutated := range mutations {
		t.Run(name, func(t *testing.T) {
			if mutated == workflow {
				t.Fatal("test mutation did not change the workflow")
			}
			if err := validateCIActionUses(mutated); err == nil {
				t.Fatal("the CI action contract accepted the mutation")
			}
		})
	}
}

func TestCIActionEnumerationIgnoresComments(t *testing.T) {
	workflow := readCIWorkflow(t)
	want, err := ciActionUses(workflow)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ciActionUses(workflow + "\n# uses: actions/checkout@v7\n")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("CI action uses with a comment = %v, want %v", got, want)
	}
}

func TestCIMinimumGoJobUsesTheLocalToolchain(t *testing.T) {
	workflow := readCIWorkflow(t)
	job := ciJob(t, workflow, "minimum-go", "generated-code-compatibility")
	if !strings.Contains(workflow, `MINIMUM_GO_VERSION: "1.24.0"`) {
		t.Error("the workflow does not pin minimum Go 1.24.0")
	}
	for _, required := range []string{
		"GOTOOLCHAIN: local",
		"go-version: ${{ env.MINIMUM_GO_VERSION }}",
		`go build -o "${RUNNER_TEMP}/chgen" ./cmd/chgen`,
		"run: go test ./...",
	} {
		if !strings.Contains(job, required) {
			t.Errorf("the minimum Go contract does not contain %q", required)
		}
	}
	if strings.Contains(job, "clickhouse-server") || strings.Contains(job, "fuzzoracle") || strings.Contains(job, "execoracle") {
		t.Error("the minimum Go job must not run a ClickHouse oracle")
	}
}

func TestCIGeneratedCodeUsesEachSupportedPair(t *testing.T) {
	job := ciJob(t, readCIWorkflow(t), "generated-code-compatibility", "build")
	for _, required := range []string{
		"GOTOOLCHAIN: local",
		`go: "1.24.0"`,
		`driver: "v2.42.0"`,
		`go: "1.25.0"`,
		`driver: "v2.47.0"`,
		`go run ./internal/tooling/cmd/drivercompat -go "${{ matrix.go }}" -driver "${{ matrix.driver }}"`,
	} {
		if !strings.Contains(job, required) {
			t.Errorf("the generated-code contract does not contain %q", required)
		}
	}
	if strings.Contains(job, "clickhouse-server") {
		t.Error("the generated-code compile job must not start ClickHouse")
	}
}

func ciJob(t *testing.T, workflow, job, nextJob string) string {
	t.Helper()
	startMarker := "\n  " + job + ":\n"
	start := strings.Index(workflow, startMarker)
	if start < 0 {
		t.Fatalf("CI job %q is absent", job)
	}
	start += len(startMarker)
	if nextJob == "" {
		return workflow[start:]
	}
	end := strings.Index(workflow[start:], "\n  "+nextJob+":\n")
	if end < 0 {
		t.Fatalf("CI job %q does not end before %q", job, nextJob)
	}
	return workflow[start : start+end]
}

func ciEnvValue(job, name string) (string, error) {
	prefix := name + ":"
	var value string
	for _, line := range strings.Split(job, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		if value != "" {
			return "", fmt.Errorf("CI job has more than one %s assignment", name)
		}
		value = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, prefix)), `"'`)
	}
	if value == "" {
		return "", fmt.Errorf("CI job has no %s assignment", name)
	}
	return value, nil
}

func validateVersionBoundaryOracleURLs(job string) error {
	want := map[string]struct {
		host string
		port string
	}{
		"PINNED_ORACLE_URL":    {host: "localhost", port: "8123"},
		"CANDIDATE_ORACLE_URL": {host: "localhost", port: "18123"},
	}
	wantQuery := map[string]string{
		"allow_experimental_variant_type": "1",
		"allow_experimental_dynamic_type": "1",
		"allow_experimental_json_type":    "1",
	}
	for name, endpoint := range want {
		raw, err := ciEnvValue(job, name)
		if err != nil {
			return err
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("%s is not a URL: %w", name, err)
		}
		if parsed.Scheme != "http" || parsed.Hostname() != endpoint.host || parsed.Port() != endpoint.port || parsed.Path != "" || parsed.Fragment != "" {
			return fmt.Errorf("%s has endpoint %q, want http://%s:%s", name, raw, endpoint.host, endpoint.port)
		}
		query := parsed.Query()
		if len(query) != len(wantQuery) {
			return fmt.Errorf("%s has %d query settings, want %d", name, len(query), len(wantQuery))
		}
		for key, value := range wantQuery {
			values, ok := query[key]
			if !ok || len(values) != 1 || values[0] != value {
				return fmt.Errorf("%s setting %s is %q, want one value %q", name, key, values, value)
			}
		}
	}
	return nil
}

func TestCIHasNoVersionedGrammarAxis(t *testing.T) {
	workflow := readCIWorkflow(t)
	for _, forbidden := range []string{
		"CHGEN_ORACLE_GRAMMAR",
		"matrix.grammar",
		"grammar: [v2, v3]",
		"grammar: [v3, v2]",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("CI still contains the versioned grammar axis %q", forbidden)
		}
	}

	typeOracle := ciJob(t, workflow, "type-oracle", "exec-oracle")
	if strings.Contains(typeOracle, "strategy:") || strings.Contains(typeOracle, "matrix:") {
		t.Error("the current type oracle must use one job without a matrix")
	}
	if !strings.Contains(typeOracle, `args+=(-report "${{ github.workspace }}/oracle-report-${CHGEN_ORACLE_PLAN}-${CHGEN_ORACLE_FIXTURE_ID}-seed${seed}.json")`) {
		t.Error("the union gate must pass bare report paths")
	}
}

func TestCILiveTypeOracleCellsNameCurrentIdentity(t *testing.T) {
	workflow := readCIWorkflow(t)
	jobs := map[string]struct {
		body      string
		fixtureID string
	}{
		"type-oracle": {
			body:      ciJob(t, workflow, "type-oracle", "exec-oracle"),
			fixtureID: "current-fixture",
		},
		"version-matrix": {
			body:      ciJob(t, workflow, "version-matrix", ""),
			fixtureID: "cross-version-common-v1",
		},
	}
	for name, job := range jobs {
		t.Run(name, func(t *testing.T) {
			for _, required := range []string{
				"CHGEN_ORACLE_PLAN: current-combined-v1",
				"CHGEN_ORACLE_FIXTURE_ID: " + job.fixtureID,
				"current-combined-v1-" + job.fixtureID,
			} {
				if !strings.Contains(job.body, required) {
					t.Errorf("live job does not name %q", required)
				}
			}
		})
	}
	versionBoundary := jobs["version-matrix"].body
	if err := validateVersionBoundaryOracleURLs(versionBoundary); err != nil {
		t.Fatal(err)
	}
}

func TestCIVersionBoundaryURLSettingsRejectAsymmetricMutations(t *testing.T) {
	job := ciJob(t, readCIWorkflow(t), "version-matrix", "")
	mutations := map[string]string{
		"comment cannot replace pinned setting": strings.Replace(job,
			"PINNED_ORACLE_URL: http://localhost:8123?allow_experimental_variant_type=1&allow_experimental_dynamic_type=1&allow_experimental_json_type=1",
			"PINNED_ORACLE_URL: http://localhost:8123?allow_experimental_variant_type=1&allow_experimental_json_type=1\n      # allow_experimental_dynamic_type=1",
			1),
		"candidate duplicate is not one setting": strings.Replace(job,
			"CANDIDATE_ORACLE_URL: http://localhost:18123?allow_experimental_variant_type=1&allow_experimental_dynamic_type=1&allow_experimental_json_type=1",
			"CANDIDATE_ORACLE_URL: http://localhost:18123?allow_experimental_variant_type=1&allow_experimental_variant_type=1&allow_experimental_dynamic_type=1&allow_experimental_json_type=1",
			1),
		"duplicate assignment is not allowed": job + "\n    PINNED_ORACLE_URL: http://localhost:8123?allow_experimental_variant_type=1&allow_experimental_dynamic_type=1&allow_experimental_json_type=1\n",
	}
	for name, mutated := range mutations {
		t.Run(name, func(t *testing.T) {
			if mutated == job {
				t.Fatal("test mutation did not change the CI job")
			}
			if err := validateVersionBoundaryOracleURLs(mutated); err == nil {
				t.Fatal("asymmetric URL settings mutation did not make the contract refuse")
			}
		})
	}
}

func TestCIVersionBoundaryRunsExecutedKnownProbes(t *testing.T) {
	job := ciJob(t, readCIWorkflow(t), "version-matrix", "")
	for _, required := range []string{
		`"${{ runner.temp }}/probe" -url "${PINNED_ORACLE_URL}" -out "${PINNED_PROBE_OUT}"`,
		`"${{ runner.temp }}/probe" -url "${CANDIDATE_ORACLE_URL}" -out "${CANDIDATE_PROBE_OUT}"`,
		`"${{ runner.temp }}/probegate" -known-version-boundary "${PINNED_PROBE_OUT}" "${CANDIDATE_PROBE_OUT}"`,
		`for path in "${PINNED_OUT}" "${CANDIDATE_OUT}" "${ORACLE_DIFF_OUT}"`,
	} {
		if !strings.Contains(job, required) {
			t.Errorf("version boundary does not contain %q", required)
		}
	}
}

func TestCIFixedSeedUnionUsesOneServerJob(t *testing.T) {
	workflow := readCIWorkflow(t)
	job := ciJob(t, workflow, "type-oracle", "exec-oracle")
	for _, required := range []string{
		`ORACLE_SEEDS: "1 7 42 99"`,
		"for seed in ${ORACLE_SEEDS}; do",
		`"${{ runner.temp }}/oraclegate" -union "${args[@]}"`,
	} {
		if !strings.Contains(job, required) {
			t.Errorf("the fixed-seed union contract does not contain %q", required)
		}
	}
	if got := strings.Count(job, "image: clickhouse/clickhouse-server:"); got != 1 {
		t.Errorf("the fixed-seed union uses %d ClickHouse services, want 1", got)
	}
}

func TestCILiveGatesStayIndependentAndBlocking(t *testing.T) {
	workflow := readCIWorkflow(t)
	jobs := map[string]string{
		"type-oracle":         ciJob(t, workflow, "type-oracle", "exec-oracle"),
		"exec-oracle":         ciJob(t, workflow, "exec-oracle", "type-boundary-probe"),
		"type-boundary-probe": ciJob(t, workflow, "type-boundary-probe", "version-matrix"),
	}
	for name, job := range jobs {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(job, "\n    needs:") {
				t.Error("a live gate must not wait for another live gate")
			}
			if strings.Contains(job, "continue-on-error:") {
				t.Error("a live gate must block on a failed check")
			}
		})
	}
	if !strings.Contains(jobs["exec-oracle"], "CHGEN_EXEC_NATIVE:") || !strings.Contains(jobs["exec-oracle"], "./internal/tooling/cmd/execoraclegate") {
		t.Error("the native execution oracle and its gate must stay in CI")
	}
	if !strings.Contains(jobs["type-boundary-probe"], "./internal/tooling/cmd/probe") || !strings.Contains(jobs["type-boundary-probe"], "./internal/tooling/cmd/probegate") {
		t.Error("the deterministic probe and its gate must stay in CI")
	}
}

func TestCIClickHouseServicesPermitTheWorkflowClient(t *testing.T) {
	workflow := readCIWorkflow(t)
	services := strings.Count(workflow, "image: clickhouse/clickhouse-server:")
	userSetups := strings.Count(workflow, `CLICKHOUSE_SKIP_USER_SETUP: "1"`)
	if services != userSetups {
		t.Errorf("%d ClickHouse services exist, but %d permit the workflow client", services, userSetups)
	}
}
