package conformance

import "testing"

func TestFinalMatrixMutationsFailClosed(t *testing.T) {
	artifact := CurrentFinalArtifact("25.8.29.51")
	if err := ValidateFinalArtifact(artifact); err != nil {
		t.Fatal(err)
	}
	for remove := range artifact.Cells {
		mutated := artifact
		mutated.Cells = append([]FinalCell(nil), artifact.Cells[:remove]...)
		mutated.Cells = append(mutated.Cells, artifact.Cells[remove+1:]...)
		if err := ValidateFinalArtifact(mutated); err == nil {
			t.Errorf("deletion of %s passed", artifact.Cells[remove].ID)
		}
	}
	mutations := []func(*FinalArtifact){
		func(value *FinalArtifact) { value.Cells[0].ID = value.Cells[1].ID },
		func(value *FinalArtifact) { value.Cells[0].Query = "" },
		func(value *FinalArtifact) { value.Cells[0].Query += " LIMIT 1" },
		func(value *FinalArtifact) { value.Cells[0].Chgen = "maybe" },
		func(value *FinalArtifact) { value.Cells[0].Results = nil },
		func(value *FinalArtifact) { value.Cells[4].ChgenError = "" },
		func(value *FinalArtifact) { value.Cells[4].AnalysisCode = 0 },
		func(value *FinalArtifact) { value.Cells[4].ExecutionCode = 0 },
		func(value *FinalArtifact) { value.Cells[0].ValueProbe = "" },
	}
	for index, mutate := range mutations {
		mutated := artifact
		mutated.Cells = append([]FinalCell(nil), artifact.Cells...)
		mutate(&mutated)
		if err := ValidateFinalArtifact(mutated); err == nil {
			t.Errorf("mutation %d passed", index)
		}
	}
}
