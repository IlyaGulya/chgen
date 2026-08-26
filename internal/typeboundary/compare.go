package typeboundary

// This file compares two probe artifacts and names what moved between them.
//
// # The uptime problem, and why a probe answers it differently from the oracle
//
// oraclereport.Compare refuses two reports from different server runs,
// because the type oracle samples a RANDOM subset of a grammar and the
// server answers a shifting subset of the population as it warms up; on one
// and the same commit, 347/1457 CH_ERROR findings on a fresh server became
// 348/1456 two hours later. That instability is a property of the SAMPLE,
// not of any one fixed query: the two runs did not ask the server the same
// exact questions, because the random draw itself moved.
//
// A probe cell is not a sample. It is one fixed, named, committed piece of
// SQL. Sent twice to one warm server it gives the same verdict both times —
// measured directly on ClickHouse 25.8.29.51 in this package's tests, both
// immediately after fixture creation and against the live long-running
// instance used through this whole session. So Compare here does NOT need
// oraclereport's cross-instance refusal for its core verdict: a probe
// artifact is comparable to another probe artifact of the same catalog
// regardless of uptime, because there is no sampling step whose population
// could have shifted.
//
// This does not make the server fully deterministic; it narrows what a probe
// can promise. A probe cell could in principle still depend on server state
// that this package has not measured (a cache, a setting default that
// drifts with a background thread). Compare therefore still RECORDS both
// artifacts' ServerRun and UptimeS in the Result unconditionally, so a
// reader who does not trust the "fixed query is stable" claim for some
// future cell can see the exact instance identity of both sides and
// re-measure. What Compare refuses to do is READ that identity into the
// verdict: it never reports "same server, ignore" or "different server,
// discount", because either reading would smuggle the same false comfort
// that the oracle's own docs warn against — a total (or a verdict) that
// silently depends on an axis the report does not show, one of the false
// agreements found. Records, does not decide.
//
// # The governing rule, applied to a probe cell
//
// A cell can move in either direction and both are faults of the same
// weight, per the project's own rule that a refusal wider than the server's
// breaks a query that runs:
//
//   - the server used to answer a type and now refuses (a narrowing:
//     TYPED_TO_REFUSED). A generated wrapper that used to compile and run
//     may now fail at run time against the new server.
//   - the server used to refuse and now answers a type (a widening:
//     REFUSED_TO_TYPED). A caller who could not use to write this query is
//     now unblocked, and any chgen rule that assumed the refusal is now
//     stale.
//   - the server answers both times but the type itself changed
//     (RETYPED). The generated Go field type may now be wrong for the new
//     column.
//
// Movement records which of these three happened, by name, never folded
// into a single count: a reader must see WHICH cell and WHICH KIND of move,
// the same discipline oraclereport.Compare applies to findings.

import (
	"fmt"
	"sort"
	"strings"
)

// MoveKind names how one cell's verdict changed between two artifacts.
type MoveKind string

const (
	// TypedToRefused: the OLD artifact recorded a type, the NEW one an
	// error. A narrowing: a query that used to run may now fail.
	TypedToRefused MoveKind = "TYPED_TO_REFUSED"
	// RefusedToTyped: the OLD artifact recorded an error, the NEW one a
	// type. A widening: a query that used to be refused now runs.
	RefusedToTyped MoveKind = "REFUSED_TO_TYPED"
	// Retyped: both artifacts recorded a type, and the type differs.
	Retyped MoveKind = "RETYPED"
	// RefusalChanged: both artifacts recorded an error, and the error
	// code differs. Still worth naming: a caller that matched on the old
	// code will stop matching.
	RefusalChanged MoveKind = "REFUSAL_CHANGED"
)

// Move is one named difference between two artifacts.
type Move struct {
	Cell string
	Kind MoveKind
	Old  Verdict
	New  Verdict
}

func (m Move) String() string {
	return fmt.Sprintf("%s: %s  old=%s new=%s", m.Cell, m.Kind, m.Old, m.New)
}

// CompareResult is the outcome of comparing two artifacts.
type CompareResult struct {
	Old, New  ServerInfo
	CellCount int
	Moves     []Move
}

// Identical reports whether no cell moved.
func (r *CompareResult) Identical() bool { return len(r.Moves) == 0 }

// Compare compares two probe artifacts cell by cell and returns every named
// move. It refuses, rather than answering, when the two artifacts did not
// measure the same question:
//
//   - a different catalog (a cell present on one side and absent on the
//     other would otherwise silently compare as "no difference" instead of
//     "the question was not fully asked");
//   - an artifact with zero cells (Load already refuses this, kept here too
//     so a caller that builds an Artifact by hand cannot bypass the guard).
func Compare(oldArt, newArt *Artifact) (*CompareResult, error) {
	if len(oldArt.Cells) == 0 {
		return nil, fmt.Errorf("old artifact holds no cells; a probe that never ran must not be comparable")
	}
	if len(newArt.Cells) == 0 {
		return nil, fmt.Errorf("new artifact holds no cells; a probe that never ran must not be comparable")
	}
	if diff := catalogDiff(oldArt.CatalogNames, newArt.CatalogNames); diff != "" {
		return nil, fmt.Errorf("the two artifacts do not cover the same catalog, so a missing cell on either "+
			"side would silently compare as \"no difference\":\n%s", diff)
	}

	res := &CompareResult{Old: oldArt.Server, New: newArt.Server, CellCount: len(oldArt.Cells)}
	names := make([]string, 0, len(oldArt.Cells))
	for name := range oldArt.Cells {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		o, n := oldArt.Cells[name], newArt.Cells[name]
		if o.Equal(n) {
			continue
		}
		kind := classifyMove(o, n)
		res.Moves = append(res.Moves, Move{Cell: name, Kind: kind, Old: o, New: n})
	}
	return res, nil
}

func classifyMove(o, n Verdict) MoveKind {
	switch {
	case o.ErrorCode == 0 && n.ErrorCode != 0:
		return TypedToRefused
	case o.ErrorCode != 0 && n.ErrorCode == 0:
		return RefusedToTyped
	case o.ErrorCode != 0 && n.ErrorCode != 0:
		return RefusalChanged
	default:
		return Retyped
	}
}

func catalogDiff(oldNames, newNames []string) string {
	oldSet := make(map[string]bool, len(oldNames))
	for _, n := range oldNames {
		oldSet[n] = true
	}
	newSet := make(map[string]bool, len(newNames))
	for _, n := range newNames {
		newSet[n] = true
	}
	var sb strings.Builder
	var onlyOld, onlyNew []string
	for _, n := range oldNames {
		if !newSet[n] {
			onlyOld = append(onlyOld, n)
		}
	}
	for _, n := range newNames {
		if !oldSet[n] {
			onlyNew = append(onlyNew, n)
		}
	}
	if len(onlyOld) == 0 && len(onlyNew) == 0 {
		return ""
	}
	sort.Strings(onlyOld)
	sort.Strings(onlyNew)
	if len(onlyOld) > 0 {
		fmt.Fprintf(&sb, "  only in old: %s\n", strings.Join(onlyOld, ", "))
	}
	if len(onlyNew) > 0 {
		fmt.Fprintf(&sb, "  only in new: %s\n", strings.Join(onlyNew, ", "))
	}
	return sb.String()
}

// String renders a human-readable verdict. The server identity of both
// sides is always printed (see the package comment on why it is recorded
// and not read into the verdict).
func (r *CompareResult) String() string {
	var sb strings.Builder
	if r.Identical() {
		fmt.Fprintf(&sb, "IDENTICAL (%d cells compared)\n", r.CellCount)
	} else {
		fmt.Fprintf(&sb, "MOVED (%d of %d cells)\n", len(r.Moves), r.CellCount)
	}
	fmt.Fprintf(&sb, "  old server: %s (server_run=%d uptime_s=%d)\n", r.Old.Version, r.Old.ServerRun, r.Old.UptimeS)
	fmt.Fprintf(&sb, "  new server: %s (server_run=%d uptime_s=%d)\n", r.New.Version, r.New.ServerRun, r.New.UptimeS)
	for _, m := range r.Moves {
		fmt.Fprintf(&sb, "  %s\n", m)
	}
	return sb.String()
}
