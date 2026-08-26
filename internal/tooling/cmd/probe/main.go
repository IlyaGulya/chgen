// Command probe captures a durable snapshot of the ClickHouse type
// boundary from one live server.
//
// Usage:
//
//	go run ./internal/tooling/cmd/probe -url http://default:p@localhost:8123 -out probe.json
//
// The output is a JSON artifact (internal/typeboundary.Artifact) that names the
// server measured and records, per named cell, either the type ClickHouse
// assigned or the numeric error code it refused with. Every cell is a FIXED
// query over real table columns (internal/typeboundary.Catalog), never a
// random draw, so the artifact is comparable to another probe artifact of
// one server or of two different servers without the uptime tolerance the
// type oracle's own comparator needs. See internal/tooling/cmd/probegate and
// internal/typeboundary/compare.go for the reasoning in full.
//
// probe creates a private fixture table (chgen_probe_t) in the database
// given by -database (default: the server's default database) and drops it
// again when the run ends.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/IlyaGulya/chgen/internal/typeboundary"
)

func main() {
	url := flag.String("url", "", "ClickHouse HTTP URL, e.g. http://default:p@localhost:8123")
	database := flag.String("database", "default", "database to create the probe fixture table in")
	out := flag.String("out", "", "path to write the probe artifact JSON to")
	flag.Parse()

	if *url == "" {
		fmt.Fprintln(os.Stderr, "probe: -url is required")
		os.Exit(2)
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "probe: -out is required")
		os.Exit(2)
	}

	client := typeboundary.NewClient(*url, *database)
	artifact, err := typeboundary.Run(context.Background(), client)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: %v\n", err)
		os.Exit(2)
	}
	if err := artifact.Save(*out); err != nil {
		fmt.Fprintf(os.Stderr, "probe: write %s: %v\n", *out, err)
		os.Exit(2)
	}
	fmt.Printf("probe: wrote %s (%s, server_run=%d uptime_s=%d, %d cells)\n",
		*out, artifact.Server.Version, artifact.Server.ServerRun, artifact.Server.UptimeS, len(artifact.Cells))
}
