package series

import (
	"os"
	"slices"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func scalarValues[R any, V any](rows []R, field func(R) V) []V {
	values := make([]V, len(rows))
	for i, row := range rows {
		values[i] = field(row)
	}
	return values
}

func TestSeriesRowsAndTypes(t *testing.T) {
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{os.Getenv("CHGEN_CONTRACT_NATIVE")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	var version string
	if err := conn.QueryRow(t.Context(), "SELECT version()").Scan(&version); err != nil || version != "25.8.29.51" {
		t.Fatalf("series evidence requires ClickHouse 25.8.29.51: %q, %v", version, err)
	}
	q := New(conn)
	rows, err := q.Series(t.Context(), SeriesParams{})
	if err != nil || len(rows) != 3 {
		t.Fatalf("series rows: %+v, %v", rows, err)
	}
	for i, want := range []uint64{10, 12, 14} {
		var zero uint8 = rows[i].Zero
		if rows[i].Number != want || zero != 0 {
			t.Fatalf("series row: %+v", rows[i])
		}
	}
	parallel, err := q.Parallel(t.Context(), ParallelParams{})
	if err != nil || len(parallel) != 3 {
		t.Fatalf("parallel series rows: %+v, %v", parallel, err)
	}
	for i, want := range []uint64{10, 12, 14} {
		var zero uint8 = parallel[i].Zero
		if parallel[i].Number != want || zero != 0 {
			t.Fatalf("parallel series row: %+v", parallel[i])
		}
	}
	for _, run := range []func() ([]uint64, error){
		func() ([]uint64, error) {
			rows, err := q.Inclusive(t.Context(), InclusiveParams{})
			return scalarValues(rows, func(row InclusiveRow) uint64 { return row.Value }), err
		},
		func() ([]uint64, error) {
			rows, err := q.Camel(t.Context(), CamelParams{})
			return scalarValues(rows, func(row CamelRow) uint64 { return row.Value }), err
		},
		func() ([]uint64, error) {
			rows, err := q.ParamInclusive(t.Context(), ParamInclusiveParams{Start: 2, Stop: 8, Step: 2})
			return scalarValues(rows, func(row ParamInclusiveRow) uint64 { return row.Value }), err
		},
		func() ([]uint64, error) {
			rows, err := q.ParamCamel(t.Context(), ParamCamelParams{Start: 2, Stop: 8, Step: 2})
			return scalarValues(rows, func(row ParamCamelRow) uint64 { return row.Value }), err
		},
	} {
		values, err := run()
		if err != nil || !slices.Equal(values, []uint64{2, 4, 6, 8}) {
			t.Fatalf("inclusive values: %v, %v", values, err)
		}
	}
	for _, run := range []func() ([]uint64, error){
		func() ([]uint64, error) {
			rows, err := q.Parameterized(t.Context(), ParameterizedParams{Start: 10, Length: 6, Step: 2})
			return scalarValues(rows, func(row ParameterizedRow) uint64 { return row.Number }), err
		},
		func() ([]uint64, error) {
			rows, err := q.ParamParallel(t.Context(), ParamParallelParams{Start: 10, Length: 6, Step: 2})
			return scalarValues(rows, func(row ParamParallelRow) uint64 { return row.Number }), err
		},
	} {
		values, err := run()
		if err != nil || !slices.Equal(values, []uint64{10, 12, 14}) {
			t.Fatalf("parameterized values: %v, %v", values, err)
		}
	}
	for _, run := range []func() ([]uint8, error){
		func() ([]uint8, error) {
			rows, err := q.ParamZeros(t.Context(), ParamZerosParams{Length: 3})
			return scalarValues(rows, func(row ParamZerosRow) uint8 { return row.Zero }), err
		},
		func() ([]uint8, error) {
			rows, err := q.ParamZerosParallel(t.Context(), ParamZerosParallelParams{Length: 3})
			return scalarValues(rows, func(row ParamZerosParallelRow) uint8 { return row.Zero }), err
		},
	} {
		values, err := run()
		if err != nil || !slices.Equal(values, []uint8{0, 0, 0}) {
			t.Fatalf("zero values: %v, %v", values, err)
		}
	}
	values, err := q.Unlimited(t.Context(), UnlimitedParams{})
	if err != nil || !slices.Equal(scalarValues(values, func(row UnlimitedRow) uint64 { return row.Number }), []uint64{0, 1, 2}) {
		t.Fatalf("bounded unlimited series: %v, %v", values, err)
	}
	empty, err := q.Empty(t.Context(), EmptyParams{})
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty series: %v, %v", empty, err)
	}
	if _, err := q.Parameterized(t.Context(), ParameterizedParams{Start: 1, Length: 3, Step: 0}); err == nil {
		t.Fatal("zero step accepted")
	}
	filtered, err := q.BoundFilter(t.Context(), BoundFilterParams{})
	if err != nil || len(filtered) != 1 || filtered[0].Number != 3 {
		t.Fatalf("bound filter: %+v, %v", filtered, err)
	}
}
