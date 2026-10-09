package queries

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func TestComposedBindings(t *testing.T) {
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{os.Getenv("CHGEN_CONTRACT_NATIVE")}, Auth: clickhouse.Auth{Database: os.Getenv("CHGEN_COMPOSITION_DATABASE")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := New(conn)
	ctx := clickhouse.Context(t.Context(), clickhouse.WithParameters(clickhouse.Parameters{"Unrelated": "stale"}))
	for _, tc := range []struct {
		source  ReadSource
		after   *ReadAfterParams
		changed *ReadChangedParams
		want    []uint64
	}{
		{ReadSourceCurrent, nil, nil, []uint64{1, 3}},
		{ReadSourceCurrent, &ReadAfterParams{AfterID: 1}, nil, []uint64{3}},
		{ReadSourceCurrent, nil, &ReadChangedParams{ChangedIDs: []RequestedKeysRow{{ID: 1}}}, []uint64{1}},
		{ReadSourceCurrent, &ReadAfterParams{AfterID: 1}, &ReadChangedParams{ChangedIDs: []RequestedKeysRow{{ID: 1}, {ID: 3}}}, []uint64{3}},
		{ReadSourceCurrent, nil, &ReadChangedParams{}, []uint64{}},
		{ReadSourceArchive, nil, nil, []uint64{8}},
		{ReadSourceArchive, &ReadAfterParams{AfterID: 8}, &ReadChangedParams{}, []uint64{}},
	} {
		rows, err := q.Read(ctx, ReadParams{Source: tc.source, Keys: []string{"a", "quote'", "slash\\"}, Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), After: tc.after, Changed: tc.changed})
		if err != nil {
			t.Fatal(err)
		}
		got := make([]uint64, len(rows))
		for i, row := range rows {
			got[i] = row.ID
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("composed bindings: got %v, want %v", got, tc.want)
		}
	}
	if _, err := q.Read(t.Context(), ReadParams{Source: ReadSource("current; DROP DATABASE x")}); err == nil {
		t.Fatal("accepted an undeclared table name")
	}
}
