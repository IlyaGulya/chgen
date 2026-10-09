package queries

import (
	"os"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func TestArrayRoundTrip(t *testing.T) {
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{os.Getenv("CHGEN_CONTRACT_NATIVE")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := New(conn)
	for _, tc := range []struct {
		keys   []string
		joined string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"quote'", "slash\\", "line\n", "nul\x00", "; DROP TABLE never"}, "quote'|slash\\|line\n|nul\x00|; DROP TABLE never"},
	} {
		row, err := q.Read(t.Context(), ReadParams{Keys: tc.keys})
		if err != nil || row.Joined != tc.joined || row.Size != uint64(len(tc.keys)) {
			t.Fatalf("round trip: %+v, %v", row, err)
		}
	}
}
