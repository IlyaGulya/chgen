package scopedcontracts

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func TestScopedContracts(t *testing.T) {
	address := os.Getenv("CHGEN_CONTRACT_NATIVE")
	if address == "" {
		t.Skip("CHGEN_CONTRACT_NATIVE is not set")
	}
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{address}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.Exec(t.Context(), "CREATE TABLE scoped_events (id UInt64, n Nullable(UInt64)) ENGINE=Memory"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := conn.Exec(ctx, "DROP TABLE scoped_events"); err != nil {
			t.Error(err)
		}
	})
	if err := conn.Exec(t.Context(), "INSERT INTO scoped_events VALUES (1,NULL),(2,5),(3,0)"); err != nil {
		t.Fatal(err)
	}
	q := New(conn)
	lambda, err := q.Lambda(t.Context(), LambdaParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(lambda) != 3 {
		t.Fatalf("lambda rows: %+v", lambda)
	}
	for i, want := range []uint64{0, 1, 1} {
		if len(lambda[i].Values) != 1 || lambda[i].Values[0] != want {
			t.Fatalf("lambda rows: %+v", lambda)
		}
	}
	rows, err := q.Read(t.Context(), ReadParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].ID != 1 || rows[0].Value != nil || rows[1].Value == nil || *rows[1].Value != 2 || rows[2].Value == nil || *rows[2].Value != 0 {
		t.Fatalf("lost nullable/value semantics across CTE: %+v", rows)
	}
	// A wrong output contract is caught even when the SELECT produces no rows.
	for _, after := range []uint64{0, 100} {
		if _, err := q.WrongContract(t.Context(), WrongContractParams{After: after}); err == nil || !strings.Contains(err.Error(), "expected UInt32, got UInt64") {
			t.Fatalf("after=%d: missing metadata mismatch: %v", after, err)
		}
	}
}
