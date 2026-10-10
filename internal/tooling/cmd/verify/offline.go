package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/IlyaGulya/chgen/internal/drivercompat"
)

func (r *runner) offline(_ options) {
	r.command("verify", nil, "bash", "scripts/verify.sh")
	if tool := r.tool("supportmanifest"); tool != "" {
		if r.gate("support-manifest", tool, "-check") {
			r.output("api-coverage", "api-support-coverage.json", nil, tool, "-report")
		}
	}
	if tool := r.tool("chgen"); tool != "" {
		r.output("syntax-coverage", "syntax-coverage.json", nil, tool, "coverage", "-corpus", "testdata/syntax-corpus.json")
	}
}

func (r *runner) minimum(_ options) {
	data, err := os.ReadFile(filepath.Join(r.root, "go.mod"))
	if err != nil || !strings.Contains(string(data), "\ngo 1.24.0\n") {
		r.refuse("go-directive", "go.mod must retain minimum Go 1.24.0")
		return
	}
	env := map[string]string{"GOTOOLCHAIN": "go1.24.0"}
	if !r.command("compiler", env, "go", "version") {
		return
	}
	data, err = os.ReadFile(filepath.Join(r.out, "minimum-compiler.log"))
	if err != nil || !strings.Contains(string(data), "go version go1.24.0 ") {
		r.refuse("compiler", "minimum verification requires exact Go 1.24.0")
		return
	}
	if r.command("build", env, "go", "build", "-o", filepath.Join(r.out, "chgen-minimum"), "./cmd/chgen") {
		r.command("tests", env, "go", "test", "./...")
	}
}

func (r *runner) compatibility(o options) {
	cells := drivercompat.Supported()
	if o.goVersion != "" {
		cell, err := drivercompat.Find(o.goVersion, o.driver)
		if err != nil {
			r.refuse("matrix", "unsupported Go/driver pair; use a pair from drivercompat -list")
			return
		}
		cells = []drivercompat.Cell{cell}
	}
	tool := r.tool("drivercompat")
	if tool == "" {
		return
	}
	for _, cell := range cells {
		env := map[string]string{"GOTOOLCHAIN": "go" + cell.MinimumGo}
		id := "go" + cell.MinimumGo + "-driver" + cell.DriverVersion
		// drivercompat deliberately disables implicit toolchain switching.
		// Resolve the requested compiler first and pass its actual executable.
		if !r.command(id+"-compiler", env, "go", "env", "GOROOT") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.out, "compatibility-"+id+"-compiler.log"))
		if err != nil || !filepath.IsAbs(strings.TrimSpace(string(data))) {
			r.refuse(id, "cannot resolve requested Go compiler")
			continue
		}
		compiler := filepath.Join(strings.TrimSpace(string(data)), "bin", "go")
		env["GOROOT"] = strings.TrimSpace(string(data))
		r.command(id, env, tool, "-go", cell.MinimumGo, "-driver", cell.DriverVersion, "-go-command", compiler, "-root", r.root)
	}
}

func (r *runner) fuzz(o options) {
	r.command("public-pipeline", nil, "go", "test", ".", "-run", "^$", "-fuzz", "^FuzzPublicSQLPipeline$", "-fuzztime", o.fuzzTime, "-parallel", "2", "-timeout", "15m")
}
