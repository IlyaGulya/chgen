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
	"github.com/IlyaGulya/chgen/internal/diagnostic"
	"github.com/IlyaGulya/chgen/internal/project"
)

// serverWorkflowFlags keeps the common CLI on one explicit capture/replay path.
// The existing advanced commands remain available and share the same runner.
type serverWorkflowFlags struct {
	server, database, params, contracts string
}

func (options *serverWorkflowFlags) register(flags *flag.FlagSet) {
	flags.StringVar(&options.server, "server", "", "analyze SELECT queries on your test ClickHouse HTTP(S) endpoint")
	flags.StringVar(&options.database, "database", "", "existing test database; migrations are not executed")
	flags.StringVar(&options.params, "params", "", "JSON parameter examples, keyed by package.Query")
	flags.StringVar(&options.contracts, "contracts", "", "with -server: save result contracts; without -server: generate offline from saved contracts")
}

func (options serverWorkflowFlags) selected() bool {
	return options.server != "" || options.contracts != "" || options.database != "" || options.params != ""
}

func (options serverWorkflowFlags) run(config string, stderr io.Writer, check bool, report io.Writer) int {
	if options.server == "" && options.contracts == "" {
		_, _ = fmt.Fprintln(stderr, "chgen: -database and -params need -server URL or -contracts FILE; ordinary generation stays offline")
		return 2
	}
	if check && options.server != "" && options.contracts != "" {
		_, _ = fmt.Fprintln(stderr, "chgen: check never saves contracts; use chgen -server URL -contracts FILE to capture, or chgen check -contracts FILE to verify offline")
		return 2
	}
	args := []string{"-f", config}
	if options.server != "" {
		args = append(args, "-server", options.server)
	}
	if options.contracts != "" {
		flagName := "-snapshot-in"
		if options.server != "" {
			flagName = "-snapshot-out"
		}
		args = append(args, flagName, options.contracts)
	}
	if options.database != "" {
		args = append(args, "-database", options.database)
	}
	if options.params != "" {
		args = append(args, "-params", options.params)
	}
	return runServerGenerationWithReport(args, stderr, check, report)
}

func runServerGeneration(args []string, stderr io.Writer, check bool) int {
	return runServerGenerationWithReport(args, stderr, check, nil)
}

func runServerGenerationWithReport(args []string, stderr io.Writer, check bool, reportOutput io.Writer) int {
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
	source := "live"
	if *snapshotIn != "" {
		source = "saved"
	}
	fail := func(err error, code int) int {
		_, _ = fmt.Fprintf(stderr, "chgen: server generation: %v\n", err)
		if d := diagnostic.Describe(err); d.Code != "unclassified" {
			_, _ = fmt.Fprintf(stderr, "[%s/%s] %s\n", d.Status, d.Code, d.Hint)
		}
		if reportOutput != nil {
			if writeErr := writeServerCheckJSON(reportOutput, source, err); writeErr != nil {
				_, _ = fmt.Fprintf(stderr, "chgen: write check report: %v\n", writeErr)
				return 1
			}
		}
		return code
	}
	examples := make(map[string]map[string]string)
	if *params != "" {
		data, err := os.ReadFile(*params)
		if err != nil {
			return fail(fmt.Errorf("read parameter examples: %w", err), 2)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(&examples); err != nil {
			return fail(fmt.Errorf("invalid parameter examples: %w", err), 2)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return fail(fmt.Errorf("parameter examples must be one JSON object"), 2)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	options := describe.Options{Server: *server, Database: *database,
		User: os.Getenv("CHGEN_DESCRIBE_USER"), Password: os.Getenv("CHGEN_DESCRIBE_PASSWORD")}
	if err := project.RunServerWithSnapshot(ctx, *config, options, examples, *params, check, project.ServerSnapshotOptions{Input: *snapshotIn, Output: *snapshotOut}); err != nil {
		return fail(err, 1)
	}
	if check {
		if reportOutput != nil {
			if err := writeServerCheckJSON(reportOutput, source, nil); err != nil {
				_, _ = fmt.Fprintf(stderr, "chgen: write check report: %v\n", err)
				return 1
			}
			return 0
		}
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

func writeServerCheckJSON(output io.Writer, source string, err error) error {
	report := struct {
		Status, Analysis, ContractSource string
		CanGenerate, FilesChanged        bool
		Diagnostics                      []diagnostic.Detail
		Warning                          string
	}{Status: diagnostic.Confirmed, Analysis: "server", ContractSource: source, CanGenerate: err == nil,
		Diagnostics: []diagnostic.Detail{}, Warning: "Checks result contracts and generated files, not execution or business values. Saved contracts do not verify the current server schema."}
	if err != nil {
		d := diagnostic.Describe(err)
		report.Status = d.Status
		report.Diagnostics = append(report.Diagnostics, d)
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
