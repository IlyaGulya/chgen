package contracts

import (
	"context"
	"os"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func TestGeneratedSignature(t *testing.T) {
	ctx := context.Background()
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{os.Getenv("CHGEN_SYSTEM_HTTP")}, Protocol: clickhouse.HTTP})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var version string
	if err := conn.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "25.8.29.51" {
		t.Fatalf("wrong server version: %s", version)
	}
	// A nonexistent table must still produce the single zero aggregate row.
	row, err := New(conn).ReadFactTableChangeSignature(ctx, ReadFactTableChangeSignatureParams{Table: "chgen_no_such_table"})
	if err != nil {
		t.Fatal(err)
	}
	if row.Signature != 0 {
		t.Fatalf("empty signature: %d", row.Signature)
	}
	if _, err := New(conn).ReadFactTableChangeSignature(ctx, ReadFactTableChangeSignatureParams{Table: "parts"}); err != nil {
		t.Fatal(err)
	}
}
