package queries

import (
	"os"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func TestRequestScopedExternalRows(t *testing.T) {
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{os.Getenv("CHGEN_CONTRACT_NATIVE")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	at := time.Date(2026, 10, 9, 12, 0, 0, 123456000, time.UTC)
	q := New(conn)
	rows, err := q.Read(t.Context(), ReadParams{Keys: []RequestedKeysRow{{Key: "b", At: at}, {Key: "a'\\", At: at}}})
	if err != nil || len(rows) != 2 || rows[0].Key != "a'\\" || rows[0].Micros != 1791547200123456 {
		t.Fatalf("external rows: %+v, %v", rows, err)
	}
	rows, err = q.Read(t.Context(), ReadParams{})
	if err != nil || len(rows) != 0 {
		t.Fatalf("request data leaked: %+v, %v", rows, err)
	}
}
