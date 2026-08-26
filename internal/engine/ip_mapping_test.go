package engine

import (
	"strings"
	"testing"
)

// These tests pin the Go mapping of ClickHouse IPv4 and IPv6 columns.
//
// The old mapping was string. It was wrong in two independent ways, both
// measured against ClickHouse 25.8 with clickhouse-go v2.47.0:
//
//   - A container of IP addresses gives raw binary, not text. Array(IPv4)
//     scanned into []string gives "\xc0\xa8\x01\x01" for 192.168.1.1. There
//     is no error, so the caller gets silent garbage. Only the bare scalar
//     scanned into a string gives text, thus the type name cannot show the
//     difference (execution-oracle finding R5).
//   - An IPv4-mapped IPv6 address loses its family. The IPv6 value
//     ::ffff:1.2.3.4 scanned into a string gives "1.2.3.4". The address is
//     no longer an IPv6 value, and a re-insert puts a different value in an
//     IPv6 column (execution-oracle finding R6).
//
// net.IP is the measured correct target. It scans and binds in the bare,
// Nullable, Array, Array(Nullable), Map value and nested Array shapes, and
// it holds the full 16 bytes of an IPv4-mapped IPv6 address, so the round
// trip through the independent HTTP TabSeparated channel returns
// ::ffff:1.2.3.4 unchanged. netip.Addr is not usable: the driver refuses it
// in a container ("converting net.IP to netip.Addr is unsupported").
func ipTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE hosts
(
    v4 IPv4,
    v6 IPv6,
    n4 Nullable(IPv4),
    n6 Nullable(IPv6),
    a4 Array(IPv4),
    a6 Array(IPv6),
    an6 Array(Nullable(IPv6)),
    m6 Map(String, IPv6),
    aa6 Array(Array(IPv6))
) ENGINE = MergeTree ORDER BY v4;`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

func TestIPColumnsMapToNetIP(t *testing.T) {
	schema := ipTestSchema(t)
	query := Query{
		Name:    "ReadHosts",
		Command: CommandMany,
		SQL:     "SELECT v4, v6, n4, n6, a4, a6, an6, m6, aa6 FROM hosts",
	}
	if err := resolveQuery(&query, schema); err != nil {
		t.Fatalf("resolveQuery() error = %v", err)
	}
	want := []string{
		"net.IP",
		"net.IP",
		"*net.IP",
		"*net.IP",
		"[]net.IP",
		"[]net.IP",
		"[]*net.IP",
		"map[string]net.IP",
		"[][]net.IP",
	}
	if len(query.Results) != len(want) {
		t.Fatalf("results = %d, want %d", len(query.Results), len(want))
	}
	for index, wantType := range want {
		if got := query.Results[index].GoType; got != wantType {
			t.Errorf("result[%d] GoType = %q, want %q", index, got, wantType)
		}
	}
}

// An IP address as a Map key cannot be represented. The driver refuses the
// column itself: Map(IPv4, String) fails the block decode with
// "unsupported column type". The rule of the project is never silently
// wrong, so generation must stop with an explicit error.
func TestMapWithIPKeyIsRefused(t *testing.T) {
	for _, keyType := range []string{"IPv4", "IPv6"} {
		schema, err := schemaFromDDLErr(t, `CREATE TABLE routes (m Map(`+keyType+`, String)) ENGINE = MergeTree ORDER BY tuple();`)
		if err != nil {
			t.Fatalf("schemaFromDDLErr() error = %v", err)
		}
		query := Query{
			Name:    "ReadRoutes",
			Command: CommandMany,
			SQL:     "SELECT m FROM routes",
		}
		err = resolveQuery(&query, schema)
		if err == nil {
			t.Fatalf("Map(%s, String) resolved without an error, want a refusal", keyType)
		}
		if !strings.Contains(err.Error(), "Map key") {
			t.Fatalf("Map(%s, String) error = %v, want a Map key refusal", keyType, err)
		}
	}
}

func TestGeneratedCodeImportsNet(t *testing.T) {
	schema := ipTestSchema(t)
	queries, err := parseQueriesWithSchema(t, `-- name: ReadAddresses :many
SELECT v6 FROM hosts;`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(string(generated), "\t\"net\"\n") {
		t.Fatalf("generated code does not import net:\n%s", generated)
	}
}

func TestGeneratedCodeWithoutIPOmitsNetImport(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE plain (v UInt32) ENGINE = MergeTree ORDER BY v;`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadPlain :many
SELECT v FROM plain;`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if strings.Contains(string(generated), "\t\"net\"\n") {
		t.Fatalf("generated code imports net without an IP column")
	}
}

func TestResultOverrideAcceptsNetIP(t *testing.T) {
	for _, goType := range []string{"net.IP", "*net.IP", "[]net.IP", "map[string]net.IP"} {
		if err := validateGoType(goType); err != nil {
			t.Fatalf("validateGoType(%s) error = %v", goType, err)
		}
	}
}
