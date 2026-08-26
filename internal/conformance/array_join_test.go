package conformance

import "testing"

func TestArrayJoinMatrixMutationsFailClosed(t *testing.T) {
	artifact := CurrentArrayJoinArtifact("25.8.29.51")
	if err := ValidateArrayJoinArtifact(artifact); err != nil {
		t.Fatal(err)
	}
	for remove := range artifact.Cells {
		mutated := artifact
		mutated.Cells = append([]ArrayJoinCell(nil), artifact.Cells[:remove]...)
		mutated.Cells = append(mutated.Cells, artifact.Cells[remove+1:]...)
		if err := ValidateArrayJoinArtifact(mutated); err == nil {
			t.Errorf("deletion of %s passed", artifact.Cells[remove].ID)
		}
	}
	mutations := []func(*ArrayJoinArtifact){
		func(value *ArrayJoinArtifact) { value.Cells[0].ID = value.Cells[1].ID },
		func(value *ArrayJoinArtifact) { value.Cells[0].Query = "" },
		func(value *ArrayJoinArtifact) { value.Cells[0].Query += " LIMIT 1" },
		func(value *ArrayJoinArtifact) { value.Cells[0].Chgen = "maybe" },
		func(value *ArrayJoinArtifact) { value.Cells[0].Results = nil },
		func(value *ArrayJoinArtifact) { value.Cells[17].ChgenError = "" },
		func(value *ArrayJoinArtifact) { value.Cells[17].AnalysisCode = 0 },
		func(value *ArrayJoinArtifact) { value.Cells[17].ExecutionCode = 0 },
		func(value *ArrayJoinArtifact) { value.Cells[0].ValueProbe = "" },
		func(value *ArrayJoinArtifact) { value.Cells[36].ChgenError = "" },
		func(value *ArrayJoinArtifact) {
			value.Cells[38].Results = []ArrayJoinResult{{Name: "values", Type: "Int32"}, {Name: "b", Type: "Int32"}}
		},
		func(value *ArrayJoinArtifact) { value.Cells[38].ExecutionCode = 0 },
		func(value *ArrayJoinArtifact) { value.Cells[39].ValueProbe = "" },
	}
	for index, mutate := range mutations {
		mutated := artifact
		mutated.Cells = append([]ArrayJoinCell(nil), artifact.Cells...)
		mutate(&mutated)
		if err := ValidateArrayJoinArtifact(mutated); err == nil {
			t.Errorf("mutation %d passed", index)
		}
	}
}
