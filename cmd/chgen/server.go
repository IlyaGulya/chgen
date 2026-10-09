package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/IlyaGulya/chgen/internal/describe"
	"github.com/IlyaGulya/chgen/internal/project"
)

func runServerGeneration(args []string, stderr io.Writer, check bool) int {
	command := "generate-server"
	if check {
		command = "check-server"
	}
	flags := flag.NewFlagSet("chgen "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	config := flags.String("f", "chgen.yaml", "configuration file")
	server := flags.String("server", "", "explicit test ClickHouse HTTP(S) endpoint; mutually exclusive with snapshot-in")
	database := flags.String("database", "", "existing test database; schema inputs are never applied")
	params := flags.String("params", "", "JSON native parameter examples by query name")
	snapshotIn := flags.String("snapshot-in", "", "replay saved analysis contracts without a server")
	snapshotOut := flags.String("snapshot-out", "", "save analysis contracts alongside generated output")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || (*server == "") == (*snapshotIn == "") || *snapshotIn != "" && *snapshotOut != "" || check && *snapshotOut != "" {
		_, _ = fmt.Fprintf(stderr, "Usage: chgen %s -f chgen.yaml (-server URL | -snapshot-in contracts.json) [-database fixture] [-params examples.json] [-snapshot-out contracts.json]\n", command)
		return 2
	}
	examples := make(map[string]map[string]string)
	if *params != "" {
		data, err := os.ReadFile(*params)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "chgen: read parameter examples: %v\n", err)
			return 2
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(&examples); err != nil {
			_, _ = fmt.Fprintf(stderr, "chgen: invalid parameter examples: %v\n", err)
			return 2
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			_, _ = fmt.Fprintln(stderr, "chgen: parameter examples must be one JSON object")
			return 2
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	options := describe.Options{Server: *server, Database: *database,
		User: os.Getenv("CHGEN_DESCRIBE_USER"), Password: os.Getenv("CHGEN_DESCRIBE_PASSWORD")}
	if err := project.RunServerWithSnapshot(ctx, *config, options, examples, *params, check, project.ServerSnapshotOptions{Input: *snapshotIn, Output: *snapshotOut}); err != nil {
		_, _ = fmt.Fprintf(stderr, "chgen: server generation: %v\n", err)
		return 1
	}
	if check {
		if *snapshotIn != "" {
			_, _ = fmt.Fprintln(stderr, "Generated files match saved contracts. No server was contacted and no files were changed.")
		} else {
			_, _ = fmt.Fprintln(stderr, "Generated server contracts are current. No files were changed.")
		}
		return 0
	}
	if *snapshotIn != "" {
		_, _ = fmt.Fprintln(stderr, "Generated from saved server contracts without network access. A snapshot does not verify the current server schema; refresh it after schema, settings, or server changes.")
	} else {
		_, _ = fmt.Fprintln(stderr, "Generated from server analysis. Output metadata is checked before Scan; execution and value semantics are not proved. Re-run analysis after schema, settings, or server changes.")
	}
	return 0
}
