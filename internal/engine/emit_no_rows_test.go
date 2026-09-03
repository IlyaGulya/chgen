package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateOneExportsWrappedErrNoRows(t *testing.T) {
	generated := generateOneQueryForNoRowsTest(t)
	text := string(generated)

	for _, want := range []string{
		`"errors"`,
		`var ErrNoRows = errors.New("no rows")`,
		`return result, fmt.Errorf("ReadEvent: %w", ErrNoRows)`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("generated output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, `fmt.Errorf("ReadEvent: no rows")`) {
		t.Errorf("generated output still creates an unmatchable no-rows error:\n%s", text)
	}
}

func TestGenerateWithoutOneOmitsErrNoRows(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events (id UInt64);`)
	if err != nil {
		t.Fatal(err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ListEvents :many
SELECT id FROM events`, schema)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatal(err)
	}
	text := string(generated)
	if strings.Contains(text, `"errors"`) || strings.Contains(text, "ErrNoRows") {
		t.Errorf("a package without :one queries must not emit the sentinel:\n%s", text)
	}
}

func TestGeneratedOneNoRowsErrorContract(t *testing.T) {
	buildRoot := newGeneratedCompileModule(t)
	packageDir := filepath.Join(buildRoot, "norows")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "generated.go"), generateOneQueryForNoRowsTest(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "generated_test.go"), []byte(generatedNoRowsRuntimeTest), 0o644); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("go", "test", "./norows")
	command.Dir = buildRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated no-rows contract failed: %v\n%s", err, output)
	}
}

func generateOneQueryForNoRowsTest(t *testing.T) []byte {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE events (id UInt64);`)
	if err != nil {
		t.Fatal(err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: ReadEvent :one
SELECT id FROM events WHERE id = 1`, schema)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := Generate("norows", queries)
	if err != nil {
		t.Fatal(err)
	}
	return generated
}

const generatedNoRowsRuntimeTest = `package norows

import (
	"context"
	"errors"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type testConn struct {
	driver.Conn
	rows     driver.Rows
	queryErr error
}

func (c testConn) Query(context.Context, string, ...any) (driver.Rows, error) {
	return c.rows, c.queryErr
}

type testRows struct {
	driver.Rows
	hasRow bool
	err    error
	value  uint64
}

func (r *testRows) Next() bool {
	if !r.hasRow {
		return false
	}
	r.hasRow = false
	return true
}

func (r *testRows) Scan(dest ...any) error {
	*dest[0].(*uint64) = r.value
	return nil
}

func (r *testRows) Close() error { return nil }
func (r *testRows) Err() error   { return r.err }

func TestEmptyResultMatchesErrNoRows(t *testing.T) {
	queries := New(testConn{rows: &testRows{}})
	_, err := queries.ReadEvent(context.Background(), ReadEventParams{})
	if !errors.Is(err, ErrNoRows) {
		t.Fatalf("error = %v, want errors.Is(err, ErrNoRows)", err)
	}
	if got, want := err.Error(), "ReadEvent: no rows"; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
}

func TestQueryFailureDoesNotMatchErrNoRows(t *testing.T) {
	driverFailure := errors.New("driver failed")
	queries := New(testConn{queryErr: driverFailure})
	_, err := queries.ReadEvent(context.Background(), ReadEventParams{})
	if errors.Is(err, ErrNoRows) {
		t.Fatalf("error = %v, unexpectedly matches ErrNoRows", err)
	}
	if !errors.Is(err, driverFailure) {
		t.Fatalf("error = %v, want wrapped driver error", err)
	}
}

func TestRowsFailureDoesNotMatchErrNoRows(t *testing.T) {
	driverFailure := errors.New("rows failed")
	queries := New(testConn{rows: &testRows{err: driverFailure}})
	_, err := queries.ReadEvent(context.Background(), ReadEventParams{})
	if errors.Is(err, ErrNoRows) {
		t.Fatalf("error = %v, unexpectedly matches ErrNoRows", err)
	}
	if !errors.Is(err, driverFailure) {
		t.Fatalf("error = %v, want wrapped rows error", err)
	}
}

func TestRowResultIsUnchanged(t *testing.T) {
	queries := New(testConn{rows: &testRows{hasRow: true, value: 7}})
	row, err := queries.ReadEvent(context.Background(), ReadEventParams{})
	if err != nil {
		t.Fatal(err)
	}
	if row.ID != 7 {
		t.Fatalf("row ID = %d, want 7", row.ID)
	}
}
`
