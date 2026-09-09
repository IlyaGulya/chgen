package cursorqueries

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func TestNullableArrayIndexScan(t *testing.T) {
	address := os.Getenv("CHGEN_CONTRACT_NATIVE")
	if address == "" {
		t.Skip("CHGEN_CONTRACT_NATIVE is not set")
	}
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{address}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.Exec(t.Context(), "CREATE TABLE nullable_index_events (id UInt8, a Array(Int32), n Nullable(UInt64)) ENGINE=Memory"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := conn.Exec(ctx, "DROP TABLE nullable_index_events"); err != nil {
			t.Error(err)
		}
	})
	if err := conn.Exec(t.Context(), "INSERT INTO nullable_index_events VALUES (1,[42],NULL),(2,[42],1)"); err != nil {
		t.Fatal(err)
	}
	rows, err := New(conn).NullableArrayIndices(t.Context(), NullableArrayIndicesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != 1 || rows[0].Value != nil || rows[1].ID != 2 || rows[1].Value == nil || *rows[1].Value != 42 {
		t.Fatalf("nullable index rows: %+v", rows)
	}
}

func TestExternalCursorKeys(t *testing.T) {
	address := os.Getenv("CHGEN_CONTRACT_NATIVE")
	if address == "" {
		t.Skip("CHGEN_CONTRACT_NATIVE is not set")
	}
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{address}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx := t.Context()
	if err := conn.Exec(ctx, "CREATE TABLE cursor_events (scope UInt32, at DateTime64(3, 'UTC'), id String, cursor UInt64) ENGINE=Memory"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := conn.Exec(cleanup, "DROP TABLE cursor_events"); err != nil {
			t.Error(err)
		}
	})
	if err := conn.Exec(ctx, `INSERT INTO cursor_events VALUES
(1, '2024-01-01 00:01:35.123', 'modern', 1786844068050894855),
(1, '2024-01-01 00:01:35.123', 'modern', 1786844068050894856),
(2, '2024-01-01 00:01:35.123', 'other-scope', 1786844068050894855),
(1, '1970-01-01 00:00:00.000', 'epoch', 7),
(1, '1970-01-01 00:00:00.001', 'millisecond', 1048579)`); err != nil {
		t.Fatal(err)
	}
	keys := []RequestedKeysRow{
		{Ordinal: 2, ID: "modern", Cursor: 1786844068050894856},
		{Ordinal: 0, ID: "epoch", Cursor: 7},
		{Ordinal: 1, ID: "millisecond", Cursor: 1048579},
		{Ordinal: 3, ID: "modern", Cursor: 1786844068050894856},
		{Ordinal: 4, ID: "missing", Cursor: 7},
		{Ordinal: 5, ID: "other-scope", Cursor: 1786844068050894855},
	}
	q := New(conn)
	for _, input := range [][]RequestedKeysRow{keys, nil} {
		tuples, err := q.TupleKeys(ctx, TupleKeysParams{Keys: input, Scope: 1})
		if err != nil {
			t.Fatal(err)
		}
		columns, err := q.ColumnKeys(ctx, ColumnKeysParams{Keys: input, Scope: 1})
		if err != nil {
			t.Fatal(err)
		}
		want := 3
		if len(input) == 0 {
			want = 0
		}
		if len(tuples) != want || len(columns) != want {
			t.Fatalf("rows: tuple=%+v columns=%+v; want %d", tuples, columns, want)
		}
		ids := []string{"epoch", "millisecond", "modern"}
		times := []time.Time{time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(1970, 1, 1, 0, 0, 0, 1000000, time.UTC), time.Date(2024, 1, 1, 0, 1, 35, 123000000, time.UTC)}
		cursors := []uint64{7, 1048579, 1786844068050894856}
		for i := range tuples {
			if tuples[i].ID != ids[i] || !tuples[i].At.Equal(times[i]) || tuples[i].Cursor != cursors[i] {
				t.Fatalf("tuple row %d: %+v", i, tuples[i])
			}
			if columns[i].ID != ids[i] || !columns[i].At.Equal(times[i]) || columns[i].Cursor != cursors[i] {
				t.Fatalf("column row %d: %+v", i, columns[i])
			}
		}
	}
}
