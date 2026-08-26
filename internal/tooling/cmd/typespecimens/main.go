// Command typespecimens writes or checks the real-column type catalog.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/IlyaGulya/chgen/internal/apiinventory"
	"github.com/IlyaGulya/chgen/internal/typespecimen"
)

func main() {
	serverURL := flag.String("url", os.Getenv("CHGEN_TYPE_SPECIMEN_URL"), "ClickHouse HTTP URL")
	inventoryPath := flag.String("inventory", "testdata/clickhouse-api-inventory.json", "ClickHouse API inventory path")
	outputPath := flag.String("out", "testdata/clickhouse-type-specimens.json", "type specimen catalog path")
	goOutputPath := flag.String("go-out", "internal/engine/type_specimen_catalog_generated_test.go", "generated fixture column path")
	packageName := flag.String("go-package", "engine", "generated Go package name")
	check := flag.Bool("check", false, "check generated outputs")
	fromCatalog := flag.Bool("from-catalog", false, "regenerate Go from the pinned catalog without a live server")
	flag.Parse()

	inventory, err := apiinventory.Load(*inventoryPath)
	if err != nil {
		fail("read API inventory", err)
	}
	var catalog typespecimen.Catalog
	if *serverURL == "" {
		if !*check && !*fromCatalog {
			fmt.Fprintln(os.Stderr, "typespecimens: set -url, CHGEN_TYPE_SPECIMEN_URL, or -from-catalog")
			os.Exit(2)
		}
		catalog, err = typespecimen.Load(*outputPath)
		if err != nil {
			fail("read type specimen catalog", err)
		}
	} else {
		catalog, err = typespecimen.NewCollector(*serverURL).Collect(context.Background(), inventory)
		if err != nil {
			fail("collect type specimens", err)
		}
	}
	if err := catalog.ValidateAgainst(inventory); err != nil {
		fail("validate type specimens", err)
	}
	catalogData, err := typespecimen.Marshal(catalog)
	if err != nil {
		fail("encode type specimens", err)
	}
	goData := typespecimen.GeneratedGo(*packageName, catalog)

	if *check {
		checkFile(*outputPath, catalogData)
		checkFile(*goOutputPath, goData)
		fmt.Println("IDENTICAL")
		return
	}
	if err := os.WriteFile(*outputPath, catalogData, 0o644); err != nil {
		fail("write type specimens", err)
	}
	if err := os.WriteFile(*goOutputPath, goData, 0o644); err != nil {
		fail("write fixture columns", err)
	}
	fmt.Printf("WROTE %s and %s\n", *outputPath, *goOutputPath)
}

func checkFile(path string, expected []byte) {
	current, err := os.ReadFile(path)
	if err != nil {
		fail("read "+path, err)
	}
	if !bytes.Equal(current, expected) {
		fmt.Fprintf(os.Stderr, "typespecimens: %s is stale\n", path)
		os.Exit(1)
	}
}

func fail(action string, err error) {
	fmt.Fprintf(os.Stderr, "typespecimens: %s: %v\n", action, err)
	os.Exit(2)
}
