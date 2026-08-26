// Command apiinventory writes or checks the pinned ClickHouse API inventory.
//
// Use -url or CHGEN_API_INVENTORY_URL to select the server. The command
// refuses a server version that differs from chgen.MeasuredCHVersion.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/IlyaGulya/chgen"
	"github.com/IlyaGulya/chgen/internal/apiinventory"
)

func main() {
	serverURL := flag.String("url", os.Getenv("CHGEN_API_INVENTORY_URL"), "ClickHouse HTTP URL")
	outputPath := flag.String("out", "testdata/clickhouse-api-inventory.json", "inventory output path")
	check := flag.Bool("check", false, "compare the server with the inventory file")
	flag.Parse()

	if *serverURL == "" {
		fmt.Fprintln(os.Stderr, "apiinventory: set -url or CHGEN_API_INVENTORY_URL")
		os.Exit(2)
	}
	inventory, err := apiinventory.NewCollector(*serverURL).Collect(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "apiinventory: collect inventory: %v\n", err)
		os.Exit(2)
	}
	if inventory.Source.Version != chgen.MeasuredCHVersion {
		fmt.Fprintf(os.Stderr, "apiinventory: server version is %s, want %s\n", inventory.Source.Version, chgen.MeasuredCHVersion)
		os.Exit(2)
	}

	if *check {
		checkInventory(*outputPath, inventory)
		return
	}
	data, err := apiinventory.Marshal(inventory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apiinventory: encode inventory: %v\n", err)
		os.Exit(2)
	}
	if err := os.WriteFile(*outputPath, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "apiinventory: write %s: %v\n", *outputPath, err)
		os.Exit(2)
	}
	fmt.Printf("WROTE %s\n", *outputPath)
}

func checkInventory(path string, candidate apiinventory.Inventory) {
	reference, err := apiinventory.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apiinventory: read %s: %v\n", path, err)
		os.Exit(2)
	}
	difference, err := apiinventory.Compare(reference, candidate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apiinventory: compare inventories: %v\n", err)
		os.Exit(2)
	}
	fmt.Print(difference.String())
	if !difference.Identical() {
		os.Exit(1)
	}
}
