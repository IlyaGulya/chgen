package engine

import (
	"strings"
	"testing"
)

const nullLiteralTestDDL = `
CREATE TABLE probe (
    flag UInt8,
    value Float64,
    selected UInt8,
    measure Float64,
    recorded_at DateTime64(3, 'UTC'),
    ` + "`NULL`" + ` Int32,
    arr Array(Int32),
    map_value Map(String, Int32),
    tuple_value Tuple(Int32, String)
) ENGINE = Memory;
CREATE TABLE null_sink (
    optional_measure Nullable(Float64)
) ENGINE = Memory;
`

func TestBareNullLiteralShapes(t *testing.T) {
	schema := schemaFromDDL(t, nullLiteralTestDDL)
	tests := []struct {
		expression string
		want       string
	}{
		{expression: "NULL", want: "Nullable(Nothing)"},
		{expression: "CAST(NULL, 'Nullable(Float64)')", want: "Nullable(Float64)"},
		{expression: "CAST(NULL AS Nullable(Float64))", want: "Nullable(Float64)"},
		{expression: "NULL IS NULL", want: "UInt8"},
		{expression: "NULL IS NOT NULL", want: "UInt8"},
		{expression: "if(flag = 1, NULL, value)", want: "Nullable(Float64)"},
		{expression: "ifNull(NULL, value)", want: "Float64"},
		{expression: "coalesce(NULL, value)", want: "Float64"},
		{expression: "ifNull(NULL, arr)", want: "Array(Int32)"},
		{expression: "coalesce(NULL, map_value)", want: "Map(String, Int32)"},
		{expression: "ifNull(NULL, tuple_value)", want: "Tuple(Int32, String)"},
		{expression: "CAST(if(flag = 1, NULL, NULL) AS Nullable(Int32))", want: "Nullable(Int32)"},
		{expression: "CAST(ifNull(NULL, NULL), 'Nullable(Float64)')", want: "Nullable(Float64)"},
		{expression: "CAST(coalesce(NULL, NULL) AS Nullable(Int32))", want: "Nullable(Int32)"},
		{expression: "CAST(arrayElement([NULL], 1) AS Nullable(Int32))", want: "Nullable(Int32)"},
		{expression: "CAST((NULL, 1) AS Tuple(Nullable(Int32), UInt8))", want: "Tuple(Nullable(Int32), UInt8)"},
		{expression: "'NULL'", want: "String"},
		{expression: "\"NULL\"", want: "Int32"},
		{expression: "`NULL`", want: "Int32"},
		{expression: "probe.NULL", want: "Int32"},
	}
	for _, test := range tests {
		got, err := inferTestExprType(t, schema, test.expression)
		if err != nil {
			t.Errorf("%s: %v", test.expression, err)
		} else if got != test.want {
			t.Errorf("type of %s = %s, want %s", test.expression, got, test.want)
		}
	}
}

func TestBareNullLiteralRefusesNonNullableBranchAndCastTargets(t *testing.T) {
	schema := schemaFromDDL(t, nullLiteralTestDDL)
	for _, expression := range []string{
		"if(flag = 1, NULL, arr)",
		"multiIf(flag = 1, NULL, arr)",
		"CASE WHEN flag = 1 THEN NULL ELSE arr END",
		"if(flag = 1, NULL, map_value)",
		"if(flag = 1, NULL, tuple_value)",
		"[NULL, arr]",
		"CAST(NULL, 'Float64')",
		"CAST(NULL AS Int32)",
		"CAST(NULL, 'Array(Int32)')",
		"CAST(NULL, 'Nullable(Array(Int32))')",
		"CAST((NULL), 'Float64')",
		"CAST((NULL) AS Int32)",
		"CAST((((NULL))) AS Float64)",
		"CAST(if(flag = 1, NULL, NULL) AS Int32)",
		"CAST(ifNull(NULL, NULL), 'Float64')",
		"CAST(coalesce(NULL, NULL) AS Int32)",
		"CAST(arrayElement([NULL], 1) AS Int32)",
	} {
		if got, err := inferTestExprType(t, schema, expression); err == nil {
			t.Errorf("type of %s = %s, want a refusal", expression, got)
		}
	}
}

func TestBareNullLiteralKeepsStandaloneResultFailClosed(t *testing.T) {
	_, err := parseQueriesWithDDL(t, nullLiteralTestDDL, "-- name: ReadBareNull :many\nSELECT NULL AS value FROM probe")
	if err == nil || !strings.Contains(err.Error(), "Nothing") {
		t.Fatalf("standalone NULL error = %v, want the missing Go type cause", err)
	}
}

func TestNullInsertSelectShapeParsesAndGenerates(t *testing.T) {
	queries := `-- name: ReadOptionalMeasure :many
SELECT CAST(NULL, 'Nullable(Float64)') AS optional_measure
FROM probe

-- name: RefreshOptionalMeasure :exec
INSERT INTO null_sink (optional_measure)
SELECT
    if(
        countIf(selected = 1) = 0,
        CAST(NULL, 'Nullable(Float64)'),
        sumIf(measure, selected = 1)
    ) AS optional_measure
FROM probe`
	parsed, err := parseQueriesWithDDL(t, nullLiteralTestDDL, queries)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 {
		t.Fatalf("query count = %d, want 2", len(parsed))
	}
	if got := parsed[0].Results[0].GoType; got != "*float64" {
		t.Fatalf("generated NULL duration Go type = %s, want *float64", got)
	}
	generated, err := Generate("nullcorpus", parsed)
	if err != nil {
		t.Fatal(err)
	}
	text := string(generated)
	for _, want := range []string{"OptionalMeasure *float64", "CAST(NULL, 'Nullable(Float64)')", "RefreshOptionalMeasure"} {
		if !strings.Contains(text, want) {
			t.Errorf("generated corpus has no %q", want)
		}
	}
}

func TestBareNullLiteralRuleMutationsFailClosed(t *testing.T) {
	schema := schemaFromDDL(t, nullLiteralTestDDL)
	check := func() error {
		for _, test := range []struct{ expression, want string }{
			{expression: "CAST(NULL, 'Nullable(Float64)')", want: "Nullable(Float64)"},
			{expression: "if(flag = 1, NULL, value)", want: "Nullable(Float64)"},
			{expression: "NULL IS NULL", want: "UInt8"},
			{expression: "\"NULL\"", want: "Int32"},
			{expression: "probe.NULL", want: "Int32"},
			{expression: "ifNull(NULL, arr)", want: "Array(Int32)"},
		} {
			got, err := inferTestExprType(t, schema, test.expression)
			if err != nil {
				return err
			}
			if got != test.want {
				return &nullLiteralMutationError{expression: test.expression, got: got, want: test.want}
			}
		}
		for _, expression := range []string{
			"if(flag = 1, NULL, arr)",
			"[NULL, arr]",
			"CAST(NULL, 'Float64')",
			"CAST((NULL) AS Float64)",
		} {
			if got, err := inferTestExprType(t, schema, expression); err == nil {
				return &nullLiteralMutationError{expression: expression, got: got, want: "refusal"}
			}
		}
		return nil
	}
	if err := check(); err != nil {
		t.Fatalf("production contract: %v", err)
	}

	originalNormalize := normalizeBareNullIdentifier
	originalBranch := untypedNullNeutralInBranches
	originalPeer := untypedNullRequiresNullablePeer
	originalEliminator := untypedNullNeutralInNullEliminators
	originalCast := bareNullCastRequiresNullableTarget
	mutations := map[string]func(){
		"missing parser normalization": func() { normalizeBareNullIdentifier = false },
		"missing branch neutral rule":  func() { untypedNullNeutralInBranches = false },
		"wide container branch rule":   func() { untypedNullRequiresNullablePeer = false },
		"missing eliminator rule":      func() { untypedNullNeutralInNullEliminators = false },
		"wide CAST target rule":        func() { bareNullCastRequiresNullableTarget = false },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			normalizeBareNullIdentifier = originalNormalize
			untypedNullNeutralInBranches = originalBranch
			untypedNullRequiresNullablePeer = originalPeer
			untypedNullNeutralInNullEliminators = originalEliminator
			bareNullCastRequiresNullableTarget = originalCast
			mutate()
			t.Cleanup(func() {
				normalizeBareNullIdentifier = originalNormalize
				untypedNullNeutralInBranches = originalBranch
				untypedNullRequiresNullablePeer = originalPeer
				untypedNullNeutralInNullEliminators = originalEliminator
				bareNullCastRequiresNullableTarget = originalCast
			})
			if err := check(); err == nil {
				t.Fatal("NULL literal contract accepted the rule mutation")
			}
		})
	}
}

type nullLiteralMutationError struct {
	expression string
	got        string
	want       string
}

func (e *nullLiteralMutationError) Error() string {
	return "type of " + e.expression + " = " + e.got + ", want " + e.want
}
