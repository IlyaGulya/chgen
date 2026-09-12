package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/IlyaGulya/chgen"
)

func runCheck(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("chgen check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	config := flags.String("f", "chgen.yaml", "configuration file to validate without writing outputs")
	jsonOutput := flags.Bool("json", false, "print a structured report")
	strict := flags.Bool("require-confirmed", false, "fail on client-asserted result types and unchecked settings")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "chgen check takes no positional arguments")
		return 2
	}
	report, checkErr := chgen.Check(*config)
	if err := writeCheckReport(stdout, report, *jsonOutput); err != nil {
		_, _ = fmt.Fprintf(stderr, "chgen: write check report: %v\n", err)
		return 1
	}
	if checkErr != nil || *strict && report.Status != "confirmed" {
		return 1
	}
	return 0
}

func writeCheckReport(output io.Writer, report chgen.CheckReport, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	if _, err := fmt.Fprintf(output, "chgen check: %s; can generate: %t (%d packages, %d queries)\n", report.Status, report.CanGenerate, report.Packages, report.Queries); err != nil {
		return err
	}
	for _, d := range report.Diagnostics {
		if _, err := fmt.Fprintf(output, "[%s/%s] %s\n  %s\n", d.Status, d.Code, d.Message, d.Hint); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(output, "Offline model check only; no files written and no queries executed.")
	return err
}
