//go:build fuzzoracle

package engine

// The version gate.
//
// Both measured goldens (wrappergrid_test.go, aggregate_combinator_grid_test.go)
// carry a "# clickhouse_version <v>" header line. Before this file existed,
// the header was written but never read: gridCompareGolden and
// combGridCompareGolden both skip every "#" line, thus a golden that was
// regenerated against the WRONG server passed the offline compare gate with
// zero failures (the regression). This file gives both goldens ONE shared
// mechanism instead of two copies of the same rule, so the rule cannot drift
// between the two grids.
//
// The gate runs in gridCompareGolden and combGridCompareGolden, which are
// called only from TestWrapperGrid and TestCombinatorGrid, and only after
// those tests have already asked the LIVE server for its version with
// "SELECT version()". Compare mode therefore always has a live version to
// check the header against. Regeneration mode (-chgen-grid-regenerate) does
// not call the compare functions at all, so the gate does not run there; a
// deliberate re-pin instead needs the -chgen-grid-i-measured-this flag (see
// below), enforced at write time in gridWriteGolden and combGridWriteGolden.
//
// Offline mode (no -chgen-grid-url, which is how "go test ./internal/engine" and
// "go test -tags fuzzoracle ./internal/engine" without a URL both run) never reaches
// this gate: TestWrapperGrid and TestCombinatorGrid skip before measuring
// anything, thus before there is a live version to compare against. The
// gate cannot weaken the offline run, because the offline run never calls
// it; it also cannot silently skip while a server IS given, because the
// live version is mandatory input to the two callers, not an optional one.

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"strings"
	"testing"
)

// gridExpectVersion is the explicit re-pin flag, in the same spirit as
// oraclegate's "-i-measured-this": to change the version a golden is pinned
// to, the person must NAME the version they measured, and a mismatch
// against the live server is an error rather than a silent acceptance.
//
// This flag is read by gridWriteGolden and combGridWriteGolden at
// REGENERATION time, not by the compare gate. Regeneration always writes
// whatever version the live server reports (SELECT version()); the flag
// exists only to make a person confirm, in the command line itself, which
// version they are about to pin the golden to. A regeneration run against
// the wrong server by mistake, without the matching flag value, refuses to
// write the file, so the version that ends up in the golden always agrees
// with a version the operator explicitly typed.
var gridExpectVersion = flag.String("chgen-grid-i-measured-this", "",
	"required for -chgen-grid-regenerate: the ClickHouse version the caller "+
		"measured, which must equal the live server's own SELECT version(). "+
		"Naming the version is the deliberate act that lets a re-pin happen "+
		"on purpose and never as a side effect of running the wrong server.")

// gridReadHeaderVersion reads the "# clickhouse_version\t<v>" line out of a
// golden file's header. It reports ok=false when the field is absent, so
// that an absent version is a distinct, reportable state and never silently
// read as "any version".
func gridReadHeaderVersion(raw []byte) (version string, ok bool) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "#") {
			// The header ends at the first non-comment line; the version
			// field, if present, always comes before the cell rows.
			break
		}
		fields := strings.SplitN(strings.TrimPrefix(line, "#"), "\t", 2)
		if len(fields) != 2 {
			continue
		}
		if strings.TrimSpace(fields[0]) == "clickhouse_version" {
			return strings.TrimSpace(fields[1]), true
		}
	}
	return "", false
}

// gridCheckVersionGate refuses the run when the golden's header does not
// carry a version, or when that version disagrees with the live server the
// caller just measured. It is the ONE mechanism behind both grids: the
// wrapper grid and the combinator grid call the same function so the version
// rule cannot drift into two different spellings.
//
// This gate is about the RECORDED version alone, never about a cell count:
// oraclediff already refuses to compare counts across runs, because the
// oracle's totals move with the age of the server on one and the same
// commit. A count-based check would inherit that same instability; a
// version-string check does not, because the version string does not drift
// between two runs of the same server build.
//
// Why a blessed regeneration cannot launder a wrong-server measurement
// through this gate: this function does not compare the header against
// itself or against any other part of the committed file. It compares the
// header against liveVersion, which the caller obtained by asking the LIVE
// server "SELECT version()" moments earlier in the same test run (see
// TestWrapperGrid and TestCombinatorGrid). A regeneration on the wrong
// server rewrites the header to match THAT server, so the header always
// equals the version of whichever server produced it; the gate then asks a
// SEPARATE, freshly-measured server for its own version and requires
// agreement. Regenerating against server A and then comparing against
// server A always agrees, by construction, no matter which server A is:
// that is expected and correct, because the golden was validly measured on
// A. What the gate prevents is comparing a golden measured on A against a
// DIFFERENT live server B without anyone noticing, which is exactly
// the regression's reproduction (golden written on 24.8, then read as if it
// still meant 25.8). The gate cannot be fooled by regenerating on B and
// calling it A, because gridWriteGolden/combGridWriteGolden refuse to write
// unless the caller's own -chgen-grid-i-measured-this claim equals what
// "SELECT version()" returns for the server the write is running against;
// there is no path that writes a header naming a version the write did not
// itself measure.
func gridCheckVersionGate(errorf func(format string, args ...any), goldenPath string, raw []byte, liveVersion string) {
	headerVersion, ok := gridReadHeaderVersion(raw)
	if !ok {
		errorf("%s has no \"# clickhouse_version\" header field. "+
			"The gate cannot tell what server the golden was measured on, thus it "+
			"cannot tell whether this run's server (%s) still agrees with it. "+
			"Regenerate with -chgen-grid-regenerate "+
			"-chgen-grid-i-measured-this=%s to record the version.",
			goldenPath, liveVersion, liveVersion)
		return
	}
	if headerVersion != liveVersion {
		errorf("%s was measured on ClickHouse %s but this run's server answers %s. "+
			"A golden that was silently re-measured on a different server is "+
			"the exact defect this gate exists to catch: some cells "+
			"can move in the dangerous direction (server refuses, chgen still "+
			"answers a type) without changing a single byte that an old-server "+
			"gate would notice. If %s is the version you now intend to pin, "+
			"regenerate on purpose with -chgen-grid-regenerate "+
			"-chgen-grid-i-measured-this=%s.",
			goldenPath, headerVersion, liveVersion, liveVersion, liveVersion)
	}
}

// gridCheckWriteVersion refuses a regeneration unless the caller repeats
// the live server's own version through -chgen-grid-i-measured-this. This
// is the write-time half of the mechanism: it stops a person from
// regenerating a golden against a server they did not mean to measure,
// which is the only way a wrong version could ever reach the header that
// gridCheckVersionGate later trusts.
func gridCheckWriteVersion(confirm, liveVersion string) error {
	if confirm == "" {
		return fmt.Errorf(
			"-chgen-grid-regenerate needs -chgen-grid-i-measured-this=<clickhouse-version>. " +
				"The golden records the measured answers of one pinned server, thus it " +
				"must never be rewritten by a command that a person ran by mistake")
	}
	if confirm != liveVersion {
		return fmt.Errorf(
			"-chgen-grid-i-measured-this=%q does not equal the live server's own "+
				"version (%q, from SELECT version()). Naming the version you intend "+
				"to pin is the point of the flag: it must be typed, not merely true "+
				"by coincidence",
			confirm, liveVersion)
	}
	return nil
}

// TestGridVersionGateFailsInBothDirections guards the regression with no live
// server needed: it exercises gridCheckVersionGate directly against
// synthetic header bytes.
//
// A one-direction test would be a single witness (see the repo's own
// "two witnesses, and they must be able to disagree" rule): a check that
// only ever fires on a mismatch could pass by coincidence if it happened to
// fire on EVERY input, absent input included, which is exactly a check that
// looks alive but never distinguishes a real mismatch from an absent field.
// This test forces both failure shapes and one success shape, so a
// regression in either direction is caught:
//
//  1. header ABSENT -> must fail (the field is not present at all).
//  2. header DISAGREES with the live server -> must fail (the exact
//     the regression reproduction: a golden measured on one server compared
//     against a different one).
//  3. header AGREES with the live server -> must NOT fail (so the gate is
//     not so wide that it refuses every run, including the one the
//     golden was honestly measured for).
func TestGridVersionGateFailsInBothDirections(t *testing.T) {
	path := moduleRootPath("testdata", "synthetic_for_gate_test.golden")

	header := func(fields ...string) []byte {
		var b bytes.Buffer
		b.WriteString("# chgen synthetic golden for TestGridVersionGateFailsInBothDirections.\n")
		for _, f := range fields {
			b.WriteString(f)
			b.WriteString("\n")
		}
		b.WriteString("fn/probe/bare/i32\tAGREE\tInt32\t\tInt32\ttoInt32(c_bare_i32)\n")
		return b.Bytes()
	}

	t.Run("absent header version fails", func(t *testing.T) {
		raw := header() // no "# clickhouse_version" line at all
		spy := &testing.T{}
		gridCheckVersionGate(spy.Errorf, path, raw, "25.8.29.51")
		if !spy.Failed() {
			t.Fatal("gridCheckVersionGate did not fail when the header carried no " +
				"clickhouse_version field; an absent version must be as loud as a " +
				"disagreeing one, never read as \"any version is fine\"")
		}
	})

	t.Run("disagreeing header version fails", func(t *testing.T) {
		raw := header("# clickhouse_version\t25.8.29.51")
		spy := &testing.T{}
		gridCheckVersionGate(spy.Errorf, path, raw, "24.8.14.39")
		if !spy.Failed() {
			t.Fatal("gridCheckVersionGate did not fail when the header's " +
				"clickhouse_version (25.8.29.51) disagreed with the live server " +
				"(24.8.14.39); this is the exact version-boundary reproduction")
		}
	})

	t.Run("agreeing header version passes", func(t *testing.T) {
		raw := header("# clickhouse_version\t25.8.29.51")
		spy := &testing.T{}
		gridCheckVersionGate(spy.Errorf, path, raw, "25.8.29.51")
		if spy.Failed() {
			t.Fatal("gridCheckVersionGate failed even though the header's " +
				"clickhouse_version equals the live server; the gate must not " +
				"refuse the run that honestly matches its own golden")
		}
	})
}

// TestGridCheckWriteVersionFailsInBothDirections guards the write-time
// half: a regeneration must refuse both an empty confirmation and a
// confirmation that does not equal the live server, and must accept the
// one confirmation that does.
func TestGridCheckWriteVersionFailsInBothDirections(t *testing.T) {
	if err := gridCheckWriteVersion("", "25.8.29.51"); err == nil {
		t.Fatal("gridCheckWriteVersion did not refuse an empty -chgen-grid-i-measured-this")
	}
	if err := gridCheckWriteVersion("24.8.14.39", "25.8.29.51"); err == nil {
		t.Fatal("gridCheckWriteVersion did not refuse a confirmation that disagrees " +
			"with the live server")
	}
	if err := gridCheckWriteVersion("25.8.29.51", "25.8.29.51"); err != nil {
		t.Fatalf("gridCheckWriteVersion refused a confirmation that agrees with the "+
			"live server: %v", err)
	}
}
