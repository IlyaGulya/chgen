package main

import (
	"net/url"
	"path/filepath"
	"strconv"
)

const pinnedClickHouse = "25.8.29.51"

// These adapters only select and execute existing checks. Oracle semantics,
// baselines and generated-code fixtures remain owned by their original tools.
func (r *runner) types(o options) {
	env := map[string]string{"CHGEN_ORACLE_URL": o.http, "CHGEN_CONTRACT_NATIVE": o.native}
	if tool := r.tool("apiinventory"); tool != "" {
		r.gate("api-inventory", tool, "-url", o.http, "-check")
	}
	if tool := r.tool("supportmanifest"); tool != "" {
		if r.gate("support-manifest", tool, "-check") {
			r.output("support-coverage", "api-support-coverage.json", nil, tool, "-report")
		}
	}
	r.test("settings", env, "fuzzoracle", "^TestSettingsReadControlsAgainstClickHouse$", "./internal/engine")
	if tool := r.tool("functionrules"); tool != "" {
		name := "function-measurements.json"
		r.gate("function-measurements", tool, "-url", o.http, "-check", "-evidence", "testdata/clickhouse-function-rules.json", "-report", filepath.Join(r.out, name))
		r.artifact(name)
		r.artifact(name + ".diff.json")
		name = "string-function-measurements.json"
		r.gate("string-function-measurements", tool, "-url", o.http, "-check", "-evidence", "testdata/clickhouse-string-function-rules.json", "-report", filepath.Join(r.out, name))
		r.artifact(name)
		r.artifact(name + ".diff.json")
		r.output("function-gaps", "function-gaps.json", nil, tool, "-gaps", "-url", o.http)
	}
	r.test("function-rules-runtime", env, "", "^TestFunctionRulesGeneratedRuntime$", "./internal/functionrules")
	r.test("function-evidence-codec", map[string]string{"CHGEN_FUNCTION_RULES_URL": o.http}, "", "^TestMeasurementWritesCompactEvidenceButKeepsExpandedLiveDiagnostics$", "./internal/functionrules")
	r.test("function-spelling", env, "", "^TestMeasuredScalarSpellingAgainstClickHouse$", "./internal/functionrules")
	r.test("function-regressions", env, "fuzzoracle", "^(TestAggregateGeoAliasesAgainstClickHouse|TestArrayResizeSizeDomainAgainstClickHouse)$", "./internal/engine")
	if r.sql("flush-system-logs", o.http, "SYSTEM FLUSH LOGS") {
		if tool := r.tool("systemcatalog"); tool != "" {
			r.command("system-catalog", env, tool, "-check")
		}
		systemEnv := map[string]string{"CHGEN_SYSTEM_HTTP": endpointHost(o.http)}
		r.test("system-runtime", systemEnv, "", "^TestGeneratedSystemPartsSignatureRuntime$", "./internal/engine")
	}
	r.test("catalog-exchange", env, "fuzzoracle", "^TestCatalogExchangeAgainstClickHouse$", "./internal/engine")
	r.test("date-nullability", env, "fuzzoracle", "^(TestNumericDateKeysAgainstClickHouse|TestNullableOracleRegressionsAgainstClickHouse)$", "./internal/engine")
	r.test("generated-runtime", env, "", "^(TestResultContractGeneratedRuntime|TestEmptyAggregateGeneratedRuntime|TestCursorQueriesGeneratedRuntime|TestOracleRegressionBoundaryAgainstClickHouse|TestDescribeLiveContractRoundTrip|TestScopedContractsGeneratedRuntime|TestComposedPaginationGeneratedRuntime)$", ".", "./internal/engine")
	r.test("server-generation", env, "", "^(TestServer.*|TestCheckServer.*)$", ".")
	args := []string{"-union"}
	ready := true
	for _, seed := range o.seeds {
		name := "type-seed" + strconv.Itoa(seed) + ".json"
		env := map[string]string{
			"CHGEN_ORACLE_URL":        o.http,
			"CHGEN_ORACLE_SEED":       strconv.Itoa(seed),
			"CHGEN_ORACLE_N":          strconv.Itoa(o.samples),
			"CHGEN_ORACLE_PLAN":       "current-combined-v1",
			"CHGEN_ORACLE_FIXTURE_ID": "current-fixture",
			"CHGEN_ORACLE_OUT":        filepath.Join(r.out, name),
		}
		if !r.test("oracle-seed"+strconv.Itoa(seed), env, "fuzzoracle", "^TestTypeOracle$", "./internal/engine") {
			ready = false
		}
		if !r.artifact(name) {
			ready = false
		}
		args = append(args, "-report", filepath.Join(r.out, name))
	}
	if ready {
		if tool := r.tool("oraclegate"); tool != "" {
			r.gate("type-union-gate", tool, args...)
		}
	} else {
		r.blocked("type-union-gate", "not every selected seed produced a valid run and report")
	}
}

func endpointHost(endpoint string) string {
	u, _ := url.Parse(endpoint)
	return u.Host
}

func (r *runner) execution(o options) {
	if tool := r.tool("execoraclegate"); tool != "" {
		seeds := []int{1}
		if o.deep {
			seeds = append(seeds, o.explorationSeed)
		}
		for _, seed := range seeds {
			name := "execution-seed" + strconv.Itoa(seed) + ".json"
			env := map[string]string{
				"CHGEN_EXEC_HTTP": o.http, "CHGEN_EXEC_NATIVE": o.native,
				"CHGEN_EXEC_SEED": strconv.Itoa(seed), "CHGEN_EXEC_RANDN": strconv.Itoa(o.execSamples),
				"CHGEN_EXEC_OUT": filepath.Join(r.out, name),
			}
			if r.testRequired("oracle-seed"+strconv.Itoa(seed), env, "execoracle", "", []string{"TestExecOracle"}, "./internal/engine") && r.artifact(name) {
				r.gate("execution-gate-seed"+strconv.Itoa(seed), tool, "-report", filepath.Join(r.out, name))
			} else {
				r.blocked("execution-gate-seed"+strconv.Itoa(seed), "execution oracle did not produce a valid run and report")
			}
		}
	}
}

func (r *runner) boundary(o options) {
	probe, gate := r.tool("probe"), r.tool("probegate")
	if probe == "" || gate == "" {
		return
	}
	if !r.gate("gate-selftest", gate, "-selftest") {
		r.blocked("boundary-gate", "gate self-test failed")
		return
	}
	name := "probe-current.json"
	if r.command("probe", nil, probe, "-url", o.http, "-out", filepath.Join(r.out, name)) && r.artifact(name) {
		r.gate("boundary-gate", gate, "testdata/probe-baseline-25.8.29.51.json", filepath.Join(r.out, name))
	} else {
		r.blocked("boundary-gate", "probe did not produce a valid artifact")
	}
}

func (r *runner) versions(o options) {
	probe, gate, diff := r.tool("probe"), r.tool("probegate"), r.tool("oraclediff")
	if probe == "" || gate == "" || diff == "" {
		return
	}
	endpoints := []string{o.http, o.candidate}
	probeNames := []string{"version-probe-pinned.json", "version-probe-candidate.json"}
	oracleNames := []string{"version-oracle-pinned.json", "version-oracle-candidate.json"}
	ready := true
	for i, endpoint := range endpoints {
		// Both versions receive the same measured experimental-type settings.
		endpoint += "?allow_experimental_variant_type=1&allow_experimental_dynamic_type=1&allow_experimental_json_type=1"
		if !r.command("probe-version"+strconv.Itoa(i), nil, probe, "-url", endpoint, "-out", filepath.Join(r.out, probeNames[i])) || !r.artifact(probeNames[i]) {
			ready = false
		}
		env := map[string]string{
			"CHGEN_ORACLE_URL": endpoint, "CHGEN_ORACLE_SEED": "1", "CHGEN_ORACLE_N": strconv.Itoa(o.samples),
			"CHGEN_ORACLE_PLAN": "current-combined-v1", "CHGEN_ORACLE_FIXTURE_ID": "cross-version-common-v1",
			"CHGEN_ORACLE_OUT": filepath.Join(r.out, oracleNames[i]),
		}
		if !r.test("oracle-version"+strconv.Itoa(i), env, "fuzzoracle", "^TestTypeOracle$", "./internal/engine") || !r.artifact(oracleNames[i]) {
			ready = false
		}
	}
	if !ready {
		r.blocked("version-comparison", "both versions must produce probe and oracle artifacts")
		return
	}
	if r.gate("known-version-boundary", gate, "-known-version-boundary", filepath.Join(r.out, probeNames[0]), filepath.Join(r.out, probeNames[1])) {
		r.expectedDifference("probe-difference", gate, filepath.Join(r.out, probeNames[0]), filepath.Join(r.out, probeNames[1]))
	}
	// A sampled cross-version difference is an observation, not a chgen defect.
	r.observation("oracle-difference", diff, "-cross-instance", "-cross-version", filepath.Join(r.out, oracleNames[0]), filepath.Join(r.out, oracleNames[1]))
}
