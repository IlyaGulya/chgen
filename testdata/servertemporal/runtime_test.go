package queries

import (
	"os"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func TestNullableAndTemporalRoundTrip(t *testing.T) {
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{os.Getenv("CHGEN_CONTRACT_NATIVE")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	at := time.Date(2026, 10, 9, 17, 0, 0, 123456000, time.FixedZone("local", 5*3600))
	label := "quote' slash\\ newline\n nul\x00"
	for _, value := range []*string{nil, &label} {
		row, err := New(conn).Read(t.Context(), ReadParams{At: at, Label: value})
		want := "none"
		if value != nil {
			want = label
		}
		if err != nil || row.Micros != 1791547200123456 || row.Label != want {
			t.Fatalf("round trip: %+v, %v", row, err)
		}
	}
	row, err := New(conn).Read(t.Context(), ReadParams{At: time.Unix(-1, 500000000)})
	if err != nil || row.Micros != -500000 {
		t.Fatalf("pre-epoch timestamp: %+v, %v", row, err)
	}
}
