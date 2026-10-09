package queries

import (
	"os"
	"reflect"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func TestOptionalCursor(t *testing.T) {
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{os.Getenv("CHGEN_CONTRACT_NATIVE")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := New(conn)
	for _, tc := range []struct {
		after *ReadAfterParams
		want  []uint64
	}{
		{nil, []uint64{0, 1, 2, 3, 4}},
		{&ReadAfterParams{AfterID: 1}, []uint64{2, 3, 4}},
		{&ReadAfterParams{AfterID: 4}, []uint64{}},
	} {
		rows, err := q.Read(t.Context(), ReadParams{Limit: 5, After: tc.after})
		if err != nil {
			t.Fatal(err)
		}
		got := make([]uint64, len(rows))
		for i, row := range rows {
			got[i] = row.Number
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("cursor: got %v, want %v", got, tc.want)
		}
	}
}
