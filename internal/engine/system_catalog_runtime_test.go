package engine

import (
	"os"
	"testing"
)

func TestGeneratedSystemPartsSignatureRuntime(t *testing.T) {
	if os.Getenv("CHGEN_SYSTEM_HTTP") == "" {
		t.Skip("set CHGEN_SYSTEM_HTTP to a disposable pinned ClickHouse HTTP address")
	}
	sql, err := os.ReadFile(moduleRootPath("testdata", "golden", "system_parts", "queries.sql"))
	if err != nil {
		t.Fatal(err)
	}
	queries, err := parseQueriesWithSchema(t, string(sql), &Schema{Tables: map[string]Table{}})
	if err != nil {
		t.Fatal(err)
	}
	generated, err := Generate("contracts", queries)
	if err != nil {
		t.Fatal(err)
	}
	runGeneratedRuntimeFixture(t, generated, "systemcontracts")
}
