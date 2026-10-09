package systemcatalog

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCollectRejectsWrongVersionAndEmptyCatalog(t *testing.T) {
	for _, tc := range []struct{ version, columns, want string }{
		{"25.8.29.51", `{"data":[{"table":"parts","name":"rows","type":"UInt64","position":1}]}`, ""},
		{"26.1.1.1", `{"data":[]}`, "server version"},
		{"25.8.29.51", `{"data":[]}`, "no columns"},
		{"25.8.29.51", `invalid`, "invalid character"},
	} {
		t.Run(tc.version+tc.want, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				if strings.Contains(string(body), "version()") {
					io.WriteString(w, tc.version+"\n")
					return
				}
				io.WriteString(w, tc.columns)
			}))
			defer server.Close()
			snapshot, err := Collect(context.Background(), server.URL, "25.8.29.51")
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %q, got %v", tc.want, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Version != tc.version || len(snapshot.Columns) != 1 {
				t.Fatalf("wrong snapshot: %+v", snapshot)
			}
		})
	}
}

func TestPinnedSnapshotIsOrderedAndComplete(t *testing.T) {
	snapshot, err := Pinned()
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	var previous Column
	for _, column := range snapshot.Columns {
		key := column.Table + "." + column.Name
		if seen[key] || column.Type == "" || column.Position == 0 {
			t.Fatalf("invalid column: %+v", column)
		}
		if column.Table < previous.Table || column.Table == previous.Table && column.Position <= previous.Position {
			t.Fatalf("out-of-order column: %+v", column)
		}
		seen[key] = true
		previous = column
	}
	for _, key := range []string{"parts.database", "parts.table", "parts.partition_id", "parts.name", "parts.active", "parts.rows", "parts.bytes_on_disk", "parts.data_compressed_bytes", "parts.min_block_number", "parts.max_block_number", "parts.data_version", "parts.modification_time", "parts.level", "tables.database", "tables.name", "tables.engine", "tables.sorting_key", "tables.partition_key", "tables.engine_full"} {
		if !seen[key] {
			t.Errorf("missing %s", key)
		}
	}
}
