package chgen_test

import (
	"fmt"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen"
)

func TestCursorQueriesGeneratedRuntime(t *testing.T) {
	const ddl = `CREATE TABLE cursor_events (scope UInt32, at DateTime64(3, 'UTC'), id String, cursor UInt64) ENGINE = Memory;
CREATE TABLE nullable_index_events (id UInt8, a Array(Int32), n Nullable(UInt64)) ENGINE = Memory;
-- chgen:external
CREATE TABLE requested_keys (ordinal UInt64, id String, cursor UInt64);`
	var sql strings.Builder
	for _, tuple := range []bool{false, true} {
		name := "ColumnKeys"
		projection := "fromUnixTimestamp64Milli(toInt64(bitShiftRight(cursor, 20)), 'UTC'), id"
		if tuple {
			name = "TupleKeys"
			projection = "(" + projection + ")"
		}
		fmt.Fprintf(&sql, `-- name: %s :many
SELECT events.id, events.at, events.cursor
FROM cursor_events AS events
INNER JOIN chgen.external('Keys', requested_keys) AS requested ON requested.id = events.id
WHERE events.scope = chgen.arg('Scope')
AND (events.at, events.id) IN (SELECT %s FROM chgen.external('Keys', requested_keys))
AND events.cursor = requested.cursor
ORDER BY requested.ordinal
LIMIT 1 BY events.id;
`, name, projection)
	}
	sql.WriteString(`-- name: NullableArrayIndices :many
SELECT id, arrayElement(a,n) AS value FROM nullable_index_events ORDER BY id;
`)
	queries, err := parsePublicQuery(t, ddl, sql.String())
	if err != nil {
		t.Fatal(err)
	}
	generated, err := chgen.Generate("cursorqueries", queries)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/cursorqueries/runtime_test.go")
	if err != nil {
		t.Fatal(err)
	}
	runGeneratedRuntime(t, "cursorqueries", generated, fixture)
}

func runGeneratedRuntime(t *testing.T, packageName string, generated, fixture []byte) {
	t.Helper()
	drivers := []string{"v2.42.0"}
	if version.Compare(runtime.Version(), "go1.25") >= 0 {
		drivers = append(drivers, "v2.47.0")
	}
	for _, driver := range drivers {
		t.Run(driver, func(t *testing.T) {
			dir := t.TempDir()
			mod := "module example.com/" + packageName + "\n\ngo 1.24\n\nrequire github.com/ClickHouse/clickhouse-go/v2 " + driver + "\n"
			for name, data := range map[string][]byte{"go.mod": []byte(mod), "queries.go": generated, "runtime_test.go": fixture} {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "-count=1", "-v", ".")
			cmd.Dir = dir
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("generated runtime: %v\n%s", err, output)
			}
			t.Logf("%s", output)
		})
	}
}

func parsePublicQuery(t *testing.T, ddl, sql string) ([]chgen.Query, error) {
	t.Helper()
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.sql")
	queryPath := filepath.Join(dir, "queries.sql")
	for path, content := range map[string]string{schemaPath: ddl, queryPath: sql} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalogs, err := chgen.ParseSchemaCatalogs([]string{schemaPath})
	if err != nil {
		t.Fatal(err)
	}
	return chgen.ParseQueryFiles([]string{queryPath}, catalogs)
}

func TestTupleProjectionCanSupplyAnINKey(t *testing.T) {
	const ddl = `CREATE TABLE events (at DateTime64(3, 'UTC'), id String) ENGINE = Memory`
	const sql = `-- name: Read :many
SELECT id FROM events
WHERE (at, id) IN (SELECT (at, id) FROM events);`
	queries, err := parsePublicQuery(t, ddl, sql)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := chgen.Generate("queries", queries)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "IN (SELECT (at, id) FROM events)") {
		t.Fatal("generation rewrote the tuple projection")
	}
}

func TestTupleINKeyArityDiagnostic(t *testing.T) {
	_, err := parsePublicQuery(t,
		"CREATE TABLE events (a UInt32, b UInt32, c UInt32) ENGINE=Memory",
		"-- name: Read :many\nSELECT a FROM events WHERE (a,b) IN (SELECT (a,b,c) FROM events);")
	if err == nil || !strings.Contains(err.Error(), "IN set tuple has 3 components, want 2") {
		t.Fatalf("wrong tuple arity diagnostic: %v", err)
	}
}

func TestCursorFunctionTypeBoundaries(t *testing.T) {
	const ddl = `CREATE TABLE events (u UInt64, i Int64, small UInt8, signed Int8,
n Nullable(UInt64), lc LowCardinality(UInt64), millis Nullable(Int64),
lc_millis LowCardinality(Int64), zone String, f Float64) ENGINE=Memory`
	for _, tc := range []struct{ expression, want string }{
		{"bitShiftRight(u, 20)", "UInt64"},
		{"bitShiftRight(i, 20)", "Int64"},
		{"bitShiftRight(small, u)", "UInt64"},
		{"bitShiftRight(u, signed)", "Int64"},
		{"bitShiftRight(n, 20)", "Nullable(UInt64)"},
		{"bitShiftRight(lc, 20)", "LowCardinality(UInt64)"},
		{"bitShiftRight(lc, u)", "UInt64"},
		{"fromUnixTimestamp64Milli(i)", "DateTime64(3)"},
		{"fromUnixTimestamp64Milli(millis, 'UTC')", "Nullable(DateTime64(3, 'UTC'))"},
		{"fromUnixTimestamp64Milli(lc_millis, 'UTC')", "DateTime64(3, 'UTC')"},
		{"fromUnixTimestamp64Milli(toInt64(bitShiftRight(u, 20)), 'UTC')", "DateTime64(3, 'UTC')"},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			queries, err := parsePublicQuery(t, ddl, "-- name: Read :many\nSELECT "+tc.expression+" AS value FROM events;")
			if err != nil {
				t.Fatal(err)
			}
			if got := queries[0].Results[0].CHType.String(); got != tc.want {
				t.Fatalf("type=%s; want %s", got, tc.want)
			}
			if _, err := chgen.Generate("queries", queries); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, expression := range []string{"bitShiftRight(u)", "bitShiftRight(u, 1, 2)", "bitShiftRight(zone, 20)", "fromUnixTimestamp64Milli(f)", "fromUnixTimestamp64Milli(i, zone)", "fromUnixTimestamp64Milli()"} {
		t.Run(expression, func(t *testing.T) {
			if _, err := parsePublicQuery(t, ddl, "-- name: Read :many\nSELECT "+expression+" AS value FROM events;"); err == nil {
				t.Fatal("expected refusal")
			}
		})
	}
}

func TestTupleINPreservesNestedAndNamedKeys(t *testing.T) {
	const ddl = `CREATE TABLE events (a UInt32, b UInt32, c UInt32,
named Tuple(x UInt32, y UInt32), n Nullable(UInt32)) ENGINE=Memory`
	for _, predicate := range []string{
		"(a,b) IN (SELECT named FROM events)",
		"(a,(b,c)) IN (SELECT (a,(b,c)) FROM events)",
		"(a,n) IN (SELECT (a,n) FROM events WHERE 0)",
		"(a,n) NOT IN (SELECT (a,n) FROM events)",
	} {
		t.Run(predicate, func(t *testing.T) {
			queries, err := parsePublicQuery(t, ddl, "-- name: Read :many\nSELECT a FROM events WHERE "+predicate+";")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := chgen.Generate("queries", queries); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCursorCanBeShiftedInSQL(t *testing.T) {
	queries, err := parsePublicQuery(t,
		"CREATE TABLE events (cursor UInt64) ENGINE = Memory",
		"-- name: Read :many\nSELECT bitShiftRight(cursor, 20) AS millis FROM events;")
	if err != nil {
		t.Fatal(err)
	}
	result := queries[0].Results[0]
	if result.CHType.String() != "UInt64" || result.GoType != "uint64" {
		t.Fatalf("shift result = %+v; want UInt64 / uint64", result)
	}
	if _, err := chgen.Generate("queries", queries); err != nil {
		t.Fatal(err)
	}
}

func TestUnixMillisecondsCanBecomeATimestamp(t *testing.T) {
	queries, err := parsePublicQuery(t,
		"CREATE TABLE events (millis Int64) ENGINE = Memory",
		"-- name: Read :many\nSELECT fromUnixTimestamp64Milli(millis, 'UTC') AS at FROM events;")
	if err != nil {
		t.Fatal(err)
	}
	result := queries[0].Results[0]
	if result.CHType.String() != "DateTime64(3, 'UTC')" || result.GoType != "time.Time" {
		t.Fatalf("timestamp result = %+v; want DateTime64(3, 'UTC') / time.Time", result)
	}
	if _, err := chgen.Generate("queries", queries); err != nil {
		t.Fatal(err)
	}
}
