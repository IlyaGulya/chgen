package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseSchemaFilesMergesMigrationCatalog(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.sql")
	second := filepath.Join(dir, "second.sql")
	if err := os.WriteFile(first, []byte("CREATE TABLE first (id UInt64);"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("CREATE TABLE second (label String);"), 0o600); err != nil {
		t.Fatal(err)
	}

	schema, err := schemasFromFilesErr(t, []string{first, second})
	if err != nil {
		t.Fatalf("schemasFromFilesErr() error = %v", err)
	}
	if _, ok := schema.Tables["first"]; !ok {
		t.Fatal("merged schema is missing first table")
	}
	if _, ok := schema.Tables["second"]; !ok {
		t.Fatal("merged schema is missing second table")
	}
}

func TestParseWithSchemasGeneratesReusableExternalTableParams(t *testing.T) {
	physical, err := schemaFromDDLErr(t, `CREATE TABLE events (id String, payload String);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr(t, physical) error = %v", err)
	}
	external, err := schemaFromDDLErr(t, `CREATE TABLE ordered_string_keys
(
    ordinal UInt32,
    id String
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr(t, external) error = %v", err)
	}

	queries, err := parseQueriesWithCatalogsErr(t, `-- name: ReadIncludedEvents :many
SELECT events.id, events.payload
FROM events
INNER JOIN chgen.external('IncludedKeys', ordered_string_keys) AS included
    ON included.id = events.id
LEFT JOIN chgen.external('ExcludedKeys', ordered_string_keys) AS excluded
    ON excluded.id = events.id
WHERE excluded.id = ''
ORDER BY included.ordinal`, physical, external)
	if err != nil {
		t.Fatalf("parseQueriesWithCatalogsErr() error = %v", err)
	}
	if got, want := len(queries[0].ExternalParams), 2; got != want {
		t.Fatalf("external parameter count = %d, want %d", got, want)
	}
	if got, want := queries[0].ExternalParams[0].GoName, "IncludedKeys"; got != want {
		t.Errorf("first external parameter = %q, want %q", got, want)
	}
	if got, want := queries[0].ExternalParams[1].WireName, "excludedKeys"; got != want {
		t.Errorf("second external wire name = %q, want %q", got, want)
	}
	if strings.Contains(queries[0].SQL, "chgen.external") {
		t.Fatalf("source-only external function leaked into runtime SQL: %s", queries[0].SQL)
	}
	for _, want := range []string{"includedKeys AS included", "excludedKeys AS excluded"} {
		if !strings.Contains(queries[0].SQL, want) {
			t.Errorf("runtime SQL missing %q: %s", want, queries[0].SQL)
		}
	}

	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(generated)
	for _, want := range []string{
		"type OrderedStringKeysRow struct",
		"IncludedKeys []OrderedStringKeysRow",
		"ExcludedKeys []OrderedStringKeysRow",
		`newOrderedStringKeysRowExternalTable("includedKeys", arg.IncludedKeys)`,
		`newOrderedStringKeysRowExternalTable("excludedKeys", arg.ExcludedKeys)`,
		"clickhouse.WithExternalTable(externalTables...)",
		`ext.Column("ordinal", "UInt32")`,
		`table.Append(row.Ordinal, row.ID)`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("generated output missing %q:\n%s", want, text)
		}
	}
}

func TestParseWithSchemasInfersExternalParamNameFromSchema(t *testing.T) {
	physical, err := schemaFromDDLErr(t, `CREATE TABLE events (id String);`)
	if err != nil {
		t.Fatal(err)
	}
	external, err := schemaFromDDLErr(t, `CREATE TABLE requested_keys (id String);`)
	if err != nil {
		t.Fatal(err)
	}
	queries, err := parseQueriesWithCatalogsErr(t, `-- name: ReadEvents :many
SELECT events.id
FROM events
INNER JOIN chgen.external(requested_keys) AS requested ON requested.id = events.id`, physical, external)
	if err != nil {
		t.Fatalf("parseQueriesWithCatalogsErr() error = %v", err)
	}
	if got, want := queries[0].ExternalParams[0].GoName, "RequestedKeys"; got != want {
		t.Fatalf("external parameter name = %q, want %q", got, want)
	}
	if got, want := queries[0].ExternalParams[0].WireName, "requested_keys"; got != want {
		t.Fatalf("external wire name = %q, want %q", got, want)
	}
}

func TestParseQueryFileRejectsRemovedIncludeDirective(t *testing.T) {
	// A query file that still carries the removed include directive must fail
	// with the message that tells the reader what to do with the stale line.
	dir := t.TempDir()
	queryPath := filepath.Join(dir, "queries.sql")
	querySource := `-- chgen:external-schema schemas/external_tables.sql

-- name: ReadEvents :many
SELECT events.id
FROM events`
	if err := os.WriteFile(queryPath, []byte(querySource), 0o600); err != nil {
		t.Fatal(err)
	}
	physical, err := schemaFromDDLErr(t, `CREATE TABLE events (id String);`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = parseQueryFileWithSchema(t, queryPath, physical)
	want := queryPath + `:1: the -- chgen:external-schema directive was removed; ` +
		`delete this line, add "schemas/external_tables.sql" to schema in chgen.yaml, ` +
		`and mark each row schema with -- chgen:external before its CREATE TABLE`
	if err == nil || err.Error() != want {
		t.Fatalf("parseQueryFileWithSchema() error = %v, want %q", err, want)
	}
}

func TestParseWithSchemasRejectsUnknownExternalSchema(t *testing.T) {
	physical, err := schemaFromDDLErr(t, `CREATE TABLE events (id String);`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = parseQueriesWithCatalogsErr(t, `-- name: ReadEvents :many
SELECT events.id
FROM events
INNER JOIN chgen.external(missing_keys) AS requested ON requested.id = events.id`, physical, &Schema{Tables: map[string]Table{}})
	if err == nil || !strings.Contains(err.Error(), "chgen.external(missing_keys): unknown external schema") {
		t.Fatalf("parseQueriesWithCatalogsErr() error = %v, want unknown external schema", err)
	}
}

func TestNormalizeExternalTablesSkipsSQLLiteralsAndComments(t *testing.T) {
	external, err := schemaFromDDLErr(t, `CREATE TABLE requested_keys (id String);`)
	if err != nil {
		t.Fatal(err)
	}
	input := `SELECT 'chgen.external(fake_literal)'
FROM chgen.external(requested_keys)
-- chgen.external(fake_line_comment)
/* chgen.external(fake_block_comment) */`
	normalized, params, err := normalizeExternalTables(input, external, nil)
	if err != nil {
		t.Fatalf("normalizeExternalTables() error = %v", err)
	}
	if got, want := len(params), 1; got != want {
		t.Fatalf("external parameter count = %d, want %d", got, want)
	}
	for _, want := range []string{
		"'chgen.external(fake_literal)'",
		"-- chgen.external(fake_line_comment)",
		"/* chgen.external(fake_block_comment) */",
	} {
		if !strings.Contains(normalized, want) {
			t.Errorf("normalized SQL lost %q: %s", want, normalized)
		}
	}
}

func TestParseAndGenerateActiveScopeQuery(t *testing.T) {
	input := `-- name: ListActiveScopes :many
-- param: Limit uint64
-- result: ScopeType scope_type string
-- result: ScopeKey scope_key string
-- result: LastSeenAt last_seen_at time.Time
SELECT
    scope_type AS scope_type,
    scope_key AS scope_key,
    maxMerge(last_seen_at_state) AS last_seen_at
FROM domain_event_active_scopes_v2
GROUP BY scope_type, scope_key
ORDER BY last_seen_at DESC
LIMIT chgen.arg('Limit')`

	queries, err := parseQueriesWithDDL(t, `CREATE TABLE domain_event_active_scopes_v2
(
    scope_type LowCardinality(String),
    scope_key String,
    last_seen_at_state AggregateFunction(max, DateTime64(3, 'UTC'))
)
ENGINE = AggregatingMergeTree
ORDER BY (scope_type, scope_key);`, input)
	if err != nil {
		t.Fatalf("parseQueriesWithDDL() error = %v", err)
	}
	if len(queries) != 1 {
		t.Fatalf("parseQueriesWithDDL() returned %d queries, want 1", len(queries))
	}
	if got := queries[0].Results[2].SQLName; got != "last_seen_at" {
		t.Fatalf("third result alias = %q, want last_seen_at", got)
	}

	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(generated)
	for _, want := range []string{
		"type ListActiveScopesRow struct",
		"LastSeenAt time.Time",
		"type ListActiveScopesParams struct",
		"type Querier interface",
		"type MockQuerier struct",
		"ListActiveScopesFunc func(ctx context.Context, arg ListActiveScopesParams)",
		"arg.Limit",
		"maxMerge(last_seen_at_state)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("generated output missing %q:\n%s", want, text)
		}
	}
}

func TestGenerateManyQueryUsesResultCapacityHint(t *testing.T) {
	input := `-- name: ReadEvents :many
-- param: Keys []string
-- result-capacity: Keys
-- result: Value value string
SELECT value AS value
FROM events
WHERE value IN chgen.arg('Keys')`

	queries, err := parseQueriesWithDDL(t, `CREATE TABLE events (value String);`, input)
	if err != nil {
		t.Fatalf("parseQueriesWithDDL() error = %v", err)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(generated)
	if !strings.Contains(text, "result := make([]ReadEventsRow, 0, len(arg.Keys))") {
		t.Fatalf("generated output has no result capacity hint:\n%s", text)
	}
	if !strings.Contains(text, "var row ReadEventsRow\n\tfor rows.Next()") {
		t.Fatalf("generated output does not reuse the scan row:\n%s", text)
	}
}

func TestGenerateRejectsInvalidResultCapacityHint(t *testing.T) {
	input := `-- name: ReadEvents :many
-- param: Limit uint64
-- result-capacity: Limit
-- result: Value value string
SELECT value AS value
FROM events
LIMIT chgen.arg('Limit')`

	queries, err := parseQueriesWithDDL(t, `CREATE TABLE events (value String);`, input)
	if err != nil {
		t.Fatalf("parseQueriesWithDDL() error = %v", err)
	}
	_, err = Generate("querygen", queries)
	if err == nil || !strings.Contains(err.Error(), "is not a slice parameter") {
		t.Fatalf("Generate() error = %v, want invalid result capacity", err)
	}
}

func TestParseWithSchemaInfersTypesFromDDL(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE domain_event_active_scopes_v2
(
    scope_type LowCardinality(String),
    scope_key String,
    last_seen_at_state AggregateFunction(max, DateTime64(3, 'UTC'))
)
ENGINE = AggregatingMergeTree
ORDER BY (scope_type, scope_key);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}

	queries, err := parseQueriesWithSchema(t, `-- name: ListScopes :many
-- param: Limit
SELECT
    scope_type AS scope_type,
    scope_key AS scope_key,
    maxMerge(last_seen_at_state) AS last_seen_at
FROM domain_event_active_scopes_v2
GROUP BY scope_type, scope_key
LIMIT chgen.arg('Limit')`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Params[0].GoType, "uint64"; got != want {
		t.Errorf("inferred parameter type = %q, want %q", got, want)
	}
	for index, want := range []struct {
		name string
		typ  string
	}{
		{name: "ScopeType", typ: "string"},
		{name: "ScopeKey", typ: "string"},
		{name: "LastSeenAt", typ: "time.Time"},
	} {
		result := queries[0].Results[index]
		if result.GoName != want.name || result.GoType != want.typ {
			t.Errorf("result[%d] = %#v, want name=%q type=%q", index, result, want.name, want.typ)
		}
	}
}

func TestParseSchemaMapsNullableAndContainerTypes(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE example
(
    label Nullable(String),
    ids Array(UInt64),
    attrs Map(String, String)
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadExample :many
SELECT
    label AS label,
    ids AS ids,
    attrs AS attrs
FROM example`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	got := []string{
		queries[0].Results[0].GoType,
		queries[0].Results[1].GoType,
		queries[0].Results[2].GoType,
	}
	want := []string{"*string", "[]uint64", "map[string]string"}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("result[%d] type = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestParseWithSchemaInfersSortedUniqueAggregateArrays(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events
(
    run_id UInt64,
    worker_id String,
    failed UInt8
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}

	queries, err := parseQueriesWithSchema(t, `-- name: ReadDeterministicSamples :one
SELECT
    arraySlice(arraySort(groupUniqArray(run_id)), 1, 5) AS run_ids,
    arraySlice(arraySort(groupUniqArrayIf(worker_id, failed = 1)), 1, 5) AS worker_ids
FROM events`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}

	if got, want := queries[0].Results, []Result{
		{GoName: "RunIds", SQLName: "run_ids", GoType: "[]uint64"},
		{GoName: "WorkerIds", SQLName: "worker_ids", GoType: "[]string"},
	}; !equalResults(got, want) {
		t.Fatalf("sorted aggregate results = %#v, want %#v", got, want)
	}
}

func TestParseWithSchemaInfersTupleCTEJoinKeyAndArrayResize(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events (scope String, value UInt64);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}

	queries, err := parseQueriesWithSchema(t, `-- name: ReadBoundedMinima :many
WITH source AS
(
    SELECT scope, value, tuple(scope) AS grain
    FROM events
), first AS
(
    SELECT grain, min(value) AS value_1
    FROM source
    GROUP BY grain
)
SELECT arrayResize(array(first.value_1, source.value), 1) AS values
FROM source INNER JOIN first USING (grain)`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}

	if got, want := queries[0].Results[0].GoType, "[]uint64"; got != want {
		t.Fatalf("bounded minima result type = %q, want %q", got, want)
	}
}

func TestParseWithSchemaAllowsPartialResultTypeOverrides(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events (scope_key String, payload String);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadEvents :many
-- result: Payload payload []byte
SELECT scope_key, payload
FROM events`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Results[0].GoType, "string"; got != want {
		t.Errorf("inferred result type = %q, want %q", got, want)
	}
	if got, want := queries[0].Results[1].GoName, "Payload"; got != want {
		t.Errorf("overridden result name = %q, want %q", got, want)
	}
	if got, want := queries[0].Results[1].GoType, "[]byte"; got != want {
		t.Errorf("overridden result type = %q, want %q", got, want)
	}
}

func TestGenerateAllowsJSONRawMessageResultOverride(t *testing.T) {
	queries, err := parseQueriesWithDDL(t, `CREATE TABLE events (payload String);`, `-- name: ReadPayload :many
-- result: Payload payload json.RawMessage
SELECT payload AS payload
FROM events`)
	if err != nil {
		t.Fatalf("parseQueriesWithDDL() error = %v", err)
	}

	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(generated)
	for _, want := range []string{`"encoding/json"`, "Payload json.RawMessage"} {
		if !strings.Contains(text, want) {
			t.Errorf("generated output missing %q:\n%s", want, text)
		}
	}
}

func TestParseWithSchemaInfersPredicateParameterType(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE example (scope_key String);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: FindExample :many
-- param: ScopeKey
SELECT scope_key AS scope_key
FROM example
WHERE scope_key = chgen.arg('ScopeKey')`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Params[0].GoType, "string"; got != want {
		t.Errorf("inferred predicate parameter type = %q, want %q", got, want)
	}
}

func TestParseWithSchemaInfersParameterAndColumnNames(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events
(
    aggregate_id String,
    scope_key String
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadEvents :many
SELECT aggregate_id, scope_key
FROM events
WHERE aggregate_id IN (chgen.arg('AggregateIDs'))`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Params[0].GoName, "AggregateIDs"; got != want {
		t.Errorf("inferred parameter name = %q, want %q", got, want)
	}
	if got, want := queries[0].Params[0].GoType, "[]string"; got != want {
		t.Errorf("inferred parameter type = %q, want %q", got, want)
	}
	for index, want := range []string{"AggregateID", "ScopeKey"} {
		if got := queries[0].Results[index].GoName; got != want {
			t.Errorf("result[%d] GoName = %q, want %q", index, got, want)
		}
	}
}

func TestParseWithSchemaInfersCTEAndDerivedTableTypes(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events
(
    event_id UInt64,
    scope_key String,
    occurred_at DateTime64(3, 'UTC')
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadThroughRelations :many
WITH candidate AS (
    SELECT event_id, occurred_at
    FROM events
    WHERE scope_key = chgen.arg('ScopeKey')
)
SELECT payload.event_id, payload.occurred_at
FROM (
    SELECT event_id, occurred_at
    FROM candidate
    WHERE event_id > chgen.arg('AfterEventID')
) AS payload
WHERE payload.occurred_at >= chgen.arg('Floor')`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Params, []Param{
		{GoName: "ScopeKey", GoType: "string"},
		{GoName: "AfterEventID", GoType: "uint64"},
		{GoName: "Floor", GoType: "time.Time"},
	}; !equalParams(got, want) {
		t.Fatalf("relation parameters = %#v, want %#v", got, want)
	}
	for index, want := range []struct {
		name string
		typ  string
	}{
		{name: "EventID", typ: "uint64"},
		{name: "OccurredAt", typ: "time.Time"},
	} {
		result := queries[0].Results[index]
		if result.GoName != want.name || result.GoType != want.typ {
			t.Errorf("result[%d] = %#v, want name=%q type=%q", index, result, want.name, want.typ)
		}
	}
}

func TestParseWithSchemaInfersScalarCTEType(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE progress (reset_epoch UInt64, position UInt64);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadProgress :one
WITH (
    SELECT max(reset_epoch)
    FROM progress
    WHERE reset_epoch = chgen.arg('ResetEpoch')
) AS current_epoch
SELECT (
    SELECT max(position)
    FROM progress
    WHERE reset_epoch = current_epoch
) AS cursor_seq`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Params, []Param{{GoName: "ResetEpoch", GoType: "uint64"}}; !equalParams(got, want) {
		t.Fatalf("scalar CTE parameters = %#v, want %#v", got, want)
	}
	if got, want := queries[0].Results[0], (Result{GoName: "CursorSeq", SQLName: "cursor_seq", GoType: "*uint64"}); !equalResult(got, want) {
		t.Fatalf("scalar CTE result = %#v, want %#v", got, want)
	}
}

func TestParseWithSchemaNamedParamAnnotationOverridesOneArgument(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events (event_id UInt64);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadEventsWithNullableLimit :many
-- param: Limit *uint64
SELECT event_id
FROM events
LIMIT ifNull(chgen.arg('Limit'), toUInt64(-1))`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Params, []Param{{GoName: "Limit", GoType: "*uint64"}}; !equalParams(got, want) {
		t.Fatalf("annotated named parameters = %#v, want %#v", got, want)
	}
	if got, want := queries[0].ParamIndexes, []int{0}; !equalInts(got, want) {
		t.Fatalf("annotated named parameter indexes = %#v, want %#v", got, want)
	}
}

func TestParseWithSchemaInfersLimitTypeWithoutParamAnnotation(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events (scope_key String);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ListEvents :many
SELECT scope_key
FROM events
LIMIT chgen.arg('Limit')`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Params[0], (Param{GoName: "Limit", GoType: "uint64"}); !equalParam(got, want) {
		t.Errorf("inferred limit parameter = %#v, want %#v", got, want)
	}
}

func TestParseWithSchemaReusesRepeatedAutoParameter(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events (scope_key String);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadEvents :many
SELECT scope_key
FROM events
WHERE (chgen.arg('ScopeKey') = '' OR startsWith(scope_key, chgen.arg('ScopeKey')))`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if strings.Contains(queries[0].SQL, "chgen.arg") {
		t.Fatalf("source-only chgen.arg syntax leaked into generated SQL: %s", queries[0].SQL)
	}
	if got, want := queries[0].Params, []Param{{GoName: "ScopeKey", GoType: "string"}}; !equalParams(got, want) {
		t.Fatalf("reused params = %#v, want %#v", got, want)
	}
	if got, want := queries[0].ParamIndexes, []int{0, 0}; !equalInts(got, want) {
		t.Fatalf("parameter indexes = %#v, want %#v", got, want)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(string(generated), "arg.ScopeKey, arg.ScopeKey") {
		t.Fatalf("generated query does not bind the repeated parameter twice:\n%s", generated)
	}
}

func TestParseWithSchemaNamedCollectionParameterKeepsSourceName(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events (aggregate_id UInt64);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadEventsByIDs :many
SELECT aggregate_id
FROM events
WHERE aggregate_id IN (chgen.arg('IDs'))`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Params, []Param{{GoName: "IDs", GoType: "[]uint64"}}; !equalParams(got, want) {
		t.Fatalf("named collection params = %#v, want %#v", got, want)
	}
}

func TestNormalizeNamedArgsSkipsSQLLiteralsAndComments(t *testing.T) {
	input := `SELECT chgen.arg('Value'), 'chgen.arg(Fake)', "chgen.arg(Identifier)"
-- chgen.arg('LineComment')
/* chgen.arg('BlockComment') */`
	normalized, names, err := normalizeNamedArgs(input)
	if err != nil {
		t.Fatalf("normalizeNamedArgs() error = %v", err)
	}
	if got, want := names, []string{"Value"}; !equalStrings(got, want) {
		t.Fatalf("named args = %#v, want %#v", got, want)
	}
	if got := strings.Count(normalized, "?"); got != 1 {
		t.Fatalf("normalized SQL has %d placeholders, want 1: %s", got, normalized)
	}
	for _, want := range []string{"'chgen.arg(Fake)'", `"chgen.arg(Identifier)"`, "-- chgen.arg('LineComment')", "/* chgen.arg('BlockComment') */"} {
		if !strings.Contains(normalized, want) {
			t.Errorf("normalized SQL lost %q: %s", want, normalized)
		}
	}
}

func TestParseWithSchemaRejectsNamedParameterTypeConflict(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events
(
    text_value String,
    numeric_value UInt64
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	_, err = parseQueriesWithSchema(t, `-- name: ReadEvents :many
SELECT text_value
FROM events
WHERE chgen.arg('Value') = text_value
   OR chgen.arg('Value') = numeric_value`, schema)
	if err == nil {
		t.Fatal("parseQueriesWithSchema() succeeded for incompatible repeated named argument")
	}
	for _, want := range []string{"chgen.arg(\"Value\")", "String", "UInt64"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

func TestParseWithSchemaAllowsLowCardinalityNamedParameterReuse(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events
(
    plain_name String,
    compact_name LowCardinality(String)
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadEvents :many
SELECT plain_name, compact_name
FROM events
WHERE chgen.arg('Name') = plain_name
   OR chgen.arg('Name') = compact_name`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Params, []Param{{GoName: "Name", GoType: "string"}}; !equalParams(got, want) {
		t.Fatalf("low-cardinality named parameters = %#v, want %#v", got, want)
	}
}

func TestParseWithSchemaInfersNullableStatsAndArrayPrefixParameter(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events
(
    event_type String,
    occurred_at DateTime64(3, 'UTC'),
    drain_cursor UInt64
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: Stats :one
-- param: EventTypePrefixes []string
SELECT
    count() AS count,
    minOrNull(occurred_at) AS oldest_occurred_at,
    minOrNull(drain_cursor) AS min_drain_cursor
FROM events
WHERE empty(chgen.arg('EventTypePrefixes'))
   OR arrayExists(prefix -> startsWith(event_type, prefix), chgen.arg('EventTypePrefixes'))`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Params, []Param{{GoName: "EventTypePrefixes", GoType: "[]string"}}; !equalParams(got, want) {
		t.Fatalf("stats parameters = %#v, want %#v", got, want)
	}
	for index, want := range []struct {
		name string
		typ  string
	}{
		{name: "Count", typ: "uint64"},
		{name: "OldestOccurredAt", typ: "*time.Time"},
		{name: "MinDrainCursor", typ: "*uint64"},
	} {
		result := queries[0].Results[index]
		if result.GoName != want.name || result.GoType != want.typ {
			t.Errorf("result[%d] = %#v, want name=%q type=%q", index, result, want.name, want.typ)
		}
	}
}

func TestParseWithSchemaGeneratesFixedInsertAndUpdateParams(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events
(
    aggregate_id String,
    scope_key String,
    aggregate_version UInt64
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: InsertEvent :exec
INSERT INTO events (aggregate_id, scope_key, aggregate_version)
VALUES (chgen.arg('AggregateID'), chgen.arg('ScopeKey'), chgen.arg('AggregateVersion'))

-- name: UpdateEvent :exec
ALTER TABLE events
UPDATE scope_key = chgen.arg('ScopeKey'), aggregate_version = chgen.arg('AggregateVersion')
WHERE aggregate_id = chgen.arg('AggregateID')`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Params, []Param{
		{GoName: "AggregateID", GoType: "string"},
		{GoName: "ScopeKey", GoType: "string"},
		{GoName: "AggregateVersion", GoType: "uint64"},
	}; !equalParams(got, want) {
		t.Errorf("INSERT params = %#v, want %#v", got, want)
	}
	if got, want := queries[1].Params, []Param{
		{GoName: "ScopeKey", GoType: "string"},
		{GoName: "AggregateVersion", GoType: "uint64"},
		{GoName: "AggregateID", GoType: "string"},
	}; !equalParams(got, want) {
		t.Errorf("UPDATE params = %#v, want %#v", got, want)
	}

	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	for _, want := range []string{
		"func (q *Queries) InsertEvent",
		"func (q *Queries) UpdateEvent",
		"arg.AggregateVersion",
	} {
		if !strings.Contains(string(generated), want) {
			t.Errorf("generated output missing %q:\n%s", want, generated)
		}
	}
}

func TestParseWithSchemaGeneratesInsertSelectParams(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE facts
(
    occurred_at DateTime64(3, 'UTC'),
    repository String,
    value UInt64
);
CREATE TABLE rollups
(
    bucket_start DateTime64(3, 'UTC'),
    repository String,
    total UInt64
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: RefreshRollup :exec
-- param: WindowStart time.Time
-- param: WindowEnd time.Time
INSERT INTO rollups (bucket_start, repository, total)
SELECT
    toDateTime(chgen.arg('WindowStart')),
    repository,
    sum(value)
FROM facts
WHERE occurred_at >= toDateTime(chgen.arg('WindowStart'))
  AND occurred_at < toDateTime(chgen.arg('WindowEnd'))
GROUP BY repository`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Params, []Param{
		{GoName: "WindowStart", GoType: "time.Time"},
		{GoName: "WindowEnd", GoType: "time.Time"},
	}; !equalParams(got, want) {
		t.Fatalf("INSERT SELECT params = %#v, want %#v", got, want)
	}
	if got, want := queries[0].ParamIndexes, []int{0, 0, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("INSERT SELECT parameter indexes = %#v, want %#v", got, want)
	}

	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(generated)
	for _, want := range []string{
		"func (q *Queries) RefreshRollup",
		"arg.WindowStart, arg.WindowStart, arg.WindowEnd",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("generated output missing %q:\n%s", want, text)
		}
	}
}

func TestParseWithSchemaGeneratesAlterDeleteParams(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE rollups
(
    bucket_start DateTime64(3, 'UTC'),
    repository String
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: DeleteRollupWindow :exec
-- param: WindowStart time.Time
-- param: WindowEnd time.Time
ALTER TABLE rollups
DELETE WHERE bucket_start >= toDateTime(chgen.arg('WindowStart'))
  AND bucket_start < toDateTime(chgen.arg('WindowEnd'))
SETTINGS mutations_sync = 2`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Params, []Param{
		{GoName: "WindowStart", GoType: "time.Time"},
		{GoName: "WindowEnd", GoType: "time.Time"},
	}; !equalParams(got, want) {
		t.Fatalf("ALTER DELETE params = %#v, want %#v", got, want)
	}
	if got, want := queries[0].ParamIndexes, []int{0, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ALTER DELETE parameter indexes = %#v, want %#v", got, want)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(string(generated), "SETTINGS mutations_sync = 2") {
		t.Fatalf("generated ALTER DELETE SQL lost the SETTINGS clause:\n%s", generated)
	}
}

func TestParseWithSchemaPreservesLimitWithTies(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events
(
    id String,
    drain_cursor UInt64
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadCursorBoundary :many
-- param: Limit *uint64
SELECT id, drain_cursor
FROM
(
    SELECT id, drain_cursor
    FROM events
    ORDER BY drain_cursor
    LIMIT ifNull(chgen.arg('Limit'), toUInt64(-1)) WITH TIES
)`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(string(generated), "LIMIT ifNull(?, toUInt64(-1)) WITH TIES") {
		t.Fatalf("generated SELECT SQL lost the WITH TIES modifier:\n%s", generated)
	}
}

func TestParseWithSchemaRejectsInsertSelectTypeMismatch(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE facts (value String);
CREATE TABLE rollups (total UInt64);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}

	_, err = parseQueriesWithSchema(t, `-- name: RefreshRollup :exec
INSERT INTO rollups (total)
SELECT value
FROM facts`, schema)
	if err == nil {
		t.Fatal("parseQueriesWithSchema() succeeded for incompatible INSERT SELECT result")
	}
	for _, want := range []string{"target column total", "String", "UInt64"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

func TestParseWithSchemaRejectsImplicitInsertSelectColumnCountMismatch(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE facts (value UInt64);
CREATE TABLE rollups (total UInt64, label String);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}

	_, err = parseQueriesWithSchema(t, `-- name: RefreshRollup :exec
INSERT INTO rollups
SELECT value
FROM facts`, schema)
	if err == nil || !strings.Contains(err.Error(), "insertable columns") {
		t.Fatalf("parseQueriesWithSchema() error = %v, want implicit target column count error", err)
	}
}

func TestParseWithSchemaInfersClickHouseFunctionResultTypes(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE facts
(
    small UInt8,
    signed Int8,
    values UInt32
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}

	queries, err := parseQueriesWithSchema(t, `-- name: ReadStats :one
SELECT
    sum(small) AS sum_small,
    sum(signed) AS sum_signed,
    quantile(0.5)(values) AS p50,
    if(1, small, values) AS promoted,
    multiIf(1, small, 0, values, values) AS multi_promoted
FROM facts
GROUP BY small, values`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}

	want := []string{"uint64", "int64", "float64", "uint32", "uint32"}
	for index, expected := range want {
		if got := queries[0].Results[index].GoType; got != expected {
			t.Errorf("result[%d] GoType = %q, want %q", index, got, expected)
		}
	}
}

func TestParseWithSchemaRejectsIncompatibleNumericNamedArgumentReuse(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events
(
    narrow UInt8,
    wide UInt64
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}

	_, err = parseQueriesWithSchema(t, `-- name: ReadEvents :many
SELECT narrow
FROM events
WHERE chgen.arg('Value') = narrow
   OR chgen.arg('Value') = wide`, schema)
	if err == nil {
		t.Fatal("parseQueriesWithSchema() succeeded for incompatible numeric named argument reuse")
	}
	for _, want := range []string{"chgen.arg(\"Value\")", "UInt8", "UInt64"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

func TestGenerateNullableExecParamsUnwrapPointers(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events
(
    label Nullable(String),
    observed_at Nullable(DateTime64(3, 'UTC'))
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: InsertEvent :exec
INSERT INTO events (label, observed_at) VALUES (chgen.arg('Label'), chgen.arg('ObservedAt'))`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(generated)
	// A fixed INSERT whose VALUES tuple is bare placeholders now uses the
	// native PrepareBatch/Append path. That path binds a Nullable column from
	// the pointer itself: a nil pointer becomes NULL and a non-nil pointer
	// keeps its sub-second fraction, which the client-side text interpolation
	// of conn.Exec drops. Measured against ClickHouse 25.8.29.51 with driver
	// v2.47.0. chgenNullableParam therefore belongs to the Exec path only.
	for _, want := range []string{
		"Label      *string",
		"ObservedAt *time.Time",
		"batch.Append(arg.Label, arg.ObservedAt)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("generated output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "chgenNullableParam(arg.Label)") {
		t.Errorf("batch path must pass the pointer directly, not unwrap it:\n%s", text)
	}
}

// equalParams compares the two fields that a caller declares: the generated
// field name and its Go type. It ignores the temporal walk plan, which is an
// internal detail derived from the ClickHouse type and is asserted by the
// temporal tests through the generated output.
// equalParam compares the generated shape of one parameter. Param now carries
// the inferred CHType, which holds slices, thus the struct is no longer
// comparable with ==. These tests assert the generated Go shape only.
func equalParam(left, right Param) bool {
	return left.GoName == right.GoName && left.GoType == right.GoType
}

// equalResult compares the generated shape of one result column, for the same
// reason as equalParam.
func equalResult(left, right Result) bool {
	return left.GoName == right.GoName &&
		left.SQLName == right.SQLName &&
		left.GoType == right.GoType
}

func equalResults(left, right []Result) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !equalResult(left[index], right[index]) {
			return false
		}
	}
	return true
}

func equalParams(left, right []Param) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !equalParam(left[index], right[index]) {
			return false
		}
	}
	return true
}

func equalInts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestParseWithSchemaInfersParametricAggregateTypes(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE rollups
(
    duration_state AggregateFunction(quantile(0.5), Float64),
    values Array(UInt64),
    attrs Map(String, String)
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadRollup :many
SELECT
    quantileMerge(0.5)(duration_state) AS p50,
    arrayElement(values, 1) AS first_value,
    mapKeys(attrs) AS keys
FROM rollups
GROUP BY values, attrs`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	want := []string{"float64", "uint64", "[]string"}
	for index, expected := range want {
		if got := queries[0].Results[index].GoType; got != expected {
			t.Errorf("result[%d] type = %q, want %q", index, got, expected)
		}
	}
}

func TestParseWithSchemaModelsClickHouseAggregatePromotion(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE facts
(
    event_day Date,
    sample UInt32,
    amount Decimal64(2)
);
CREATE TABLE rollups
(
    day_p50 Date,
    sample_state AggregateFunction(quantile(0.5), UInt32),
    amount_sum Decimal(38, 2)
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	if got, want := schema.Tables["rollups"].Columns["sample_state"].Type.String(), "AggregateFunction(quantile(0.5), UInt32)"; got != want {
		t.Fatalf("quantile state type = %q, want %q", got, want)
	}
	if got, want := schema.Tables["facts"].Columns["amount"].Type.String(), "Decimal64(2)"; got != want {
		t.Fatalf("decimal type = %q, want %q", got, want)
	}

	queries := []string{
		`-- name: ReadAggregatePromotions :one
SELECT
    quantile(0.5)(event_day) AS day_p50,
    sum(amount) AS amount_sum
FROM facts`,
		`-- name: InsertDateQuantile :exec
INSERT INTO rollups (day_p50)
SELECT quantile(0.5)(event_day)
FROM facts`,
		`-- name: InsertQuantileState :exec
INSERT INTO rollups (sample_state)
SELECT quantileState(0.5)(sample)
FROM facts`,
		`-- name: InsertDecimalSum :exec
INSERT INTO rollups (amount_sum)
SELECT sum(amount)
FROM facts`,
	}

	for _, input := range queries {
		parsed, err := parseQueriesWithSchema(t, input, schema)
		if err != nil {
			t.Fatalf("parseQueriesWithSchema() error = %v\nSQL:\n%s", err, input)
		}
		if len(parsed) != 1 {
			t.Fatalf("parseQueriesWithSchema() returned %d queries, want 1", len(parsed))
		}
	}

	read, err := parseQueriesWithSchema(t, queries[0], schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema(t, read) error = %v", err)
	}
	wantTypes := []string{"time.Time", "decimal.Decimal"}
	for index, want := range wantTypes {
		if got := read[0].Results[index].GoType; got != want {
			t.Errorf("result[%d] GoType = %q, want %q", index, got, want)
		}
	}

	// Reading a state column is a refusal, not a Go type. clickhouse-go
	// v2.47.0 cannot decode an AggregateFunction column at all: the read
	// fails in the block decoder before any Go value is built. The write
	// path below stays available, because an INSERT ... SELECT moves the
	// state inside the server.
	_, err = parseQueriesWithSchema(t, `-- name: ReadQuantileState :one
SELECT quantileState(0.5)(sample) AS sample_state
FROM facts`, schema)
	if err == nil || !strings.Contains(err.Error(), "AggregateFunction") {
		t.Fatalf("parseQueriesWithSchema(t, read state) error = %v, want a refusal that names AggregateFunction", err)
	}

	wrongLevelSchema, err := schemaFromDDLErr(t, `CREATE TABLE facts (sample UInt32);
CREATE TABLE rollups (sample_state AggregateFunction(quantile(0.9), UInt32));`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr(t, wrong level) error = %v", err)
	}
	_, err = parseQueriesWithSchema(t, `-- name: InsertWrongQuantileLevel :exec
INSERT INTO rollups (sample_state)
SELECT quantileState(0.5)(sample)
FROM facts`, wrongLevelSchema)
	if err == nil || !strings.Contains(err.Error(), "quantile(0.9)") {
		t.Fatalf("parseQueriesWithSchema(t, wrong level) error = %v, want quantile level mismatch", err)
	}
}

func TestParseWithSchemaModelsValidDecimalAliasesAndPrecisionPromotion(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE facts
(
    amount32 Decimal32(2),
    amount64 Decimal64(2),
    amount128 Decimal128(2),
    amount256 Decimal256(2),
    amount_high_precision Decimal(40, 2)
);
CREATE TABLE rollups
(
    total32 Decimal(38, 2),
    total64 Decimal(38, 2),
    total128 Decimal(38, 2),
    total256 Decimal(76, 2),
    total_high_precision Decimal(76, 2)
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}

	cases := []struct {
		name   string
		source string
		target string
	}{
		{name: "Decimal32", source: "amount32", target: "total32"},
		{name: "Decimal64", source: "amount64", target: "total64"},
		{name: "Decimal128", source: "amount128", target: "total128"},
		{name: "Decimal256", source: "amount256", target: "total256"},
		{name: "Decimal precision above 38", source: "amount_high_precision", target: "total_high_precision"},
	}
	for index, testCase := range cases {
		input := fmt.Sprintf(`-- name: Sum%d :exec
INSERT INTO rollups (%s)
SELECT sum(%s)
FROM facts`, index, testCase.target, testCase.source)
		if _, err := parseQueriesWithSchema(t, input, schema); err != nil {
			t.Errorf("parseQueriesWithSchema(t, %s) error = %v", testCase.name, err)
		}
	}
}

func TestFunctionRegistryAllowsFixedResultWithPlaceholderArgument(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE counters (value UInt64);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: CountAbove :many
-- param: Threshold
SELECT countIf(value > chgen.arg('Threshold')) AS count
FROM counters`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Results[0].GoType, "uint64"; got != want {
		t.Errorf("countIf result type = %q, want %q", got, want)
	}
	if got, want := queries[0].Params[0].GoType, "uint64"; got != want {
		t.Errorf("countIf parameter type = %q, want %q", got, want)
	}
}

func TestParseWithSchemaInfersArrayParameterForInPredicate(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events
(
    database_id UInt64,
    aggregate_id String
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadEvents :many
-- param: DatabaseIDs
SELECT aggregate_id AS aggregate_id
FROM events
WHERE database_id IN (chgen.arg('DatabaseIDs'))`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Params[0].GoType, "[]uint64"; got != want {
		t.Errorf("IN parameter type = %q, want %q", got, want)
	}
}

func TestParseRejectsPlaceholderMismatch(t *testing.T) {
	input := `-- name: Broken :many
-- result: Value value string
SELECT value AS value FROM events WHERE id = ?`

	_, err := parseQueriesWithDDL(t, `CREATE TABLE events (id String, value String);`, input)
	if err == nil || !strings.Contains(err.Error(), "raw positional placeholder") {
		t.Fatalf("parseQueriesWithDDL() error = %v, want placeholder mismatch", err)
	}
}

// TestParseRejectsMissingAlias covers the alias rule that the catalog pipeline
// applies. The removed annotation-only mode demanded an AS alias on EVERY
// SELECT item; the catalog pipeline takes the name of a direct column
// reference implicitly and demands an alias only for an expression that has no
// such name. A bare `SELECT value` is therefore legal now, and the rule that
// survives is the one below.
func TestParseRejectsMissingAlias(t *testing.T) {
	input := `-- name: Broken :many
-- result: Value value string
SELECT upper(value) FROM events`

	_, err := parseQueriesWithDDL(t, `CREATE TABLE events (value String);`, input)
	if err == nil || !strings.Contains(err.Error(), "explicit AS alias") {
		t.Fatalf("parseQueriesWithDDL() error = %v, want explicit alias error", err)
	}
}

// TestSchemaAwareTakesDirectColumnNameWithoutAlias records the other half of
// that rule: a direct column reference needs no alias.
func TestSchemaAwareTakesDirectColumnNameWithoutAlias(t *testing.T) {
	queries, err := parseQueriesWithDDL(t, `CREATE TABLE events (value String);`, `-- name: ReadValues :many
SELECT value FROM events`)
	if err != nil {
		t.Fatalf("parseQueriesWithDDL() error = %v", err)
	}
	if got, want := queries[0].Results[0].SQLName, "value"; got != want {
		t.Fatalf("implicit result name = %q, want %q", got, want)
	}
}

// TestParseWithSchemaGeneratesDropPartitionParams covers the DROP PARTITION support:
// ALTER TABLE ... DROP PARTITION as a supported :exec form. The partition
// expression addresses a partition VALUE rather than a table column, so the
// placeholder type comes from the `-- param:` annotation.
func TestParseWithSchemaGeneratesDropPartitionParams(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE rollups
(
    bucket_start DateTime64(3, 'UTC'),
    repository String
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}

	queries, err := parseQueriesWithSchema(t, `-- name: DropRollupPartition :exec
-- param: Partition uint32
ALTER TABLE rollups
DROP PARTITION chgen.arg('Partition')`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}

	if got, want := queries[0].Params, []Param{
		{GoName: "Partition", GoType: "uint32"},
	}; !equalParams(got, want) {
		t.Fatalf("DROP PARTITION params = %#v, want %#v", got, want)
	}

	if got, want := queries[0].ParamIndexes, []int{0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DROP PARTITION parameter indexes = %#v, want %#v", got, want)
	}

	generated, err := Generate("servinggen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	if !strings.Contains(string(generated), "DROP PARTITION ?") {
		t.Fatalf("generated SQL lost its DROP PARTITION placeholder:\n%s", generated)
	}
}

// TestParseWithSchemaRejectsUnsupportedAlterClause pins the error message that
// lists the supported :exec forms, so adding a form updates this expectation.
func TestParseWithSchemaRejectsUnsupportedAlterClause(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE rollups (bucket_start DateTime64(3, 'UTC'));`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}

	_, err = parseQueriesWithSchema(t, `-- name: DetachRollupPartition :exec
-- param: Partition uint32
ALTER TABLE rollups
DETACH PARTITION chgen.arg('Partition')`, schema)
	if err == nil {
		t.Fatal("parseQueriesWithSchema() accepted DETACH PARTITION, want error")
	}

	if !strings.Contains(err.Error(), "DROP PARTITION") {
		t.Fatalf("error must list the supported forms, got: %v", err)
	}
}

func TestParseQueriesDoesNotAbsorbTrailingComments(t *testing.T) {
	// A comment that introduces the NEXT query must not become part of the SQL
	// of the previous one. The body ends at its last SQL line.
	queries, err := parseQueriesWithSchema(t, `-- name: First :many
SELECT id FROM events

-- 2. The next query, with a heading comment.
--
-- More prose about it.
-- name: Second :many
SELECT id FROM events`, mustEventsSchema(t))
	if err != nil {
		t.Fatalf("parseQueriesWithDDL() error = %v", err)
	}
	if len(queries) != 2 {
		t.Fatalf("got %d queries, want 2", len(queries))
	}
	if got, want := queries[0].SQL, "SELECT id FROM events"; got != want {
		t.Fatalf("First SQL = %q, want %q", got, want)
	}
}

func TestParseQueriesKeepsCommentsInsideTheBody(t *testing.T) {
	// A comment BETWEEN SQL lines is part of the statement and must survive,
	// because trailing-comment trimming must not reach into the body.
	queries, err := parseQueriesWithSchema(t, `-- name: First :many
SELECT
    id, -- the key
    -- a whole-line note
    kind
FROM events`, mustEventsSchema(t))
	if err != nil {
		t.Fatalf("parseQueriesWithDDL() error = %v", err)
	}
	if !strings.Contains(queries[0].SQL, "a whole-line note") {
		t.Fatalf("First SQL = %q, want the inner comment kept", queries[0].SQL)
	}
	if !strings.HasSuffix(queries[0].SQL, "FROM events") {
		t.Fatalf("First SQL = %q, want it to end at the SQL", queries[0].SQL)
	}
}

func mustEventsSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events (id String, kind String);`)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestParseQueriesKeepsCommentLikeTailInsideAStringLiteral(t *testing.T) {
	// A line that starts with "--" inside a multi-line string literal is DATA.
	// Trimming it would truncate the literal and make the SQL unparsable, so
	// the trailing-comment scan must skip quoted text.
	queries, err := parseQueriesWithSchema(t, `-- name: First :many
SELECT id, 'text
-- trailing line inside the literal' AS note FROM events`, mustEventsSchema(t))
	if err != nil {
		t.Fatalf("parseQueriesWithDDL() error = %v", err)
	}
	if !strings.Contains(queries[0].SQL, "-- trailing line inside the literal' AS note") {
		t.Fatalf("First SQL = %q, want the literal kept whole", queries[0].SQL)
	}
}

func TestParseQueriesTrimsTrailingCommentAfterAStringLiteral(t *testing.T) {
	// The literal closes on an earlier line, so the comment that follows the
	// statement is a real comment and is still trimmed.
	queries, err := parseQueriesWithSchema(t, `-- name: First :many
SELECT id, 'text
-- inside' AS note
FROM events

-- heading of the next query
-- name: Second :many
SELECT id FROM events`, mustEventsSchema(t))
	if err != nil {
		t.Fatalf("parseQueriesWithDDL() error = %v", err)
	}
	if !strings.HasSuffix(queries[0].SQL, "FROM events") {
		t.Fatalf("First SQL = %q, want the trailing comment trimmed", queries[0].SQL)
	}
	if strings.Contains(queries[0].SQL, "heading of the next query") {
		t.Fatalf("First SQL = %q, want no heading of the next query", queries[0].SQL)
	}
}

func TestParseQueriesKeepsCommentLikeTailInsideABlockComment(t *testing.T) {
	// A block comment that spans lines is not a whole-line comment tail. The
	// statement continues after it, so nothing may be trimmed from the body.
	queries, err := parseQueriesWithSchema(t, `-- name: First :many
SELECT id /* note
-- looks like a comment
*/ FROM events`, mustEventsSchema(t))
	if err != nil {
		t.Fatalf("parseQueriesWithDDL() error = %v", err)
	}
	if !strings.HasSuffix(queries[0].SQL, "*/ FROM events") {
		t.Fatalf("First SQL = %q, want the block comment kept", queries[0].SQL)
	}
}

// TestNullableParamHelperIsGatedOnTextPathUse checks that the generator
// declares chgenNullableParam if and only if some generated call site calls
// it. Only the text path wraps a parameter. The native PrepareBatch/Append
// path gives the pointer to batch.Append without a wrap, thus a package whose
// only pointer parameter takes that path must declare no helper at all.
func TestNullableParamHelperIsGatedOnTextPathUse(t *testing.T) {
	const helperDeclaration = "func chgenNullableParam[T any](value *T) any {"

	schema, err := schemaFromDDLErr(t, `CREATE TABLE events
(
    id UInt32,
    label Nullable(String)
);`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}

	// A bare-placeholder INSERT takes the native batch path. Its *string
	// parameter reaches batch.Append directly, thus nothing calls the helper.
	batchQueries, err := parseQueriesWithSchema(t, `-- name: InsertEvent :exec
INSERT INTO events (id, label) VALUES (chgen.arg('ID'), chgen.arg('Label'))`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if !batchQueries[0].batchInsert {
		t.Fatalf("the INSERT must take the native batch path for this test to mean anything")
	}
	batchText, err := Generate("querygen", batchQueries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if strings.Contains(string(batchText), helperDeclaration) {
		t.Errorf("a batch-only package must declare no chgenNullableParam:\n%s", batchText)
	}

	// A SELECT that binds the same Nullable column takes the text path, thus
	// generatedParamArg wraps the parameter and the helper must be present.
	textQueries, err := parseQueriesWithSchema(t, `-- name: ReadEvents :many
SELECT id FROM events WHERE label = chgen.arg('Label')`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	textOutput, err := Generate("querygen", textQueries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(textOutput)
	if !strings.Contains(text, helperDeclaration) {
		t.Errorf("the text path calls the helper, thus it must be declared:\n%s", text)
	}
	if !strings.Contains(text, "chgenNullableParam(arg.Label)") {
		t.Errorf("the text path must wrap the pointer parameter:\n%s", text)
	}
}
