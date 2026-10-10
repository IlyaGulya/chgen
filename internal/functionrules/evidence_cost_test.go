package functionrules_test

import (
	"os"
	"testing"

	"github.com/IlyaGulya/chgen/internal/functionrules"
)

// Bound work per full seed without depending on host speed or a wall-clock
// deadline. The fuzz worker has a fixed ten-second deadline per input.
func TestCompactEvidenceFullNumericSeedHasBoundedAllocations(t *testing.T) {
	data, err := os.ReadFile("../../testdata/clickhouse-function-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	allocations := testing.AllocsPerRun(1, func() {
		report, err := functionrules.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := functionrules.EncodeCompact(report); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("full seed decode/encode: %.0f allocations", allocations)
	if allocations > 100_000 {
		t.Fatalf("full seed decode/encode allocated %.0f objects; budget 100000", allocations)
	}
}
