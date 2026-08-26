// Package execoraclereport reads the JSON artifact that the execution oracle
// (execoracle_test.go, build tag execoracle) writes, and gates a run
// against a committed baseline of accepted finding signatures.
//
// This mirrors internal/oraclereport, which does the same job for the type
// oracle, but the two packages stay separate because the two oracles write
// artifacts of a different shape: the type oracle counts named MISMATCH and
// blindness classes over a fuzzed expression stream, while the execution
// oracle emits a flat list of findings, each already carrying a case name, a
// direction and a class. A finding here needs no counting step to become a
// class label; it already is one.
//
// # Why a gate, and why signatures and never totals
//
// The execution oracle runs against a live ClickHouse server, the same
// server family the type oracle uses, and that server is not deterministic
// across container lifetimes: on one unchanged commit, a fresh container and
// a container that has run for hours can answer differently. A gate on a
// COUNT of findings would therefore go red or green according to how long
// the service container had been up, which is not a property of the code.
// See docs/ci-and-the-oracle-baseline.md for the type-oracle measurement
// that established this rule; the execution oracle runs on the same kind of
// server and inherits the same constraint.
//
// The gate below asks the question that survives a change of server
// instance: is the SET of finding signatures that this run produced a
// SUBSET of the set the baseline accepts? A signature outside the accepted
// set is a new defect, or a new blindness, and fails the run. A signature
// inside the accepted set that this run did not reach does not fail,
// because one seed is one sample of a randomised type population
// (CHGEN_EXEC_RANDN draws random nested types) and a class absent from one
// run can appear on the next.
package execoraclereport

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Finding is one entry of the execution oracle's findings list. The field
// tags MUST equal the tags that execoracle_test.go writes.
type Finding struct {
	Case      string `json:"case"`
	CHType    string `json:"ch_type"`
	Direction string `json:"direction"`
	RowID     int    `json:"rowid"`
	Class     string `json:"class"`
	Reference string `json:"reference"`
	Got       string `json:"got"`
	Detail    string `json:"detail"`
	Inherited bool   `json:"inherited,omitempty"`
}

// Report is the part of the execution-oracle JSON artifact that the gate
// reads.
type Report struct {
	Date          string `json:"date"`
	CHVersion     string `json:"clickhouse_version"`
	DriverVersion string `json:"driver_version"`
	RunDatabase   string `json:"run_database"`
	Seed          int64  `json:"seed"`
	RandomTypes   int    `json:"random_types"`
	Cases         int    `json:"cases"`
	ActiveCases   int    `json:"active_cases"`
	ScanCells     int    `json:"scan_cells_compared"`
	InsertCells   int    `json:"insert_cells_compared"`
	TypesChecked  int    `json:"types_checked"`

	FindingClasses map[string]int `json:"finding_classes"`
	Findings       []Finding      `json:"findings"`
}

// Load reads an execution-oracle report file.
func Load(path string) (*Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("decode exec-oracle report %s: %w", path, err)
	}
	return &r, nil
}

// Signature builds the exact identity of a measured finding.
//
// The identity includes the case, direction, class, row, and ClickHouse
// type. A random case name can name a different type under a different seed.
// The type field prevents an old acceptance from matching that new type. The
// row field prevents one measured bad row from accepting all rows in a case.
// The identity does not include the driver error text. That text can change
// without a change to the measured boundary.
func Signature(f Finding) string {
	return fmt.Sprintf("%s:%s:%s:row=%d:type=%s", f.Case, f.Direction, f.Class, f.RowID, f.CHType)
}

// Signatures returns the sorted, de-duplicated set of signatures in a
// report.
func Signatures(r *Report) []string {
	set := map[string]bool{}
	for _, f := range r.Findings {
		set[Signature(f)] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// baselineVersion is the shape number of the file this package writes.
const baselineVersion = 2

// Baseline is the committed set of accepted execution-oracle finding
// signatures.
type Baseline struct {
	// Version names the shape of this file. A reader who meets a number
	// this program does not know gets a refusal, not a silent partial
	// read.
	Version int `json:"version"`

	// CHVersion is the ClickHouse version that produced the baseline. A
	// version change moves the measured answers, so the gate refuses a
	// run against a different version instead of comparing across them
	// silently.
	CHVersion string `json:"clickhouse_version"`

	// DriverVersion is recorded for the reader. The gate never compares
	// it: the two independent channels (HTTP text reference vs. the
	// native driver under test) are what the oracle exists to compare,
	// and a driver bump is exactly the kind of change this oracle should
	// be free to catch, not refuse on sight.
	DriverVersion string `json:"driver_version"`

	// Note is free text for the reader. The gate never reads it.
	Note string `json:"note,omitempty"`

	// Mandatory lists signatures that MUST be present in every run. These
	// are the "BLIND ORACLE" witnesses that execoracle_test.go
	// already t.Fatalf's on when absent. They include canary_null_zero,
	// unsupported_tuple, and the four wide integer Map refusals. Recording
	// them here too
	// means a baseline regeneration cannot silently drop a mandatory
	// signature from the accepted set: the two checks would then
	// disagree, because the test's own t.Fatalf still requires them.
	// This field is for the reader and for RequireMandatory below; the
	// main subset gate (Gate) does not treat mandatory differently from
	// any other accepted signature.
	Mandatory []string `json:"mandatory_signatures"`

	// Accepted is the full accepted signature set, sorted. It is a
	// superset of Mandatory.
	Accepted []string `json:"accepted_signatures"`

	// Reasons maps each accepted signature to the measured reason for the
	// acceptance. A reason is required for every signature. This rule makes
	// each accepted row an explicit decision.
	Reasons map[string]string `json:"accepted_reasons"`
}

// LoadBaseline reads a baseline file.
func LoadBaseline(path string) (*Baseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	// Unknown keys are refused. A baseline written by a later,
	// incompatible version of this program must fail loudly and not
	// decode into a set of zero values, which would read as "nothing
	// accepted" and gate on nothing.
	dec.DisallowUnknownFields()
	var b Baseline
	if err := dec.Decode(&b); err != nil {
		return nil, fmt.Errorf("decode exec-oracle baseline %s: %w", path, err)
	}
	if b.Version != baselineVersion {
		return nil, fmt.Errorf("baseline %s has version %d, this program knows version %d",
			path, b.Version, baselineVersion)
	}
	if len(b.Accepted) == 0 {
		return nil, fmt.Errorf("baseline %s holds no accepted signature; an empty baseline gates on nothing", path)
	}
	if err := b.validateReasons(); err != nil {
		return nil, fmt.Errorf("baseline %s: %w", path, err)
	}
	return &b, nil
}

// Marshal renders a baseline as the bytes that belong in the repository. The
// set is sorted so a git diff shows exactly which signature appeared or
// left, one per line.
func (b *Baseline) Marshal() ([]byte, error) {
	b.Version = baselineVersion
	sort.Strings(b.Mandatory)
	sort.Strings(b.Accepted)
	if err := b.validateReasons(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (b *Baseline) validateReasons() error {
	accepted := make(map[string]bool, len(b.Accepted))
	for _, signature := range b.Accepted {
		if accepted[signature] {
			return fmt.Errorf("accepted signature %q occurs more than once", signature)
		}
		accepted[signature] = true
		if strings.TrimSpace(b.Reasons[signature]) == "" {
			return fmt.Errorf("accepted signature %q has no measured reason", signature)
		}
	}
	for signature := range b.Reasons {
		if !accepted[signature] {
			return fmt.Errorf("reason for %q has no accepted signature", signature)
		}
	}
	return nil
}

// GateResult is the verdict of one run against the baseline.
type GateResult struct {
	// New names signatures the run found that the baseline does not
	// accept. A non-empty New fails the gate.
	New []string
	// Gone names signatures the baseline accepts that the run did not
	// find. It NEVER fails the gate: CHGEN_EXEC_RANDN draws a random type
	// population, so a class absent on one seed's run can appear on
	// another. It is reported so a person can retire a stale line
	// deliberately.
	Gone []string
	// RunTotal and BaselineTotal record how much the gate actually read.
	// A verdict computed over an empty read must not look like a pass.
	RunTotal      int
	BaselineTotal int
}

// OK reports whether the run passes the gate.
func (g *GateResult) OK() bool { return len(g.New) == 0 }

// Gate checks a report's finding signatures against the baseline's accepted
// set.
//
// It returns an error, and no result, when the report and the baseline did
// not measure comparable populations: a different ClickHouse version, or a
// report that ran zero cases. Either means an answer here would answer a
// question nobody asked.
func (b *Baseline) Gate(r *Report) (*GateResult, error) {
	if r.Cases == 0 {
		return nil, fmt.Errorf("the run report ran 0 cases; an empty read is not a measurement")
	}
	if b.CHVersion != r.CHVersion {
		return nil, fmt.Errorf(
			"the baseline was measured on ClickHouse %s and this run on %s. A version change "+
				"moves the measured answers, thus it must be a deliberate commit and not a "+
				"silent drift. Pin the server, or regenerate the baseline on purpose",
			b.CHVersion, r.CHVersion)
	}

	run := Signatures(r)
	res := &GateResult{
		New:           missing(run, b.Accepted),
		Gone:          missing(b.Accepted, run),
		RunTotal:      len(run),
		BaselineTotal: len(b.Accepted),
	}
	return res, nil
}

// RequireMandatory checks that every mandatory signature in the baseline is
// present in the run. This is the same requirement that
// execoracle_test.go already enforces with t.Fatalf("BLIND ORACLE..."). This
// function lets the gate refuse a baseline that lost a required signature.
func (b *Baseline) RequireMandatory(r *Report) error {
	run := map[string]bool{}
	for _, s := range Signatures(r) {
		run[s] = true
	}
	var lost []string
	for _, m := range b.Mandatory {
		if !run[m] {
			lost = append(lost, m)
		}
	}
	if len(lost) > 0 {
		return fmt.Errorf(
			"BLIND ORACLE: mandatory signature(s) missing from the run: %s. "+
				"These are required witnesses (a live distortion the oracle must "+
				"catch, and a refusal the oracle must still be able to record); "+
				"their absence means the oracle stopped seeing what it exists to see",
			strings.Join(lost, ", "))
	}
	return nil
}

// missing returns the members of want that have does not hold.
func missing(want, have []string) []string {
	set := make(map[string]bool, len(have))
	for _, s := range have {
		set[s] = true
	}
	out := []string{}
	for _, s := range want {
		if !set[s] {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// String renders the verdict WITH the volume that produced it, so a reader
// cannot mistake an empty read for a pass.
func (g *GateResult) String() string {
	var sb strings.Builder
	if g.OK() {
		fmt.Fprintf(&sb, "PASS (run=%d signatures, baseline=%d signatures)\n", g.RunTotal, g.BaselineTotal)
	} else {
		fmt.Fprintf(&sb, "FAIL (%d new signature(s), run=%d, baseline=%d)\n",
			len(g.New), g.RunTotal, g.BaselineTotal)
	}
	list := func(title string, xs []string) {
		if len(xs) == 0 {
			return
		}
		fmt.Fprintf(&sb, "  %s:\n", title)
		for _, s := range xs {
			fmt.Fprintf(&sb, "    %s\n", s)
		}
	}
	list("NEW signatures, not in the baseline", g.New)
	list("in the baseline, absent from this run (does NOT fail; retire deliberately)", g.Gone)
	return sb.String()
}
