package oraclereport

// This file holds the BASELINE gate, which is a different question from the
// comparison in report.go. Compare answers "did these two runs give the same
// numbers", and it REFUSES two reports from different server runs, because a
// count moves with the age of the server on one and the same commit.
//
// A committed baseline is always from a different server run than the run
// under test, thus Compare can never gate against one. The gate below asks a
// question that survives the change of instance:
//
//	is the SET of signatures that this run found a subset of the SET that
//	the baseline records?
//
// A signature is a class label and not a count: "kind: chgen=X ch=Y". The
// counts inside a class move with the sample and with the server, but a class
// that no run ever saw before is a NEW class, and a new class is a new defect
// or a new blindness. Thus the gate compares set membership only and never a
// total. The ticket asks for exactly this, and the rule of this project that
// forbids a gate on a total is kept.
//
// Signature, not expression, is the unit on purpose. An expression is a sample
// of a class: the generator draws different expressions from one seed as soon
// as the grammar changes by one node, and the server refuses a different
// subset of them as it warms. A class label survives both.

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Baseline is the committed set of signatures that a run may show. It is the
// file that a person regenerates deliberately.
//
// The two gated classes are the two classes that name a WRONG ANSWER:
//
//   - Mismatch: chgen and the server both answered and the names differ.
//   - Blind43: chgen gave a type where the server refuses the expression
//     with ILLEGAL_TYPE_OF_ARGUMENT. By the governing rule of this project
//     this is the most costly class, because it is a silently wrong type.
//
// CH_ERROR as a whole is NOT gated. It is generator waste, its total moves
// with the age of the server, and the project already records that it can
// never be a hard gate.
type Baseline struct {
	// Version names the shape of this file. A reader who meets a number
	// that this program does not know gets a refusal and not a silent
	// partial read.
	Version int `json:"version"`

	// CHVersion is the ClickHouse version that produced the baseline. The
	// gate refuses a run against a different version, because a version
	// change moves the measured answers and thus must be a deliberate
	// commit.
	CHVersion string `json:"clickhouse_version"`

	// Note is free text for the reader. The gate never reads it.
	Note string `json:"note,omitempty"`

	// Cells holds one entry per (profile, seed, n) that the workflow runs.
	// The key starts with the report-owned profile ID.
	Cells map[string]*BaselineCell `json:"cells"`

	// UnionCells holds one entry per profile, not per seed. The key is the
	// profile ID. Each entry is the accepted signature
	// set across a FIXED SET OF SEEDS, unioned before the compare.
	//
	// This is what lets the CI seed set grow without a hand-written cell per
	// new seed: GateUnion folds every report of one profile into one set and
	// compares that set against this one entry, so a caller adds a seed to
	// the run and the baseline shape does not change. See UnionGateResult and
	// GateUnion below, and docs/ci-and-the-oracle-baseline.md for the design
	// choice among the three options the ticket posed.
	UnionCells map[string]*UnionBaselineCell `json:"union_cells,omitempty"`
}

// UnionBaselineCell is the accepted signature set across a fixed seed set,
// for one profile. It carries one population, fixture and server run because
// every input report must share these identities. It carries several seeds
// because the cell is the union of those runs.
type UnionBaselineCell struct {
	Population       PopulationIdentity `json:"population"`
	FixtureHash      string             `json:"fixture_hash"`
	FixtureSignature string             `json:"fixture_signature"`

	// Seeds names the seed set that produced this cell, for the reader. The
	// gate does not compare against this list: adding a seed to the CI run
	// is exactly the change this design must allow without touching the
	// baseline. It is here so that a diff of the file shows which seeds were
	// measured when the accepted set last changed.
	Seeds []int64 `json:"seeds"`

	// ServerRun names one representative instance of the measurement, for
	// the audit trail. Every report that fed this cell must in practice
	// share one server run (the CI job pins one container per profile), but
	// the gate never compares this field, for the same reason BaselineCell
	// does not: a committed baseline is always a different run than the run
	// under test.
	ServerRun int64 `json:"server_run"`

	// Mismatch and Blind43 are the accepted signatures, sorted, unioned
	// across every seed in Seeds. A signature here may be either "understood
	// and fixed away" (in which case, once the run no longer shows it, it
	// falls into GoneMismatch/GoneBlind43 and a person may retire the line)
	// or "a known, tracked, unfixed defect" (in which case Note or a comment
	// beside the entry names the tracking item that tracks it, and the gate accepts
	// it on purpose rather than failing forever on something already
	// tracked).
	Mismatch []string `json:"mismatch_signatures"`
	Blind43  []string `json:"blind43_signatures"`

	// Retired is the audit trail of signatures that USED to sit in Mismatch
	// or Blind43 and were removed one at a time by -retire-union, keyed by
	// the exact signature string. This field is optional and additive: a
	// baseline written before this record existed has no "retired" key, and
	// LoadBaseline must still read it, because the committed file in git
	// today has the old shape.
	//
	// A retirement never happens because a run stayed silent about a
	// signature. It happens because a PERSON asserts that the signature is
	// fixed, and the tool records that assertion together with the
	// ClickHouse version measured and the reason. Read RetirementRecord for
	// the fields it keeps.
	Retired map[string]*RetirementRecord `json:"retired,omitempty"`
}

// RetirementRecord names WHY one signature left a union cell, so that a
// reader of the baseline file can tell which sentence belongs to which
// retired signature. The old flat-list shape held one free-text Note per
// WHOLE cell, which could not answer that question once more than one
// signature had ever been retired.
type RetirementRecord struct {
	// CHVersion is the ClickHouse version that the retiring run measured
	// against, taken from -i-measured-this. A retirement with no measured
	// version is a hand edit under another name.
	CHVersion string `json:"clickhouse_version"`

	// Reason is required text that names why the signature retired, for
	// example a tracking id. It answers the question a shared, one-per-cell Note
	// could not: which sentence explains which signature.
	Reason string `json:"reason"`
}

// BaselineCell is the accepted signature set of one oracle run.
type BaselineCell struct {
	Population PopulationIdentity `json:"population"`
	Seed       int64              `json:"seed"`
	N          int                `json:"generated_expressions"`

	// FixtureSignature is the shape of the fixture table that produced the
	// baseline. A run against a different fixture did not measure the same
	// population, thus the gate refuses instead of answering.
	FixtureSignature string `json:"fixture_signature"`
	FixtureHash      string `json:"fixture_hash"`

	// ServerRun and ServerUptimeS record the instance that produced the
	// baseline. They are for the reader and for the audit trail; the gate
	// never compares them, because a baseline is always from another run.
	ServerRun     int64 `json:"server_run"`
	ServerUptimeS int64 `json:"server_uptime_s"`

	// Mismatch holds the accepted MISMATCH signatures, sorted. There is no
	// standing Bool-against-UInt8 equivalence any more: the regression removed
	// it, because the earlier rewrite (the regression) hid a real defect. See
	// docs/decision-bool-vs-uint8.md for the full history.
	Mismatch []string `json:"mismatch_signatures"`

	// Blind43 holds the accepted CH_ERROR_43_CHGEN_TYPED signatures,
	// sorted. The signature is "kind: chgen=Type", derived from the
	// uncapped findings list of that class.
	Blind43 []string `json:"blind43_signatures"`
}

// baselineVersion is the shape number of the file that this program writes.
const baselineVersion = 3

// CellName is the key of one run inside a baseline.
func CellName(profileID string, seed int64, n int) string {
	return fmt.Sprintf("%s/seed-%d/n-%d", profileID, seed, n)
}

// MismatchSignatures returns the sorted MISMATCH signatures of a report.
//
// The keys of mismatch_signatures are already class labels of the shape
// "kind: chgen=X ch=Y", and that map is uncapped, thus the set it names is
// complete.
func MismatchSignatures(r *Report) []string {
	return sortedKeys(r.MismatchSig)
}

// Blind43Signatures returns the sorted CH_ERROR_43_CHGEN_TYPED signatures of a
// report, in the shape "kind: chgen=Type".
//
// It reads the FINDINGS list and not blind_code43_by_kind. Both are uncapped
// for this class, but the per-kind map loses the type that chgen gave, and
// that type is the part which separates one blindness from another inside one
// kind. The findings entries of this class are never truncated, so the set is
// complete. See the note on Findings in the oracle.
func Blind43Signatures(r *Report) []string {
	set := map[string]int{}
	for _, f := range r.Findings {
		if f.Class != "CH_ERROR_43_CHGEN_TYPED" {
			continue
		}
		set[fmt.Sprintf("%s: chgen=%s", f.Kind, f.ChgenType)]++
	}
	return sortedKeys(set)
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	if len(out) == 0 {
		// An empty, non-nil slice writes as [] and not as null. A
		// reviewer must see an empty set as an empty set.
		return []string{}
	}
	return out
}

// CellFromReport builds the baseline cell that a report proves.
func CellFromReport(r *Report) *BaselineCell {
	return &BaselineCell{
		Population:       r.Population,
		Seed:             r.Seed,
		N:                r.Generated,
		FixtureSignature: r.FixtureSignature,
		FixtureHash:      r.FixtureHash,
		ServerRun:        r.ServerRun,
		ServerUptimeS:    r.ServerUptimeS,
		Mismatch:         MismatchSignatures(r),
		Blind43:          Blind43Signatures(r),
	}
}

// LoadBaseline reads a baseline file.
func LoadBaseline(path string) (*Baseline, error) {
	return loadBaseline(path, false)
}

// LoadBaselineForUpdate reads a baseline that a union update will repair.
// It keeps all strict checks except the old union-to-cell population match.
func LoadBaselineForUpdate(path string) (*Baseline, error) {
	return loadBaseline(path, true)
}

func loadBaseline(path string, allowPopulationRepair bool) (*Baseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode baseline version %s: %w", path, err)
	}
	if envelope.Version == 1 || envelope.Version == 2 {
		return nil, fmt.Errorf("baseline %s has legacy version %d with grammar-based identity; run the current sampling profile and regenerate the baseline because an automatic profile assignment would be a guess", path, envelope.Version)
	}
	var b Baseline
	// Unknown keys are refused. A baseline written by a later, incompatible
	// version must fail loudly and not decode into a set of zero values,
	// which would read as "no accepted signature" and gate on nothing.
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return nil, fmt.Errorf("decode baseline %s: %w", path, err)
	}
	if b.Version != baselineVersion {
		return nil, fmt.Errorf("baseline %s has version %d, this program knows version %d",
			path, b.Version, baselineVersion)
	}
	if len(b.Cells) == 0 {
		return nil, fmt.Errorf("baseline %s holds no cell; an empty baseline gates on nothing", path)
	}
	var population PopulationIdentity
	var fixtureHash, fixtureShape string
	for name, cell := range b.Cells {
		if cell == nil || !cell.Population.valid() || name != CellName(cell.Population.ProfileID, cell.Seed, cell.N) {
			return nil, fmt.Errorf("baseline %s cell %q has a missing or mismatched population identity", path, name)
		}
		if cell.FixtureHash == "" || cell.FixtureSignature == "" || cell.ServerRun == 0 {
			return nil, fmt.Errorf("baseline %s cell %q has incomplete fixture or server identity", path, name)
		}
		if population.ProfileID == "" {
			population, fixtureHash, fixtureShape = cell.Population, cell.FixtureHash, cell.FixtureSignature
		} else if cell.Population != population || cell.FixtureHash != fixtureHash || cell.FixtureSignature != fixtureShape {
			return nil, fmt.Errorf("baseline %s has mixed sampling-profile or fixture identities", path)
		}
	}
	for name, cell := range b.UnionCells {
		if cell == nil || !cell.Population.valid() || name != cell.Population.ProfileID {
			return nil, fmt.Errorf("baseline %s union cell %q has a missing or mismatched population identity", path, name)
		}
		if cell.FixtureHash == "" || cell.FixtureSignature == "" || cell.ServerRun == 0 {
			return nil, fmt.Errorf("baseline %s union cell %q has incomplete fixture or server identity", path, name)
		}
		if !allowPopulationRepair &&
			(cell.Population != population || cell.FixtureHash != fixtureHash || cell.FixtureSignature != fixtureShape) {
			return nil, fmt.Errorf("baseline %s union cell %q does not match the baseline population and fixture", path, name)
		}
		for signature, record := range cell.Retired {
			if signature == "" || record == nil || record.CHVersion == "" || record.Reason == "" {
				return nil, fmt.Errorf("baseline %s union cell %q has an incomplete retirement record at %q", path, name, signature)
			}
			if containsString(cell.Mismatch, signature) || containsString(cell.Blind43, signature) {
				return nil, fmt.Errorf("baseline %s union cell %q has retired and accepted signature collision at %q", path, name, signature)
			}
		}
	}
	return &b, nil
}

// Marshal renders a baseline as the bytes that belong in the repository. The
// output is indented and every set is sorted, thus a git diff shows exactly
// WHICH signature appeared or went away, one per line.
func (b *Baseline) Marshal() ([]byte, error) {
	b.Version = baselineVersion
	for name, cell := range b.Cells {
		if cell.Mismatch == nil {
			cell.Mismatch = []string{}
		}
		if cell.Blind43 == nil {
			cell.Blind43 = []string{}
		}
		sort.Strings(cell.Mismatch)
		sort.Strings(cell.Blind43)
		b.Cells[name] = cell
	}
	for name, cell := range b.UnionCells {
		if cell.Mismatch == nil {
			cell.Mismatch = []string{}
		}
		if cell.Blind43 == nil {
			cell.Blind43 = []string{}
		}
		if cell.Seeds == nil {
			cell.Seeds = []int64{}
		}
		sort.Strings(cell.Mismatch)
		sort.Strings(cell.Blind43)
		sort.Slice(cell.Seeds, func(i, j int) bool { return cell.Seeds[i] < cell.Seeds[j] })
		b.UnionCells[name] = cell
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// GateResult is the verdict of one cell.
type GateResult struct {
	Cell string
	// New names the signatures that the run found and the baseline does
	// not hold. A non-empty New fails the gate.
	NewMismatch []string
	NewBlind43  []string
	// Gone names the signatures that the baseline holds and the run did
	// not find. It NEVER fails the gate: the class may be absent because
	// the sample did not reach it, and a gate that failed on an absence
	// would turn the noise of the server into a red build. It is reported
	// so that a person can retire a stale line deliberately.
	GoneMismatch []string
	GoneBlind43  []string
	// Volume records how much the gate actually read. A verdict with an
	// empty volume is an empty read and not a pass.
	RunMismatch      int
	RunBlind43       int
	BaselineMismatch int
	BaselineBlind43  int
}

// OK reports whether the cell passes the gate.
func (g *GateResult) OK() bool {
	return len(g.NewMismatch) == 0 && len(g.NewBlind43) == 0
}

// Gate checks one report against the cell of a baseline.
//
// It returns an error, and no result, when the gate cannot answer the question
// that the caller asked: an unknown cell, a different fixture, or a different
// ClickHouse version. Each of these means the run and the baseline did not
// measure the same population, and an answer would be a check that answers a
// question nobody asked.
func (b *Baseline) Gate(r *Report) (*GateResult, error) {
	if err := r.ValidateIdentity(); err != nil {
		return nil, err
	}
	if err := r.ValidateCoverage(); err != nil {
		return nil, err
	}
	name := CellName(r.Population.ProfileID, r.Seed, r.Generated)
	cell, ok := b.Cells[name]
	if !ok {
		return nil, fmt.Errorf(
			"the baseline holds no cell %q; the run measured a population that no committed "+
				"baseline covers. Add the cell with the regenerate command, or correct the "+
				"profile, seed and expression count of the run", name)
	}
	if cell.ServerRun == 0 {
		return nil, fmt.Errorf("baseline cell %q does not name a nonzero server run", name)
	}
	if b.CHVersion != r.CHVersion {
		return nil, fmt.Errorf(
			"the baseline was measured on ClickHouse %s and this run on %s. A version change "+
				"moves the measured answers, thus it must be a deliberate commit and not a "+
				"silent drift. Pin the server, or regenerate the baseline on purpose",
			b.CHVersion, r.CHVersion)
	}
	if cell.Population != r.Population {
		return nil, fmt.Errorf("different sampling profiles: baseline=%s/%s run=%s/%s", cell.Population.ProfileID, cell.Population.ProfileHash, r.Population.ProfileID, r.Population.ProfileHash)
	}
	if cell.FixtureHash != r.FixtureHash {
		return nil, fmt.Errorf("different fixture hashes: baseline=%s run=%s", cell.FixtureHash, r.FixtureHash)
	}
	if cell.FixtureSignature != r.FixtureSignature {
		return nil, fmt.Errorf(
			"different fixtures, thus the two runs did not measure the same population:\n"+
				"  baseline: %s\n  run:      %s",
			cell.FixtureSignature, r.FixtureSignature)
	}

	runMismatch := MismatchSignatures(r)
	runBlind43 := Blind43Signatures(r)
	if len(runMismatch) == 0 && len(runBlind43) == 0 && r.Generated == 0 {
		return nil, fmt.Errorf("the run report holds no generated expression; an empty read is not a pass")
	}

	res := &GateResult{
		Cell:             name,
		NewMismatch:      missing(runMismatch, cell.Mismatch),
		NewBlind43:       missing(runBlind43, cell.Blind43),
		GoneMismatch:     missing(cell.Mismatch, runMismatch),
		GoneBlind43:      missing(cell.Blind43, runBlind43),
		RunMismatch:      len(runMismatch),
		RunBlind43:       len(runBlind43),
		BaselineMismatch: len(cell.Mismatch),
		BaselineBlind43:  len(cell.Blind43),
	}
	return res, nil
}

// UnionGateResult is the verdict of one profile's union cell.
type UnionGateResult struct {
	ProfileID string
	Seeds     []int64
	// New names the signatures that the UNION of the runs found and the
	// baseline does not hold. A non-empty New fails the gate. A signature
	// found on only one seed of the set still lands here if the baseline
	// does not accept it: the union is exactly what stops a defect that
	// seed 42 finds from hiding behind a passing seed 1.
	NewMismatch []string
	NewBlind43  []string
	// Gone names signatures the baseline accepts that the union of THIS run
	// did not find on any seed of the set. It never fails the gate, for the
	// same reason GateResult.Gone does not: a seed set is still a sample,
	// and a class absent from every seed run this time is not proof that
	// the underlying case is fixed. It is reported so a person can retire a
	// stale line on purpose, the same discipline as the single-cell gate.
	GoneMismatch []string
	GoneBlind43  []string

	RunMismatch      int
	RunBlind43       int
	BaselineMismatch int
	BaselineBlind43  int
}

// OK reports whether the union cell passes the gate.
func (g *UnionGateResult) OK() bool {
	return len(g.NewMismatch) == 0 && len(g.NewBlind43) == 0
}

// GateUnion checks the UNION of several reports of one profile against the
// profile's union cell.
//
// reports must all name the same profile and must come from distinct seeds;
// GateUnion refuses otherwise, because a duplicated seed would silently
// shrink the population the gate believes it measured, and a mixed profile
// would union classes that the fixture never intended to compare together.
//
// GateUnion refuses, rather than answering, when the total population is
// empty: this is what keeps a misconfigured or empty seed set from passing
// by accident, because a run over zero reports (or reports that generated
// zero expressions) proves nothing about the profile's health, even though
// looping over zero reports would otherwise produce an empty New set and a
// silent PASS.
func (b *Baseline) GateUnion(reports []*Report) (*UnionGateResult, error) {
	if len(reports) == 0 {
		return nil, fmt.Errorf("REFUSED: no report supplied; a gate over zero reports would pass on an empty population, which is exactly the false PASS this gate exists to stop")
	}
	profileID := reports[0].Population.ProfileID
	cell, ok := b.UnionCells[profileID]
	if !ok {
		return nil, fmt.Errorf(
			"the baseline holds no union cell for profile %q; add it with -update-union once the seed set for that profile has been measured", profileID)
	}
	if cell.ServerRun == 0 || cell.FixtureHash == "" || cell.FixtureSignature == "" || !cell.Population.valid() || cell.Population.ProfileID != profileID {
		return nil, fmt.Errorf("the baseline union cell for profile %q has incomplete population, fixture, or server identity", profileID)
	}

	seenSeed := map[int64]bool{}
	seeds := make([]int64, 0, len(reports))
	mismatchSet := map[string]bool{}
	blind43Set := map[string]bool{}
	totalGenerated := 0
	var first *Report
	for _, r := range reports {
		if err := r.ValidateIdentity(); err != nil {
			return nil, fmt.Errorf("profile %q seed %d: %w", profileID, r.Seed, err)
		}
		if err := r.ValidateCoverage(); err != nil {
			return nil, fmt.Errorf("profile %q seed %d: %w", profileID, r.Seed, err)
		}
		if first == nil {
			first = r
		} else {
			if r.Population != first.Population {
				return nil, fmt.Errorf("mixed sampling profiles in profile %q union: seed %d has %s/%s, seed %d has %s/%s", profileID, first.Seed, first.Population.ProfileID, first.Population.ProfileHash, r.Seed, r.Population.ProfileID, r.Population.ProfileHash)
			}
			if r.FixtureHash != first.FixtureHash || r.FixtureSignature != first.FixtureSignature {
				return nil, fmt.Errorf("mixed fixtures in profile %q union between seeds %d and %d", profileID, first.Seed, r.Seed)
			}
			if r.CHVersion != first.CHVersion {
				return nil, fmt.Errorf("mixed ClickHouse versions in profile %q union: seed %d has %s, seed %d has %s", profileID, first.Seed, first.CHVersion, r.Seed, r.CHVersion)
			}
			if !sameServerRun(r.ServerRun, first.ServerRun) {
				return nil, fmt.Errorf("mixed server runs in profile %q union: seed %d has %d, seed %d has %d", profileID, first.Seed, first.ServerRun, r.Seed, r.ServerRun)
			}
		}
		if seenSeed[r.Seed] {
			return nil, fmt.Errorf(
				"seed %d appears more than once in the reports passed for profile %q; "+
					"the union gate needs one report per DISTINCT seed, or the population "+
					"is smaller than it looks", r.Seed, profileID)
		}
		seenSeed[r.Seed] = true
		seeds = append(seeds, r.Seed)
		totalGenerated += r.Generated
		if b.CHVersion != r.CHVersion {
			return nil, fmt.Errorf(
				"the baseline was measured on ClickHouse %s and the seed-%d run on %s. A "+
					"version change moves the measured answers, thus it must be a deliberate "+
					"commit and not a silent drift",
				b.CHVersion, r.Seed, r.CHVersion)
		}
		for _, s := range MismatchSignatures(r) {
			mismatchSet[s] = true
		}
		for _, s := range Blind43Signatures(r) {
			blind43Set[s] = true
		}
	}
	if cell.Population != first.Population {
		return nil, fmt.Errorf("different sampling profiles: baseline=%s/%s run=%s/%s", cell.Population.ProfileID, cell.Population.ProfileHash, first.Population.ProfileID, first.Population.ProfileHash)
	}
	if cell.FixtureHash != first.FixtureHash || cell.FixtureSignature != first.FixtureSignature {
		return nil, fmt.Errorf("the union report fixture does not match the baseline union fixture")
	}
	if totalGenerated == 0 {
		return nil, fmt.Errorf(
			"REFUSED: every report for profile %q generated zero expressions; an empty "+
				"population is not a measurement", profileID)
	}

	runMismatch := sortedSet(mismatchSet)
	runBlind43 := sortedSet(blind43Set)
	sort.Slice(seeds, func(i, j int) bool { return seeds[i] < seeds[j] })

	res := &UnionGateResult{
		ProfileID:        profileID,
		Seeds:            seeds,
		NewMismatch:      missing(runMismatch, cell.Mismatch),
		NewBlind43:       missing(runBlind43, cell.Blind43),
		GoneMismatch:     missing(cell.Mismatch, runMismatch),
		GoneBlind43:      missing(cell.Blind43, runBlind43),
		RunMismatch:      len(runMismatch),
		RunBlind43:       len(runBlind43),
		BaselineMismatch: len(cell.Mismatch),
		BaselineBlind43:  len(cell.Blind43),
	}
	return res, nil
}

// RetirementRequest names one signature to retire from a profile's union
// cell, together with the required reason.
type RetirementRequest struct {
	// ProfileID names the union cell.
	ProfileID string
	// Signature is the exact accepted signature to remove, for example
	// "v3-fn-equals: chgen=UInt8". It must match the string stored in the
	// cell's Mismatch or Blind43 list exactly; a near-miss is refused and
	// named, so a typo can never retire the wrong line or pass in silence.
	Signature string
	// Reason is required free text, for example a tracking id, that says WHY
	// this exact signature is retired. It is stored in the cell's Retired
	// map beside the signature, not blended into one shared cell-wide note.
	Reason string
}

// RetireUnion removes exactly the named signatures from their profiles'
// union cells, refusing the WHOLE batch if any single request cannot be
// honoured. reportsByProfile holds, for each profile that at least one
// request names, every report of the run that was measured for that
// profile; RetireUnion unions their signatures with the same derivation the
// gate itself uses, so requirement 1 below is checked the same way a normal
// gate run would see the signature.
//
// Two refusals hold the retirement contract:
//
//  1. A signature that the given reports STILL SHOW is not retirable.
//     Absence from a run is never sufficient on its own: the person
//     asserts the fix, and the tool refuses the assertion when the run
//     contradicts it.
//  2. A signature that is not in the baseline's union cell cannot be
//     retired: a typo in the signature string must not pass in silence.
//
// Every request needs Reason, and the whole call needs chVersion (the same
// -i-measured-this discipline as -update-union), so a retirement can never
// be the hand edit under another name: it always records who measured what,
// against which version, and why.
//
// RetireUnion mutates b in place and returns the sorted list of profiles it
// touched, for the caller to print. It never touches a Cells entry, a
// UnionCells entry that no request named, Seeds, ServerRun or CHVersion:
// only the Mismatch/Blind43 lists and the Retired map of the named cells
// change.
func (b *Baseline) RetireUnion(
	reportsByProfile map[string][]*Report,
	requests []RetirementRequest,
	chVersion string,
) ([]string, error) {
	if chVersion == "" {
		return nil, fmt.Errorf(
			"-retire-union needs -i-measured-this=<clickhouse-version>. A retirement records " +
				"the version that the person measured against, thus it must never happen " +
				"without naming it")
	}
	if len(requests) == 0 {
		return nil, fmt.Errorf("no -retire-union request passed; nothing to retire")
	}
	for _, req := range requests {
		if req.Reason == "" {
			return nil, fmt.Errorf(
				"retiring %q from profile %q needs a reason (a tracking id or a short sentence), "+
					"so a later reader can tell why this exact signature retired",
				req.Signature, req.ProfileID)
		}
	}

	// Requirement 2 first: refuse on a typo before touching anything.
	for _, req := range requests {
		cell, ok := b.UnionCells[req.ProfileID]
		if !ok {
			return nil, fmt.Errorf(
				"the baseline holds no union cell for profile %q; nothing to retire from",
				req.ProfileID)
		}
		if !containsString(cell.Mismatch, req.Signature) && !containsString(cell.Blind43, req.Signature) {
			return nil, fmt.Errorf(
				"profile %q: signature %q is not in the baseline's union cell; check for a "+
					"typo. A signature that was never accepted cannot be retired",
				req.ProfileID, req.Signature)
		}
	}

	// Requirement 1: refuse a signature the given run still shows, computed
	// per profile over the reports supplied for that profile.
	runSigByGrammar := map[string]map[string]bool{}
	for profileID, rs := range reportsByProfile {
		set := map[string]bool{}
		for _, r := range rs {
			for _, s := range MismatchSignatures(r) {
				set[s] = true
			}
			for _, s := range Blind43Signatures(r) {
				set[s] = true
			}
		}
		runSigByGrammar[profileID] = set
	}
	for _, req := range requests {
		if runSigByGrammar[req.ProfileID][req.Signature] {
			return nil, fmt.Errorf(
				"profile %q: signature %q is still present in the given reports; a signature "+
					"the run still shows is not retirable. Fix the defect first, then retire "+
					"once the reports no longer reproduce it",
				req.ProfileID, req.Signature)
		}
	}

	touched := map[string]bool{}
	for _, req := range requests {
		cell := b.UnionCells[req.ProfileID]
		cell.Mismatch = removeString(cell.Mismatch, req.Signature)
		cell.Blind43 = removeString(cell.Blind43, req.Signature)
		if cell.Retired == nil {
			cell.Retired = map[string]*RetirementRecord{}
		}
		cell.Retired[req.Signature] = &RetirementRecord{
			CHVersion: chVersion,
			Reason:    req.Reason,
		}
		touched[req.ProfileID] = true
	}

	out := make([]string, 0, len(touched))
	for gr := range touched {
		out = append(out, gr)
	}
	sort.Strings(out)
	return out, nil
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func removeString(xs []string, s string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// String renders the union verdict WITH the seed set and the volume that
// produced it, the same discipline as GateResult.String.
func (g *UnionGateResult) String() string {
	var sb strings.Builder
	if g.OK() {
		fmt.Fprintf(&sb, "PASS %s (seeds %v)\n", g.ProfileID, g.Seeds)
	} else {
		fmt.Fprintf(&sb, "FAIL %s (seeds %v, %d new signatures)\n",
			g.ProfileID, g.Seeds, len(g.NewMismatch)+len(g.NewBlind43))
	}
	fmt.Fprintf(&sb, "  run:      mismatch=%d blind43=%d signatures (union of seeds %v)\n",
		g.RunMismatch, g.RunBlind43, g.Seeds)
	fmt.Fprintf(&sb, "  baseline: mismatch=%d blind43=%d signatures\n", g.BaselineMismatch, g.BaselineBlind43)
	list := func(title string, xs []string) {
		if len(xs) == 0 {
			return
		}
		fmt.Fprintf(&sb, "  %s:\n", title)
		for _, s := range xs {
			fmt.Fprintf(&sb, "    %s\n", s)
		}
	}
	list("NEW MISMATCH signatures, not in the baseline", g.NewMismatch)
	list("NEW blindness signatures (chgen typed, server refused with code 43)", g.NewBlind43)
	list("in the baseline, absent from every seed of this run (does NOT fail; retire deliberately)", g.GoneMismatch)
	list("in the baseline, absent from every seed of this run (does NOT fail; retire deliberately)", g.GoneBlind43)
	return sb.String()
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

// String renders the verdict WITH the volume that produced it, for the same
// reason that Result.String does: "pass, 0 new" with nothing read is an empty
// read and not a pass.
func (g *GateResult) String() string {
	var sb strings.Builder
	if g.OK() {
		fmt.Fprintf(&sb, "PASS %s\n", g.Cell)
	} else {
		fmt.Fprintf(&sb, "FAIL %s (%d new signatures)\n",
			g.Cell, len(g.NewMismatch)+len(g.NewBlind43))
	}
	fmt.Fprintf(&sb, "  run:      mismatch=%d blind43=%d signatures\n", g.RunMismatch, g.RunBlind43)
	fmt.Fprintf(&sb, "  baseline: mismatch=%d blind43=%d signatures\n", g.BaselineMismatch, g.BaselineBlind43)
	list := func(title string, xs []string) {
		if len(xs) == 0 {
			return
		}
		fmt.Fprintf(&sb, "  %s:\n", title)
		for _, s := range xs {
			fmt.Fprintf(&sb, "    %s\n", s)
		}
	}
	list("NEW MISMATCH signatures, not in the baseline", g.NewMismatch)
	list("NEW blindness signatures (chgen typed, server refused with code 43)", g.NewBlind43)
	list("in the baseline, absent from this run (does NOT fail; retire deliberately)", g.GoneMismatch)
	list("in the baseline, absent from this run (does NOT fail; retire deliberately)", g.GoneBlind43)
	return sb.String()
}
