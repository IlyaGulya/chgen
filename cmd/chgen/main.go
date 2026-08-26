// Command chgen generates typed ClickHouse query wrappers from annotated SQL.
// It reads chgen.yaml from the working directory (or the file given with -f)
// and generates every package the file declares.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/IlyaGulya/chgen"
)

// version is set with -ldflags "-X main.version=..." on release builds.
var version = "dev"

func main() {
	configPath := flag.String("f", "chgen.yaml", "path to the chgen.yaml configuration file")
	showVersion := flag.Bool("version", false, "print the chgen version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("chgen " + buildVersion())
		return
	}
	if flag.NArg() > 0 {
		fatal("unexpected argument %q; chgen takes no positional arguments", flag.Arg(0))
	}
	if err := chgen.Run(*configPath); err != nil {
		fatal("%v", err)
	}
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

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "chgen: "+format+"\n", args...)
	os.Exit(1)
}
