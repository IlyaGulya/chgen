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

	"github.com/IlyaGulya/chgen/internal/describe"
)

func runDescribe(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("chgen describe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "explicit test ClickHouse HTTP(S) endpoint (required)")
	sqlPath := flags.String("sql", "", "file containing one concrete SELECT (required)")
	database := flags.String("database", "", "existing fixture database; no schema is applied")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *server == "" || *sqlPath == "" {
		_, _ = fmt.Fprintln(stderr, "Usage: chgen describe -server http://test-clickhouse:8123 -sql concrete-select.sql [-database fixture]")
		return 2
	}
	sql, err := os.ReadFile(*sqlPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "chgen: read describe input: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	report, err := describe.Query(ctx, describe.Options{
		Server: *server, Database: *database,
		User: os.Getenv("CHGEN_DESCRIBE_USER"), Password: os.Getenv("CHGEN_DESCRIBE_PASSWORD"),
	}, string(sql))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "chgen: describe: %v\n", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		_, _ = fmt.Fprintf(stderr, "chgen: write describe report: %v\n", err)
		return 1
	}
	return 0
}
