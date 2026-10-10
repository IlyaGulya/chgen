package functionrules_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/functionrules"
)

func TestMeasuredRegistryCandidateFitsAReviewableBudget(t *testing.T) {
	// The generated roster is a review artifact. Its size must scale with
	// measured facts, not copies of the family's evaluation machinery.
	source := []byte("package engine\nvar functionSemanticSpecs = map[string]functionSpec{}\n")
	var reports []functionrules.Report
	for _, path := range []string{"../../testdata/clickhouse-function-rules.json", "../../testdata/clickhouse-string-function-rules.json"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		report, err := functionrules.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		reports = append(reports, report)
		source, _, err = functionrules.Generate(source, report)
		if err != nil {
			t.Fatal(err)
		}
	}
	if count := strings.Count(string(source), "primitives:"); count != 5 {
		t.Fatalf("35 measured members need five distinct measured domains, got %d copies", count)
	}
	if strings.Contains(string(source), "expected:") {
		t.Fatal("generated diagnostic descriptions duplicate the measured domain")
	}
	if len(source) > 12000 {
		t.Fatalf("35 measured rules occupy %d bytes; review budget is 12000", len(source))
	}
	for _, report := range reports {
		regenerated, _, err := functionrules.Generate(source, report)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(source, regenerated) {
			t.Fatalf("regenerating %s changes shared families or the other profile", report.Profile)
		}
	}
}
