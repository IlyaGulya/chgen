package typeboundary

import (
	"slices"
	"testing"

	"github.com/IlyaGulya/chgen/internal/conformance"
)

func testBoundaryArtifacts() (*Artifact, *Artifact) {
	makeArtifact := func(version string, pinned bool) *Artifact {
		artifact := &Artifact{
			Version:     ArtifactVersion,
			Server:      ServerInfo{Version: version, ServerRun: 1, UptimeS: 2},
			FixtureHash: "fixture", SeedHash: "seed",
			Cells: make(map[string]Verdict),
		}
		var inputs []conformance.Input
		for _, witness := range knownVersionBoundary {
			analysis := witness.candidateAnalysis
			verdict := witness.candidateVerdict
			execution := conformance.ExecutionResult{Ran: true}
			if pinned {
				analysis = witness.pinnedAnalysis
				verdict = witness.pinnedVerdict
			} else if witness.candidateExecCode != 0 {
				execution = conformance.ExecutionResult{Error: "server refusal", ErrorCode: witness.candidateExecCode}
			}
			artifact.CatalogNames = append(artifact.CatalogNames, witness.id)
			artifact.Cells[witness.id] = verdict
			artifact.Conformance = append(artifact.Conformance, conformance.Cell{
				ID: witness.id, Expression: witness.expression, Table: "chgen_probe_t",
				Chgen:     conformance.TypeResult{Error: "not part of this contract"},
				Analysis:  conformance.TypeResult{Raw: analysis, Canonical: &conformance.Type{Name: "test"}},
				Execution: execution,
			})
			inputs = append(inputs, conformance.Input{ID: witness.id, Expression: witness.expression, Table: "chgen_probe_t"})
		}
		slices.Sort(artifact.CatalogNames)
		artifact.MatrixHash = conformance.MatrixHash(inputs)
		return artifact
	}
	return makeArtifact(PinnedBoundaryVersion, true), makeArtifact(CandidateBoundaryVersion, false)
}

func TestVerifyKnownVersionBoundary(t *testing.T) {
	pinned, candidate := testBoundaryArtifacts()
	if err := VerifyKnownVersionBoundary(pinned, candidate); err != nil {
		t.Fatalf("valid boundary: %v", err)
	}
}

func TestKnownVersionBoundaryCatalogCellsStayExact(t *testing.T) {
	catalog := make(map[string]string, len(Catalog))
	for _, cell := range Catalog {
		catalog[cell.Name] = cell.Query
	}
	for _, witness := range knownVersionBoundary {
		if got, ok := catalog[witness.id]; !ok || got != witness.expression {
			t.Errorf("catalog cell %q is %q, want %q", witness.id, got, witness.expression)
		}
	}
}

func TestHistoricalBoundaryCatalogCellsStayExact(t *testing.T) {
	want := map[string]string{
		"v24_8_greatest_decimal": "greatest(dec, dec)",
		"v24_8_least_decimal":    "least(dec, dec)",
		"v24_8_cityhash64_array": "cityHash64(arr_i32)",
	}
	for _, cell := range Catalog {
		if expression, ok := want[cell.Name]; ok {
			if cell.Query != expression {
				t.Errorf("historical cell %q is %q, want %q", cell.Name, cell.Query, expression)
			}
			delete(want, cell.Name)
		}
	}
	for name := range want {
		t.Errorf("historical catalog cell %q is absent", name)
	}
}

func TestVerifyKnownVersionBoundaryRejectsMutations(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*Artifact, *Artifact)
	}{
		{"missing family", func(_ *Artifact, candidate *Artifact) { delete(candidate.Cells, "v24_8_greatest_safn_i32") }},
		{"repointed cell", func(_ *Artifact, candidate *Artifact) { candidate.Conformance[0].Expression = "toString(fs)" }},
		{"unexecuted cell", func(pinned *Artifact, _ *Artifact) { pinned.Conformance[0].Execution = conformance.ExecutionResult{} }},
		{"wrong refusal", func(_ *Artifact, candidate *Artifact) {
			candidate.Cells["v24_8_trim_fixedstring"] = Verdict{ErrorCode: 44}
		}},
		{"wrong server", func(_ *Artifact, candidate *Artifact) { candidate.Server.Version = PinnedBoundaryVersion }},
		{"different fixture", func(_ *Artifact, candidate *Artifact) { candidate.FixtureHash = "other" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			pinned, candidate := testBoundaryArtifacts()
			mutation.mutate(pinned, candidate)
			if err := VerifyKnownVersionBoundary(pinned, candidate); err == nil {
				t.Fatal("mutation did not make the boundary gate refuse")
			}
		})
	}
}
