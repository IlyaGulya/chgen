// Command verify orchestrates existing maintainer checks without replacing them.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() { os.Exit(run()) }

func run() int {
	root := flag.String("root", ".", "repository root")
	selected := flag.String("suite", "", "comma-separated suites; overrides profile selection")
	profile := flag.String("profile", "quick", "quick (offline), full (live), or deep (extended exploration)")
	report := flag.String("report", "", "new or empty report directory; default creates a fresh bundle under verify-report")
	list := flag.Bool("list", false, "list the shared verification suites as JSON")
	merge := flag.String("merge", "", "merge report bundles from a directory without rerunning checks")
	expect := flag.String("expect", "", "required suite IDs for merge, or ci for the complete CI matrix")
	o := parseOptions()
	flag.Parse()
	if *list {
		if err := json.NewEncoder(os.Stdout).Encode(catalog()); err != nil {
			return 2
		}
		return 0
	}
	if *merge != "" {
		return mergeReports(*merge, *report, *expect)
	}
	ids, err := selection(*profile, *selected)
	if err != nil || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "invalid verification selection:", err)
		return 2
	}
	if err := o.finish(*profile); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	repo, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	out, err := prepareReport(*report)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := runner{ctx: ctx, root: repo, out: out, report: summary{Version: 1, Profile: *profile, Configuration: o.publicConfiguration()}}
	for _, id := range ids {
		r.report.Suites = append(r.report.Suites, suiteResult{ID: id, Status: "passed", Configuration: o.publicConfiguration()})
		r.current = &r.report.Suites[len(r.report.Suites)-1]
		if ctx.Err() != nil {
			r.refuse("cancelled", "verification cancelled; this suite was not run")
		} else {
			r.runSuite(id, *o)
		}
		if err := r.save(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	fmt.Printf("Verification %s; report: %s\n", r.report.Status, filepath.Join(out, "summary.md"))
	return reportExit(r.report)
}

func prepareReport(path string) (string, error) {
	if path == "" {
		if err := os.MkdirAll("verify-report", 0o700); err != nil {
			return "", err
		}
		var err error
		path, err = os.MkdirTemp("verify-report", time.Now().UTC().Format("20060102T150405")+"-")
		if err != nil {
			return "", err
		}
	}
	out, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(out)
	if err == nil && len(entries) != 0 {
		return "", fmt.Errorf("report directory is not empty; choose a new directory")
	}
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return out, os.MkdirAll(out, 0o700)
}
