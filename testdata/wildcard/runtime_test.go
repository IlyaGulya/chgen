package wildcard

import (
	"context"
	"github.com/ClickHouse/clickhouse-go/v2"
	"os"
	"strings"
	"testing"
)

func TestWildcardRowsAndMetadata(t *testing.T) {
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{os.Getenv("CHGEN_CONTRACT_NATIVE")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	var version string
	if err := conn.QueryRow(t.Context(), "SELECT version()").Scan(&version); err != nil || version != "25.8.29.51" {
		t.Fatalf("unexpected version: %q, %v", version, err)
	}
	if err := conn.Exec(t.Context(), "CREATE TABLE chgen_star_runtime (z UInt64, a String DEFAULT 'value', m UInt64 MATERIALIZED z, x UInt64 ALIAS z) ENGINE=Memory"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Exec(context.WithoutCancel(t.Context()), "DROP TABLE IF EXISTS chgen_star_runtime") })
	if err := conn.Exec(t.Context(), "INSERT INTO chgen_star_runtime (z) VALUES (7)"); err != nil {
		t.Fatal(err)
	}
	q := New(conn)
	plain, err := q.Plain(t.Context(), PlainParams{})
	if err != nil || len(plain) != 1 || plain[0].Z != 7 || plain[0].A != "value" {
		t.Fatalf("plain: %+v, %v", plain, err)
	}
	qualified, err := q.Qualified(t.Context(), QualifiedParams{})
	if err != nil || len(qualified) != 1 || qualified[0].Z != 7 || qualified[0].A != "value" {
		t.Fatalf("qualified: %+v, %v", qualified, err)
	}
	nested, err := q.Nested(t.Context(), NestedParams{})
	if err != nil || len(nested) != 1 || nested[0].Z != 7 || nested[0].A != "value" {
		t.Fatalf("nested: %+v, %v", nested, err)
	}
	included, err := q.Included(t.Context(), IncludedParams{})
	if err != nil || len(included) != 1 || included[0].M != 7 || included[0].X != 7 {
		t.Fatalf("included: %+v, %v", included, err)
	}
	series, err := q.Series(t.Context(), SeriesParams{})
	if err != nil || len(series) != 3 || series[0].Number != 0 || series[1].Number != 1 || series[2].Number != 2 {
		t.Fatalf("series: %+v, %v", series, err)
	}
	union, err := q.Union(t.Context(), UnionParams{})
	if err != nil || len(union) != 2 || union[0].Z != 7 || union[1].A != "value" {
		t.Fatalf("union: %+v, %v", union, err)
	}
	changed := clickhouse.Context(t.Context(), clickhouse.WithSettings(clickhouse.Settings{"asterisk_include_alias_columns": 1}))
	if _, err := q.Plain(changed, PlainParams{}); err == nil || !strings.Contains(err.Error(), "result contract") {
		t.Fatalf("changed session shape not rejected before Scan: %v", err)
	}
	if err := conn.Exec(t.Context(), "ALTER TABLE chgen_star_runtime ADD COLUMN added UInt8 DEFAULT 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Plain(t.Context(), PlainParams{}); err == nil || !strings.Contains(err.Error(), "result contract") {
		t.Fatalf("changed schema shape not rejected before Scan: %v", err)
	}
}
