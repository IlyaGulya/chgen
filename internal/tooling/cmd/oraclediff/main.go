// Command oraclediff compares two type-oracle JSON reports.
//
// Usage:
//
//	go run ./internal/tooling/cmd/oraclediff a.json b.json
//	go run ./internal/tooling/cmd/oraclediff -cross-instance a.json b.json
//	go run ./internal/tooling/cmd/oraclediff -cross-instance -cross-version a.json b.json
//
// It exits with status 0 when the reports agree, 1 when they differ, and 2
// when the comparison cannot answer the question, for example when the two
// reports come from different server runs. A difference in server run is an
// error and not a warning on purpose: on one and the same commit a fresh
// server and a server that has been warm for two hours give different
// server-side CH_ERROR numbers, thus such a comparison would report a
// difference in the server as a difference in the code.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/IlyaGulya/chgen/internal/oraclereport"
)

func main() {
	cross := flag.Bool("cross-instance", false,
		"allow a comparison of two reports that come from different server runs; "+
			"read every difference as 'code or server, unknown which'")
	crossVersion := flag.Bool("cross-version", false,
		"allow different ClickHouse versions for an explicit version-boundary measurement; requires -cross-instance")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: oraclediff [-cross-instance] [-cross-version] <report-a.json> <report-b.json>")
		os.Exit(2)
	}
	a, err := oraclereport.Load(flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", flag.Arg(0), err)
		os.Exit(2)
	}
	b, err := oraclereport.Load(flag.Arg(1))
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", flag.Arg(1), err)
		os.Exit(2)
	}
	res, err := oraclereport.Compare(a, b, oraclereport.Options{CrossInstance: *cross, CrossVersion: *crossVersion})
	if err != nil {
		fmt.Fprintf(os.Stderr, "REFUSED: %v\n", err)
		os.Exit(2)
	}
	fmt.Print(res)
	if !res.Identical {
		os.Exit(1)
	}
}
