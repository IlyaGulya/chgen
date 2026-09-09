package aggregates

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func TestScanRangeIsNotAnAbsenceCheck(t *testing.T) {
	for _, instant := range []time.Time{
		time.Unix(0, 0),
		time.Unix(chgenReadableMinUnix, 0),
		time.Unix(chgenReadableMaxUnix, chgenReadableMaxNanosecond),
	} {
		if err := chgenCheckDateTime64ScanRange(instant, "timestamp"); err != nil {
			t.Errorf("representable instant %s: %v", instant, err)
		}
	}
	if err := chgenCheckDateTime64ScanRange(time.Unix(chgenReadableMinUnix-1, 0), "timestamp"); err == nil {
		t.Fatal("wrapped timestamp passed the range check")
	}
}

func TestLiveEmptyAggregates(t *testing.T) {
	address := os.Getenv("CHGEN_CONTRACT_NATIVE")
	if address == "" {
		t.Skip("CHGEN_CONTRACT_NATIVE is not set")
	}
	admin, err := clickhouse.Open(&clickhouse.Options{Addr: []string{address}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	database := fmt.Sprintf("chgen_empty_aggregates_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec(t.Context(), "CREATE DATABASE "+database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// t.Context is canceled before cleanup. Use a bounded cleanup context.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := admin.Exec(ctx, "DROP DATABASE "+database); err != nil {
			t.Errorf("drop fixture: %v", err)
		}
	})
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{address}, Auth: clickhouse.Auth{Database: database},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, statement := range []string{
		`CREATE TABLE aggregate_events (
    scope UInt32, occurred_at DateTime64(3, 'UTC'), value Int32
) ENGINE = Memory`,
		`INSERT INTO aggregate_events VALUES
    (1, '1970-01-01 00:00:00.000', 0),
    (2, '2299-12-31 00:00:00.000', 1)`,
	} {
		if err := conn.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	q := New(conn)
	for _, scope := range []uint32{0, 1} {
		plain, err := q.PlainSpan(t.Context(), PlainSpanParams{Scope: scope})
		if err != nil || plain.MinTime.Unix() != 0 || plain.MaxTime.Unix() != 0 {
			t.Fatalf("plain scope %d: %+v, %v", scope, plain, err)
		}
		nullable, err := q.NullableSpan(t.Context(), NullableSpanParams{Scope: scope})
		if err != nil {
			t.Fatal(err)
		}
		if scope == 0 {
			if nullable.MinTime != nil || nullable.MaxTime != nil {
				t.Fatalf("empty aggregate must scan NULL: %+v", nullable)
			}
		} else if nullable.MinTime == nil || nullable.MaxTime == nil || nullable.MinTime.Unix() != 0 || nullable.MaxTime.Unix() != 0 {
			t.Fatalf("real epoch must stay non-nil: %+v", nullable)
		}
	}
	plainMany, err := q.PlainSpans(t.Context(), PlainSpansParams{Scope: 0})
	if err != nil || len(plainMany) != 1 || plainMany[0].MinTime.Unix() != 0 {
		t.Fatalf("plain empty :many returns one aggregate row: %+v, %v", plainMany, err)
	}
	nullMany, err := q.NullableSpans(t.Context(), NullableSpansParams{Scope: 0})
	if err != nil || len(nullMany) != 1 || nullMany[0].MinTime != nil || nullMany[0].MaxTime != nil {
		t.Fatalf("nullable empty :many: %+v, %v", nullMany, err)
	}
	if _, err := q.GroupedSpan(t.Context(), GroupedSpanParams{Scope: 0}); !errors.Is(err, ErrNoRows) {
		t.Fatalf("empty GROUP BY: %v", err)
	}
	if _, err := q.PresentSpan(t.Context(), PresentSpanParams{Scope: 0}); !errors.Is(err, ErrNoRows) {
		t.Fatalf("empty HAVING count() > 0: %v", err)
	}
	if row, err := q.GroupedSpan(t.Context(), GroupedSpanParams{Scope: 1}); err != nil || row.MinTime.Unix() != 0 {
		t.Fatalf("nonempty GROUP BY: %+v, %v", row, err)
	}
	if row, err := q.PresentSpan(t.Context(), PresentSpanParams{Scope: 1}); err != nil || row.MinTime.Unix() != 0 {
		t.Fatalf("nonempty HAVING: %+v, %v", row, err)
	}
	// A group can exist while an -If aggregate accepts no input rows.
	if row, err := q.ConditionalSpan(t.Context(), ConditionalSpanParams{Scope: 1}); err != nil || row.MinTime != nil || row.MaxTime != nil {
		t.Fatalf("empty conditional aggregate in existing group: %+v, %v", row, err)
	}
	emptyStats, err := q.NullableStatistics(t.Context(), NullableStatisticsParams{Scope: 0})
	if err != nil {
		t.Fatal(err)
	}
	if emptyStats.Total != nil || emptyStats.Average != nil || emptyStats.FirstTime != nil || emptyStats.LastTime != nil {
		t.Fatalf("empty numeric and argMin/argMax aggregates: %+v", emptyStats)
	}
	zeroStats, err := q.NullableStatistics(t.Context(), NullableStatisticsParams{Scope: 1})
	if err != nil {
		t.Fatal(err)
	}
	if zeroStats.Total == nil || *zeroStats.Total != 0 || zeroStats.Average == nil || *zeroStats.Average != 0 {
		t.Fatalf("real numeric zero must stay non-nil: %+v", zeroStats)
	}
	for _, instant := range []*time.Time{zeroStats.FirstTime, zeroStats.LastTime} {
		if instant == nil || instant.Unix() != 0 {
			t.Fatalf("argMin/argMax at the real epoch must stay non-nil: %+v", zeroStats)
		}
	}
	// An aggregate can still return a value beyond the driver's range.
	if _, err := q.PlainSpan(t.Context(), PlainSpanParams{Scope: 2}); err == nil || !strings.Contains(err.Error(), "driver wrapped") {
		t.Fatalf("plain aggregate lost its overflow guard: %v", err)
	}
	if _, err := q.NullableSpan(t.Context(), NullableSpanParams{Scope: 2}); err == nil || !strings.Contains(err.Error(), "driver wrapped") {
		t.Fatalf("nullable aggregate lost its overflow guard: %v", err)
	}
}
