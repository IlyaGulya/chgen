//go:build execoracle

package engine

import (
	"fmt"
	"testing"

	"github.com/IlyaGulya/chgen/internal/conformance"
)

// buildNestedScopeValueProbes returns native value checks for each supported
// nested scope shape and for exact shadowing.
func buildNestedScopeValueProbes() []probe {
	return []probe{
		{
			name:   "probe_nested_scalar",
			chgen:  "-- name: NestedScalar :many\nSELECT rowid, ifNull((SELECT max(v) FROM c_bare_int32 WHERE rowid < 1000), toInt32(0)) AS result FROM c_bare_int32 WHERE rowid < 1000 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.NestedScalar(ctx, gen.NestedScalarParams{}) }",
			refSQL: "SELECT rowid, ifNull((SELECT max(v) FROM c_bare_int32 WHERE rowid < 1000), toInt32(0)) AS result FROM c_bare_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("int32"), col: 1, ncols: 2,
			note: "generated nested scalar value lane",
		},
		{
			name:   "probe_nested_exists",
			chgen:  "-- name: NestedExists :many\nSELECT outer_scope.rowid, EXISTS(SELECT 1 FROM c_bare_int32 AS inner_scope WHERE inner_scope.rowid = outer_scope.rowid AND inner_scope.rowid < 1000) AS result FROM c_bare_int32 AS outer_scope WHERE outer_scope.rowid < 1000 ORDER BY outer_scope.rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.NestedExists(ctx, gen.NestedExistsParams{}) }",
			refSQL: "SELECT outer_scope.rowid, EXISTS(SELECT 1 FROM c_bare_int32 AS inner_scope WHERE inner_scope.rowid = outer_scope.rowid AND inner_scope.rowid < 1000) AS result FROM c_bare_int32 AS outer_scope WHERE outer_scope.rowid < 1000 ORDER BY outer_scope.rowid",
			typ:    scalar("uint8"), col: 1, ncols: 2,
			note: "generated correlated EXISTS value lane",
		},
		{
			name:   "probe_nested_in",
			chgen:  "-- name: NestedIn :many\nSELECT outer_scope.rowid, outer_scope.rowid IN (SELECT inner_scope.rowid FROM c_bare_int32 AS inner_scope WHERE inner_scope.v > 0 AND inner_scope.rowid < 1000) AS result FROM c_bare_int32 AS outer_scope WHERE outer_scope.rowid < 1000 ORDER BY outer_scope.rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.NestedIn(ctx, gen.NestedInParams{}) }",
			refSQL: "SELECT outer_scope.rowid, outer_scope.rowid IN (SELECT inner_scope.rowid FROM c_bare_int32 AS inner_scope WHERE inner_scope.v > 0 AND inner_scope.rowid < 1000) AS result FROM c_bare_int32 AS outer_scope WHERE outer_scope.rowid < 1000 ORDER BY outer_scope.rowid",
			typ:    scalar("uint8"), col: 1, ncols: 2,
			note: "generated uncorrelated IN value lane",
		},
		{
			name:   "probe_nested_derived",
			chgen:  "-- name: NestedDerived :many\nSELECT nested.rowid, nested.v AS result FROM (SELECT rowid, v FROM c_bare_int32 WHERE rowid < 1000) AS nested ORDER BY nested.rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.NestedDerived(ctx, gen.NestedDerivedParams{}) }",
			refSQL: "SELECT nested.rowid, nested.v AS result FROM (SELECT rowid, v FROM c_bare_int32 WHERE rowid < 1000) AS nested ORDER BY nested.rowid",
			typ:    scalar("int32"), col: 1, ncols: 2,
			note: "generated derived relation value lane",
		},
		{
			name:   "probe_nested_shadow",
			chgen:  "-- name: NestedShadow :many\nSELECT same_name.rowid, ifNull((SELECT max(same_name.v) FROM c_bare_int32 AS same_name WHERE same_name.rowid < 1000), toInt32(0)) AS result FROM c_bare_int32 AS same_name WHERE same_name.rowid < 1000 ORDER BY same_name.rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.NestedShadow(ctx, gen.NestedShadowParams{}) }",
			refSQL: "SELECT same_name.rowid, ifNull((SELECT max(same_name.v) FROM c_bare_int32 AS same_name WHERE same_name.rowid < 1000), toInt32(0)) AS result FROM c_bare_int32 AS same_name WHERE same_name.rowid < 1000 ORDER BY same_name.rowid",
			typ:    scalar("int32"), col: 1, ncols: 2,
			note: "generated exact shadowing value lane",
		},
		{
			name:   "probe_using_left_unmatched",
			chgen:  "-- name: UsingLeftUnmatched :many\nSELECT id AS result FROM (SELECT toUInt64(2) AS id) AS l LEFT JOIN (SELECT toUInt32(3) AS id) AS r USING (id);",
			call:   "func(ctx context.Context) (any, error) { return q.UsingLeftUnmatched(ctx, gen.UsingLeftUnmatchedParams{}) }",
			refSQL: "SELECT id AS result FROM (SELECT toUInt64(2) AS id) AS l LEFT JOIN (SELECT toUInt32(3) AS id) AS r USING (id)",
			typ:    scalar("uint64"), col: 0, ncols: 1,
			note: "generated LEFT USING unmatched key lane",
		},
		{
			name:   "probe_using_right_unmatched",
			chgen:  "-- name: UsingRightUnmatched :many\nSELECT id AS result FROM (SELECT toUInt64(2) AS id) AS l RIGHT JOIN (SELECT toUInt32(3) AS id) AS r USING (id);",
			call:   "func(ctx context.Context) (any, error) { return q.UsingRightUnmatched(ctx, gen.UsingRightUnmatchedParams{}) }",
			refSQL: "SELECT id AS result FROM (SELECT toUInt64(2) AS id) AS l RIGHT JOIN (SELECT toUInt32(3) AS id) AS r USING (id)",
			typ:    scalar("uint64"), col: 0, ncols: 1,
			note: "generated RIGHT USING unmatched key lane",
		},
		{
			name:   "probe_using_full_unmatched",
			chgen:  "-- name: UsingFullUnmatched :many\nSELECT id AS result FROM (SELECT toUInt64(2) AS id) AS l FULL JOIN (SELECT toUInt32(3) AS id) AS r USING (id) ORDER BY result;",
			call:   "func(ctx context.Context) (any, error) { return q.UsingFullUnmatched(ctx, gen.UsingFullUnmatchedParams{}) }",
			refSQL: "SELECT id AS result FROM (SELECT toUInt64(2) AS id) AS l FULL JOIN (SELECT toUInt32(3) AS id) AS r USING (id) ORDER BY result",
			typ:    scalar("uint64"), col: 0, ncols: 1,
			note: "generated FULL USING left-biased unmatched key lane",
		},
		{
			name:   "probe_join_on_left_bias",
			chgen:  "-- name: JoinOnLeftBias :many\nSELECT value AS result FROM (SELECT toInt32(11) AS value, toUInt8(1) AS id) AS l JOIN (SELECT toInt16(22) AS value, toUInt8(1) AS id) AS r ON l.id = r.id;",
			call:   "func(ctx context.Context) (any, error) { return q.JoinOnLeftBias(ctx, gen.JoinOnLeftBiasParams{}) }",
			refSQL: "SELECT value AS result FROM (SELECT toInt32(11) AS value, toUInt8(1) AS id) AS l JOIN (SELECT toInt16(22) AS value, toUInt8(1) AS id) AS r ON l.id = r.id",
			typ:    scalar("int32"), col: 0, ncols: 1,
			note: "generated plain JOIN left-biased unqualified value lane",
		},
		{
			name:   "probe_nested_scalar_nullable_null",
			chgen:  "-- name: NestedScalarNullableNull :many\nSELECT (SELECT v FROM c_bare_int32 WHERE rowid = 999999 LIMIT 1) AS result;",
			call:   "func(ctx context.Context) (any, error) { return q.NestedScalarNullableNull(ctx, gen.NestedScalarNullableNullParams{}) }",
			refSQL: "SELECT (SELECT v FROM c_bare_int32 WHERE rowid = 999999 LIMIT 1) AS result",
			typ:    nullableT(scalar("int32")), col: 0, ncols: 1,
			note: "generated nullable scalar empty-result lane",
		},
		{
			name:   "probe_nested_scalar_nullable_value",
			chgen:  "-- name: NestedScalarNullableValue :many\nSELECT (SELECT toInt32(7)) AS result;",
			call:   "func(ctx context.Context) (any, error) { return q.NestedScalarNullableValue(ctx, gen.NestedScalarNullableValueParams{}) }",
			refSQL: "SELECT (SELECT toInt32(7)) AS result",
			typ:    nullableT(scalar("int32")), col: 0, ncols: 1,
			note: "generated nullable scalar value lane",
		},
		{
			name:   "probe_nested_scalar_limit_zero",
			chgen:  "-- name: NestedScalarLimitZero :many\nSELECT (SELECT v FROM c_bare_int32 LIMIT 0) AS result;",
			call:   "func(ctx context.Context) (any, error) { return q.NestedScalarLimitZero(ctx, gen.NestedScalarLimitZeroParams{}) }",
			refSQL: "SELECT (SELECT v FROM c_bare_int32 LIMIT 0) AS result",
			typ:    nullableT(scalar("int32")), col: 0, ncols: 1,
			note: "generated nullable scalar LIMIT zero lane",
		},
		{
			name:   "probe_nested_direct_scalar_cte",
			chgen:  "-- name: NestedDirectScalarCTE :many\nWITH toInt32(7) AS q SELECT q AS result;",
			call:   "func(ctx context.Context) (any, error) { return q.NestedDirectScalarCTE(ctx, gen.NestedDirectScalarCTEParams{}) }",
			refSQL: "WITH toInt32(7) AS q SELECT q AS result",
			typ:    scalar("int32"), col: 0, ncols: 1,
			note: "generated direct scalar CTE value lane",
		},
	}
}

func TestNestedScopeValueProbeLinks(t *testing.T) {
	if err := validateNestedScopeValueProbes(buildNestedScopeValueProbes()); err != nil {
		t.Fatal(err)
	}
	known := make(map[string]probe)
	for _, value := range buildNestedScopeValueProbes() {
		known[value.name] = value
	}
	for _, cell := range conformance.CurrentNestedScopeArtifact(MeasuredCHVersion).Cells {
		if cell.ValueProbe == "" {
			continue
		}
		if _, found := known[cell.ValueProbe]; !found {
			t.Errorf("nested scope cell %s links missing value probe %s", cell.ID, cell.ValueProbe)
		}
	}
}

func TestNestedScopeValueProbeMutationGate(t *testing.T) {
	probes := buildNestedScopeValueProbes()
	probes[0].refSQL = ""
	if err := validateNestedScopeValueProbes(probes); err == nil {
		t.Fatal("missing reference mutation passed")
	}
}

func TestEveryNestedScopeValueProbeDeletionFails(t *testing.T) {
	probes := buildNestedScopeValueProbes()
	for remove := range probes {
		mutated := append([]probe(nil), probes[:remove]...)
		mutated = append(mutated, probes[remove+1:]...)
		if err := validateNestedScopeValueProbes(mutated); err == nil {
			t.Errorf("deletion of %s passed", probes[remove].name)
		}
	}
}

func validateNestedScopeValueProbes(probes []probe) error {
	seen := make(map[string]bool, len(probes))
	for _, value := range probes {
		if value.name == "" || value.chgen == "" || value.call == "" || value.refSQL == "" || value.typ == nil || value.ncols == 0 {
			return fmt.Errorf("nested scope value probe is incomplete")
		}
		if seen[value.name] {
			return fmt.Errorf("nested scope value probe %s is duplicate", value.name)
		}
		seen[value.name] = true
	}
	for _, required := range []string{
		"probe_nested_scalar", "probe_nested_exists", "probe_nested_in", "probe_nested_derived", "probe_nested_shadow",
		"probe_using_left_unmatched", "probe_using_right_unmatched", "probe_using_full_unmatched",
		"probe_join_on_left_bias",
		"probe_nested_scalar_nullable_null", "probe_nested_scalar_nullable_value", "probe_nested_scalar_limit_zero",
		"probe_nested_direct_scalar_cte",
	} {
		if !seen[required] {
			return fmt.Errorf("nested scope value probe %s is missing", required)
		}
	}
	return nil
}
