package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/apiinventory"
	"github.com/IlyaGulya/chgen/internal/typespecimen"
)

var typeSpecimenCatalogPath = moduleRootPath("testdata", "clickhouse-type-specimens.json")

func TestPinnedTypeSpecimenCatalogCoversInventory(t *testing.T) {
	inventory, catalog := loadPinnedTypeSpecimens(t)
	if err := catalog.ValidateAgainst(inventory); err != nil {
		t.Fatal(err)
	}
	if got, want := len(catalog.Entries), len(inventory.DataTypeFamilies.Canonical); got != want {
		t.Fatalf("catalog has %d entries, want %d", got, want)
	}
}

func TestPinnedTypeSpecimenGeneratedColumnsAreCurrent(t *testing.T) {
	_, catalog := loadPinnedTypeSpecimens(t)
	want := typespecimen.GeneratedGo("engine", catalog)
	got, err := os.ReadFile("type_specimen_catalog_generated_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("generated type specimen columns are stale; run go run ./internal/tooling/cmd/typespecimens -url URL")
	}
}

func TestTypeSpecimenCatalogFeedsTheOracleFixture(t *testing.T) {
	_, catalog := loadPinnedTypeSpecimens(t)
	if err := validateTypeSpecimenFixture(t, catalog, oracleSchemaDDL); err != nil {
		t.Fatal(err)
	}
	accepted := catalog.AcceptedEntries()
	if len(accepted) == 0 {
		t.Fatal("catalog has no accepted specimen to mutate")
	}
	missing := accepted[0]
	mutated := strings.Replace(oracleSchemaDDL, "    "+missing.Column+" "+missing.ObservedType, "    removed_"+missing.Column+" "+missing.ObservedType, 1)
	if err := validateTypeSpecimenFixture(t, catalog, mutated); err == nil {
		t.Fatal("fixture reachability gate accepted a removed specimen column")
	}
}

func TestTypeSpecimenCatalogMatchesServer(t *testing.T) {
	serverURL := os.Getenv("CHGEN_TYPE_SPECIMEN_URL")
	if serverURL == "" {
		t.Skip("CHGEN_TYPE_SPECIMEN_URL is not set")
	}
	inventory, reference := loadPinnedTypeSpecimens(t)
	candidate, err := typespecimen.NewCollector(serverURL).Collect(context.Background(), inventory)
	if err != nil {
		t.Fatal(err)
	}
	referenceBytes, err := typespecimen.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	candidateBytes, err := typespecimen.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(referenceBytes, candidateBytes) {
		t.Fatal("live type specimens differ from the pinned catalog")
	}
}

func validateTypeSpecimenFixture(t *testing.T, catalog typespecimen.Catalog, ddl string) error {
	t.Helper()
	schema, err := schemaFromDDLErr(t, ddl)
	if err != nil {
		return err
	}
	table, found := schema.Tables["t"]
	if !found {
		return fmt.Errorf("oracle fixture has no table t")
	}
	for _, specimen := range catalog.AcceptedEntries() {
		column, found := table.Columns[specimen.Column]
		if !found {
			return fmt.Errorf("oracle fixture has no specimen column %s", specimen.Column)
		}
		expected, err := parseCHTypeName(specimen.ObservedType)
		if err != nil {
			return fmt.Errorf("parse observed type for %s: %w", specimen.Column, err)
		}
		if column.Type.String() != expected.String() {
			return fmt.Errorf("specimen column %s has type %s, want %s", specimen.Column, column.Type.String(), expected.String())
		}
	}
	return nil
}

func loadPinnedTypeSpecimens(t *testing.T) (apiinventory.Inventory, typespecimen.Catalog) {
	t.Helper()
	inventory, err := apiinventory.Load(moduleRootPath("testdata", "clickhouse-api-inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := typespecimen.Load(typeSpecimenCatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	return inventory, catalog
}
