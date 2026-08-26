// Command supportmanifest writes, checks, or reports ClickHouse API support.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/IlyaGulya/chgen/internal/apiinventory"
	"github.com/IlyaGulya/chgen/internal/supportmanifest"
)

func main() {
	inventoryPath := flag.String("inventory", "testdata/clickhouse-api-inventory.json", "ClickHouse API inventory path")
	configPath := flag.String("config", "testdata/clickhouse-support-overrides.json", "support override path")
	catalogPath := flag.String("function-catalog", "testdata/clickhouse-function-probes.json", "function probe catalog path")
	evidencePath := flag.String("function-evidence", "testdata/clickhouse-function-probe-evidence.json", "live function evidence path")
	outputPath := flag.String("out", "testdata/clickhouse-support-manifest.json", "support manifest path")
	check := flag.Bool("check", false, "check the generated support manifest")
	report := flag.Bool("report", false, "write the coverage report to standard output")
	flag.Parse()

	inventory, err := apiinventory.Load(*inventoryPath)
	if err != nil {
		fail("read API inventory", err)
	}
	config, err := supportmanifest.LoadConfig(*configPath)
	if err != nil {
		fail("read support config", err)
	}
	roster, err := supportmanifest.LoadFunctionEvidence(*catalogPath, *evidencePath)
	if err != nil {
		fail("read semantic roster", err)
	}
	if *report {
		manifest, loadErr := supportmanifest.Load(*outputPath)
		if loadErr != nil {
			fail("read support manifest", loadErr)
		}
		if validateErr := supportmanifest.ValidateCurrent(inventory, roster, config, manifest); validateErr != nil {
			fail("validate support manifest", validateErr)
		}
		data, marshalErr := json.MarshalIndent(supportmanifest.Coverage(manifest), "", "  ")
		if marshalErr != nil {
			fail("encode coverage report", marshalErr)
		}
		fmt.Println(string(data))
		return
	}

	manifest, err := supportmanifest.Build(inventory, roster, config)
	if err != nil {
		fail("build support manifest", err)
	}
	data, err := supportmanifest.Marshal(manifest)
	if err != nil {
		fail("encode support manifest", err)
	}
	if *check {
		current, readErr := os.ReadFile(*outputPath)
		if readErr != nil {
			fail("read current support manifest", readErr)
		}
		if !bytes.Equal(current, data) {
			fmt.Fprintln(os.Stderr, "supportmanifest: support manifest is stale")
			os.Exit(1)
		}
		fmt.Println("IDENTICAL")
		return
	}
	if err := os.WriteFile(*outputPath, data, 0o644); err != nil {
		fail("write support manifest", err)
	}
	fmt.Printf("WROTE %s\n", *outputPath)
}

func fail(action string, err error) {
	fmt.Fprintf(os.Stderr, "supportmanifest: %s: %v\n", action, err)
	os.Exit(2)
}
