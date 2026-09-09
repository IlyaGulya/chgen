package engine

import (
	"strings"
	"testing"
)

func TestResultTypeContracts(t *testing.T) {
	schema := schemaFromDDL(t, "CREATE TABLE events (d Date, n Nullable(UInt32), id UInt32)")
	for _, expression := range []string{"clientMonth(d)", "clientMonth(otherClient(d))", "id"} {
		source := "-- name: Read :many\n-- result-chtype: month UInt32\nSELECT " + expression + " AS month FROM events"
		queries, err := parseQueriesWithSchema(t, source, schema)
		if err != nil {
			t.Fatalf("%s: %v", expression, err)
		}
		result := queries[0].Results[0]
		if result.GoType != "uint32" || result.CHType.String() != "UInt32" || !result.Asserted {
			t.Fatalf("unexpected result: %+v", result)
		}
		code, err := Generate("contracts", queries)
		if err != nil || !strings.Contains(string(code), "chgenCheckResultContract(rows,") {
			t.Fatalf("missing runtime enforcement: %v\n%s", err, code)
		}
	}
	for _, source := range []string{
		"-- name: Read :many\nSELECT clientMonth(d) AS month FROM events",
		"-- name: Read :many\n-- result: Month month uint32\nSELECT clientMonth(d) AS month FROM events",
	} {
		if _, err := parseQueriesWithSchema(t, source, schema); err == nil {
			t.Fatal("unknown function accepted without a CH contract")
		}
	}
	if _, err := parseQueriesWithSchema(t, "-- name: Ordered :many\n-- result-chtype: month UInt32\nSELECT clientMonth(d) AS month FROM events ORDER BY id", schema); err != nil {
		t.Fatalf("independently typed ordering refused: %v", err)
	}
}

func TestResultTypeContractRefusals(t *testing.T) {
	schema := schemaFromDDL(t, "CREATE TABLE events (d Date, n Nullable(UInt32), id UInt32)")
	for _, expression := range []string{
		"clientMonth(missing)",
		"clientMonth(otherClient(d), missing)",
		"clientMonth(toYYYYMM('invalid'))",
		"clientMonth(otherClient(d), toYYYYMM('invalid'))",
		"clientMonth(d) + 1",
		"toUInt32(clientMonth(d))",
		"n",
		"d",
	} {
		source := "-- name: Read :many\n-- result-chtype: month UInt32\nSELECT " + expression + " AS month FROM events"
		if _, err := parseQueriesWithSchema(t, source, schema); err == nil {
			t.Errorf("contract hid an error in %s", expression)
		}
	}
	for _, source := range []string{
		"-- result-chtype: month UInt32\n-- name: Read :many\nSELECT id AS month FROM events",
		"-- name: Read :many\nSELECT id AS month FROM events\n-- result-chtype: month UInt32",
		"-- name: Read :exec\n-- result-chtype: month UInt32\nSELECT id AS month FROM events",
		"-- name: Read :many\n-- result-chtype: month UInt32\n-- result-chtype: month UInt32\nSELECT id AS month FROM events",
		"-- name: Read :many\n-- result-chtype: unused UInt32\nSELECT id AS month FROM events",
		"-- name: Read :many\n-- result-chtype: month UInt32\nSELECT id AS month, id AS month FROM events",
		"-- name: Read :many\n-- result-chtype: month UInt32\nSELECT id AS month FROM events UNION ALL SELECT id FROM events",
		"-- name: Read :many\n-- result-chtype: month UInt32\nSELECT *, id AS month FROM events",
		"-- name: Read :many\n-- result-chtype: month UInt32 DEFAULT 1\nSELECT id AS month FROM events",
		"-- name: Read :many\n-- result-chtype: month UInt32\nSELECT clientMonth(d) AS month FROM events ORDER BY month",
	} {
		if _, err := parseQueriesWithSchema(t, source, schema); err == nil {
			t.Errorf("accepted invalid contract: %s", source)
		}
	}
}

func TestResultTypeContractTypeSyntax(t *testing.T) {
	for _, typeName := range []string{"UInt32", "Nullable(UInt32)", "DateTime64(3, 'UTC')", "Array(UInt32)"} {
		_, _, err := parseResultTypeContract("-- result-chtype: value "+typeName, 2)
		if err != nil {
			t.Errorf("%s: %v", typeName, err)
		}
	}
}

func TestResultContractLexicalBoundary(t *testing.T) {
	const sql = `-- name: Read :one
SELECT 'first line
-- result-chtype: value UInt32
last line' AS value;
`
	queries, err := parseQueriesWithSchema(t, sql, &Schema{Tables: map[string]Table{}})
	if err != nil {
		t.Fatal(err)
	}
	if queries[0].Results[0].Asserted || !strings.Contains(queries[0].SQL, "-- result-chtype:") {
		t.Fatal("annotation-looking string contents changed")
	}
}

func TestResultContractParameters(t *testing.T) {
	schema := &Schema{Tables: map[string]Table{}}
	const header = "-- name: Read :one\n-- result-chtype: value UInt32\n"
	if _, err := parseQueriesWithSchema(t, header+"-- param: Input uint32\nSELECT clientFn(chgen.arg('input')) AS value", schema); err != nil {
		t.Fatalf("typed parameter refused: %v", err)
	}
	if _, err := parseQueriesWithSchema(t, header+"SELECT clientFn(chgen.arg('input')) AS value", schema); err == nil {
		t.Fatal("result contract invented a parameter type")
	}
}
