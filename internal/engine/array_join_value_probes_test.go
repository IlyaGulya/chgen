//go:build execoracle

package engine

import (
	"fmt"
	"testing"

	"github.com/IlyaGulya/chgen/internal/conformance"
)

const arrayJoinSetProbeSQL = "SELECT result FROM (SELECT item AS result FROM array_join_probe_rows ARRAY JOIN values AS item UNION ALL SELECT toInt64(7) AS result) ORDER BY result"

func buildArrayJoinValueProbes() []probe {
	return []probe{
		{name: "probe_array_join_bare", chgen: "-- name: ArrayJoinBare :many\nSELECT values AS result FROM array_join_probe_rows ARRAY JOIN values ORDER BY id, result;", call: "func(ctx context.Context) (any, error) { return q.ArrayJoinBare(ctx, gen.ArrayJoinBareParams{}) }", refSQL: "SELECT values AS result FROM array_join_probe_rows ARRAY JOIN values ORDER BY id, result", typ: scalar("int32"), col: 0, ncols: 1, note: "generated bare ARRAY JOIN value lane"},
		{name: "probe_array_join_alias", chgen: "-- name: ArrayJoinAlias :many\nSELECT values AS original, item AS result FROM array_join_probe_rows ARRAY JOIN values AS item ORDER BY id, result;", call: "func(ctx context.Context) (any, error) { return q.ArrayJoinAlias(ctx, gen.ArrayJoinAliasParams{}) }", refSQL: "SELECT values AS original, item AS result FROM array_join_probe_rows ARRAY JOIN values AS item ORDER BY id, result", typ: scalar("int32"), col: 1, ncols: 2, note: "generated ARRAY JOIN alias value lane"},
		{name: "probe_array_join_left", chgen: "-- name: ArrayJoinLeft :many\nSELECT id, item AS result FROM array_join_probe_rows LEFT ARRAY JOIN empty_values AS item ORDER BY id;", call: "func(ctx context.Context) (any, error) { return q.ArrayJoinLeft(ctx, gen.ArrayJoinLeftParams{}) }", refSQL: "SELECT id, item AS result FROM array_join_probe_rows LEFT ARRAY JOIN empty_values AS item ORDER BY id", typ: scalar("uint32"), col: 1, ncols: 2, note: "generated LEFT ARRAY JOIN default value lane"},
		{name: "probe_array_join_multiple", chgen: "-- name: ArrayJoinMultiple :many\nSELECT a, b AS result FROM array_join_probe_rows ARRAY JOIN values AS a, peers AS b ORDER BY id, a;", call: "func(ctx context.Context) (any, error) { return q.ArrayJoinMultiple(ctx, gen.ArrayJoinMultipleParams{}) }", refSQL: "SELECT a, b AS result FROM array_join_probe_rows ARRAY JOIN values AS a, peers AS b ORDER BY id, a", typ: scalar("uint16"), col: 1, ncols: 2, note: "generated multiple ARRAY JOIN value lane"},
		{name: "probe_array_join_nullable", chgen: "-- name: ArrayJoinNullable :many\nSELECT item AS result FROM array_join_probe_rows ARRAY JOIN nullable_values AS item ORDER BY id, isNull(item), item;", call: "func(ctx context.Context) (any, error) { return q.ArrayJoinNullable(ctx, gen.ArrayJoinNullableParams{}) }", refSQL: "SELECT item AS result FROM array_join_probe_rows ARRAY JOIN nullable_values AS item ORDER BY id, isNull(item), item", typ: nullableT(scalar("int16")), col: 0, ncols: 1, note: "generated nullable ARRAY JOIN value lane"},
		{name: "probe_array_join_low_cardinality", chgen: "-- name: ArrayJoinLowCardinality :many\nSELECT item AS result FROM array_join_probe_rows ARRAY JOIN low_values AS item ORDER BY id, result;", call: "func(ctx context.Context) (any, error) { return q.ArrayJoinLowCardinality(ctx, gen.ArrayJoinLowCardinalityParams{}) }", refSQL: "SELECT item AS result FROM array_join_probe_rows ARRAY JOIN low_values AS item ORDER BY id, result", typ: scalar("string"), col: 0, ncols: 1, note: "generated LowCardinality ARRAY JOIN value lane"},
		{name: "probe_array_join_tuple", chgen: "-- name: ArrayJoinTuple :many\nSELECT item.x AS x, item.y AS result FROM array_join_probe_rows ARRAY JOIN tuple_values AS item ORDER BY id, x;", call: "func(ctx context.Context) (any, error) { return q.ArrayJoinTuple(ctx, gen.ArrayJoinTupleParams{}) }", refSQL: "SELECT item.x AS x, item.y AS result FROM array_join_probe_rows ARRAY JOIN tuple_values AS item ORDER BY id, x", typ: nullableT(scalar("string")), col: 1, ncols: 2, note: "generated Tuple ARRAY JOIN value lane"},
		{name: "probe_array_join_nested", chgen: "-- name: ArrayJoinNested :many\nSELECT nested_values.k AS k, nested_values.v AS result FROM array_join_probe_rows ARRAY JOIN nested_values ORDER BY id, k;", call: "func(ctx context.Context) (any, error) { return q.ArrayJoinNested(ctx, gen.ArrayJoinNestedParams{}) }", refSQL: "SELECT nested_values.k AS k, nested_values.v AS result FROM array_join_probe_rows ARRAY JOIN nested_values ORDER BY id, k", typ: scalar("string"), col: 1, ncols: 2, note: "generated Nested ARRAY JOIN value lane"},
		{name: "probe_array_join_map", chgen: "-- name: ArrayJoinMap :many\nSELECT item.1 AS key, item.2 AS result FROM array_join_probe_rows ARRAY JOIN mapped AS item ORDER BY id, key;", call: "func(ctx context.Context) (any, error) { return q.ArrayJoinMap(ctx, gen.ArrayJoinMapParams{}) }", refSQL: "SELECT item.1 AS key, item.2 AS result FROM array_join_probe_rows ARRAY JOIN mapped AS item ORDER BY id, key", typ: scalar("uint16"), col: 1, ncols: 2, note: "generated Map ARRAY JOIN value lane"},
		{name: "probe_array_join_param", chgen: "-- name: ArrayJoinParam :many\nSELECT item AS result FROM array_join_probe_rows ARRAY JOIN values AS item WHERE item > chgen.arg('Minimum') ORDER BY result;", call: "func(ctx context.Context) (any, error) { return q.ArrayJoinParam(ctx, gen.ArrayJoinParamParams{Minimum: 15}) }", refSQL: "SELECT item AS result FROM array_join_probe_rows ARRAY JOIN values AS item WHERE item > 15 ORDER BY result", typ: scalar("int32"), col: 0, ncols: 1, note: "generated ARRAY JOIN parameter value lane"},
		{name: "probe_array_join_set", chgen: "-- name: ArrayJoinSet :many\n" + arrayJoinSetProbeSQL + ";", call: "func(ctx context.Context) (any, error) { return q.ArrayJoinSet(ctx, gen.ArrayJoinSetParams{}) }", refSQL: arrayJoinSetProbeSQL, typ: scalar("int64"), col: 0, ncols: 1, note: "generated set-operation ARRAY JOIN value lane"},
		{name: "probe_array_join_empty_anonymous", chgen: "-- name: ArrayJoinEmptyAnonymous :many\nSELECT id AS result FROM array_join_probe_rows ARRAY JOIN [] ORDER BY id;", call: "func(ctx context.Context) (any, error) { return q.ArrayJoinEmptyAnonymous(ctx, gen.ArrayJoinEmptyAnonymousParams{}) }", refSQL: "SELECT id AS result FROM array_join_probe_rows ARRAY JOIN [] ORDER BY id", typ: scalar("uint8"), col: 0, ncols: 1, note: "generated anonymous empty ARRAY JOIN zero-row lane"},
		{name: "probe_array_join_sequential_visibility", chgen: "-- name: ArrayJoinSequentialVisibility :many\nSELECT a, b AS result FROM array_join_probe_rows ARRAY JOIN values AS a ARRAY JOIN [a] AS b ORDER BY id, a;", call: "func(ctx context.Context) (any, error) { return q.ArrayJoinSequentialVisibility(ctx, gen.ArrayJoinSequentialVisibilityParams{}) }", refSQL: "SELECT a, b AS result FROM array_join_probe_rows ARRAY JOIN values AS a ARRAY JOIN [a] AS b ORDER BY id, a", typ: scalar("int32"), col: 1, ncols: 2, note: "generated sequential ARRAY JOIN visibility lane"},
		{name: "probe_array_join_sequential_cardinality", chgen: "-- name: ArrayJoinSequentialCardinality :many\nSELECT a, b AS result FROM array_join_probe_rows ARRAY JOIN values AS a ARRAY JOIN peers AS b ORDER BY id, a, b;", call: "func(ctx context.Context) (any, error) { return q.ArrayJoinSequentialCardinality(ctx, gen.ArrayJoinSequentialCardinalityParams{}) }", refSQL: "SELECT a, b AS result FROM array_join_probe_rows ARRAY JOIN values AS a ARRAY JOIN peers AS b ORDER BY id, a, b", typ: scalar("uint16"), col: 1, ncols: 2, note: "generated sequential ARRAY JOIN cross-product lane"},
	}
}

func TestArrayJoinValueProbeLinks(t *testing.T) {
	if err := validateArrayJoinValueProbes(buildArrayJoinValueProbes()); err != nil {
		t.Fatal(err)
	}
	known := make(map[string]bool)
	for _, value := range buildArrayJoinValueProbes() {
		known[value.name] = true
	}
	for _, cell := range conformance.CurrentArrayJoinArtifact(MeasuredCHVersion).Cells {
		if cell.ValueProbe != "" && !known[cell.ValueProbe] {
			t.Errorf("ARRAY JOIN cell %s links missing probe %s", cell.ID, cell.ValueProbe)
		}
	}
}

func TestEveryArrayJoinValueProbeDeletionFails(t *testing.T) {
	probes := buildArrayJoinValueProbes()
	for remove := range probes {
		mutated := append([]probe(nil), probes[:remove]...)
		mutated = append(mutated, probes[remove+1:]...)
		if err := validateArrayJoinValueProbes(mutated); err == nil {
			t.Errorf("deletion of %s passed", probes[remove].name)
		}
	}
}

func TestArrayJoinSetProbeUsesGlobalOrder(t *testing.T) {
	for _, value := range buildArrayJoinValueProbes() {
		if value.name != "probe_array_join_set" {
			continue
		}
		if value.refSQL != arrayJoinSetProbeSQL {
			t.Fatalf("the ARRAY JOIN set probe is not globally ordered: %s", value.refSQL)
		}
		if value.chgen != "-- name: ArrayJoinSet :many\n"+arrayJoinSetProbeSQL+";" {
			t.Fatalf("the generated ARRAY JOIN set query is not globally ordered: %s", value.chgen)
		}
		return
	}
	t.Fatal("the ARRAY JOIN set probe is missing")
}

func TestArrayJoinSetProbeRejectsBranchLocalOrder(t *testing.T) {
	probes := buildArrayJoinValueProbes()
	for index := range probes {
		if probes[index].name != "probe_array_join_set" {
			continue
		}
		probes[index].refSQL = "SELECT item AS result FROM array_join_probe_rows ARRAY JOIN values AS item UNION ALL SELECT toInt64(7) AS result ORDER BY result"
		probes[index].chgen = "-- name: ArrayJoinSet :many\n" + probes[index].refSQL + ";"
		if err := validateArrayJoinValueProbes(probes); err == nil {
			t.Fatal("the branch-local ORDER BY mutation passed")
		}
		return
	}
	t.Fatal("the ARRAY JOIN set probe is missing")
}

func validateArrayJoinValueProbes(probes []probe) error {
	seen := make(map[string]bool, len(probes))
	for _, value := range probes {
		if value.name == "" || value.chgen == "" || value.call == "" || value.refSQL == "" || value.typ == nil || value.ncols == 0 || seen[value.name] {
			return fmt.Errorf("ARRAY JOIN value probe is incomplete or duplicate")
		}
		seen[value.name] = true
		if value.name == "probe_array_join_set" && (value.refSQL != arrayJoinSetProbeSQL || value.chgen != "-- name: ArrayJoinSet :many\n"+arrayJoinSetProbeSQL+";") {
			return fmt.Errorf("ARRAY JOIN value probe %s must use a global ORDER BY", value.name)
		}
	}
	artifact := conformance.CurrentArrayJoinArtifact(MeasuredCHVersion)
	for _, cell := range artifact.Cells {
		if cell.ValueProbe != "" && !seen[cell.ValueProbe] {
			return fmt.Errorf("ARRAY JOIN value probe %s is missing", cell.ValueProbe)
		}
	}
	return nil
}
