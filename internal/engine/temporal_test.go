package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExplicitTemporalParamGoShapeControlsTopNullableGuard(t *testing.T) {
	nonPointer := generateNestedTemporalOutput(t, "time.Time")
	if !strings.Contains(nonPointer, `chgenGuardDateTime64(arg.Cutoff, "Cutoff")`) {
		t.Fatalf("non-pointer parameter has no direct guard:\n%s", nonPointer)
	}
	if strings.Contains(nonPointer, "arg.Cutoff != nil") || strings.Contains(nonPointer, "(*arg.Cutoff)") {
		t.Fatalf("non-pointer parameter has a pointer guard:\n%s", nonPointer)
	}

	pointer := generateNestedTemporalOutput(t, "*time.Time")
	for _, want := range []string{"if arg.Cutoff != nil", `chgenGuardDateTime64((*arg.Cutoff), "Cutoff")`} {
		if !strings.Contains(pointer, want) {
			t.Fatalf("pointer parameter has no %q:\n%s", want, pointer)
		}
	}
}

func TestUnsupportedExplicitTemporalParamTypeKeepsHistoricNullablePlan(t *testing.T) {
	columnType, err := parseCHTypeName("Nullable(DateTime64(3, 'UTC'))")
	if err != nil {
		t.Fatal(err)
	}
	shape, ok := (Param{GoName: "Cutoff", GoType: "string", CHType: columnType}).temporalPlan()
	if !ok || !shape.Nullable || shape.Kind != temporalDateTime64 {
		t.Fatalf("unsupported explicit type plan = %+v, %v, want the historic nullable DateTime64 plan", shape, ok)
	}
}

func TestExplicitNonPointerTemporalParamGeneratedSourceCompiles(t *testing.T) {
	source := generateNestedTemporalOutput(t, "time.Time")
	moduleRoot := moduleRootPath()
	buildDir, err := os.MkdirTemp(moduleRoot, ".temporal-param-build-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(buildDir) })
	if err := os.WriteFile(filepath.Join(buildDir, "queries.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(moduleRoot, buildDir)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "./"+filepath.ToSlash(relative))
	command.Dir = moduleRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated nested source does not compile: %v\n%s", err, output)
	}
}

func TestExplicitTemporalParamGoShapeMutationFails(t *testing.T) {
	original := temporalParamPlanUsesDeclaredPointerShape
	t.Cleanup(func() { temporalParamPlanUsesDeclaredPointerShape = original })
	check := func() bool {
		text := generateNestedTemporalOutput(t, "time.Time")
		return strings.Contains(text, `chgenGuardDateTime64(arg.Cutoff, "Cutoff")`) &&
			!strings.Contains(text, "arg.Cutoff != nil")
	}
	if !check() {
		t.Fatal("production temporal parameter rule is not active")
	}
	temporalParamPlanUsesDeclaredPointerShape = false
	if check() {
		t.Fatal("temporal parameter contract accepted the deleted Go-shape rule")
	}
}

func generateNestedTemporalOutput(t *testing.T, goType string) string {
	t.Helper()
	schema := schemaFromDDL(t, isNullExprTestDDL)
	source := `-- name: ListSelectedRows :many
-- param: Cutoff ` + goType + `
SELECT id, available_at
FROM
(
    SELECT
        id,
        argMax(kind, version) AS kind,
        argMax(available_at, version) AS available_at
    FROM probe
    GROUP BY id
) AS current_state
WHERE kind IN ('a', 'b')
  AND (available_at IS NULL OR available_at <= chgen.arg('Cutoff'))
ORDER BY id ASC
LIMIT chgen.arg('Limit')`
	queries, err := parseQueriesWithSchema(t, source, schema)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatal(err)
	}
	return string(generated)
}

// temporalTestSchema declares one column for every temporal family plus the
// container shapes that the temporal guards must walk.
const temporalTestSchema = `CREATE TABLE events
(
    rowid UInt32,
    d Date,
    d32 Date32,
    dt DateTime,
    dt3 DateTime64(3),
    dt9 DateTime64(9, 'UTC'),
    dts Array(DateTime64(3)),
    dtm Map(String, DateTime64(3)),
    note Nullable(String),
    seen Nullable(DateTime64(3))
);`

func generateTemporalTestOutput(t *testing.T, querySQL string) string {
	t.Helper()
	schema, err := schemaFromDDLErr(t, temporalTestSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, querySQL, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	return string(generated)
}

// TestTemporalBoundsAreTheMeasuredValues pins the range constants to the
// instants that were MEASURED against ClickHouse 25.8.29.51 through the HTTP
// interface. The HTTP channel does not involve the Go driver, so these numbers
// describe the server, not the client.
//
// If a future ClickHouse release moves a limit, this test fails first and
// names the family, instead of a guard silently rejecting a legal value.
func TestTemporalBoundsAreTheMeasuredValues(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		unix     int64
		expected string
	}{
		// toDate('2149-06-07') returns 2149-06-06: the server saturates.
		{"Date max", chgenDateMaxUnix, "2149-06-06T23:59:59Z"},
		{"Date min", chgenDateMinUnix, "1970-01-01T00:00:00Z"},
		// toDate32('1899-12-31') returns 1900-01-01 and toDate32('2300-01-01')
		// returns 2299-12-31.
		{"Date32 min", chgenDate32MinUnix, "1900-01-01T00:00:00Z"},
		{"Date32 max", chgenDate32MaxUnix, "2299-12-31T23:59:59Z"},
		// toDateTime('2106-02-07 06:28:16') returns 2106-02-07 06:28:15.
		{"DateTime min", chgenDateTimeMinUnix, "1970-01-01T00:00:00Z"},
		{"DateTime max", chgenDateTimeMaxUnix, "2106-02-07T06:28:15Z"},
		// A DateTime64 column with precision 0 through 8 holds the Date32
		// calendar, but the DRIVER carries every DateTime64 through int64
		// nanoseconds, so the write limit is the nanosecond ceiling.
		{"DateTime64 min", chgenDateTime64MinUnix, "1900-01-01T00:00:00Z"},
		{"DateTime64 guard max", chgenDateTime64MaxUnix, "2262-04-11T23:47:16Z"},
		{"DateTime64 column max", chgenDateTime64ColumnMaxUnix, "2299-12-31T23:59:59Z"},
		// DateTime64(9) counts nanoseconds in a signed 64-bit integer.
		// toDateTime64('2262-04-11 23:47:17', 9) raises DECIMAL_OVERFLOW.
		{"DateTime64(9) min", chgenDateTime64NanoMinUnix, "1900-01-01T00:00:00Z"},
		{"DateTime64(9) max", chgenDateTime64NanoMaxUnix, "2262-04-11T23:47:16Z"},
		// The read-side limit is the same nanosecond limit: the driver carries
		// every DateTime64 through int64 nanoseconds.
		{"readable max", chgenReadableMaxUnix, "2262-04-11T23:47:16Z"},
	} {
		got := time.Unix(testCase.unix, 0).UTC().Format(time.RFC3339)
		if got != testCase.expected {
			t.Errorf("%s = %s, measured %s", testCase.name, got, testCase.expected)
		}
	}
}

// TestTemporalKindOfPrecision checks that the guard family follows the
// declared precision, and that a timezone argument does not change it.
// DateTime64(9) needs its own family: its upper limit is nearly 40 years
// earlier than the limit of the lower precisions.
func TestTemporalKindOfPrecision(t *testing.T) {
	for _, testCase := range []struct {
		declaration string
		expected    temporalKind
	}{
		{"Date", temporalDate},
		{"Date32", temporalDate32},
		{"DateTime", temporalDateTime},
		{"DateTime64(0)", temporalDateTime64},
		{"DateTime64(3)", temporalDateTime64},
		{"DateTime64(6)", temporalDateTime64},
		{"DateTime64(9)", temporalDateTime64Nano},
		{"DateTime64(9, 'UTC')", temporalDateTime64Nano},
		{"DateTime64(3, 'Europe/Moscow')", temporalDateTime64},
		{"String", temporalNone},
	} {
		columnType, err := parseCHTypeName(testCase.declaration)
		if err != nil {
			t.Fatalf("parseCHTypeName(%q) error = %v", testCase.declaration, err)
		}
		if got := temporalKindOf(columnType); got != testCase.expected {
			t.Errorf("temporalKindOf(%s) = %q, want %q", testCase.declaration, got, testCase.expected)
		}
	}
}

// A fixed INSERT whose VALUES tuple holds only bare placeholders uses the
// native PrepareBatch/Append path. Measured with driver v2.47.0 against
// ClickHouse 25.8.29.51: the client-side text interpolation of conn.Exec
// silently drops the sub-second fraction of a time.Time
// (2024-01-02 03:04:05.123 arrives as 03:04:05.000), while the batch path
// keeps it exactly. The execution oracle verifies the generated scan value
// against the same value from the HTTP reference channel.
//
// The batch path alone does NOT close R2. It still corrupts a far date, only
// differently: 2299-12-31 through conn.Exec stores 2106-02-07, and the same
// value through the batch path stores 1900-01-01 00:00:00.291, both with
// err = nil. The range guards below are therefore necessary on both paths.
func TestGenerateBatchInsertForBareValuePlaceholders(t *testing.T) {
	text := generateTemporalTestOutput(t, `-- name: InsertEvent :exec
INSERT INTO events (rowid, note, seen) VALUES (chgen.arg('Rowid'), chgen.arg('Note'), chgen.arg('Seen'))`)
	for _, want := range []string{
		"q.conn.PrepareBatch(ctx, insertEventBatchSQL)",
		"batch.Append(arg.Rowid, arg.Note, arg.Seen)",
		"batch.Send()",
		`const insertEventBatchSQL = "INSERT INTO events (rowid, note, seen)"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("generated output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "q.conn.Exec(ctx, insertEventSQL, ") {
		t.Errorf("generated output still uses the text-interpolation Exec path:\n%s", text)
	}
	// The batch path binds a Nullable column from the pointer itself, so it
	// must not go through chgenNullableParam.
	if strings.Contains(text, "chgenNullableParam(arg.Note)") {
		t.Errorf("batch path must pass the pointer directly, not unwrap it:\n%s", text)
	}
}

// An INSERT whose VALUES tuple wraps a placeholder in an expression cannot use
// the batch path, because there is no column to append into. It keeps
// conn.Exec, and the guards still apply there.
func TestGenerateKeepsExecPathForExpressionValues(t *testing.T) {
	schema, err := schemaFromDDLErr(t, temporalTestSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: InsertEvent :exec
INSERT INTO events (dt3) VALUES (toDateTime64(chgen.arg('Dt3'), 3))`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(generated)
	if !strings.Contains(text, "q.conn.Exec(ctx, insertEventSQL, arg.Dt3)") {
		t.Errorf("expression VALUES must keep the Exec path:\n%s", text)
	}
	if strings.Contains(text, "PrepareBatch") {
		t.Errorf("expression VALUES must not use the batch path:\n%s", text)
	}
	// The Exec path is the one that saturates a far date to the 32-bit
	// DateTime range, so it needs the guard at least as much as the batch path.
	if !strings.Contains(text, `chgenGuardDateTime64(arg.Dt3, "Dt3")`) {
		t.Errorf("Exec path must still guard the temporal parameter:\n%s", text)
	}
}

// Every temporal parameter of a batch INSERT gets a range guard, because the
// driver reports no error for a value that the column cannot hold. Measured:
// a DateTime64(3) write of 2299-12-31 stores 1900-01-01 00:00:00.291 through
// the batch path and 2106-02-07 06:28:15 through conn.Exec; both return nil.
func TestGenerateBatchInsertGuardsTemporalColumns(t *testing.T) {
	text := generateTemporalTestOutput(t, `-- name: InsertEvent :exec
INSERT INTO events (rowid, d, d32, dt, dt3, dt9, dts, dtm, seen)
VALUES (chgen.arg('Rowid'), chgen.arg('D'), chgen.arg('D32'), chgen.arg('Dt'), chgen.arg('Dt3'), chgen.arg('Dt9'), chgen.arg('Dts'), chgen.arg('Dtm'), chgen.arg('Seen'))`)
	for _, want := range []string{
		`chgenGuardDate(arg.D, "D")`,
		`chgenGuardDate32(arg.D32, "D32")`,
		`chgenGuardDateTime(arg.Dt, "Dt")`,
		`chgenGuardDateTime64(arg.Dt3, "Dt3")`,
		// Precision 9 has its own, much narrower, upper limit.
		`chgenGuardDateTime64Nano(arg.Dt9, "Dt9")`,
		// Array elements and Map values are guarded one by one.
		"for _, chgenV0 := range arg.Dts",
		`chgenGuardDateTime64(chgenV0, "Dts")`,
		"for chgenK0, chgenV0 := range arg.Dtm",
		`chgenGuardDateTime64(chgenV0, "Dtm")`,
		// A Nullable temporal is guarded only when it is not nil.
		"if arg.Seen != nil",
		`chgenGuardDateTime64((*arg.Seen), "Seen")`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("generated output missing %q:\n%s", want, text)
		}
	}
	// A non-temporal parameter gets no guard.
	if strings.Contains(text, `arg.Rowid, "Rowid"`) {
		t.Errorf("a non-temporal parameter must not be guarded:\n%s", text)
	}
}

// A Date used as a Map KEY is guarded as well. The oracle findings
// map_date_decimal10_2, rand_12 and rand_39 are insert failures with code 62
// on such a key: the text path renders the key with the Go default time
// format, which the server cannot parse. The batch path removes that
// rendering, and the guard covers the range.
func TestGenerateGuardsMapKeyTemporal(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE keyed (rowid UInt32, v Map(Date, Int8));`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	queries, err := parseQueriesWithSchema(t, `-- name: InsertKeyed :exec
INSERT INTO keyed (rowid, v) VALUES (chgen.arg('Rowid'), chgen.arg('V'))`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	generated, err := Generate("querygen", queries)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(generated)
	for _, want := range []string{
		"for chgenK0 := range arg.V",
		`chgenGuardDate(chgenK0, "V")`,
		"q.conn.PrepareBatch(ctx, insertKeyedBatchSQL)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("generated output missing %q:\n%s", want, text)
		}
	}
}

// Scanned DateTime64 results pass through chgenCheckScannedTime. The driver
// converts DateTime64 through int64 nanoseconds, so a stored 2299-12-31
// arrives as 1715-06-12 with no error (measured, driver v2.47.0). The wrap is
// not reversible from the arriving value alone, so the cell becomes an
// explicit error rather than a plausible but wrong instant.
func TestGenerateScanCheckForTemporalResults(t *testing.T) {
	text := generateTemporalTestOutput(t, `-- name: ReadEvents :many
SELECT rowid AS rowid, dt3 AS dt3, dts AS dts, dtm AS dtm, seen AS seen FROM events`)
	for _, want := range []string{
		`chgenCheckScannedTime(row.Dt3, "Dt3")`,
		"for _, chgenV0 := range row.Dts",
		"for chgenK0, chgenV0 := range row.Dtm",
		"if row.Seen != nil",
		`chgenCheckScannedTime((*row.Seen), "Seen")`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("generated output missing %q:\n%s", want, text)
		}
	}
	one := generateTemporalTestOutput(t, `-- name: ReadOne :one
SELECT dt3 AS dt3 FROM events LIMIT 1`)
	if !strings.Contains(one, `chgenCheckScannedTime(result.Dt3, "Dt3")`) {
		t.Errorf(":one output missing the scan check:\n%s", one)
	}
}

// Date, Date32 and DateTime cannot exceed the int64 nanosecond limit, so they
// need no read-side check. An unnecessary check would add cost and noise for
// a wrap that cannot happen.
func TestGenerateNoScanCheckForNarrowTemporals(t *testing.T) {
	text := generateTemporalTestOutput(t, `-- name: ReadNarrow :many
SELECT d AS d, d32 AS d32, dt AS dt FROM events`)
	if strings.Contains(text, "chgenCheckScannedTime") {
		t.Errorf("Date, Date32 and DateTime need no read-side check:\n%s", text)
	}
}

// A time.Time parameter that travels through client-side text interpolation
// (an ALTER predicate, a SELECT predicate) is guarded too. That path formats
// the value as whole seconds inside the 32-bit DateTime range, so both a
// sub-second fraction and an out-of-range value would be silently distorted.
func TestGenerateTextBoundTemporalParamGuard(t *testing.T) {
	text := generateTemporalTestOutput(t, `-- name: DeleteBefore :exec
ALTER TABLE events DELETE WHERE dt3 < chgen.arg('Before')`)
	if !strings.Contains(text, `chgenGuardDateTime64(arg.Before, "Before")`) {
		t.Errorf("ALTER output missing the text-bind guard:\n%s", text)
	}
	sel := generateTemporalTestOutput(t, `-- name: CountSince :many
SELECT rowid AS rowid FROM events WHERE dt3 >= chgen.arg('Since')`)
	if !strings.Contains(sel, `chgenGuardDateTime64(arg.Since, "Since")`) {
		t.Errorf("SELECT output missing the text-bind guard:\n%s", sel)
	}
}

// The generated file carries the measured limits as named constants, so a
// reader of the output sees the numbers without opening chgen.
func TestGeneratedGuardConstantsCarryMeasuredNumbers(t *testing.T) {
	text := generateTemporalTestOutput(t, `-- name: InsertEvent :exec
INSERT INTO events (rowid, dt3) VALUES (chgen.arg('Rowid'), chgen.arg('Dt3'))`)
	for _, want := range []string{
		"chgenDateMaxUnix           = int64(5662310399)",
		"chgenDate32MinUnix         = int64(-2208988800)",
		"chgenDateTimeMaxUnix       = int64(4294967295)",
		"chgenDateTime64MaxUnix     = int64(9223372036)",
		"chgenDateTime64NanoMaxUnix = int64(9223372036)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("generated output missing the measured constant %q:\n%s", want, text)
		}
	}
}

// A query with no temporal parameter and no temporal result emits none of the
// temporal runtime helpers.
func TestGenerateOmitsTemporalHelpersWhenUnused(t *testing.T) {
	text := generateTemporalTestOutput(t, `-- name: ReadIDs :many
SELECT rowid AS rowid FROM events`)
	for _, unwanted := range []string{"chgenGuardDate", "chgenCheckScannedTime", "chgenDateMaxUnix"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("generated output should not contain %q when no temporal is used:\n%s", unwanted, text)
		}
	}
}
