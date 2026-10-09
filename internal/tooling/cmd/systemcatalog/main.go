// Command systemcatalog regenerates or checks all pinned system-table columns.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/IlyaGulya/chgen"
	"github.com/IlyaGulya/chgen/internal/systemcatalog"
)

func main() {
	server := flag.String("url", os.Getenv("CHGEN_ORACLE_URL"), "disposable ClickHouse HTTP URL")
	output := flag.String("out", "internal/systemcatalog/catalog.json", "snapshot path")
	check := flag.Bool("check", false, "compare snapshot with the pinned server")
	flag.Parse()
	if *server == "" {
		fail(fmt.Errorf("set -url or CHGEN_ORACLE_URL"))
	}
	snapshot, err := systemcatalog.Collect(context.Background(), *server, chgen.MeasuredCHVersion)
	if err != nil {
		fail(err)
	}
	data, err := systemcatalog.Marshal(snapshot)
	if err != nil {
		fail(err)
	}
	if *check {
		reference, err := os.ReadFile(*output)
		if err != nil {
			fail(err)
		}
		if !bytes.Equal(reference, data) {
			fail(fmt.Errorf("system catalog differs from %s; regenerate against the pinned server", *output))
		}
		fmt.Println("IDENTICAL")
		return
	}
	if err := os.WriteFile(*output, data, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("WROTE %s (%d columns)\n", *output, len(snapshot.Columns))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "systemcatalog:", err)
	os.Exit(1)
}
