package conformance

import "testing"

func TestSetOperationMatrixMutationsFailClosed(t *testing.T) {
	artifact := CurrentSetOperationArtifact("25.8.29.51")
	if err := ValidateSetOperationArtifact(artifact); err != nil {
		t.Fatal(err)
	}
	for remove := range artifact.Cells {
		mutated := artifact
		mutated.Cells = append([]SetOperationCell(nil), artifact.Cells[:remove]...)
		mutated.Cells = append(mutated.Cells, artifact.Cells[remove+1:]...)
		if err := ValidateSetOperationArtifact(mutated); err == nil {
			t.Errorf("deletion of %s passed", artifact.Cells[remove].ID)
		}
	}
	mutations := []func(*SetOperationArtifact){
		func(value *SetOperationArtifact) { value.Cells[0].ID = value.Cells[1].ID },
		func(value *SetOperationArtifact) { value.Cells[0].Query = "" },
		func(value *SetOperationArtifact) { value.Cells[0].Query += " LIMIT 1" },
		func(value *SetOperationArtifact) { value.Cells[0].Chgen = "maybe" },
		func(value *SetOperationArtifact) { value.Cells[0].Results = nil },
		func(value *SetOperationArtifact) { value.Cells[5].ChgenError = "" },
		func(value *SetOperationArtifact) { value.Cells[5].AnalysisCode = 0 },
		func(value *SetOperationArtifact) { value.Cells[5].ExecutionCode = 0 },
		func(value *SetOperationArtifact) { value.Cells[0].ValueProbe = "" },
		func(value *SetOperationArtifact) { value.Cells[len(value.Cells)-1].Owner = "" },
		func(value *SetOperationArtifact) { value.Cells[len(value.Cells)-1].Owner = "chgen-mutated" },
	}
	for index, mutate := range mutations {
		mutated := artifact
		mutated.Cells = append([]SetOperationCell(nil), artifact.Cells...)
		mutate(&mutated)
		if err := ValidateSetOperationArtifact(mutated); err == nil {
			t.Errorf("mutation %d passed", index)
		}
	}
}
