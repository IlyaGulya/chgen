package conformance

import "testing"

func TestNestedScopeMatrixHasAllMeasuredAxes(t *testing.T) {
	if err := ValidateNestedScopeArtifact(CurrentNestedScopeArtifact("25.8.29.51")); err != nil {
		t.Fatal(err)
	}
}

func TestNestedScopeMatrixMutationsFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NestedScopeArtifact)
	}{
		{name: "duplicate", mutate: func(artifact *NestedScopeArtifact) { artifact.Cells[1].ID = artifact.Cells[0].ID }},
		{name: "query", mutate: func(artifact *NestedScopeArtifact) { artifact.Cells[0].Query = "" }},
		{name: "chgen lane", mutate: func(artifact *NestedScopeArtifact) { artifact.Cells[0].Chgen = "maybe" }},
		{name: "chgen type", mutate: func(artifact *NestedScopeArtifact) { artifact.Cells[0].ChgenType = "" }},
		{name: "chgen error", mutate: func(artifact *NestedScopeArtifact) { artifact.Cells[3].ChgenError = "" }},
		{name: "analysis type", mutate: func(artifact *NestedScopeArtifact) { artifact.Cells[0].AnalysisType = "" }},
		{name: "result name", mutate: func(artifact *NestedScopeArtifact) { artifact.Cells[0].ResultName = "" }},
		{name: "analysis code", mutate: func(artifact *NestedScopeArtifact) { artifact.Cells[3].AnalysisCode = 0 }},
		{name: "execution code", mutate: func(artifact *NestedScopeArtifact) { artifact.Cells[3].ExecutionCode = 0 }},
		{name: "owner", mutate: func(artifact *NestedScopeArtifact) { artifact.Cells[4].Owner = "" }},
		{name: "unexpected owner", mutate: func(artifact *NestedScopeArtifact) { artifact.Cells[0].Owner = "nested-scope-known-gap" }},
		{name: "probe", mutate: func(artifact *NestedScopeArtifact) { artifact.Cells[0].ValueProbe = "" }},
		{name: "probe duplicate", mutate: func(artifact *NestedScopeArtifact) { artifact.Cells[4].ValueProbe = artifact.Cells[0].ValueProbe }},
		{name: "form", mutate: func(artifact *NestedScopeArtifact) {
			for index := range artifact.Cells {
				if artifact.Cells[index].Form == "exists" {
					artifact.Cells[index].Form = "scalar"
				}
			}
		}},
		{name: "binding", mutate: func(artifact *NestedScopeArtifact) {
			for index := range artifact.Cells {
				if artifact.Cells[index].Binding == "shadow" {
					artifact.Cells[index].Binding = "uncorrelated"
				}
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artifact := CurrentNestedScopeArtifact("25.8.29.51")
			artifact.Cells = append([]NestedScopeCell(nil), artifact.Cells...)
			test.mutate(&artifact)
			if err := ValidateNestedScopeArtifact(artifact); err == nil {
				t.Fatal("nested scope mutation passed")
			}
		})
	}
}

func TestNestedScopeKnownGapRosterIsExact(t *testing.T) {
	artifact := CurrentNestedScopeArtifact("25.8.29.51")
	owned := make(map[string]int)
	for _, cell := range artifact.Cells {
		if cell.Owner == "" {
			continue
		}
		owned[cell.Owner]++
	}
	if owned["nested-scope-known-gap"] != 4 || len(owned) != 1 {
		t.Fatalf("owned nested scope cells = %#v", owned)
	}
}

func TestEveryNestedScopeCellDeletionFails(t *testing.T) {
	artifact := CurrentNestedScopeArtifact("25.8.29.51")
	for remove := range artifact.Cells {
		mutated := artifact
		mutated.Cells = append([]NestedScopeCell(nil), artifact.Cells[:remove]...)
		mutated.Cells = append(mutated.Cells, artifact.Cells[remove+1:]...)
		if err := ValidateNestedScopeArtifact(mutated); err == nil {
			t.Errorf("deletion of %s passed", artifact.Cells[remove].ID)
		}
	}
}
