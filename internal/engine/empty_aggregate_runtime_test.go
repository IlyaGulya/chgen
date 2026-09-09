package engine

import (
	"fmt"
	"strings"
	"testing"
)

const emptyAggregateDDL = `CREATE TABLE aggregate_events (
    scope UInt32,
    occurred_at DateTime64(3, 'UTC'),
    value Int32
) ENGINE = Memory`

func emptyAggregateQueries(t *testing.T) []Query {
	t.Helper()
	var source strings.Builder
	for _, query := range []struct{ name, command, minimum, maximum, tail string }{
		{"PlainSpan", "one", "min", "max", ""},
		{"NullableSpan", "one", "minOrNull", "maxOrNull", ""},
		{"PlainSpans", "many", "min", "max", ""},
		{"NullableSpans", "many", "minOrNull", "maxOrNull", ""},
		{"GroupedSpan", "one", "min", "max", " GROUP BY scope"},
		{"PresentSpan", "one", "min", "max", " HAVING count() > 0"},
	} {
		fmt.Fprintf(&source, `-- name: %s :%s
SELECT %s(occurred_at) AS min_time, %s(occurred_at) AS max_time
FROM aggregate_events
WHERE scope = chgen.arg('Scope')%s;
`, query.name, query.command, query.minimum, query.maximum, query.tail)
	}
	source.WriteString(`-- name: ConditionalSpan :one
SELECT minIfOrNull(occurred_at, value > 0) AS min_time,
       maxIfOrNull(occurred_at, value > 0) AS max_time
FROM aggregate_events WHERE scope = chgen.arg('Scope') GROUP BY scope;

-- name: NullableStatistics :one
SELECT sumOrNull(value) AS total, avgOrNull(value) AS average,
       argMinOrNull(occurred_at, value) AS first_time,
       argMaxOrNull(occurred_at, value) AS last_time
FROM aggregate_events WHERE scope = chgen.arg('Scope');
`)
	queries, err := parseQueriesWithSchema(t, source.String(), schemaFromDDL(t, emptyAggregateDDL))
	if err != nil {
		t.Fatal(err)
	}
	return queries
}

func TestEmptyAggregateResultTypes(t *testing.T) {
	for _, query := range emptyAggregateQueries(t) {
		for _, result := range query.Results {
			wantNullable := strings.HasPrefix(query.Name, "Nullable") || query.Name == "ConditionalSpan"
			wantCH := "DateTime64(3, 'UTC')"
			switch result.SQLName {
			case "total":
				wantCH = "Int64"
			case "average":
				wantCH = "Float64"
			}
			if wantNullable {
				wantCH = "Nullable(" + wantCH + ")"
			}
			if result.CHType.String() != wantCH || result.Asserted {
				t.Errorf("%s.%s: %+v; want inferred %s", query.Name, result.SQLName, result, wantCH)
			}
			if strings.Contains(result.CHType.String(), "DateTime64") {
				wantGo := "time.Time"
				if wantNullable {
					wantGo = "*time.Time"
				}
				if result.GoType != wantGo {
					t.Errorf("%s.%s: Go %s, want %s", query.Name, result.SQLName, result.GoType, wantGo)
				}
			}
		}
	}
}

func TestEmptyAggregateGeneratedRuntime(t *testing.T) {
	generated, err := Generate("aggregates", emptyAggregateQueries(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(generated), "chgenCheckScannedTime") {
		t.Fatal("generated code retains the misleading scan-check name")
	}
	runGeneratedRuntimeFixture(t, generated, "emptyaggregates")
}

func TestResultContractCannotMakeAnAggregateNullable(t *testing.T) {
	_, err := parseQueriesWithSchema(t, `-- name: WrongSpan :one
-- result-chtype: min_time Nullable(DateTime64(3, 'UTC'))
SELECT min(occurred_at) AS min_time FROM aggregate_events;
`, schemaFromDDL(t, emptyAggregateDDL))
	const want = "asserts Nullable(DateTime64(3, 'UTC')), inferred DateTime64(3, 'UTC')"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("a type-only contract must not change the server's empty-aggregate behavior: %v", err)
	}
}
