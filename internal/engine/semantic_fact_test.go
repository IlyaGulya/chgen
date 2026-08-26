package engine

import (
	"strings"
	"testing"
)

func TestFunctionFactsAndEvidenceDoNotDrift(t *testing.T) {
	for _, problem := range auditFunctionFactEvidence(functionRegistry, functionFactEvidenceCatalog) {
		t.Error(problem)
	}
}

func TestFunctionFactEvidenceAuditSelfTest(t *testing.T) {
	registry := map[string]functionSpec{
		"measuredwithoutmark": {},
		"markedwithoutevidence": {
			facts: functionFactComparesArgPair,
		},
	}
	catalog := []functionFactEvidence{
		{
			fact:      functionFactComparesArgPair,
			name:      "test fact",
			evidence:  "self-test",
			functions: []string{"measuredwithoutmark"},
		},
	}
	problems := auditFunctionFactEvidence(registry, catalog)
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "has evidence") || !strings.Contains(joined, "has no mark") {
		t.Errorf("the audit did not report the missing mark:\n%s", joined)
	}
	if !strings.Contains(joined, "marks fact") || !strings.Contains(joined, "no measured entry") {
		t.Errorf("the audit did not report the missing evidence:\n%s", joined)
	}
}
