// Command oraclegate checks a type-oracle report against the committed
// baseline of accepted signatures, and regenerates that baseline on request.
//
// Usage:
//
//	go run ./internal/tooling/cmd/oraclegate -report report.json
//
//	go run ./internal/tooling/cmd/oraclegate -update -i-measured-this=25.8.29.51 \
//	    -report report-seed1.json -report report-seed7.json
//
//	go run ./internal/tooling/cmd/oraclegate -retire-union -i-measured-this=25.8.29.51 \
//	    -retire-union 'current-combined-v1:v3-fn-equals: chgen=UInt8=documented-reason' \
//	    -report report-seed1.json -report report-seed7.json \
//	    -report report-seed42.json -report report-seed99.json
//
// -retire-union removes exactly the named signatures from one profile's
// union cell, one at a time, and leaves every other accepted signature in
// that cell untouched. It refuses a signature the given -report reports
// still show, and it refuses a signature that the baseline's union cell
// does not hold. Absence from a run is never by itself a reason to retire:
// the person asserts the fix, names the ClickHouse version measured and a
// reason (typically a tracking id), and the tool refuses that assertion when
// the reports contradict it. See RetireUnion in internal/oraclereport for the
// full contract.
//
// It exits 0 when every report passes, 1 when a report shows a signature that
// the baseline does not hold, and 2 when the check cannot answer the question,
// for example when the ClickHouse version or the fixture differs.
//
// # Why this is not oraclediff
//
// oraclediff compares two reports and REFUSES a comparison across server runs,
// because a COUNT moves with the age of the server on one and the same commit.
// That refusal is correct and it stays. A committed baseline, however, is
// always from another server run than the run under test, thus oraclediff can
// never gate against one.
//
// This program asks the question that survives the change of instance: is the
// SET of signatures that the run found a subset of the SET that the baseline
// records? A signature is a class label, never a count. The counts inside a
// class move with the sample and with the server; a class that no run saw
// before is a new defect. Thus nothing here gates on a total.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/IlyaGulya/chgen/internal/oraclereport"
)

const (
	legacyBaselineSHA256     = "6de86c831a29f2599a18db02966dffd13bbf53a9d641e9b8fee8df1a36a29a05"
	legacyFixtureHash        = "402ea876388770c4f807938403e015359e8226665dca94a85e38949c4872e9ba"
	legacyFixtureShapeSHA256 = "8761344799be78f3ab1160a2107c9b4fe5d8cd38e7fc04ca42a970b1ed0c221c"
)

// retirements collects the repeated -retire-union arguments, each of the
// shape "<profile>:<signature>=<reason>".
//
// A signature string itself contains "=" (for example
// "v3-fn-equals: chgen=UInt8"), so the profile is cut off with ":" first,
// never used inside a signature or a profile name, and the reason is cut
// off with the LAST "=" in what remains, so a reason that itself contains
// "=" still parses, while a signature's own "=" never gets mistaken for the
// separator.
type retirements struct {
	profiles   []string
	signatures []string
	reasons    []string
}

func (r *retirements) String() string { return strings.Join(r.signatures, ",") }

func (r *retirements) Set(v string) error {
	profile, rest, ok := strings.Cut(v, ":")
	if !ok || rest == "" {
		return fmt.Errorf("want <profile>:<signature>=<reason>, got %q", v)
	}
	if profile != oraclereport.CurrentProfileID {
		return fmt.Errorf("unknown profile %q (use %s)", profile, oraclereport.CurrentProfileID)
	}
	eq := strings.LastIndex(rest, "=")
	if eq < 0 {
		return fmt.Errorf(
			"want <profile>:<signature>=<reason>, got %q; a reason (tracking id or short sentence) "+
				"is required so a later reader can tell why this signature retired", v)
	}
	signature, reason := rest[:eq], rest[eq+1:]
	if signature == "" || reason == "" {
		return fmt.Errorf("want <profile>:<signature>=<reason>, got %q (empty signature or reason)", v)
	}
	r.profiles = append(r.profiles, profile)
	r.signatures = append(r.signatures, signature)
	r.reasons = append(r.reasons, reason)
	return nil
}

// paths collects report paths. Each report owns its profile identity, so the
// command accepts no external identity label.
type paths []string

func (p *paths) String() string { return strings.Join(*p, ",") }

func (p *paths) Set(v string) error {
	if v == "" || strings.Contains(v, "=") {
		return fmt.Errorf("want a report path with no profile label, got %q", v)
	}
	*p = append(*p, v)
	return nil
}

func main() {
	var reportPaths paths
	baselinePath := flag.String("baseline", "testdata/oracle-baseline.json",
		"path of the committed baseline file")
	update := flag.Bool("update", false,
		"write the baseline from the reports instead of checking against it; "+
			"needs -i-measured-this as well")
	updateUnion := flag.Bool("update-union", false,
		"write the UNION cell (per profile, across every seed passed) instead of checking "+
			"against it; needs -i-measured-this as well. See -union below")
	union := flag.Bool("union", false,
		"gate the UNION of the reports of one profile against the profile's single union "+
			"cell, instead of gating each report against its own (profile, seed, n) cell. "+
			"Pass every seed of the fixed seed set with repeated -report flags naming the "+
			"SAME profile; the reports must come from distinct seeds")
	confirm := flag.String("i-measured-this", "",
		"with -update or -update-union: the ClickHouse version that the reports were measured "+
			"on. It must equal the version inside every report. This keeps the baseline from "+
			"being regenerated by accident")
	note := flag.String("note", "", "with -update or -update-union: free text for the reader of the baseline file")
	legacyBaseline := flag.String("migrate-legacy-retirements", "",
		"with -update-union: copy retirement audit from the one allowlisted grammar baseline path")
	var rt retirements
	flag.Var(&reportPaths, "report", "one oracle report path; repeat once per report")
	flag.Var(&rt, "retire-union", "retire one signature from a profile's union cell, as "+
		"<profile>:<signature>=<reason>; repeat once per signature. Needs -i-measured-this. "+
		"Refuses if the given -report reports still show the signature, or if the signature "+
		"is not in the baseline")
	flag.Parse()

	reports := []string(reportPaths)
	retiring := len(rt.signatures) > 0

	if flag.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "pass each report with -report <path>, not as a bare argument (%q)\n", flag.Arg(0))
		os.Exit(2)
	}
	// -retire-union may run with zero -report flags: an empty run trivially
	// satisfies "the named reports do not still show the signature",
	// because there is nothing to show. Every other mode still needs at
	// least one report.
	if len(reports) == 0 && !retiring {
		fmt.Fprintln(os.Stderr,
			"usage: oraclegate [-baseline path] [-update -i-measured-this <version>] "+
				"[-union | -update-union | -retire-union] "+
				"-report report-seed1.json [-report report-seed7.json ...]")
		os.Exit(2)
	}
	modes := 0
	for _, on := range []bool{*update, *updateUnion, retiring} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		fmt.Fprintln(os.Stderr, "usage: pass at most one of -update, -update-union and -retire-union")
		os.Exit(2)
	}
	if *legacyBaseline != "" && !*updateUnion {
		fmt.Fprintln(os.Stderr, "usage: -migrate-legacy-retirements needs -update-union")
		os.Exit(2)
	}

	loaded := make([]*oraclereport.Report, len(reports))
	for i, path := range reports {
		r, err := oraclereport.Load(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", path, err)
			os.Exit(2)
		}
		if r.Generated == 0 {
			fmt.Fprintf(os.Stderr,
				"REFUSED: %s reports no generated expression; an empty read is not a measurement\n",
				path)
			os.Exit(2)
		}
		loaded[i] = r
	}

	if *update {
		if err := writeBaseline(*baselinePath, *confirm, *note, reports, loaded); err != nil {
			fmt.Fprintf(os.Stderr, "REFUSED: %v\n", err)
			os.Exit(2)
		}
		return
	}

	if *updateUnion {
		if err := writeUnionBaseline(*baselinePath, *confirm, *note, *legacyBaseline, reports, loaded); err != nil {
			fmt.Fprintf(os.Stderr, "REFUSED: %v\n", err)
			os.Exit(2)
		}
		return
	}

	if retiring {
		if err := retireUnion(*baselinePath, *confirm, loaded, rt); err != nil {
			fmt.Fprintf(os.Stderr, "REFUSED: %v\n", err)
			os.Exit(2)
		}
		return
	}

	baseline, err := oraclereport.LoadBaseline(*baselinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "REFUSED: %v\n", err)
		os.Exit(2)
	}

	if *union {
		res, err := baseline.GateUnion(loaded)
		if err != nil {
			fmt.Fprintf(os.Stderr, "REFUSED: %v\n", err)
			os.Exit(2)
		}
		fmt.Print(res)
		if !res.OK() {
			fmt.Fprintln(os.Stderr,
				"\nThe union of the seed set found a signature that the baseline does not hold. "+
					"Either the change under review made chgen answer where it must refuse, or the "+
					"signature is understood and accepted. Read the class, then regenerate the "+
					"union baseline on purpose with -update-union.")
			os.Exit(1)
		}
		return
	}

	failed := false
	for i, r := range loaded {
		res, err := baseline.Gate(r)
		if err != nil {
			fmt.Fprintf(os.Stderr, "REFUSED for %s: %v\n", reports[i], err)
			os.Exit(2)
		}
		fmt.Print(res)
		if !res.OK() {
			failed = true
		}
	}
	if failed {
		fmt.Fprintln(os.Stderr,
			"\nThe oracle found a signature that the baseline does not hold. Either the change "+
				"under review made chgen answer where it must refuse, or the signature is "+
				"understood and accepted. Read the class, then regenerate the baseline on "+
				"purpose with -update.")
		os.Exit(1)
	}
}

// writeBaseline builds the baseline file from the reports.
//
// It refuses unless the caller repeats the ClickHouse version that the reports
// carry. The flag is not a yes-or-no switch on purpose: a person who
// regenerates a baseline must name the thing that they measured, and a person
// who ran the command by mistake cannot name it.
func writeBaseline(path, confirm, note string, names []string, reports []*oraclereport.Report) error {
	if confirm == "" {
		return fmt.Errorf(
			"-update needs -i-measured-this=<clickhouse-version>. The baseline records the " +
				"measured answers of one pinned server, thus it must never be regenerated by " +
				"a command that a person ran by mistake")
	}
	chVersion := reports[0].CHVersion
	for i, r := range reports {
		if err := r.ValidateIdentity(); err != nil {
			return fmt.Errorf("%s: %w", names[i], err)
		}
		if err := r.ValidateCoverage(); err != nil {
			return fmt.Errorf("%s: %w", names[i], err)
		}
		if r.CHVersion != chVersion {
			return fmt.Errorf(
				"the reports do not agree on the server version: %s says %q, %s says %q. "+
					"One baseline names ONE pinned server",
				names[0], chVersion, names[i], r.CHVersion)
		}
		if !oraclereport.SameServerRun(r.ServerRun, reports[0].ServerRun) {
			return fmt.Errorf("the reports do not share one server run: %s says %d, %s says %d", names[0], reports[0].ServerRun, names[i], r.ServerRun)
		}
		if r.Population != reports[0].Population || r.FixtureHash != reports[0].FixtureHash || r.FixtureSignature != reports[0].FixtureSignature {
			return fmt.Errorf("the reports do not share one sampling profile and fixture: %s and %s differ", names[0], names[i])
		}
	}
	if confirm != chVersion {
		return fmt.Errorf(
			"-i-measured-this=%q does not equal the ClickHouse version inside the reports (%q)",
			confirm, chVersion)
	}

	b := &oraclereport.Baseline{
		CHVersion: chVersion,
		Note:      note,
		Cells:     map[string]*oraclereport.BaselineCell{},
	}
	for _, r := range reports {
		cell := oraclereport.CellFromReport(r)
		name := oraclereport.CellName(r.Population.ProfileID, r.Seed, r.Generated)
		if _, seen := b.Cells[name]; seen {
			return fmt.Errorf("two reports name the same cell %q; each cell must come from one run", name)
		}
		b.Cells[name] = cell
	}
	data, err := b.Marshal()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s: ClickHouse %s, %d cells\n", path, chVersion, len(b.Cells))
	for _, name := range sortedCellNames(b) {
		c := b.Cells[name]
		fmt.Printf("  %s: mismatch=%d blind43=%d (server_run=%d, uptime %ds)\n",
			name, len(c.Mismatch), len(c.Blind43), c.ServerRun, c.ServerUptimeS)
	}
	return nil
}

// writeUnionBaseline builds the union cells of the baseline from the reports,
// one cell per profile, folding every seed of that profile into one accepted
// signature set. It leaves the per-(profile,seed,n) Cells untouched, so the
// two gate shapes can coexist in one file.
//
// It carries the same refusal discipline as writeBaseline: -i-measured-this
// must equal the ClickHouse version inside every report, and reports of one
// profile must not repeat a seed, or the union would silently drop a sample.
func writeUnionBaseline(path, confirm, note, legacyBaselinePath string, names []string, reports []*oraclereport.Report) error {
	if confirm == "" {
		return fmt.Errorf(
			"-update-union needs -i-measured-this=<clickhouse-version>. The baseline records " +
				"the measured answers of one pinned server, thus it must never be regenerated " +
				"by a command that a person ran by mistake")
	}
	if len(reports) == 0 {
		return fmt.Errorf("no report passed; nothing to union")
	}
	chVersion := reports[0].CHVersion
	for i, r := range reports {
		if err := r.ValidateIdentity(); err != nil {
			return fmt.Errorf("%s: %w", names[i], err)
		}
		if err := r.ValidateCoverage(); err != nil {
			return fmt.Errorf("%s: %w", names[i], err)
		}
		if r.CHVersion != chVersion {
			return fmt.Errorf(
				"the reports do not agree on the server version: %s says %q, %s says %q. "+
					"One baseline names ONE pinned server",
				names[0], chVersion, names[i], r.CHVersion)
		}
		if !oraclereport.SameServerRun(r.ServerRun, reports[0].ServerRun) {
			return fmt.Errorf("the reports do not share one server run: %s says %d, %s says %d", names[0], reports[0].ServerRun, names[i], r.ServerRun)
		}
	}
	if confirm != chVersion {
		return fmt.Errorf(
			"-i-measured-this=%q does not equal the ClickHouse version inside the reports (%q)",
			confirm, chVersion)
	}

	byProfile := map[string][]*oraclereport.Report{}
	order := []string{}
	for _, r := range reports {
		profileID := r.Population.ProfileID
		if _, seen := byProfile[profileID]; !seen {
			order = append(order, profileID)
		}
		byProfile[profileID] = append(byProfile[profileID], r)
	}

	baseline, err := oraclereport.LoadBaselineForUpdate(path)
	if err != nil {
		return fmt.Errorf("load existing baseline %s (union cells are added to it, not written alone): %w", path, err)
	}
	if baseline.CHVersion != chVersion {
		return fmt.Errorf(
			"the existing baseline %s was measured on ClickHouse %s but these reports are on "+
				"%s; regenerate the whole baseline with -update first so both cell shapes name "+
				"one server", path, baseline.CHVersion, chVersion)
	}
	if baseline.UnionCells == nil {
		baseline.UnionCells = map[string]*oraclereport.UnionBaselineCell{}
	}
	if note != "" {
		baseline.Note = note
	}
	updatedProfiles := make(map[string]struct{}, len(byProfile))
	for profileID := range byProfile {
		updatedProfiles[profileID] = struct{}{}
	}
	for name, cell := range baseline.Cells {
		if _, updated := updatedProfiles[cell.Population.ProfileID]; updated {
			delete(baseline.Cells, name)
		}
	}
	for _, report := range reports {
		name := oraclereport.CellName(report.Population.ProfileID, report.Seed, report.Generated)
		if _, duplicate := baseline.Cells[name]; duplicate {
			return fmt.Errorf("two reports name the same baseline cell %q", name)
		}
		baseline.Cells[name] = oraclereport.CellFromReport(report)
	}

	for _, profileID := range order {
		rs := byProfile[profileID]
		seen := map[int64]bool{}
		seeds := make([]int64, 0, len(rs))
		mismatchSet := map[string]bool{}
		blind43Set := map[string]bool{}
		var serverRun int64
		var population oraclereport.PopulationIdentity
		var fixtureHash, fixtureSignature string
		for _, r := range rs {
			if seen[r.Seed] {
				return fmt.Errorf("profile %q: seed %d appears more than once among the reports passed", profileID, r.Seed)
			}
			seen[r.Seed] = true
			seeds = append(seeds, r.Seed)
			serverRun = r.ServerRun
			if population.ProfileID == "" {
				population = r.Population
				fixtureHash = r.FixtureHash
				fixtureSignature = r.FixtureSignature
			} else {
				if population != r.Population {
					return fmt.Errorf("profile %q reports have mixed population identities", profileID)
				}
				if fixtureHash != r.FixtureHash || fixtureSignature != r.FixtureSignature {
					return fmt.Errorf("profile %q reports have mixed fixtures", profileID)
				}
			}
			for _, s := range oraclereport.MismatchSignatures(r) {
				mismatchSet[s] = true
			}
			for _, s := range oraclereport.Blind43Signatures(r) {
				blind43Set[s] = true
			}
		}
		// A fixed seed set is still a sample. If a new sampling plan does not
		// draw an accepted signature, that absence does not show that the defect
		// is fixed. Keep the old accepted set when the fixture is unchanged.
		// The retirement command is the only command that can remove one accepted
		// signature, and it needs a measured reason.
		existing := baseline.UnionCells[profileID]
		if existing != nil {
			if existing.FixtureHash != fixtureHash || existing.FixtureSignature != fixtureSignature {
				return fmt.Errorf(
					"profile %q fixture changed; update-union cannot preserve the old accepted signature set across fixtures", profileID)
			}
			for _, signature := range existing.Mismatch {
				mismatchSet[signature] = true
			}
			for _, signature := range existing.Blind43 {
				blind43Set[signature] = true
			}
		}
		retired := map[string]*oraclereport.RetirementRecord{}
		if existing != nil {
			for signature, record := range existing.Retired {
				if mismatchSet[signature] || blind43Set[signature] {
					return fmt.Errorf("retirement audit collides with a current accepted signature at %q", signature)
				}
				copyRecord := *record
				retired[signature] = &copyRecord
			}
		}
		if len(retired) == 0 {
			retired = nil
		}
		baseline.UnionCells[profileID] = &oraclereport.UnionBaselineCell{
			Population:       population,
			FixtureHash:      fixtureHash,
			FixtureSignature: fixtureSignature,
			Seeds:            seeds,
			ServerRun:        serverRun,
			Mismatch:         setToSortedSlice(mismatchSet),
			Blind43:          setToSortedSlice(blind43Set),
			Retired:          retired,
		}
	}
	if legacyBaselinePath != "" {
		data, readErr := os.ReadFile(legacyBaselinePath)
		if readErr != nil {
			return fmt.Errorf("read legacy baseline: %w", readErr)
		}
		retired, convertErr := oraclereport.ConvertLegacyBaselineRetirements(data, oraclereport.LegacyBaselineConversionSpec{
			ArtifactSHA256:     legacyBaselineSHA256,
			SourceCell:         "v3",
			FixtureHash:        legacyFixtureHash,
			FixtureShapeSHA256: legacyFixtureShapeSHA256,
			PrefixMappings:     []oraclereport.PrefixMapping{{From: "v3-", To: "v3-"}},
		})
		if convertErr != nil {
			return convertErr
		}
		cell := baseline.UnionCells[oraclereport.CurrentProfileID]
		for signature, record := range retired {
			if contains(cell.Mismatch, signature) || contains(cell.Blind43, signature) {
				return fmt.Errorf("legacy retired signature %q collides with a current accepted signature", signature)
			}
			if cell.Retired == nil {
				cell.Retired = map[string]*oraclereport.RetirementRecord{}
			}
			if _, exists := cell.Retired[signature]; exists {
				return fmt.Errorf("legacy retirement audit collides at %q", signature)
			}
			cell.Retired[signature] = record
		}
	}

	data, err := baseline.Marshal()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s: ClickHouse %s, %d union cells\n", path, chVersion, len(baseline.UnionCells))
	for _, profileID := range order {
		c := baseline.UnionCells[profileID]
		fmt.Printf("  %s: seeds=%v mismatch=%d blind43=%d\n", profileID, c.Seeds, len(c.Mismatch), len(c.Blind43))
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// retireUnion loads the baseline, retires the named signatures from their
// union cells, and writes the file back.
//
// It groups the given reports by profile, exactly like writeUnionBaseline,
// so oraclereport.Baseline.RetireUnion can refuse a retirement whose
// signature still shows up in the reports named for that profile. It is
// legal to pass zero reports for a profile (for example, retire-union with
// no -report flags at all): an empty run cannot show a signature, so it
// cannot by itself block a retirement, and requirement 1 is instead
// enforced by RetireUnion's own refusal whenever a NAMED report DOES still
// show the signature.
func retireUnion(path, confirm string, reports []*oraclereport.Report, rt retirements) error {
	if confirm == "" {
		return fmt.Errorf(
			"-retire-union needs -i-measured-this=<clickhouse-version>. A retirement records " +
				"the version that the person measured against, thus it must never happen " +
				"without naming it")
	}

	byProfile := map[string][]*oraclereport.Report{}
	for _, r := range reports {
		profileID := r.Population.ProfileID
		byProfile[profileID] = append(byProfile[profileID], r)
	}
	// Every report passed must agree with -i-measured-this, the same
	// discipline as -update-union: a retirement measured against a report
	// from a different server would record the wrong version.
	for _, r := range reports {
		if r.CHVersion != confirm {
			return fmt.Errorf(
				"-i-measured-this=%q does not equal the ClickHouse version inside a report (%q)",
				confirm, r.CHVersion)
		}
	}

	baseline, err := oraclereport.LoadBaseline(path)
	if err != nil {
		return fmt.Errorf("load existing baseline %s: %w", path, err)
	}

	requests := make([]oraclereport.RetirementRequest, len(rt.profiles))
	for i := range rt.profiles {
		requests[i] = oraclereport.RetirementRequest{
			ProfileID: rt.profiles[i],
			Signature: rt.signatures[i],
			Reason:    rt.reasons[i],
		}
	}

	touched, err := baseline.RetireUnion(byProfile, requests, confirm)
	if err != nil {
		return err
	}

	data, err := baseline.Marshal()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("retired %d signature(s) from %s\n", len(requests), path)
	for _, gr := range touched {
		c := baseline.UnionCells[gr]
		fmt.Printf("  %s: mismatch=%d blind43=%d retired=%d\n", gr, len(c.Mismatch), len(c.Blind43), len(c.Retired))
	}
	return nil
}

func setToSortedSlice(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func sortedCellNames(b *oraclereport.Baseline) []string {
	out := make([]string, 0, len(b.Cells))
	for k := range b.Cells {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
