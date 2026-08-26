//go:build fuzzoracle

package engine

import (
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
)

const isNullExprLiveDDL = `CREATE TABLE t (
    plain Int32,
    nullable Nullable(Int32),
    low_cardinality LowCardinality(String),
    low_cardinality_nullable LowCardinality(Nullable(String)),
    bad Enum8('a' = 1),
    id UInt64,
    kind LowCardinality(String),
    available_at Nullable(DateTime64(3, 'UTC')),
    version UInt64
) ENGINE = Memory SETTINGS allow_suspicious_low_cardinality_types = 1`

const isNullExprLiveSeed = `INSERT INTO t VALUES (1, NULL, 'a', NULL, 'a', 1, 'a', NULL, 1)`

func TestIsNullExprAgainstClickHouse(t *testing.T) {
	oracle := execWitnessFixtureWithDDL(t, isNullExprLiveDDL, isNullExprLiveSeed)
	schema, err := schemaFromDDLErr(t, isNullExprLiveDDL)
	if err != nil {
		t.Fatal(err)
	}
	accepted := []string{
		"plain IS NULL",
		"nullable IS NULL",
		"low_cardinality IS NULL",
		"low_cardinality_nullable IS NULL",
		"plain IS NOT NULL",
		"nullable IS NOT NULL",
		"(nullable + 1) IS NULL",
		"(nullable + 1) IS NOT NULL",
	}
	for _, expression := range accepted {
		analysis, analysisErr := oracle.exec("SELECT toTypeName(" + expression + ") FROM t")
		if analysisErr != nil {
			t.Errorf("analysis of %s: %v", expression, analysisErr)
			continue
		}
		if got := strings.TrimSpace(analysis); got != "UInt8" {
			t.Errorf("analysis type of %s = %s, want UInt8", expression, got)
		}
		if _, executionErr := oracle.exec("SELECT ignore(" + expression + ") FROM t"); executionErr != nil {
			t.Errorf("execution of %s: %v", expression, executionErr)
		}
		got, inferErr := chgenInferType(schema, expression)
		if inferErr != nil {
			t.Errorf("chgen inference of %s: %v", expression, inferErr)
		} else if got != "UInt8" {
			t.Errorf("chgen type of %s = %s, want UInt8", expression, got)
		}
	}

	aliasAnalysis, err := oracle.exec("SELECT toTypeName(alias_value IS NULL) FROM (SELECT nullable AS alias_value FROM t)")
	if err != nil {
		t.Fatalf("analysis of alias IS NULL: %v", err)
	}
	if got := strings.TrimSpace(aliasAnalysis); got != "UInt8" {
		t.Errorf("analysis type of alias IS NULL = %s, want UInt8", got)
	}
	if _, err := oracle.exec("SELECT ignore(alias_value IS NOT NULL) FROM (SELECT nullable AS alias_value FROM t)"); err != nil {
		t.Errorf("execution of alias IS NOT NULL: %v", err)
	}

	for _, expression := range []string{"{p:Nullable(Int32)} IS NULL", "{p:Nullable(Int32)} IS NOT NULL"} {
		analysis, analysisErr := execIsNullQueryParameter(oracle, "SELECT toTypeName("+expression+")")
		if analysisErr != nil {
			t.Errorf("analysis of %s: %v", expression, analysisErr)
		} else if got := strings.TrimSpace(analysis); got != "UInt8" {
			t.Errorf("analysis type of %s = %s, want UInt8", expression, got)
		}
		if _, executionErr := execIsNullQueryParameter(oracle, "SELECT ignore("+expression+")"); executionErr != nil {
			t.Errorf("execution of %s: %v", expression, executionErr)
		}
	}

	for _, testCase := range []struct {
		expression string
		code       string
	}{
		{expression: "({p:Int32} + missing) IS NULL", code: "47"},
		{expression: "(missing + {p:Int32}) IS NULL", code: "47"},
		{expression: "({p:Int32} + unknown_function(plain)) IS NULL", code: "46"},
		{expression: "(unknown_function(plain) + {p:Int32}) IS NULL", code: "46"},
	} {
		if _, err := execIsNullQueryParameter(oracle, "DESCRIBE TABLE (SELECT "+testCase.expression+" AS result FROM t) FORMAT TabSeparatedRaw"); err == nil || clickHouseErrorCode(err.Error()) != testCase.code {
			t.Errorf("analysis of %s = %v, want Code %s", testCase.expression, err, testCase.code)
		}
		if _, err := execIsNullQueryParameter(oracle, "SELECT ignore("+testCase.expression+") FROM t FORMAT Null"); err == nil || clickHouseErrorCode(err.Error()) != testCase.code {
			t.Errorf("execution of %s = %v, want Code %s", testCase.expression, err, testCase.code)
		}
	}

	const nestedQuery = `SELECT id, available_at
FROM
(
    SELECT
        id,
        argMax(kind, version) AS kind,
        argMax(available_at, version) AS available_at
    FROM t
    GROUP BY id
) AS current_state
WHERE kind IN ('a', 'b')
  AND (available_at IS NULL OR available_at <= {cutoff:DateTime64(3, 'UTC')})
ORDER BY id ASC
LIMIT {limit:UInt64}`
	parameters := map[string]string{"cutoff": "2024-02-03 00:00:00", "limit": "10"}
	description, err := execIsNullWithParameters(oracle, "DESCRIBE TABLE ("+nestedQuery+") FORMAT TabSeparatedRaw", parameters)
	if err != nil {
		t.Fatalf("analysis of the nested query: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(description), "\n")
	if len(lines) != 2 {
		t.Fatalf("nested query columns = %d, want 2: %s", len(lines), description)
	}
	wantColumns := [][2]string{{"id", "UInt64"}, {"available_at", "Nullable(DateTime64(3, 'UTC'))"}}
	for index, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 || fields[0] != wantColumns[index][0] || fields[1] != wantColumns[index][1] {
			t.Errorf("nested query column %d = %q, want %q and %q", index, line, wantColumns[index][0], wantColumns[index][1])
		}
	}
	if _, err := execIsNullWithParameters(oracle, nestedQuery+" FORMAT Null", parameters); err != nil {
		t.Errorf("execution of the nested query: %v", err)
	}

	for _, expression := range []string{"trim(bad) IS NULL", "trim(bad) IS NOT NULL"} {
		if _, err := oracle.exec("DESCRIBE TABLE (SELECT " + expression + " AS result FROM t) FORMAT TabSeparatedRaw"); err == nil || clickHouseErrorCode(err.Error()) != "43" {
			t.Errorf("analysis of %s = %v, want Code 43", expression, err)
		}
		if _, err := oracle.exec("SELECT ignore(" + expression + ") FROM t FORMAT Null"); err == nil || clickHouseErrorCode(err.Error()) != "43" {
			t.Errorf("execution of %s = %v, want Code 43", expression, err)
		}
		if _, err := chgenInferType(schema, expression); err == nil {
			t.Errorf("chgen accepted %s", expression)
		}
	}
}

func execIsNullQueryParameter(oracle *chOracle, query string) (string, error) {
	return execIsNullWithParameters(oracle, query, map[string]string{"p": "1"})
}

func execIsNullWithParameters(oracle *chOracle, query string, parameters map[string]string) (string, error) {
	separator := "/?"
	if strings.Contains(oracle.url, "?") {
		separator = "&"
	}
	params := url.Values{
		"database":       []string{oracle.database},
		"default_format": []string{"TabSeparatedRaw"},
	}
	for name, value := range parameters {
		params.Set("param_"+name, value)
	}
	response, err := oracle.client.Post(oracle.url+separator+params.Encode(), "text/plain", strings.NewReader(query))
	if err != nil {
		return "", fmt.Errorf("HTTP query parameter: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("read query parameter response: %w", err)
	}
	if response.StatusCode != 200 {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}
	return strings.TrimRight(string(body), "\n"), nil
}
