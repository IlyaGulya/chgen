package engine

import (
	"strings"
	"testing"
)

// These tests pin the Go mapping of ClickHouse UUID columns.
//
// The old mapping was string. It was wrong in the container shapes, and the
// defect has the same shape as the IPv4/IPv6 defect but a different root
// cause: the driver hands back uuid.UUID, not text. Measured on ClickHouse
// 25.8.29.51 with clickhouse-go v2.47.0 against real columns, a string
// target fails the scan in every container shape:
//
//	Array(UUID)           converting uuid.UUID to string is unsupported
//	Array(Nullable(UUID)) converting *uuid.UUID to *string is unsupported
//	Map(String, UUID)     converting Map(String, UUID) to *map[string]string
//	                      is unsupported. try using map[string]uuid.UUID
//	Array(Array(UUID))    converting uuid.UUID to string is unsupported
//
// Only the bare scalar and the Nullable scalar scan into a string, thus a
// type-name comparison can never see the difference (execution-oracle
// findings arr_uuid, arrnull_uuid and mapval_uuid, recorded under R5).
//
// uuid.UUID from github.com/google/uuid is the measured correct target. It
// scans and binds in the bare, Nullable, Array, Array(Nullable), Map value,
// Map key and nested Array shapes, verified against the independent HTTP
// TabSeparated channel. The package is already in the module graph: the
// driver itself depends on it, thus this adds no new dependency tree.
func uuidTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE things
(
    u UUID,
    nu Nullable(UUID),
    au Array(UUID),
    anu Array(Nullable(UUID)),
    mu Map(String, UUID),
    aau Array(Array(UUID)),
    mku Map(UUID, String)
) ENGINE = MergeTree ORDER BY u;`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

func TestUUIDColumnsMapToUUIDType(t *testing.T) {
	schema := uuidTestSchema(t)
	query := Query{
		Name:    "ReadThings",
		Command: CommandMany,
		SQL:     "SELECT u, nu, au, anu, mu, aau, mku FROM things",
	}
	if err := resolveQuery(&query, schema); err != nil {
		t.Fatalf("resolveQuery() error = %v", err)
	}
	want := []string{
		"uuid.UUID",
		"*uuid.UUID",
		"[]uuid.UUID",
		"[]*uuid.UUID",
		"map[string]uuid.UUID",
		"[][]uuid.UUID",
		"map[uuid.UUID]string",
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

// Unlike net.IP, uuid.UUID is a [16]byte array, thus it is a valid Go map
// key, and the driver decodes Map(UUID, String) into map[uuid.UUID]string.
// This shape therefore needs no refusal.
func TestMapWithUUIDKeyIsAllowed(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE owners (m Map(UUID, String)) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	query := Query{
		Name:    "ReadOwners",
		Command: CommandMany,
		SQL:     "SELECT m FROM owners",
	}
	if err := resolveQuery(&query, schema); err != nil {
		t.Fatalf("resolveQuery() error = %v", err)
	}
	if got := query.Results[0].GoType; got != "map[uuid.UUID]string" {
		t.Fatalf("Map(UUID, String) GoType = %q, want map[uuid.UUID]string", got)
	}
}

func TestGeneratedCodeImportsUUID(t *testing.T) {
	schema := uuidTestSchema(t)
	queries, err := parseQueriesWithSchema(t, `-- name: ReadThings :many
SELECT au FROM things;`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(string(generated), `"github.com/google/uuid"`) {
		t.Fatalf("generated code does not import github.com/google/uuid:\n%s", generated)
	}
}

func TestGeneratedCodeWithoutUUIDOmitsUUIDImport(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE plainu (v UInt32) ENGINE = MergeTree ORDER BY v;`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadPlainU :many
SELECT v FROM plainu;`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if strings.Contains(string(generated), `"github.com/google/uuid"`) {
		t.Fatalf("generated code imports github.com/google/uuid without a UUID column")
	}
}

func TestResultOverrideAcceptsUUID(t *testing.T) {
	for _, goTypeName := range []string{
		"uuid.UUID", "*uuid.UUID", "[]uuid.UUID", "[]*uuid.UUID",
		"map[string]uuid.UUID", "map[uuid.UUID]string",
	} {
		if err := validateGoType(goTypeName); err != nil {
			t.Fatalf("validateGoType(%s) error = %v", goTypeName, err)
		}
	}
}
