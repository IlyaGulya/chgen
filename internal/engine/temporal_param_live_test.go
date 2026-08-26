//go:build fuzzoracle

package engine

import (
	"strings"
	"testing"
)

func TestExplicitTemporalParamShapeAgainstClickHouse(t *testing.T) {
	oracle := execWitnessFixtureWithDDL(t, `CREATE TABLE events (
    rowid UInt32,
    seen Nullable(DateTime64(3, 'UTC'))
) ENGINE = Memory`, `INSERT INTO events VALUES
(1, NULL),
(2, toDateTime64('2024-01-02 03:04:05.000', 3, 'UTC')),
(3, toDateTime64('2024-01-04 03:04:05.000', 3, 'UTC'))`)
	expression := "seen <= toDateTime64('2024-01-03 03:04:05.000', 3, 'UTC')"
	analysis, err := oracle.exec("SELECT DISTINCT toTypeName(" + expression + ") FROM events FORMAT TabSeparatedRaw")
	if err != nil {
		t.Fatalf("analysis witness: %v", err)
	}
	if got := strings.TrimSpace(analysis); got != "Nullable(UInt8)" {
		t.Fatalf("analysis type = %s, want Nullable(UInt8)", got)
	}
	execution, err := oracle.exec("SELECT " + expression + " FROM events ORDER BY rowid FORMAT TabSeparatedRaw")
	if err != nil {
		t.Fatalf("execution witness: %v", err)
	}
	if got := strings.TrimSpace(execution); got != "\\N\n1\n0" {
		t.Fatalf("execution values = %q, want %q", got, "\\N\n1\n0")
	}
}
