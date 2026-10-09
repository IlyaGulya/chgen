package chgen_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IlyaGulya/chgen"
)

func TestCheckServerDetectsContractDriftWithoutWriting(t *testing.T) {
	cli := buildPublicCLI(t)
	config, output := writeCheckProject(t, "", "-- name: Read :one\nSELECT 1 AS value;")
	var typeName atomic.Value
	typeName.Store("UInt8")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "SELECT version()") {
			io.WriteString(w, `{"data":[{"version":"25.8.29.51"}]}`)
		} else {
			io.WriteString(w, `{"data":[{"name":"value","type":"`+typeName.Load().(string)+`"}]}`)
		}
	}))
	defer server.Close()
	run := func(command string) ([]byte, error) {
		return exec.CommandContext(t.Context(), cli, command, "-f", config, "-server", server.URL).CombinedOutput()
	}
	if out, err := run("generate-server"); err != nil {
		t.Fatalf("generate: %v\n%s", err, out)
	}
	before, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := run("check-server"); err != nil {
		t.Fatalf("unchanged contract: %v\n%s", err, out)
	}
	typeName.Store("UInt64")
	if out, err := run("check-server"); err == nil || !strings.Contains(string(out), "out of date") {
		t.Fatalf("drift not diagnosed: %v\n%s", err, out)
	}
	after, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("check overwrote output: %v", err)
	}
}

func TestServerSnapshotGeneratesWithoutServerAndRejectsChangedInputs(t *testing.T) {
	cli := buildPublicCLI(t)
	config, output := writeCheckProject(t, "", "-- name: Read :one\nSELECT 1 AS value;")
	snapshot := filepath.Join(filepath.Dir(config), "contracts.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "SELECT version()") {
			io.WriteString(w, `{"data":[{"version":"25.8.29.51"}]}`)
		} else {
			io.WriteString(w, `{"data":[{"name":"value","type":"UInt8"}]}`)
		}
	}))
	if out, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", server.URL, "-snapshot-out", snapshot).CombinedOutput(); err != nil {
		t.Fatalf("capture: %v\n%s", err, out)
	}
	server.Close()
	before, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(output); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-snapshot-in", snapshot).CombinedOutput(); err != nil {
		t.Fatalf("offline replay: %v\n%s", err, out)
	}
	after, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("snapshot changed generated code: %v", err)
	}
	if out, err := exec.CommandContext(t.Context(), cli, "check-server", "-f", config, "-snapshot-in", snapshot).CombinedOutput(); err != nil {
		t.Fatalf("offline check: %v\n%s", err, out)
	}
	if out, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-snapshot-in", snapshot, "-database", "different").CombinedOutput(); err == nil || !strings.Contains(string(out), "snapshot has no matching") {
		t.Fatalf("different database accepted: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(config), "schema.sql"), []byte("CREATE TABLE unrelated (id UInt64) ENGINE=Memory;"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-snapshot-in", snapshot).CombinedOutput(); err == nil || !strings.Contains(string(out), "snapshot inputs are stale") {
		t.Fatalf("changed migration accepted: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(config), "schema.sql"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(config), "queries.sql"), []byte("-- name: Read :one\nSELECT 2 AS value;"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-snapshot-in", snapshot).CombinedOutput(); err == nil || !strings.Contains(string(out), "snapshot") {
		t.Fatalf("changed SQL accepted: %v\n%s", err, out)
	}
	after, err = os.ReadFile(output)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected replay overwrote output: %v", err)
	}
}

func TestServerSnapshotRefusesInputAndOutputCollisions(t *testing.T) {
	cli := buildPublicCLI(t)
	config, output := writeCheckProject(t, "", "-- name: Read :one\nSELECT 1 AS value;")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "SELECT version()") {
			io.WriteString(w, `{"data":[{"version":"25.8.29.51"}]}`)
		} else {
			io.WriteString(w, `{"data":[{"name":"value","type":"UInt8"}]}`)
		}
	}))
	defer server.Close()
	for _, path := range []string{config, filepath.Join(filepath.Dir(config), "queries.sql"), output} {
		before, _ := os.ReadFile(path)
		if out, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", server.URL, "-snapshot-out", path).CombinedOutput(); err == nil || !strings.Contains(string(out), "snapshot collides") {
			t.Fatalf("unsafe snapshot path %s: %v\n%s", path, err, out)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatalf("snapshot overwrote %s", path)
		}
	}
}

func TestServerGenerationMixesExplicitPackageAnalyzers(t *testing.T) {
	cli := buildPublicCLI(t)
	dir := t.TempDir()
	files := map[string]string{
		"chgen.yaml": "version: 1\npackages:\n  - name: local\n    analysis: offline\n    output: local/queries.go\n    schema: schema.sql\n    queries: local.sql\n  - name: remote\n    analysis: server\n    output: remote/queries.go\n    schema: schema.sql\n    queries: remote.sql\n",
		"schema.sql": "CREATE TABLE events (id UInt64) ENGINE=Memory;",
		"local.sql":  "-- name: ReadLocal :many\nSELECT id FROM events;",
		"remote.sql": "-- name: ReadRemote :many\nSELECT number FROM numbers(2) QUALIFY row_number() OVER () > 0;",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "SELECT version()") {
			io.WriteString(w, `{"data":[{"version":"25.8.29.51"}]}`)
			return
		}
		if strings.Contains(string(body), "events") {
			t.Error("offline query reached server")
		}
		io.WriteString(w, `{"data":[{"name":"number","type":"UInt64"}]}`)
	}))
	defer server.Close()
	config := filepath.Join(dir, "chgen.yaml")
	if out, err := exec.CommandContext(t.Context(), cli, "-f", config).CombinedOutput(); err == nil || !strings.Contains(string(out), "generate-server") {
		t.Fatalf("offline command ignored explicit mode: %v\n%s", err, out)
	}
	if out, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", server.URL).CombinedOutput(); err != nil {
		t.Fatalf("mixed generation: %v\n%s", err, out)
	}
	for _, name := range []string{"local", "remote"} {
		data, err := os.ReadFile(filepath.Join(dir, name, "queries.go"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "server-analysis") != (name == "remote") {
			t.Fatalf("wrong analysis authority for %s", name)
		}
	}
}

func TestServerWildcardGeneratedRuntime(t *testing.T) {
	if os.Getenv("CHGEN_CONTRACT_NATIVE") == "" {
		t.Skip("requires disposable ClickHouse native endpoint")
	}
	queries, err := parsePublicQuery(t,
		"CREATE TABLE chgen_star_runtime (z UInt64, a String DEFAULT 'value', m UInt64 MATERIALIZED z, x UInt64 ALIAS z) ENGINE=Memory;",
		`-- name: Plain :many
SELECT * FROM chgen_star_runtime ORDER BY z;
-- name: Qualified :many
SELECT e.* FROM chgen_star_runtime AS e ORDER BY z;
-- name: Nested :many
WITH source AS (SELECT * FROM chgen_star_runtime) SELECT source.* FROM source ORDER BY z;
-- name: Included :many
WITH source AS (SELECT * FROM chgen_star_runtime) SELECT source.* FROM source ORDER BY z SETTINGS asterisk_include_alias_columns=1, asterisk_include_materialized_columns=1;
-- name: Series :many
SELECT * FROM numbers(3);
-- name: Union :many
SELECT * FROM chgen_star_runtime UNION ALL SELECT * FROM chgen_star_runtime;`)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := chgen.Generate("wildcard", queries)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/wildcard/runtime_test.go")
	if err != nil {
		t.Fatal(err)
	}
	runGeneratedRuntime(t, "wildcard", generated, fixture)
}

func TestServerSeriesGeneratedRuntime(t *testing.T) {
	if os.Getenv("CHGEN_CONTRACT_NATIVE") == "" {
		t.Skip("requires disposable ClickHouse native endpoint")
	}
	queries, err := parsePublicQuery(t, "", `-- name: Series :many
SELECT n.number, z.zero FROM numbers(10,6,2) AS n CROSS JOIN zeros_mt(1) AS z ORDER BY n.number;
-- name: Parallel :many
SELECT n.number, z.zero FROM numbers_mt(10,6,2) AS n CROSS JOIN zeros(1) AS z ORDER BY n.number;
-- name: Inclusive :many
SELECT generate_series AS value FROM generate_series(2,8,2) ORDER BY value;
-- name: Camel :many
SELECT generate_series AS value FROM generateSeries(2,8,2) ORDER BY value;
-- name: Parameterized :many
SELECT number FROM numbers(chgen.arg('Start'), chgen.arg('Length'), chgen.arg('Step')) ORDER BY number;
-- name: ParamParallel :many
SELECT number FROM numbers_mt(chgen.arg('Start'), chgen.arg('Length'), chgen.arg('Step')) ORDER BY number;
-- name: ParamZeros :many
SELECT zero FROM zeros(chgen.arg('Length'));
-- name: ParamZerosParallel :many
SELECT zero FROM zeros_mt(chgen.arg('Length'));
-- name: ParamInclusive :many
SELECT generate_series AS value FROM generate_series(chgen.arg('Start'),chgen.arg('Stop'),chgen.arg('Step')) ORDER BY value;
-- name: ParamCamel :many
SELECT generate_series AS value FROM generateSeries(chgen.arg('Start'),chgen.arg('Stop'),chgen.arg('Step')) ORDER BY value;
-- name: Unlimited :many
SELECT number FROM numbers() LIMIT 3;
-- name: Empty :many
SELECT number FROM numbers(0);
-- name: BoundFilter :many
SELECT number FROM numbers(5) WHERE number > 1 AND number < 4 ORDER BY number LIMIT 1 OFFSET 1;
-- name: BoundAliasFilter :many
SELECT n.number AS value FROM numbers(5) n WHERE value > 1 AND value < 4 ORDER BY value LIMIT 1 OFFSET 1;`)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := chgen.Generate("series", queries)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/series/runtime_test.go")
	if err != nil {
		t.Fatal(err)
	}
	runGeneratedRuntime(t, "series", generated, fixture)
}

func TestServerGenerationPreservesLegacyAnnotations(t *testing.T) {
	endpoint := os.Getenv("CHGEN_ORACLE_URL")
	if endpoint == "" || os.Getenv("CHGEN_CONTRACT_NATIVE") == "" {
		t.Skip("requires disposable ClickHouse HTTP and native endpoints")
	}
	cli := buildPublicCLI(t)
	config, output := writeCheckProject(t, "", `-- name: Read :one
-- param: Keys []string
-- result: Joined joined string
-- result: Size size uint64
-- result-chtype: joined String
SELECT arrayStringConcat(chgen.arg('Keys'), '|') AS joined, length(chgen.arg('Keys')) AS size;`)
	samples := filepath.Join(filepath.Dir(config), "samples.json")
	if err := os.WriteFile(samples, []byte(`{"Read":{"Keys":"['example']"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", endpoint, "-params", samples)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v\n%s", err, out)
	}
	generated, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/serverarrays/runtime_test.go")
	if err != nil {
		t.Fatal(err)
	}
	runGeneratedRuntime(t, "serverlegacy", generated, fixture)
}

func TestServerGenerationDoesNotPublishParameterExamplesAsResultNames(t *testing.T) {
	cli := buildPublicCLI(t)
	config, output := writeCheckProject(t, "", "-- name: Read :one\nSELECT {Value:String};")
	samples := filepath.Join(filepath.Dir(config), "samples.json")
	if err := os.WriteFile(samples, []byte(`{"Read":{"Value":"sensitive-example"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "SELECT version()") {
			io.WriteString(w, `{"data":[{"version":"25.8.29.51"}]}`)
			return
		}
		io.WriteString(w, `{"data":[{"name":"_CAST('sensitive-example', 'String')","type":"String"}]}`)
	}))
	defer server.Close()
	out, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", server.URL, "-params", samples).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "alias") || strings.Contains(string(out), "sensitive-example") {
		t.Fatalf("unsafe result name was not rejected privately: %v\n%s", err, out)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("published an unsafe contract: %v", err)
	}
}

func TestServerArrayParametersGeneratedRuntime(t *testing.T) {
	endpoint := os.Getenv("CHGEN_ORACLE_URL")
	if endpoint == "" || os.Getenv("CHGEN_CONTRACT_NATIVE") == "" {
		t.Skip("requires disposable ClickHouse HTTP and native endpoints")
	}
	cli := buildPublicCLI(t)
	config, output := writeCheckProject(t, "", `-- name: Read :one
SELECT arrayStringConcat({Keys:Array(String)}, '|') AS joined,
       length({Keys:Array(String)}) AS size;`)
	samples := filepath.Join(filepath.Dir(config), "samples.json")
	if err := os.WriteFile(samples, []byte(`{"Read":{"Keys":"['example']"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", endpoint, "-params", samples)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v\n%s", err, out)
	}
	generated, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/serverarrays/runtime_test.go")
	if err != nil {
		t.Fatal(err)
	}
	runGeneratedRuntime(t, "serverarrays", generated, fixture)
}

func TestServerNullableAndTemporalParametersGeneratedRuntime(t *testing.T) {
	endpoint := os.Getenv("CHGEN_ORACLE_URL")
	if endpoint == "" || os.Getenv("CHGEN_CONTRACT_NATIVE") == "" {
		t.Skip("requires disposable ClickHouse HTTP and native endpoints")
	}
	cli := buildPublicCLI(t)
	config, output := writeCheckProject(t, "", `-- name: Read :one
SELECT toUnixTimestamp64Micro({At:DateTime64(6, 'UTC')}) AS micros,
       coalesce({Label:Nullable(String)}, 'none') AS label;`)
	samples := filepath.Join(filepath.Dir(config), "samples.json")
	if err := os.WriteFile(samples, []byte(`{"Read":{"At":"2026-10-09 12:00:00.123456","Label":"\\N"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", endpoint, "-params", samples)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v\n%s", err, out)
	}
	generated, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/servertemporal/runtime_test.go")
	if err != nil {
		t.Fatal(err)
	}
	runGeneratedRuntime(t, "servertemporal", generated, fixture)
}

func TestServerExternalTablesGeneratedRuntime(t *testing.T) {
	endpoint := os.Getenv("CHGEN_ORACLE_URL")
	if endpoint == "" || os.Getenv("CHGEN_CONTRACT_NATIVE") == "" {
		t.Skip("requires disposable ClickHouse HTTP and native endpoints")
	}
	cli := buildPublicCLI(t)
	config, output := writeCheckProject(t, `-- chgen:external
CREATE TABLE requested_keys (key String, at DateTime64(6, 'Asia/Almaty'));
SYSTEM WAIT VIEW intentionally_not_replayed;`, `-- name: Read :many
SELECT key, toUnixTimestamp64Micro(at) AS micros
FROM chgen.external('Keys', requested_keys)
QUALIFY row_number() OVER () > 0 ORDER BY key;`)
	cmd := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", endpoint)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v\n%s", err, out)
	}
	generated, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/serverexternal/runtime_test.go")
	if err != nil {
		t.Fatal(err)
	}
	runGeneratedRuntime(t, "serverexternal", generated, fixture)
}

func TestServerAnnotatedArgumentsGeneratedRuntime(t *testing.T) {
	endpoint := os.Getenv("CHGEN_ORACLE_URL")
	if endpoint == "" || os.Getenv("CHGEN_CONTRACT_NATIVE") == "" {
		t.Skip("requires disposable ClickHouse HTTP and native endpoints")
	}
	cli := buildPublicCLI(t)
	for _, expression := range []string{"chgen.arg('Keys')", "?"} {
		t.Run(expression, func(t *testing.T) {
			config, output := writeCheckProject(t, "", "-- name: Read :one\n-- param-chtype: Keys Array(String)\nSELECT arrayStringConcat("+expression+", '|') AS joined, length("+expression+") AS size;")
			if expression == "?" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(config), "queries.sql"), []byte("-- name: Read :one\n-- param-chtype: Keys Array(String)\nSELECT arrayStringConcat(?, '|') AS joined, length({Keys:Array(String)}) AS size;"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			samples := filepath.Join(filepath.Dir(config), "samples.json")
			if err := os.WriteFile(samples, []byte(`{"Read":{"Keys":"['example']"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", endpoint, "-params", samples)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("generate: %v\n%s", err, out)
			}
			generated, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			fixture, err := os.ReadFile("testdata/serverarrays/runtime_test.go")
			if err != nil {
				t.Fatal(err)
			}
			runGeneratedRuntime(t, "serverarguments", generated, fixture)
		})
	}
}

func TestServerMappedParameterFamiliesGeneratedRuntime(t *testing.T) {
	endpoint := os.Getenv("CHGEN_ORACLE_URL")
	if endpoint == "" || os.Getenv("CHGEN_CONTRACT_NATIVE") == "" {
		t.Skip("requires disposable ClickHouse HTTP and native endpoints")
	}
	cli := buildPublicCLI(t)
	config, output := writeCheckProject(t, "", `-- name: Read :one
SELECT {ID:UUID} AS id, {Amount:Decimal(9, 3)} AS amount,
       toUnixTimestamp({Seconds:DateTime('Asia/Almaty')}) AS seconds,
       toUnixTimestamp64Micro({Times:Map(String, DateTime64(6, 'UTC'))}['a']) AS micros,
       {Seconds:DateTime('Asia/Almaty')} AS at_second,
       length({Events:Map(DateTime64(6, 'UTC'), String)}) AS event_count;`)
	samples := filepath.Join(filepath.Dir(config), "samples.json")
	if err := os.WriteFile(samples, []byte(`{"Read":{"ID":"00000000-0000-0000-0000-000000000001","Amount":"12.345","Seconds":"2026-10-09 17:00:00","Times":"{'a':'2026-10-09 12:00:00.123456'}","Events":"{'2026-10-09 12:00:00.123456':'example'}"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", endpoint, "-params", samples)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v\n%s", err, out)
	}
	generated, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/servermapped/runtime_test.go")
	if err != nil {
		t.Fatal(err)
	}
	runGeneratedRuntime(t, "servermapped", generated, fixture)
}

func TestServerGenerationBypassesOnlyTheLocalSQLFrontend(t *testing.T) {
	cli := buildPublicCLI(t)
	config, outputPath := writeCheckProject(t, "EXCHANGE TABLES a AND b", `-- name: Read :many
SELECT number FROM numbers({Limit:UInt64}) QUALIFY row_number() OVER () > 0;`)
	if _, err := chgen.Check(config); err == nil {
		t.Fatal("fixture must remain outside offline analysis")
	}
	samples := filepath.Join(filepath.Dir(config), "samples.json")
	if err := os.WriteFile(samples, []byte(`{"Read":{"Limit":"3"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Query().Get("readonly") != "1" {
			t.Error("server generation must request readonly analysis")
		}
		if strings.Contains(string(body), "SELECT version()") {
			_, _ = io.WriteString(w, `{"data":[{"version":"25.8.29.51"}]}`)
			return
		}
		if !strings.HasPrefix(string(body), "DESCRIBE TABLE (") || !strings.Contains(string(body), "QUALIFY") || r.URL.Query().Get("param_Limit") != "3" {
			t.Errorf("not the original SQL with native parameters: %s", body)
		}
		_, _ = io.WriteString(w, `{"data":[{"name":"number","type":"UInt64"}]}`)
	}))
	defer server.Close()
	output, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", server.URL, "-params", samples).CombinedOutput()
	if err != nil {
		t.Fatalf("server generation: %v\n%s", err, output)
	}
	generated, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Limit uint64", "Number uint64", "QUALIFY", "{Limit:UInt64}", "chgenCheckResultContract(rows", "server-analysis", "25.8.29.51"} {
		if !strings.Contains(string(generated), want) {
			t.Fatalf("missing %q:\n%s", want, generated)
		}
	}
}

func TestServerGenerationRefusesUnsafeInputsBeforeContactingServer(t *testing.T) {
	cli := buildPublicCLI(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	for _, sql := range []string{
		"SELECT 1; DROP TABLE events", "SELECT 1) FORMAT JSON", "DELETE FROM events",
		"SELECT 1 INTO OUTFILE '/tmp/chgen-output'", "SELECT 1 FORMAT JSON", "SELECT 'unterminated",
		"SELECT 1 /* unterminated", "SELECT ?", "SELECT {Table:Identifier}",
		"SELECT chgen.assumeType(clientFunction(1), 'UInt64')", "SELECT {X:UInt8}, {X:String}",
	} {
		t.Run(sql, func(t *testing.T) {
			config, outputPath := writeCheckProject(t, "", "-- name: Read :many\n"+sql)
			output, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", server.URL).CombinedOutput()
			if err == nil {
				t.Fatalf("unsafe boundary accepted: %s", output)
			}
			if _, err := os.Stat(filepath.Dir(outputPath)); !os.IsNotExist(err) {
				t.Fatalf("refusal created output: %v", err)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid inputs reached server: %d", requests.Load())
	}
}

func TestServerGenerationFailuresLeaveExistingOutputUntouched(t *testing.T) {
	cli := buildPublicCLI(t)
	for _, failure := range []string{"server refusal", "unsupported result", "field collision", "version drift", "unused examples", "examples alias output"} {
		t.Run(failure, func(t *testing.T) {
			config, outputPath := writeCheckProject(t, "", "-- name: Read :one\nSELECT 1 AS first;\n-- name: Later :one\nSELECT 2 AS later;")
			if err := os.Mkdir(filepath.Dir(outputPath), 0o700); err != nil {
				t.Fatal(err)
			}
			const original = "{}"
			if err := os.WriteFile(outputPath, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			var versions atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body), "SELECT version()") {
					version := "25.8.29.51"
					if versions.Add(1) > 1 && failure == "version drift" {
						version = "25.8.29.52"
					}
					_, _ = fmt.Fprintf(w, `{"data":[{"version":%q}]}`, version)
					return
				}
				if strings.Contains(string(body), "AS later") {
					switch failure {
					case "server refusal":
						w.Header().Set("X-ClickHouse-Exception-Code", "60")
						http.Error(w, "unknown table", http.StatusNotFound)
						return
					case "unsupported result":
						_, _ = io.WriteString(w, `{"data":[{"name":"value","type":"FutureType"}]}`)
						return
					case "field collision":
						_, _ = io.WriteString(w, `{"data":[{"name":"a-b","type":"UInt8"},{"name":"a_b","type":"UInt8"}]}`)
						return
					}
				}
				_, _ = io.WriteString(w, `{"data":[{"name":"value","type":"UInt8"}]}`)
			}))
			defer server.Close()
			args := []string{"generate-server", "-f", config, "-server", server.URL}
			switch failure {
			case "examples alias output":
				args = append(args, "-params", outputPath)
			case "unused examples":
				path := filepath.Join(filepath.Dir(config), "examples.json")
				if err := os.WriteFile(path, []byte(`{"NoSuchQuery":{}}`), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "-params", path)
			}
			if output, err := exec.CommandContext(t.Context(), cli, args...).CombinedOutput(); err == nil {
				t.Fatalf("generation did not refuse %s: %s", failure, output)
			}
			if data, err := os.ReadFile(outputPath); err != nil || string(data) != original {
				t.Fatalf("failed generation changed output: %q, %v", data, err)
			}
		})
	}
}

func TestServerScalarContractsGeneratedRuntime(t *testing.T) {
	endpoint := os.Getenv("CHGEN_ORACLE_URL")
	if endpoint == "" || os.Getenv("CHGEN_CONTRACT_NATIVE") == "" {
		t.Skip("CHGEN_ORACLE_URL and CHGEN_CONTRACT_NATIVE are required")
	}
	cli := buildPublicCLI(t)
	config, outputPath := writeCheckProject(t, "", `-- name: Read :many
SELECT number, {Text:String} AS text FROM numbers({Limit:UInt64})
QUALIFY row_number() OVER () > 1 ORDER BY number;
-- name: Empty :one
SELECT number FROM numbers(0);
-- name: Sum :one
SELECT sum(number) FROM numbers(3);
-- name: Recursive :many
WITH RECURSIVE seq AS (SELECT toUInt64(1) AS n UNION ALL SELECT n + 1 FROM seq WHERE n < 3)
SELECT n FROM seq ORDER BY n;
-- name: Drift :many
SELECT toFixedString('a', {Width:UInt64}) AS value FROM numbers(1);
-- name: EmptyDrift :many
SELECT toFixedString('a', {Width:UInt64}) AS value FROM numbers(0);
-- name: Formatting :one
SELECT format('value:{}', dummy) AS label FROM system.one;
-- name: Scalars :one
SELECT {B:Bool} AS b, {I8:Int8} AS i8, {I16:Int16} AS i16,
       {I32:Int32} AS i32, {I64:Int64} AS i64, {U8:UInt8} AS u8,
       {U16:UInt16} AS u16, {U32:UInt32} AS u32, {U64:UInt64} AS u64,
       {F32:Float32} AS f32, {F64:Float64} AS f64;`)
	samples := filepath.Join(filepath.Dir(config), "samples.json")
	if err := os.WriteFile(samples, []byte(`{"Read":{"Limit":"3","Text":"quote '\\ tab\t newline\n null\u0000"},"Drift":{"Width":"2"},"EmptyDrift":{"Width":"2"},"Scalars":{"B":"true","I8":"-1","I16":"-1","I32":"-1","I64":"-1","U8":"1","U16":"1","U32":"1","U64":"1","F32":"1.25","F64":"2.5"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", endpoint, "-params", samples).CombinedOutput()
	if err != nil {
		t.Fatalf("server generation: %v\n%s", err, output)
	}
	generated, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/serverqueries/runtime_test.go")
	if err != nil {
		t.Fatal(err)
	}
	runGeneratedRuntime(t, "serverqueries", generated, fixture)
}

func TestServerCompositionGeneratedRuntime(t *testing.T) {
	endpoint := os.Getenv("CHGEN_ORACLE_URL")
	if endpoint == "" || os.Getenv("CHGEN_CONTRACT_NATIVE") == "" {
		t.Skip("requires disposable ClickHouse HTTP and native endpoints")
	}
	cli := buildPublicCLI(t)
	config, output := writeCheckProject(t, "", `-- name: Read :many
-- param-chtype: Limit UInt64
-- param-chtype: AfterID UInt64
SELECT number FROM numbers(chgen.arg('Limit'))
-- chgen:if After
WHERE number > chgen.arg('AfterID')
-- chgen:end
QUALIFY row_number() OVER () > 0
ORDER BY number;`)
	examples := filepath.Join(filepath.Dir(config), "examples.json")
	if err := os.WriteFile(examples, []byte(`{"Read":{"Limit":"5","AfterID":"1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(filepath.Dir(config), "contracts.json")
	if out, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", endpoint, "-params", examples, "-snapshot-out", snapshot).CombinedOutput(); err != nil {
		t.Fatalf("composed generation: %v\n%s", err, out)
	}
	generated, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-params", examples, "-snapshot-in", snapshot).CombinedOutput(); err != nil {
		t.Fatalf("composed snapshot replay: %v\n%s", err, out)
	}
	replayed, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(generated, replayed) {
		t.Fatalf("composed replay changed output: %v", err)
	}
	fixture, err := os.ReadFile("testdata/servercomposition/runtime_test.go")
	if err != nil {
		t.Fatal(err)
	}
	runGeneratedRuntime(t, "servercomposition", generated, fixture)
}

func TestServerCompositionTablesGeneratedRuntime(t *testing.T) {
	endpoint := os.Getenv("CHGEN_ORACLE_URL")
	if endpoint == "" || os.Getenv("CHGEN_CONTRACT_NATIVE") == "" {
		t.Skip("requires disposable ClickHouse HTTP and native endpoints")
	}
	database := fmt.Sprintf("chgen_composition_%d", time.Now().UnixNano())
	execute := func(ctx context.Context, sql string) {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(sql))
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(response.Body)
			t.Fatalf("prepare disposable fixture: %s", body)
		}
	}
	execute(t.Context(), "CREATE DATABASE "+database)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		execute(ctx, "DROP DATABASE IF EXISTS "+database)
	})
	for _, table := range []string{"current", "archive"} {
		execute(t.Context(), "CREATE TABLE "+database+"."+table+" (id UInt64, at DateTime64(6, 'UTC'), payload String) ENGINE=Memory")
	}
	execute(t.Context(), "INSERT INTO "+database+".current VALUES (1, '2026-01-01 00:00:00', 'a'), (2, '2026-01-02 00:00:00', 'b'), (3, '2026-01-03 00:00:00', 'a')")
	execute(t.Context(), "INSERT INTO "+database+".archive VALUES (8, '2026-01-01 00:00:00', 'a')")
	cli := buildPublicCLI(t)
	config, output := writeCheckProject(t, "-- chgen:external\nCREATE TABLE requested_keys (id UInt64);", `-- name: Read :many
-- param-chtype: Keys Array(String)
-- param-chtype: AfterID UInt64
-- param-chtype: Since DateTime64(6, 'UTC')
-- chgen:table Source current archive
SELECT id, at, payload FROM chgen.table('Source')
WHERE has(chgen.arg('Keys'), payload)
-- chgen:if After
AND id > chgen.arg('AfterID')
-- chgen:end
-- chgen:if Changed
AND id IN (SELECT id FROM chgen.external('ChangedIDs', requested_keys))
-- chgen:end
AND at >= chgen.arg('Since')
QUALIFY row_number() OVER () > 0
ORDER BY id;`)
	examples := filepath.Join(filepath.Dir(config), "examples.json")
	if err := os.WriteFile(examples, []byte(`{"Read":{"Keys":"['a']","AfterID":"1","Since":"2026-01-01 00:00:00"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(filepath.Dir(config), "contracts.json")
	if out, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", endpoint, "-database", database, "-params", examples, "-snapshot-out", snapshot).CombinedOutput(); err != nil {
		t.Fatalf("composed generation: %v\n%s", err, out)
	}
	generated, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/servercompositiontables/runtime_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-database", database, "-params", examples, "-snapshot-in", snapshot).CombinedOutput(); err != nil {
		t.Fatalf("table/external snapshot replay: %v\n%s", err, out)
	}
	replayed, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(generated, replayed) {
		t.Fatalf("table/external replay changed output: %v", err)
	}
	t.Setenv("CHGEN_COMPOSITION_DATABASE", database)
	runGeneratedRuntime(t, "servercompositiontables", generated, fixture)
}

func TestServerCompositionRefusesResultShapeDrift(t *testing.T) {
	endpoint := os.Getenv("CHGEN_ORACLE_URL")
	if endpoint == "" {
		t.Skip("requires disposable ClickHouse HTTP endpoint")
	}
	cli := buildPublicCLI(t)
	config, output := writeCheckProject(t, "", `-- name: Read :one
SELECT
-- chgen:if Extra
toUInt64(2) AS extra,
-- chgen:end
toUInt64(1) AS value;`)
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		t.Fatal(err)
	}
	const original = "preserved output"
	if err := os.WriteFile(output, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", endpoint).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "result column count") {
		t.Fatalf("accepted result shape drift: %v\n%s", err, out)
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != original {
		t.Fatalf("failed composition changed output: %q, %v", data, err)
	}
}

func TestServerCompositionRefusesUnsafeInactiveVariants(t *testing.T) {
	cli := buildPublicCLI(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	for _, sql := range []string{
		"SELECT 1\n-- chgen:if Unsafe\n; DROP TABLE events\n-- chgen:end",
		"SELECT 1\n-- chgen:if Unsafe\nINTO OUTFILE '/tmp/chgen-output'\n-- chgen:end",
		"SELECT 1\n-- chgen:if Unsafe\nFORMAT JSON\n-- chgen:end",
		"-- chgen:table Source a b\nSELECT 1\n-- chgen:if Optional\nFROM chgen.table('Source')\n-- chgen:end",
		"SELECT 1\n-- chgen:if A\n-- chgen:if B\n-- chgen:end\n-- chgen:end",
		"SELECT 1\n-- chgen:if A\n-- chgen:else\n-- chgen:end",
		"SELECT 1\n-- chgen:if A\n-- chgen:end\n-- chgen:if B\n-- chgen:end\n-- chgen:if C\n-- chgen:end\n-- chgen:if D\n-- chgen:end\n-- chgen:if E\n-- chgen:end\n-- chgen:if F\n-- chgen:end",
	} {
		config, output := writeCheckProject(t, "", "-- name: Read :one\n"+sql)
		if out, err := exec.CommandContext(t.Context(), cli, "generate-server", "-f", config, "-server", server.URL).CombinedOutput(); err == nil {
			t.Fatalf("accepted unsafe composition: %s", out)
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatalf("refusal created output: %v", err)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("unsafe variants reached server: %d", requests.Load())
	}
}
