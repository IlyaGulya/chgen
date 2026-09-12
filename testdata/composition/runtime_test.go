package composition

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func TestReadPages(t *testing.T) {
	arg := ReadPageParams{Source: ReadPageSourcePagedSpans, ScopeType: "repo", ScopeKey: "one", PageRows: 2,
		Keys: []RequestedRunsRow{{ID: "run"}, {ID: "run"}}}
	arg.After = &ReadPageAfterParams{AfterID: "b"}
	arg.After = nil
	address := os.Getenv("CHGEN_CONTRACT_NATIVE")
	if address == "" {
		t.Skip("CHGEN_CONTRACT_NATIVE is not set")
	}
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{address}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.Exec(t.Context(), `CREATE TABLE paged_spans (
scope_type String, scope_key String, id String, run_span_id String, version UInt64, payload String
) ENGINE=ReplacingMergeTree(version) ORDER BY (scope_type, scope_key, id)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := conn.Exec(ctx, "DROP TABLE paged_spans"); err != nil {
			t.Error(err)
		}
	})
	if err := conn.Exec(t.Context(), `INSERT INTO paged_spans VALUES
('repo','one','a','run',1,'old'),('repo','one','a','run',2,'a'),
('repo','one','b','run',1,'b'),('repo','one','c','run',1,'c'),
('repo','one','d','run',1,'old'),('repo','one','d','other',2,'d'),
('repo','one','e','other',1,'old'),('repo','one','e','run',2,'e'),
('repo','two','f','run',1,'wrong scope')`); err != nil {
		t.Fatal(err)
	}
	q := New(conn)
	for _, requested := range []bool{false, true} {
		for _, after := range []bool{false, true} {
			filter := FilteredParams{ScopeType: "repo", ScopeKey: "one"}
			if requested {
				filter.Requested = &FilteredRequestedParams{Keys: []RequestedRunsRow{{ID: "b"}, {ID: "e"}}}
			}
			if after {
				filter.After = &FilteredAfterParams{AfterID: "b"}
			}
			rows, err := q.Filtered(t.Context(), filter)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, id := range []string{"a", "b", "c", "d", "e"} {
				if (!requested || id == "b" || id == "e") && (!after || id > "b") {
					want = append(want, id)
				}
			}
			if len(rows) != len(want) {
				t.Fatalf("filter %+v: rows=%+v want=%v", filter, rows, want)
			}
			for i, id := range want {
				if rows[i].ID != id {
					t.Fatalf("filter %+v: rows=%+v want=%v", filter, rows, want)
				}
			}
		}
	}
	if row, err := q.TimeProbe(t.Context(), TimeProbeParams{}); err != nil || row.Value.Year() != 2024 {
		t.Fatalf("absent optional temporal param was evaluated: %+v, %v", row, err)
	}
	if _, err := q.TimeProbe(t.Context(), TimeProbeParams{After: &TimeProbeAfterParams{At: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}}); !errors.Is(err, ErrNoRows) {
		t.Fatalf("composed :one lost ErrNoRows: %v", err)
	}
	if _, err := q.TimeProbe(t.Context(), TimeProbeParams{After: &TimeProbeAfterParams{At: time.Date(2299, 1, 1, 0, 0, 0, 0, time.UTC)}}); err == nil || !strings.Contains(err.Error(), "cannot hold") {
		t.Fatalf("optional temporal param evaded the range guard: %v", err)
	}
	for _, ids := range [][]string{{"a", "b"}, {"c", "e"}, {}} {
		rows, err := q.ReadPage(t.Context(), arg)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != len(ids) {
			t.Fatalf("after=%+v: rows=%+v, want %v", arg.After, rows, ids)
		}
		for i, id := range ids {
			if rows[i].ID != id || rows[i].Payload != id {
				t.Fatalf("lost FINAL or keyset semantics: %+v", rows)
			}
		}
		if len(rows) > 0 {
			arg.After = &ReadPageAfterParams{AfterID: rows[len(rows)-1].ID}
		}
	}
	arg.After, arg.Keys = nil, nil
	if rows, err := q.ReadPage(t.Context(), arg); err != nil || len(rows) != 0 {
		t.Fatalf("empty external table: %+v, %v", rows, err)
	}
	if err := conn.Exec(t.Context(), "CREATE TABLE paged_spans_archive AS paged_spans"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := conn.Exec(ctx, "DROP TABLE paged_spans_archive"); err != nil {
			t.Error(err)
		}
	})
	if err := conn.Exec(t.Context(), "INSERT INTO paged_spans_archive VALUES ('repo','one','archived','run',1,'archive')"); err != nil {
		t.Fatal(err)
	}
	arg.Source, arg.Keys = ReadPageSourcePagedSpansArchive, []RequestedRunsRow{{ID: "run"}}
	if rows, err := q.ReadPage(t.Context(), arg); err != nil || len(rows) != 1 || rows[0].ID != "archived" || rows[0].Payload != "archive" {
		t.Fatalf("table choice did not apply to both scopes: %+v, %v", rows, err)
	}
	arg.After = &ReadPageAfterParams{AfterID: "archived"}
	if rows, err := q.ReadPage(t.Context(), arg); err != nil || len(rows) != 0 {
		t.Fatalf("archive cursor variant: %+v, %v", rows, err)
	}
	for _, invalid := range []ReadPageSource{"", "paged_spans WHERE 1=1"} {
		arg.Source = invalid
		if _, err := q.ReadPage(t.Context(), arg); err == nil || !strings.Contains(err.Error(), "invalid query variant") {
			t.Fatalf("accepted an undeclared table choice: %v", err)
		}
	}
}
