// Command chgen generates typed ClickHouse query wrappers from annotated SQL.
// It reads chgen.yaml from the working directory (or the file given with -f)
// and generates every package the file declares.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/IlyaGulya/chgen"
	"github.com/IlyaGulya/chgen/internal/scaffold"
)

// version is set with -ldflags "-X main.version=..." on release builds.
var version = "dev"

func main() {
	if code := run(os.Args[1:], os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "describe" {
		return runDescribe(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "check" {
		return runCheck(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "init" {
		return runInit(args[1:], stderr)
	}

	flags := flag.NewFlagSet("chgen", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: chgen [-f chgen.yaml]")
		_, _ = fmt.Fprintln(stderr, "       chgen -version")
		_, _ = fmt.Fprintln(stderr, "       chgen init")
		_, _ = fmt.Fprintln(stderr, "       chgen check [-f chgen.yaml] [-json] [-require-confirmed]")
		_, _ = fmt.Fprintln(stderr, "       chgen describe -server URL -sql concrete-select.sql [-database fixture]")
		_, _ = fmt.Fprintln(stderr)
		_, _ = fmt.Fprintln(stderr, "The init command creates chgen.yaml, schema.sql, and queries.sql in the current directory.")
		flags.PrintDefaults()
	}
	configPath := flags.String("f", "chgen.yaml", "path to the chgen.yaml configuration file")
	showVersion := flags.Bool("version", false, "print the chgen version and exit")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		if _, err := fmt.Fprintln(stdout, "chgen "+buildVersion()); err != nil {
			_, _ = fmt.Fprintf(stderr, "chgen: write version: %v\n", err)
			return 1
		}
		return 0
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "chgen: unexpected argument %q; chgen takes no positional arguments\n", flags.Arg(0))
		return 1
	}
	if err := chgen.Run(*configPath); err != nil {
		_, _ = fmt.Fprintf(stderr, "chgen: %v\n", err)
		if d := chgen.ExplainError(err); d.Code != "unclassified" {
			_, _ = fmt.Fprintf(stderr, "[%s/%s] %s\n", d.Status, d.Code, d.Hint)
		}
		return 1
	}
	return 0
}

func runInit(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("chgen init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: chgen init")
		_, _ = fmt.Fprintln(stderr)
		_, _ = fmt.Fprintln(stderr, "Create chgen.yaml, schema.sql, and queries.sql in the current directory.")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "chgen: unexpected argument %q; chgen init takes no arguments\n", flags.Arg(0))
		flags.Usage()
		return 2
	}
	if err := scaffold.Init("."); err != nil {
		_, _ = fmt.Fprintf(stderr, "chgen: %v\n", err)
		return 1
	}
	return 0
}

func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	return resolvedVersion(version, info, ok)
}

func resolvedVersion(linked string, info *debug.BuildInfo, ok bool) string {
	if linked != "dev" {
		return linked
	}
	if !ok || info == nil || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return linked
	}
	return info.Main.Version
}
