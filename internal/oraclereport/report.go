// Package oraclereport reads the JSON report that the type oracle writes and
// compares two reports.
//
// The oracle is deterministic in its own part: a fixed seed, a fixture
// signature, a private database per run, and a hard failure instead of a
// report when the fixture breaks. The SERVER is not deterministic. The same
// commit gives different numbers on a fresh container and on a container that
// has run for two hours, and each state is deterministic in itself. Thus two
// runs in a row that agree do not show that a report can be compared with a
// stored one; they show only that the instance did not change between them.
//
// Therefore a report names the server run that produced it (ServerRun), and
// Compare REFUSES two reports from different server runs unless the caller
// says, explicitly, that the comparison crosses instances. A warning would not
// be enough: the defect class here is a check that answers a question nobody
// asked, and a warning is easy to scroll past.
package oraclereport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// ReportVersion is the current oracle report shape.
const ReportVersion = 2

// PopulationSchemaVersion is the current population identity shape.
const PopulationSchemaVersion = 1

// CurrentProfileID names the one current sampling profile.
const CurrentProfileID = "current-combined-v1"

// PopulationIdentity names the exact sampling population of one report.
// The report owns this value. A command-line label can only verify it.
type PopulationIdentity struct {
	SchemaVersion int    `json:"schema_version"`
	ProfileID     string `json:"profile_id"`
	ProfileHash   string `json:"profile_hash"`
}

// CurrentPopulation returns the identity for the current scheduler profile.
func CurrentPopulation(profileHash string) PopulationIdentity {
	return PopulationIdentity{
		SchemaVersion: PopulationSchemaVersion,
		ProfileID:     CurrentProfileID,
		ProfileHash:   profileHash,
	}
}

func (p PopulationIdentity) valid() bool {
	return p.SchemaVersion == PopulationSchemaVersion &&
		p.ProfileID == CurrentProfileID && validSHA256(p.ProfileHash)
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// Report is the part of the oracle JSON that a comparison may read. The field
// tags MUST stay equal to the tags that the oracle writes. Reading a key that
// the file does not hold gives a zero value, not an error, which is exactly
// how three false agreements were produced in one session ("Counts" read
// against a file that holds "counts"). Volume, below, exists so that such an
// empty read cannot look like agreement.
type Report struct {
	Version    int                `json:"version"`
	Population PopulationIdentity `json:"population"`
	Date       string             `json:"date"`
	CHVersion  string             `json:"clickhouse_version"`
	Seed       int64              `json:"seed"`
	Generated  int                `json:"generated_expressions"`
	Canaries   int                `json:"canary_expressions"`

	// ServerRun is the boot moment of the ClickHouse instance, in Unix
	// seconds, read as
	//
	//	SELECT toUnixTimestamp(now() - toIntervalSecond(toUInt32(uptime())))
	//
	// It is stable to the second inside one instance and it changes on
	// every restart. ClickHouse 25.8 has no getServerUUID (code 46), and
	// bare uptime cannot be compared because it moves every second.
	ServerRun int64 `json:"server_run"`
	// ServerUptimeS is the uptime in seconds at the moment of the run. It
	// is for the reader: it says how warm the server was. It is never a
	// comparison key.
	ServerUptimeS int64 `json:"server_uptime_s"`

	RunDatabase      string `json:"run_database"`
	FixtureHash      string `json:"fixture_hash"`
	FixtureSignature string `json:"fixture_signature"`

	Counts        map[string]int `json:"counts"`
	MismatchSig   map[string]int `json:"mismatch_signatures"`
	Blind43       map[string]int `json:"blind_code43_by_kind"`
	CHErrorCodes  map[string]int `json:"ch_error_codes"`
	CHErrorByKind map[string]int `json:"ch_error_by_kind"`
	CHErrorDigest string         `json:"ch_error_digest"`

	FindingsCHErrorCap int       `json:"findings_ch_error_cap"`
	Findings           []Finding `json:"findings"`

	KindChains        json.RawMessage `json:"outer_kind_chains"`
	RoundTripChecked  int             `json:"round_trip_checked"`
	RoundTripFailures json.RawMessage `json:"round_trip_failures,omitempty"`
	LaneDraws         json.RawMessage `json:"lane_draws,omitempty"`
	SamplingPlanID    string          `json:"sampling_plan_id"`
	SamplingPlanHash  string          `json:"sampling_plan_hash"`
	LaneAttempts      json.RawMessage `json:"lane_attempts,omitempty"`
	LaneAccepted      json.RawMessage `json:"lane_accepted,omitempty"`
	LaneFallback      json.RawMessage `json:"lane_fallback,omitempty"`
}

// Finding is one entry of the report findings list.
type Finding struct {
	Class     string `json:"class"`
	Expr      string `json:"expr"`
	Kind      string `json:"kind"`
	ChgenType string `json:"chgen_type,omitempty"`
	CHType    string `json:"ch_type,omitempty"`
	ChgenErr  string `json:"chgen_error,omitempty"`
	CHErr     string `json:"ch_error,omitempty"`
	MinExpr   string `json:"min_expr,omitempty"`
	MinKind   string `json:"min_kind,omitempty"`
	MinChgen  string `json:"min_chgen_type,omitempty"`
	MinCH     string `json:"min_ch_type,omitempty"`
	KindChain string `json:"kind_chain,omitempty"`
}

// Load reads and decodes a report file. Unknown keys are refused, so a report
// written by a different, incompatible version fails loudly instead of
// decoding into a set of zero values.
func Load(path string) (*Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Decode(data)
}

// Decode decodes report JSON.
func Decode(data []byte) (*Report, error) {
	var envelope struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode report version: %w", err)
	}
	if envelope.Version == 0 {
		return nil, legacyReportError()
	}
	var r Report
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("decode report: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err == nil {
		return nil, fmt.Errorf("decode report: the file has more than one JSON value")
	} else if err != io.EOF {
		return nil, fmt.Errorf("decode report trailer: %w", err)
	}
	if err := r.ValidateIdentity(); err != nil {
		return nil, err
	}
	return &r, nil
}

// ValidateIdentity refuses reports that cannot name their population and
// fixture without an external guess.
func (r *Report) ValidateIdentity() error {
	if r.Version == 0 {
		return legacyReportError()
	}
	if r.Version != ReportVersion {
		return fmt.Errorf("oracle report has version %d, this program reads version %d", r.Version, ReportVersion)
	}
	if !r.Population.valid() {
		return fmt.Errorf("oracle report has an unknown or incomplete population identity: schema_version=%d profile_id=%q profile_hash=%q", r.Population.SchemaVersion, r.Population.ProfileID, r.Population.ProfileHash)
	}
	if r.SamplingPlanID != r.Population.ProfileID || r.SamplingPlanHash != r.Population.ProfileHash {
		return fmt.Errorf("oracle report population identity does not match its scheduler identity: profile=%s/%s scheduler=%s/%s", r.Population.ProfileID, r.Population.ProfileHash, r.SamplingPlanID, r.SamplingPlanHash)
	}
	if r.FixtureHash == "" || r.FixtureSignature == "" {
		return fmt.Errorf("oracle report does not name both the fixture hash and observed fixture signature")
	}
	if r.CHVersion == "" {
		return fmt.Errorf("oracle report does not name the ClickHouse version")
	}
	if r.ServerRun == 0 {
		return fmt.Errorf("oracle report does not name a nonzero server run")
	}
	return nil
}

// ValidateCoverage checks measured cells and not scheduler attempts.
func (r *Report) ValidateCoverage() error {
	if r.Generated <= 0 {
		return fmt.Errorf("oracle report generated %d expressions; zero measured cells are not coverage", r.Generated)
	}
	accepted, err := decodeLaneCounts(r.LaneAccepted, "accepted")
	if err != nil {
		return err
	}
	attempts, err := decodeLaneCounts(r.LaneAttempts, "attempted")
	if err != nil {
		return err
	}
	if len(accepted) == 0 {
		return fmt.Errorf("oracle report has no accepted sampling cells")
	}
	for lane, count := range accepted {
		if count <= 0 {
			return fmt.Errorf("oracle report lane %q accepted %d cells; attempts are not coverage", lane, count)
		}
		if attempted, found := attempts[lane]; !found || attempted < count {
			return fmt.Errorf("oracle report lane %q has invalid attempt and accepted counts", lane)
		}
	}
	return nil
}

func decodeLaneCounts(raw json.RawMessage, name string) (map[string]int, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("oracle report has no %s lane counts", name)
	}
	var counts map[string]int
	if err := json.Unmarshal(raw, &counts); err != nil {
		return nil, fmt.Errorf("decode %s lane counts: %w", name, err)
	}
	return counts, nil
}

func legacyReportError() error {
	return fmt.Errorf("legacy oracle report is archive-only; use ConvertLegacyArtifact only with an allowlisted SHA-256 of the original JSON bytes, complete fixture and server identity, and a checked kind-prefix mapping. If one item is missing, rerun the current profile and do not assign current identity to the old file")
}

// Volume says how much the comparator actually read from one report. It is
// printed with every verdict. A verdict with an empty volume is an empty read,
// not agreement.
type Volume struct {
	CountKeys        int
	CountTotal       int
	MismatchSigKeys  int
	Blind43Keys      int
	CHErrorCodeKeys  int
	CHErrorKindKeys  int
	Findings         int
	FindingsCompared int
	HasDigest        bool
}

func volumeOf(r *Report) Volume {
	v := Volume{
		CountKeys:       len(r.Counts),
		MismatchSigKeys: len(r.MismatchSig),
		Blind43Keys:     len(r.Blind43),
		CHErrorCodeKeys: len(r.CHErrorCodes),
		CHErrorKindKeys: len(r.CHErrorByKind),
		Findings:        len(r.Findings),
		HasDigest:       r.CHErrorDigest != "",
	}
	for _, n := range r.Counts {
		v.CountTotal += n
	}
	for _, f := range r.Findings {
		if comparableClass(f.Class) {
			v.FindingsCompared++
		}
	}
	return v
}

// comparableClass reports whether the findings of a class may be compared
// between two reports. CH_ERROR may NOT: that list stops at
// findings_ch_error_cap (200 today) while counts["CH_ERROR"] keeps counting,
// so a diff or a rate taken from the list is wrong as soon as the count passes
// the cap. The uncapped ch_error_codes, ch_error_by_kind and ch_error_digest
// carry that comparison instead.
//
// CH_ERROR_43_CHGEN_TYPED MAY be compared. It is the blindness class, a subset
// of CH_ERROR that the oracle records in a list of its own which is never
// capped, thus that list is complete and a diff over it is exact.
func comparableClass(class string) bool {
	return class == "MISMATCH" || class == "CHGEN_ERROR" ||
		class == "CH_ERROR_43_CHGEN_TYPED"
}

func (v Volume) String() string {
	return fmt.Sprintf(
		"counts=%d keys/%d expressions mismatch_signatures=%d blind43=%d "+
			"ch_error_codes=%d ch_error_by_kind=%d ch_error_digest=%v "+
			"findings=%d (comparable %d, CH_ERROR entries skipped)",
		v.CountKeys, v.CountTotal, v.MismatchSigKeys, v.Blind43Keys,
		v.CHErrorCodeKeys, v.CHErrorKindKeys, v.HasDigest,
		v.Findings, v.FindingsCompared)
}

// Empty reports whether nothing worth comparing was read.
func (v Volume) Empty() bool {
	return v.CountKeys == 0 && v.CountTotal == 0 && !v.HasDigest &&
		v.MismatchSigKeys == 0 && v.CHErrorCodeKeys == 0 && v.Findings == 0
}

// ServerRunToleranceS is the largest difference in seconds that two
// server_run values may have and still name the same server run.
//
// The value is not exact, because the server computes it from two integers
// that truncate independently: now() and uptime(). Their fractional phases
// differ, thus the same live instance answers 1787145821 in one second and
// 1787145822 in another. Measured on ClickHouse 25.8.29.51: twelve reads in
// one burst gave one and the same second, while two reads minutes apart
// differed by one.
//
// An exact equality would therefore refuse two reports of ONE instance, which
// is worse than useless: it would teach the reader to pass the cross-instance
// flag every time, and the check would then guard nothing. A restart moves the
// boot moment by the whole life of the old process, which is far more than
// this tolerance, so the refusal keeps its teeth.
const ServerRunToleranceS = 5

func sameServerRun(x, y int64) bool {
	d := x - y
	if d < 0 {
		d = -d
	}
	return d <= ServerRunToleranceS
}

// SameServerRun reports whether two measured boot identities name one
// ClickHouse process within the measured timestamp tolerance.
func SameServerRun(x, y int64) bool { return x != 0 && y != 0 && sameServerRun(x, y) }

// Options controls a comparison.
type Options struct {
	// CrossInstance allows a comparison of two reports whose ServerRun
	// differs. Without it such a comparison is an error, not a warning.
	// Set it only when the difference between instances is the thing under
	// study, and read every difference as "code or server, unknown which".
	CrossInstance bool
	// CrossVersion allows a comparison of reports from different ClickHouse
	// versions. It also requires CrossInstance because one server process
	// cannot change its version. Use it only for the explicit version matrix.
	CrossVersion bool
}

// Result is the outcome of a comparison.
type Result struct {
	Identical   bool
	Differences []string
	VolumeA     Volume
	VolumeB     Volume
	// CrossInstance is true when the two reports came from different
	// server runs and the caller allowed the comparison anyway.
	CrossInstance bool
	// CrossVersion is true when the caller allowed different ClickHouse
	// versions for an explicit version-boundary measurement.
	CrossVersion bool
}

// Compare compares two reports.
//
// It returns an error, and no result, when the comparison itself cannot answer
// the question that the caller asked:
//
//   - the two reports come from different server runs and opts.CrossInstance
//     is not set. The same commit measured on a fresh server and on a server
//     that has been warm for two hours gives different CH_ERROR numbers, so a
//     difference between such reports says nothing about the code;
//   - either report is empty of everything the comparator reads. An empty read
//     that reports "identical" is the false agreement this comparator exists
//     to stop;
//   - the two reports were produced with a different seed, expression count,
//     sampling profile or fixture, because then they did not measure the same
//     population.
func Compare(a, b *Report, opts Options) (*Result, error) {
	if err := a.ValidateIdentity(); err != nil {
		return nil, fmt.Errorf("report A identity: %w", err)
	}
	if err := b.ValidateIdentity(); err != nil {
		return nil, fmt.Errorf("report B identity: %w", err)
	}
	if a.Population != b.Population {
		return nil, fmt.Errorf("different sampling profiles: A=%s/%s B=%s/%s", a.Population.ProfileID, a.Population.ProfileHash, b.Population.ProfileID, b.Population.ProfileHash)
	}
	if a.FixtureHash != b.FixtureHash {
		return nil, fmt.Errorf("different fixture hashes: A=%s B=%s", a.FixtureHash, b.FixtureHash)
	}
	crossVersion := a.CHVersion != b.CHVersion
	if crossVersion && !opts.CrossVersion {
		return nil, fmt.Errorf("different ClickHouse versions: A=%s B=%s", a.CHVersion, b.CHVersion)
	}
	if crossVersion && !opts.CrossInstance {
		return nil, fmt.Errorf("a cross-version comparison also needs the cross-instance option because one server process cannot change its version")
	}
	va, vb := volumeOf(a), volumeOf(b)
	if va.Empty() {
		return nil, fmt.Errorf("report A is empty of everything the comparator reads (%s); "+
			"check that the file is an oracle report and that its JSON keys are lowercase", va)
	}
	if vb.Empty() {
		return nil, fmt.Errorf("report B is empty of everything the comparator reads (%s); "+
			"check that the file is an oracle report and that its JSON keys are lowercase", vb)
	}

	if a.ServerRun == 0 || b.ServerRun == 0 {
		return nil, fmt.Errorf(
			"a report does not name its server run (A server_run=%d, B server_run=%d). "+
				"A report written before server_run existed cannot be compared, because "+
				"the instance it measured is unknown",
			a.ServerRun, b.ServerRun)
	}

	crossInstance := !sameServerRun(a.ServerRun, b.ServerRun)
	if crossInstance && !opts.CrossInstance {
		return nil, fmt.Errorf(
			"the two reports come from DIFFERENT server runs: A server_run=%d (uptime %ds at the run), "+
				"B server_run=%d (uptime %ds at the run). "+
				"The server-side CH_ERROR class moves with server age on one and the same commit, "+
				"thus a difference between these reports cannot be read as a difference in code. "+
				"Re-measure both sides on one instance, or pass the cross-instance option to say "+
				"that the instance difference is what you are studying",
			a.ServerRun, a.ServerUptimeS, b.ServerRun, b.ServerUptimeS)
	}
	if a.Seed != b.Seed {
		return nil, fmt.Errorf("different seeds: A seed=%d, B seed=%d; the two runs did not measure the same expressions", a.Seed, b.Seed)
	}
	if a.Generated != b.Generated {
		return nil, fmt.Errorf("different expression counts: A generated=%d, B generated=%d", a.Generated, b.Generated)
	}
	if a.FixtureSignature != b.FixtureSignature {
		return nil, fmt.Errorf("different fixtures:\n  A: %s\n  B: %s", a.FixtureSignature, b.FixtureSignature)
	}

	res := &Result{VolumeA: va, VolumeB: vb, CrossInstance: crossInstance, CrossVersion: crossVersion}
	diffMap := func(name string, x, y map[string]int) {
		keys := map[string]bool{}
		for k := range x {
			keys[k] = true
		}
		for k := range y {
			keys[k] = true
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if x[k] != y[k] {
				res.Differences = append(res.Differences,
					fmt.Sprintf("%s[%s]: A=%d B=%d", name, k, x[k], y[k]))
			}
		}
	}
	diffMap("counts", a.Counts, b.Counts)
	diffMap("mismatch_signatures", a.MismatchSig, b.MismatchSig)
	diffMap("blind_code43_by_kind", a.Blind43, b.Blind43)
	diffMap("ch_error_codes", a.CHErrorCodes, b.CHErrorCodes)
	diffMap("ch_error_by_kind", a.CHErrorByKind, b.CHErrorByKind)
	if a.CHErrorDigest != b.CHErrorDigest {
		res.Differences = append(res.Differences,
			fmt.Sprintf("ch_error_digest: A=%s B=%s", a.CHErrorDigest, b.CHErrorDigest))
	}

	// Findings: MISMATCH and CHGEN_ERROR only. The CH_ERROR entries are
	// truncated at findings_ch_error_cap and are never compared here.
	fa, fb := comparableFindings(a), comparableFindings(b)
	for _, key := range mergedKeys(fa, fb) {
		x, inA := fa[key]
		y, inB := fb[key]
		switch {
		case !inA:
			res.Differences = append(res.Differences, fmt.Sprintf("findings: only in B: %s", y))
		case !inB:
			res.Differences = append(res.Differences, fmt.Sprintf("findings: only in A: %s", x))
		case x != y:
			res.Differences = append(res.Differences, fmt.Sprintf("findings differ:\n  A: %s\n  B: %s", x, y))
		}
	}

	res.Identical = len(res.Differences) == 0
	return res, nil
}

func comparableFindings(r *Report) map[string]string {
	out := map[string]string{}
	for _, f := range r.Findings {
		if !comparableClass(f.Class) {
			continue
		}
		key := f.Class + "\x00" + f.Expr
		// ch_error is part of the rendered line, because for the
		// blindness class the server message is the evidence and the
		// other error field is always empty there.
		out[key] = fmt.Sprintf("%s %q kind=%s chgen=%s ch=%s chgen_error=%s ch_error=%s",
			f.Class, f.Expr, f.Kind, f.ChgenType, f.CHType, f.ChgenErr, f.CHErr)
	}
	return out
}

func mergedKeys(x, y map[string]string) []string {
	seen := map[string]bool{}
	keys := make([]string, 0, len(x)+len(y))
	for k := range x {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for k := range y {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// String renders the verdict WITH the volume that produced it. The volume is
// never optional: "identical=true n=0" is an empty read, not agreement, and a
// reader must be able to see the difference without opening the files.
func (r *Result) String() string {
	var sb strings.Builder
	if r.Identical {
		sb.WriteString("IDENTICAL\n")
	} else {
		fmt.Fprintf(&sb, "DIFFERENT (%d differences)\n", len(r.Differences))
	}
	fmt.Fprintf(&sb, "  read from A: %s\n", r.VolumeA)
	fmt.Fprintf(&sb, "  read from B: %s\n", r.VolumeB)
	if r.CrossInstance {
		sb.WriteString("  NOTE: the reports come from different server runs; " +
			"every difference below may be the server, not the code.\n")
	}
	if r.CrossVersion {
		sb.WriteString("  NOTE: the reports come from different ClickHouse versions; " +
			"the result is a version-boundary measurement and not a code gate.\n")
	}
	for _, d := range r.Differences {
		fmt.Fprintf(&sb, "  %s\n", d)
	}
	return sb.String()
}
