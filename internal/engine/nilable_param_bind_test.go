package engine

import (
	"strings"
	"testing"
)

// These tests pin the nil-safety of a Nullable parameter on the conn.Exec
// text path.
//
// Some Go types that the resolver selects declare Value() on the VALUE
// receiver, thus they satisfy database/sql/driver.Valuer only as a value.
// The clickhouse-go text path calls that method through the interface. If
// the generated code gives it a TYPED nil pointer, the call dereferences
// nil and the process panics. Measured with clickhouse-go v2.47.0 against
// ClickHouse 25.8.29.51, an INSERT through conn.Exec with a typed nil:
//
//	*uuid.UUID            panic: value method uuid.UUID.Value called
//	                      using nil *UUID pointer
//	*decimal.Decimal      panic: value method decimal.Decimal.Value
//	                      called using nil *Decimal pointer
//	*time.Time            no panic; the driver handles it
//	*string               no panic; the driver handles it
//	*net.IP               no panic; net.IP is itself a slice
//	untyped nil           no panic; stores NULL correctly
//
// The native PrepareBatch/Append path panics for NONE of these.
//
// The generated code is safe because it never hands a typed nil to the
// driver: generatedParamArg wraps every pointer-typed parameter in
// chgenNullableParam, which turns a nil pointer into an UNTYPED nil. The
// measurement above shows that untyped nil is the safe form.
//
// That safety is an invariant between two separate places: goType() gives
// every Nullable scalar a "*" prefix, and generatedParamArg wraps exactly
// the "*"-prefixed types. If either side stops agreeing, a typed nil
// reaches the driver again and the panic comes back. A panic is worse
// than a wrong answer, thus these tests lock the invariant.

// TestNullableParamsAreWrappedOnExecPath checks that every Nullable
// parameter of an exec query goes through chgenNullableParam, on the
// batch path and on the text path alike.
func TestNullableParamsAreWrappedOnExecPath(t *testing.T) {
	schema, err := schemaFromDDLErr(t, `CREATE TABLE things
(
    id UInt32,
    nu Nullable(UUID),
    nd Nullable(Decimal(18, 4)),
    nt Nullable(DateTime64(3)),
    ns Nullable(String)
) ENGINE = MergeTree ORDER BY id;`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}

	// Each SQL below binds a Nullable column through a placeholder that an
	// expression wraps, thus the generator cannot use the native batch
	// path and must emit conn.Exec. This is the shape that panics if the
	// argument is a typed nil.
	cases := []struct {
		name string
		sql  string
	}{
		{"uuid wrapped in a function", "INSERT INTO things (id, nu) VALUES (?, toUUIDOrNull(?))"},
		{"decimal wrapped in a function", "INSERT INTO things (id, nd) VALUES (?, toDecimal64OrNull(?, 4))"},
		{"alter update binds a uuid", "ALTER TABLE things UPDATE nu = ? WHERE id = 1"},
		{"alter update binds a decimal", "ALTER TABLE things UPDATE nd = ? WHERE id = 1"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			query := Query{Name: "Probe", Command: CommandExec, SQL: testCase.sql}
			if err := resolveQuery(&query, schema); err != nil {
				t.Fatalf("resolveQuery() error = %v", err)
			}
			if query.batchInsert {
				// The batch path is nil-safe, thus it needs no wrapper.
				// If a shape moves to that path the test must know.
				t.Skipf("shape uses the native batch path")
			}
			for _, param := range query.Params {
				if !strings.HasPrefix(param.GoType, "*") {
					continue
				}
				arg := generatedParamArg(param)
				want := "chgenNullableParam(arg." + param.GoName + ")"
				if arg != want {
					t.Fatalf("parameter %s of type %s binds as %q, want %q; a typed nil panics the driver",
						param.GoName, param.GoType, arg, want)
				}
			}
		})
	}
}

// TestNullableScalarsGetPointerGoTypes checks the other half of the
// invariant. generatedParamArg wraps a parameter only when its Go type
// starts with "*". If a Nullable scalar ever loses that prefix, the
// wrapper silently stops applying and the typed-nil panic returns.
func TestNullableScalarsGetPointerGoTypes(t *testing.T) {
	// valuerTypes declare Value() on the value receiver, thus a typed nil
	// pointer to one of them panics the driver on the text path.
	valuerTypes := map[string]string{
		"Nullable(UUID)":           "*uuid.UUID",
		"Nullable(Decimal(18, 4))": "*decimal.Decimal",
		"Nullable(DateTime64(3))":  "*time.Time",
		"Nullable(String)":         "*string",
		"Nullable(IPv6)":           "*net.IP",
	}

	for columnType, want := range valuerTypes {
		t.Run(columnType, func(t *testing.T) {
			schema, err := schemaFromDDLErr(t, "CREATE TABLE t (c "+columnType+") ENGINE = MergeTree ORDER BY tuple();")
			if err != nil {
				t.Fatalf("schemaFromDDLErr() error = %v", err)
			}
			column, ok := schema.Tables["t"].Columns["c"]
			if !ok {
				t.Fatalf("column c is missing")
			}
			got, err := goType(column.Type)
			if err != nil {
				t.Fatalf("goType() error = %v", err)
			}
			if got != want {
				t.Fatalf("goType(%s) = %q, want %q", columnType, got, want)
			}
			if !strings.HasPrefix(got, "*") {
				t.Fatalf("goType(%s) = %q has no pointer prefix, thus generatedParamArg will not wrap it", columnType, got)
			}
		})
	}
}
