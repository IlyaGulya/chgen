package functionrules_test

import (
	"os"
	"testing"

	"github.com/IlyaGulya/chgen/internal/functionrules"
)

func TestMeasuredRegistryCandidateFitsAReviewableBudget(t *testing.T) {
	// The generated roster is a review artifact. Its size must scale with
	// measured facts, not copies of the family's evaluation machinery.
	source := []byte("package engine\nvar functionSemanticSpecs = map[string]functionSpec{}\n")
	for _, path := range []string{"../../testdata/clickhouse-function-rules.json", "../../testdata/clickhouse-string-function-rules.json"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		report, err := functionrules.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		source, _, err = functionrules.Generate(source, report)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(source) > 18000 {
		t.Fatalf("35 measured rules occupy %d bytes; review budget is 18000", len(source))
	}
}
