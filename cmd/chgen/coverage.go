package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/IlyaGulya/chgen/internal/coverage"
	"github.com/IlyaGulya/chgen/internal/describe"
)

func runCoverage(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("chgen coverage", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("corpus", "", "versioned SQL corpus JSON (required)")
	server := flags.String("server", "", "optional explicit test ClickHouse HTTP(S) endpoint; analyzes query cases only")
	database := flags.String("database", "", "existing test fixture database; no DDL is applied")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *path == "" {
		_, _ = fmt.Fprintln(stderr, "Usage: chgen coverage -corpus corpus.json")
		return 2
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "chgen coverage: %v\n", err)
		return 2
	}
	var report coverage.Report
	if *server != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		report, err = coverage.Analyze(ctx, data, describe.Options{Server: *server, Database: *database,
			User: os.Getenv("CHGEN_DESCRIBE_USER"), Password: os.Getenv("CHGEN_DESCRIBE_PASSWORD")})
	} else {
		report, err = coverage.Measure(data)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "chgen coverage: %v\n", err)
		return 2
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		_, _ = fmt.Fprintf(stderr, "chgen coverage: %v\n", err)
		return 1
	}
	if report.Counts["type_comparison"]["mismatch"] > 0 {
		return 1
	}
	return 0
}
