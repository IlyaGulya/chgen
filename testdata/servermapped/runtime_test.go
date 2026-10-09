package queries

import (
	"os"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestMappedParameterRoundTrip(t *testing.T) {
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{os.Getenv("CHGEN_CONTRACT_NATIVE")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	at := time.Date(2026, 10, 9, 17, 0, 0, 123456000, time.FixedZone("local", 5*3600))
	id := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	amount := decimal.RequireFromString("12.345")
	row, err := New(conn).Read(t.Context(), ReadParams{ID: id, Amount: amount, Seconds: at, Times: map[string]time.Time{"a": at, "quote' slash\\ newline\n ; DROP TABLE never": at}, Events: map[time.Time]string{at: "local", at.UTC(): "utc"}})
	if err != nil || row.ID != id || !row.Amount.Equal(amount) || row.Seconds != 1791547200 || row.Micros != 1791547200123456 || !row.AtSecond.Equal(at.Truncate(time.Second)) || row.EventCount != 2 {
		t.Fatalf("mapped parameters: %+v, %v", row, err)
	}
}
