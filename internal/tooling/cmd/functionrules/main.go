// Command functionrules measures safe recipes and emits central-registry
// candidates. Use only an explicitly supplied disposable ClickHouse server.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"strings"
	"syscall"

	"github.com/IlyaGulya/chgen/internal/functionrules"
)

func main() {
	endpoint := flag.String("url", "", "disposable ClickHouse HTTP endpoint (measurement mode)")
	evidence := flag.String("evidence", "", "measurement JSON input/output path")
	selected := flag.String("functions", "", "comma-separated safe recipes (default: all)")
	registry := flag.String("registry", "internal/engine/registry.go", "central registry source")
	output := flag.String("out", "", "candidate registry output path (generation mode)")
	check := flag.Bool("check", false, "check generated rules, or re-measure saved evidence with -url")
	reportPath := flag.String("report", "", "fresh live-check artifact path (never overwrites pinned evidence)")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *evidence == "" {
		fail(fmt.Errorf("provide -evidence"))
	}
	if *endpoint != "" {
		if *output != "" || (*check && *selected != "") || (!*check && *reportPath != "") {
			fail(fmt.Errorf("measure first; generate separately from saved evidence"))
		}
		var names []string
		var expected functionrules.Report
		if *check {
			data, err := os.ReadFile(*evidence)
			if err != nil {
				fail(err)
			}
			expected, err = functionrules.Decode(data)
			if err != nil {
				fail(err)
			}
			for _, function := range expected.Functions {
				names = append(names, function.Name)
			}
		}
		if *selected != "" {
			names = strings.Split(*selected, ",")
		}
		report, err := functionrules.Measure(ctx, *endpoint, names)
		if err != nil {
			fail(err)
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fail(err)
		}
		if *check {
			if *reportPath != "" {
				artifact, err := os.OpenFile(*reportPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
				if err != nil {
					fail(err)
				}
				_, writeErr := artifact.Write(append(data, '\n'))
				closeErr := artifact.Close()
				if writeErr != nil {
					fail(writeErr)
				}
				if closeErr != nil {
					fail(closeErr)
				}
			}
			// Preserve actual build provenance in the artifact, but compare the
			// portable measured behavior across runner architectures.
			report.Source.BuildID = expected.Source.BuildID
			if !reflect.DeepEqual(expected, report) {
				fmt.Fprintln(os.Stderr, "CHANGED measured function behavior; inspect live report before updating rules")
				os.Exit(1)
			}
			fmt.Printf("MATCHED %d measured functions\n", len(report.Functions))
			return
		}
		if err := os.WriteFile(*evidence, append(data, '\n'), 0o644); err != nil {
			fail(err)
		}
		fmt.Printf("MEASURED %d functions; %s\n", len(report.Functions), *evidence)
		return
	}
	if (*output == "" && !*check) || (*output != "" && *check) || *selected != "" || *reportPath != "" {
		fail(fmt.Errorf("generation requires -out; -functions belongs to measurement"))
	}
	data, err := os.ReadFile(*evidence)
	if err != nil {
		fail(err)
	}
	report, err := functionrules.Decode(data)
	if err != nil {
		fail(err)
	}
	source, err := os.ReadFile(*registry)
	if err != nil {
		fail(err)
	}
	candidate, added, err := functionrules.Generate(source, report)
	if err != nil {
		fail(err)
	}
	for _, function := range report.Functions {
		if !slices.Contains(added, function.Name) {
			fmt.Fprintf(os.Stderr, "DEFERRED %s: measured profile does not prove a compatible production signature\n", function.Name)
		}
	}
	if *check {
		if !bytes.Equal(source, candidate) {
			fmt.Fprintln(os.Stderr, "STALE generated function rules; regenerate from pinned evidence")
			os.Exit(1)
		}
		fmt.Printf("IDENTICAL %d generated function rules\n", len(added))
		return
	}
	if err := os.WriteFile(*output, candidate, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("CANDIDATE %s: %s; run verification before publishing\n", *output, strings.Join(added, ", "))
}

func fail(err error) { fmt.Fprintln(os.Stderr, "functionrules:", err); os.Exit(2) }
