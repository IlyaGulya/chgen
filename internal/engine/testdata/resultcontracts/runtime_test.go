package contracts

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type fakeType struct {
	driver.ColumnType
	name string
}

func (c fakeType) DatabaseTypeName() string { return c.name }

type fakeRows struct {
	driver.Rows
	names   []string
	types   []driver.ColumnType
	empty   bool
	closed  bool
	scanned bool
	read    bool
}

func (r *fakeRows) Columns() []string                { return r.names }
func (r *fakeRows) ColumnTypes() []driver.ColumnType { return r.types }
func (r *fakeRows) Err() error                       { return nil }
func (r *fakeRows) Close() error                     { r.closed = true; return nil }
func (r *fakeRows) Next() bool {
	if r.empty || r.read {
		return false
	}
	r.read = true
	return true
}
func (r *fakeRows) Scan(dest ...any) error {
	r.scanned = true
	*dest[0].(*uint32) = 7
	return nil
}

type fakeConn struct {
	driver.Conn
	rows *fakeRows
}

func (c fakeConn) Query(context.Context, string, ...any) (driver.Rows, error) {
	return c.rows, nil
}

func TestGeneratedContractChecks(t *testing.T) {
	for _, many := range []bool{false, true} {
		for _, empty := range []bool{false, true} {
			for _, got := range []string{"UInt32", "UInt64", "Nullable(UInt32)", ""} {
				rows := &fakeRows{
					names: []string{"value"},
					types: []driver.ColumnType{fakeType{name: got}},
					empty: empty,
				}
				q := New(fakeConn{rows: rows})
				var err error
				if many {
					_, err = q.ReadMany(t.Context(), ReadManyParams{})
				} else {
					_, err = q.ReadOne(t.Context(), ReadOneParams{})
				}
				if !rows.closed {
					t.Fatal("rows were not closed")
				}
				if got != "UInt32" {
					if err == nil || !strings.Contains(err.Error(), "result contract") || rows.scanned || rows.read {
						t.Fatalf("bad type %q was not rejected before reading: %v", got, err)
					}
				} else if empty && !many {
					if !errors.Is(err, ErrNoRows) {
						t.Fatalf("empty :one: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, rows := range []*fakeRows{
		{},
		{names: []string{"value"}, types: []driver.ColumnType{nil}},
		{names: []string{"wrong"}, types: []driver.ColumnType{fakeType{name: "UInt32"}}},
	} {
		if _, err := New(fakeConn{rows: rows}).ReadOne(t.Context(), ReadOneParams{}); err == nil || !rows.closed {
			t.Fatalf("invalid metadata accepted or rows leaked: %v", err)
		}
	}
}

func TestTypeTokenBoundaries(t *testing.T) {
	if chgenTypeTokens("Tuple(a UInt32, b Nullable(String))") != chgenTypeTokens("Tuple( a UInt32 , b Nullable( String ) )") {
		t.Fatal("insignificant whitespace changed the type")
	}
	for _, pair := range [][2]string{
		{"UInt32", "Nullable(UInt32)"},
		{"String", "LowCardinality(String)"},
		{"DateTime64(3, 'UTC')", "DateTime64(6, 'UTC')"},
		{"DateTime('UTC')", "DateTime('Asia/Tokyo')"},
		{"Decimal(9, 2)", "Decimal(9, 3)"},
		{"Tuple(a UInt32)", "Tuple(aUInt32)"},
	} {
		if chgenTypeTokens(pair[0]) == chgenTypeTokens(pair[1]) {
			t.Fatalf("different types compared equal: %v", pair)
		}
	}
}

func TestLiveContracts(t *testing.T) {
	address := os.Getenv("CHGEN_CONTRACT_NATIVE")
	if address == "" {
		t.Skip("CHGEN_CONTRACT_NATIVE is not set")
	}
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{address}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := New(conn)
	if row, err := q.ReadOne(t.Context(), ReadOneParams{}); err != nil || row.Value != 7 {
		t.Fatalf("matching :one: %+v, %v", row, err)
	}
	if rows, err := q.ReadMany(t.Context(), ReadManyParams{}); err != nil || len(rows) != 1 || rows[0].Value != 7 {
		t.Fatalf("matching :many: %+v, %v", rows, err)
	}
	if _, err := q.EmptyOne(t.Context(), EmptyOneParams{}); !errors.Is(err, ErrNoRows) {
		t.Fatalf("empty :one: %v", err)
	}
	if rows, err := q.EmptyMany(t.Context(), EmptyManyParams{}); err != nil || len(rows) != 0 {
		t.Fatalf("empty :many: %+v, %v", rows, err)
	}
	if _, err := q.WrongOne(t.Context(), WrongOneParams{}); err == nil || !strings.Contains(err.Error(), "expected UInt32, got UInt64") {
		t.Fatalf("wrong :one: %v", err)
	}
	if _, err := q.WrongMany(t.Context(), WrongManyParams{}); err == nil || !strings.Contains(err.Error(), "expected UInt32, got UInt64") {
		t.Fatalf("wrong empty :many: %v", err)
	}
	if row, err := q.ReadNull(t.Context(), ReadNullParams{}); err != nil || row.Value != nil {
		t.Fatalf("nullable output: %+v, %v", row, err)
	}
	if row, err := q.ReadTime(t.Context(), ReadTimeParams{}); err != nil || row.Value.Year() != 2024 {
		t.Fatalf("temporal output: %+v, %v", row, err)
	}
	if row, err := q.ReadLowCard(t.Context(), ReadLowCardParams{}); err != nil || row.Value != "ok" {
		t.Fatalf("low cardinality output: %+v, %v", row, err)
	}
	if row, err := q.ReadDecimal(t.Context(), ReadDecimalParams{}); err != nil || row.Value.String() != "1.23" {
		t.Fatalf("decimal output: %+v, %v", row, err)
	}
	if row, err := q.ReadArray(t.Context(), ReadArrayParams{}); err != nil || len(row.Value) != 1 || row.Value[0] != 7 {
		t.Fatalf("array output: %+v, %v", row, err)
	}
}
