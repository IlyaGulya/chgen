// Command probegate compares two chgen probe artifacts and names every
// cell of the ClickHouse type boundary that moved between them.
//
// Usage:
//
//	go run ./internal/tooling/cmd/probegate old.json new.json
//	go run ./internal/tooling/cmd/probegate -selftest
//	go run ./internal/tooling/cmd/probegate -known-version-boundary old.json new.json
//
// It exits 0 when every cell agrees, 1 when at least one cell moved, and 2
// when the comparison cannot answer the question (a missing file, an empty
// artifact, or two artifacts that do not cover the same catalog).
// With -known-version-boundary, it exits 0 only when all fixed witnesses
// match the measured 25.8.29.51 to 24.8.14.39 boundary.
//
// # Why this gate does not need oraclediff's -cross-instance escape hatch
//
// The oracle difference tool refuses to compare two type-oracle reports from different
// server runs, because the oracle SAMPLES a grammar at random and the
// server answers a shifting subset of that sample as it warms up; the same
// seed on the same commit gave 347/1457 CH_ERROR findings on a fresh server
// and 348/1456 findings two hours later (typeoracle_fuzz_test.go). That is
// instability in WHICH QUESTIONS were asked across two
// runs, not in how the server answers any one fixed question.
//
// A probe artifact contains no random draw. Every cell is one committed,
// named piece of SQL over real table columns
// (internal/typeboundary.Catalog), and this package's own tests show that a
// fixed query answers the same verdict on a warm server no matter when it
// is asked. probegate therefore compares two artifacts from any two server
// runs directly: comparing across a version boundary is precisely the
// point, so refusing that comparison the way oraclediff does would defeat
// the tool.
//
// This is a narrower promise than "the server is deterministic". A probe
// artifact carries the ServerRun and UptimeS of the run that produced it
// (internal/typeboundary.ServerInfo), and probegate always prints both, so a
// reader who suspects a specific cell of secretly depending on server age
// can see the exact instance identity of each side and re-measure it
// directly, rather than trusting a count that hides the axis.
//
// # -selftest
//
// -selftest builds two artifacts from the LIVE Catalog
// (internal/typeboundary.Catalog) — so the injected shape comes from the same
// code that a real probe run would produce, not from a remembered format —
// flips one cell's verdict from TYPED_TO_REFUSED, and checks that Compare
// names exactly that cell and exactly that move kind. It exits 0 only when
// the injected difference was found and named; anything else, including
// "no difference reported", is a failure of the gate itself and exits
// non-zero. This is the check required before trusting probegate in CI: a
// gate that cannot tell "no difference" from "never ran" is worse than no
// gate.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/IlyaGulya/chgen/internal/typeboundary"
)

func main() {
	selftest := flag.Bool("selftest", false, "run the self-test: inject a known difference and verify probegate names it, then exit")
	knownBoundary := flag.Bool("known-version-boundary", false, "require the executed 25.8.29.51 to 24.8.14.39 boundary witnesses")
	flag.Parse()

	if *selftest {
		if err := runSelftest(); err != nil {
			fmt.Fprintf(os.Stderr, "probegate -selftest: FAIL: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("probegate -selftest: PASS: the injected difference was found and named")
		return
	}

	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: probegate <old.json> <new.json>")
		fmt.Fprintln(os.Stderr, "       probegate -selftest")
		fmt.Fprintln(os.Stderr, "       probegate -known-version-boundary <pinned.json> <candidate.json>")
		os.Exit(2)
	}

	oldArt, err := typeboundary.Load(flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "probegate: read %s: %v\n", flag.Arg(0), err)
		os.Exit(2)
	}
	newArt, err := typeboundary.Load(flag.Arg(1))
	if err != nil {
		fmt.Fprintf(os.Stderr, "probegate: read %s: %v\n", flag.Arg(1), err)
		os.Exit(2)
	}
	if *knownBoundary {
		if err := typeboundary.VerifyKnownVersionBoundary(oldArt, newArt); err != nil {
			fmt.Fprintf(os.Stderr, "probegate: REFUSED known version boundary: %v\n", err)
			os.Exit(2)
		}
		fmt.Println("KNOWN VERSION BOUNDARY PASS: all required cells used exact expressions and execution witnesses")
		return
	}
	if err := newArt.ValidateConformance(); err != nil {
		fmt.Fprintf(os.Stderr, "probegate: current artifact has incomplete conformance data: %v\n", err)
		os.Exit(2)
	}

	res, err := typeboundary.Compare(oldArt, newArt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probegate: REFUSED: %v\n", err)
		os.Exit(2)
	}
	fmt.Print(res)
	if !res.Identical() {
		os.Exit(1)
	}
}

// runSelftest builds two artifacts by hand, in the exact shape
// typeboundary.Run produces, from the LIVE catalog. It picks one cell that
// the live catalog holds, records its committed name, then constructs an
// "old" artifact and a "new" artifact that differ ONLY in that one cell's
// verdict, moved from a type to a refusal (TYPED_TO_REFUSED). It then runs
// the real Compare and checks that the result names exactly that cell and
// that move kind, and nothing else.
//
// The injected verdicts are synthetic (a made-up type name and error code),
// but the SHAPE they are wrapped in — Artifact.Cells, Artifact.CatalogNames,
// Verdict itself — is read from internal/typeboundary, the package that also
// produces a real artifact, never retyped here from memory. This is the
// discipline the project learned the hard way: an injected witness whose
// shape does not come from the producing code can report a difference that
// cannot occur (a prior false P0 on this project).
func runSelftest() error {
	if len(typeboundary.Catalog) == 0 {
		return fmt.Errorf("the live catalog is empty; nothing to inject a difference into")
	}
	names := make([]string, len(typeboundary.Catalog))
	cellsOld := make(map[string]typeboundary.Verdict, len(typeboundary.Catalog))
	cellsNew := make(map[string]typeboundary.Verdict, len(typeboundary.Catalog))
	for i, cell := range typeboundary.Catalog {
		names[i] = cell.Name
		v := typeboundary.Verdict{TypeName: "Int32"}
		cellsOld[cell.Name] = v
		cellsNew[cell.Name] = v
	}

	target := typeboundary.Catalog[0].Name
	cellsNew[target] = typeboundary.Verdict{ErrorCode: 43}

	server := typeboundary.ServerInfo{Version: "selftest", ServerRun: 1, UptimeS: 1}
	oldArt := &typeboundary.Artifact{
		Version:      typeboundary.ArtifactVersion,
		Server:       server,
		CatalogNames: append([]string(nil), names...),
		Cells:        cellsOld,
	}
	newArt := &typeboundary.Artifact{
		Version:      typeboundary.ArtifactVersion,
		Server:       typeboundary.ServerInfo{Version: "selftest-candidate", ServerRun: 2, UptimeS: 1},
		CatalogNames: append([]string(nil), names...),
		Cells:        cellsNew,
	}

	res, err := typeboundary.Compare(oldArt, newArt)
	if err != nil {
		return fmt.Errorf("Compare refused a comparison it should have answered: %w", err)
	}
	if res.Identical() {
		return fmt.Errorf("Compare reported IDENTICAL after a verdict was deliberately changed on cell %q; "+
			"the gate cannot distinguish \"no difference\" from \"never ran\"", target)
	}
	if len(res.Moves) != 1 {
		return fmt.Errorf("expected exactly 1 named move, got %d: %v", len(res.Moves), res.Moves)
	}
	m := res.Moves[0]
	if m.Cell != target {
		return fmt.Errorf("expected the move to name cell %q, got %q", target, m.Cell)
	}
	if m.Kind != typeboundary.TypedToRefused {
		return fmt.Errorf("expected move kind %s on cell %q, got %s", typeboundary.TypedToRefused, target, m.Kind)
	}
	return nil
}
