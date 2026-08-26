//go:build execoracle

package engine

import (
	"fmt"
	"testing"

	"github.com/IlyaGulya/chgen/internal/conformance"
)

const (
	setOperationUnionAllSQL       = "SELECT result FROM (SELECT toInt32(1) AS result UNION ALL SELECT toInt32(2) AS result) ORDER BY result"
	setOperationResultVectorSQL   = "SELECT first_id, first_num FROM (SELECT toUInt8(1) AS first_id, toInt16(-2) AS first_num UNION ALL SELECT toUInt32(3) AS ignored_id, toUInt16(4) AS ignored_num) ORDER BY first_id"
	setOperationPrecedenceSQL     = "SELECT result FROM (SELECT toUInt8(1) AS result UNION ALL SELECT toUInt16(2) INTERSECT SELECT toUInt32(2) AS result) ORDER BY result"
	setOperationLowCardinalitySQL = "SELECT result FROM (SELECT v AS result FROM c_lc_string WHERE rowid = 0 UNION ALL SELECT v AS result FROM c_lc_string WHERE rowid = 1) ORDER BY result"
)

func buildSetOperationValueProbes() []probe {
	return []probe{
		{name: "probe_set_union_all", chgen: "-- name: SetUnionAll :many\n" + setOperationUnionAllSQL + ";", call: "func(ctx context.Context) (any, error) { return q.SetUnionAll(ctx, gen.SetUnionAllParams{}) }", refSQL: setOperationUnionAllSQL, typ: scalar("int32"), col: 0, ncols: 1, note: "generated UNION ALL value lane"},
		{name: "probe_set_union_distinct", chgen: "-- name: SetUnionDistinct :many\nSELECT toInt32(1) AS result UNION DISTINCT SELECT toInt32(1) AS result;", call: "func(ctx context.Context) (any, error) { return q.SetUnionDistinct(ctx, gen.SetUnionDistinctParams{}) }", refSQL: "SELECT toInt32(1) AS result UNION DISTINCT SELECT toInt32(1) AS result", typ: scalar("int32"), col: 0, ncols: 1, note: "generated UNION DISTINCT value lane"},
		{name: "probe_set_intersect", chgen: "-- name: SetIntersect :many\nSELECT toInt32(1) AS result INTERSECT SELECT toInt32(1) AS result;", call: "func(ctx context.Context) (any, error) { return q.SetIntersect(ctx, gen.SetIntersectParams{}) }", refSQL: "SELECT toInt32(1) AS result INTERSECT SELECT toInt32(1) AS result", typ: scalar("int32"), col: 0, ncols: 1, note: "generated INTERSECT value lane"},
		{name: "probe_set_except", chgen: "-- name: SetExcept :many\nSELECT toInt32(1) AS result EXCEPT SELECT toInt32(2) AS result;", call: "func(ctx context.Context) (any, error) { return q.SetExcept(ctx, gen.SetExceptParams{}) }", refSQL: "SELECT toInt32(1) AS result EXCEPT SELECT toInt32(2) AS result", typ: scalar("int32"), col: 0, ncols: 1, note: "generated EXCEPT value lane"},
		{name: "probe_set_result_vector", chgen: "-- name: SetResultVector :many\n" + setOperationResultVectorSQL + ";", call: "func(ctx context.Context) (any, error) { return q.SetResultVector(ctx, gen.SetResultVectorParams{}) }", refSQL: setOperationResultVectorSQL, typ: scalar("int32"), col: 1, ncols: 2, note: "generated first-name and column-order lane"},
		{name: "probe_set_precedence", chgen: "-- name: SetPrecedence :many\n" + setOperationPrecedenceSQL + ";", call: "func(ctx context.Context) (any, error) { return q.SetPrecedence(ctx, gen.SetPrecedenceParams{}) }", refSQL: setOperationPrecedenceSQL, typ: scalar("uint32"), col: 0, ncols: 1, note: "generated INTERSECT precedence lane"},
		{name: "probe_set_associativity", chgen: "-- name: SetAssociativity :many\nSELECT toUInt8(1) AS result EXCEPT SELECT toUInt16(2) AS middle EXCEPT SELECT toUInt32(1) AS result;", call: "func(ctx context.Context) (any, error) { return q.SetAssociativity(ctx, gen.SetAssociativityParams{}) }", refSQL: "SELECT toUInt8(1) AS result EXCEPT SELECT toUInt16(2) AS middle EXCEPT SELECT toUInt32(1) AS result", typ: scalar("uint32"), col: 0, ncols: 1, note: "generated EXCEPT associativity lane"},
		{name: "probe_set_low_cardinality", chgen: "-- name: SetLowCardinality :many\n" + setOperationLowCardinalitySQL + ";", call: "func(ctx context.Context) (any, error) { return q.SetLowCardinality(ctx, gen.SetLowCardinalityParams{}) }", refSQL: setOperationLowCardinalitySQL, typ: scalar("string"), col: 0, ncols: 1, note: "generated LowCardinality set-result lane"},
		{name: "probe_cte_catalog_shadow", chgen: "-- name: CTECatalogShadow :many\nWITH a AS (SELECT v FROM c_bare_int32), c_bare_int32 AS (SELECT toInt16(7) AS v) SELECT v AS result FROM a;", call: "func(ctx context.Context) (any, error) { return q.CTECatalogShadow(ctx, gen.CTECatalogShadowParams{}) }", refSQL: "WITH a AS (SELECT v FROM c_bare_int32), c_bare_int32 AS (SELECT toInt16(7) AS v) SELECT v AS result FROM a", typ: scalar("int16"), col: 0, ncols: 1, note: "generated forward CTE catalog-shadow lane"},
		{name: "probe_cte_scalar_shadow", chgen: "-- name: CTEScalarShadow :many\nWITH toUInt64(100) AS x, inner_q AS (WITH x AS y, toInt16(7) AS x SELECT y AS v) SELECT v AS result FROM inner_q;", call: "func(ctx context.Context) (any, error) { return q.CTEScalarShadow(ctx, gen.CTEScalarShadowParams{}) }", refSQL: "WITH toUInt64(100) AS x, inner_q AS (WITH x AS y, toInt16(7) AS x SELECT y AS v) SELECT v AS result FROM inner_q", typ: scalar("int16"), col: 0, ncols: 1, note: "generated local scalar CTE shadow lane"},
		{name: "probe_cte_relation_shadow", chgen: "-- name: CTERelationShadow :many\nWITH q AS (SELECT toUInt64(100) AS v), inner_q AS (WITH a AS (SELECT v FROM q), q AS (SELECT toInt16(7) AS v) SELECT v FROM a) SELECT v AS result FROM inner_q;", call: "func(ctx context.Context) (any, error) { return q.CTERelationShadow(ctx, gen.CTERelationShadowParams{}) }", refSQL: "WITH q AS (SELECT toUInt64(100) AS v), inner_q AS (WITH a AS (SELECT v FROM q), q AS (SELECT toInt16(7) AS v) SELECT v FROM a) SELECT v AS result FROM inner_q", typ: scalar("int16"), col: 0, ncols: 1, note: "generated local relation CTE shadow lane"},
	}
}

func TestSetOperationValueProbeLinks(t *testing.T) {
	if err := validateSetOperationValueProbes(buildSetOperationValueProbes()); err != nil {
		t.Fatal(err)
	}
	known := make(map[string]bool)
	for _, value := range buildSetOperationValueProbes() {
		known[value.name] = true
	}
	for _, cell := range conformance.CurrentSetOperationArtifact(MeasuredCHVersion).Cells {
		if cell.ValueProbe != "" && !known[cell.ValueProbe] {
			t.Errorf("set-operation cell %s links missing probe %s", cell.ID, cell.ValueProbe)
		}
	}
}

func TestEverySetOperationValueProbeDeletionFails(t *testing.T) {
	probes := buildSetOperationValueProbes()
	for remove := range probes {
		mutated := append([]probe(nil), probes[:remove]...)
		mutated = append(mutated, probes[remove+1:]...)
		if err := validateSetOperationValueProbes(mutated); err == nil {
			t.Errorf("deletion of %s passed", probes[remove].name)
		}
	}
}

func TestSetOperationLowCardinalityProbeUsesGlobalOrder(t *testing.T) {
	for _, value := range buildSetOperationValueProbes() {
		if value.name != "probe_set_low_cardinality" {
			continue
		}
		if value.refSQL != setOperationLowCardinalitySQL {
			t.Fatalf("the LowCardinality set probe is not globally ordered: %s", value.refSQL)
		}
		if value.chgen != "-- name: SetLowCardinality :many\n"+setOperationLowCardinalitySQL+";" {
			t.Fatalf("the generated LowCardinality set query is not globally ordered: %s", value.chgen)
		}
		return
	}
	t.Fatal("the LowCardinality set probe is missing")
}

func TestSetOperationLowCardinalityProbeRejectsBranchLocalOrder(t *testing.T) {
	probes := buildSetOperationValueProbes()
	for index := range probes {
		if probes[index].name != "probe_set_low_cardinality" {
			continue
		}
		probes[index].refSQL = "SELECT v AS result FROM c_lc_string WHERE rowid = 0 UNION ALL SELECT v AS result FROM c_lc_string WHERE rowid = 1 ORDER BY result"
		probes[index].chgen = "-- name: SetLowCardinality :many\n" + probes[index].refSQL + ";"
		if err := validateSetOperationValueProbes(probes); err == nil {
			t.Fatal("the branch-local ORDER BY mutation passed")
		}
		return
	}
	t.Fatal("the LowCardinality set probe is missing")
}

func TestSetOperationOrderedProbesRejectBranchLocalOrder(t *testing.T) {
	mutations := map[string]string{
		"probe_set_union_all":     "SELECT toInt32(1) AS result UNION ALL SELECT toInt32(2) AS result ORDER BY result",
		"probe_set_result_vector": "SELECT toUInt8(1) AS first_id, toInt16(-2) AS first_num UNION ALL SELECT toUInt32(3) AS ignored_id, toUInt16(4) AS ignored_num ORDER BY first_id",
		"probe_set_precedence":    "SELECT toUInt8(1) AS result UNION ALL SELECT toUInt16(2) INTERSECT SELECT toUInt32(2) AS result ORDER BY result",
	}
	for name, branchLocal := range mutations {
		t.Run(name, func(t *testing.T) {
			probes := buildSetOperationValueProbes()
			for index := range probes {
				if probes[index].name != name {
					continue
				}
				probes[index].refSQL = branchLocal
				queryName := "SetUnionAll"
				if name == "probe_set_result_vector" {
					queryName = "SetResultVector"
				} else if name == "probe_set_precedence" {
					queryName = "SetPrecedence"
				}
				probes[index].chgen = "-- name: " + queryName + " :many\n" + branchLocal + ";"
				if err := validateSetOperationValueProbes(probes); err == nil {
					t.Fatal("the branch-local ORDER BY mutation passed")
				}
				return
			}
			t.Fatal("the ordered set probe is missing")
		})
	}
}

func validateSetOperationValueProbes(probes []probe) error {
	seen := make(map[string]bool, len(probes))
	for _, value := range probes {
		if value.name == "" || value.chgen == "" || value.call == "" || value.refSQL == "" || value.typ == nil || value.ncols == 0 || seen[value.name] {
			return fmt.Errorf("set-operation value probe is incomplete or duplicate")
		}
		seen[value.name] = true
		if value.name == "probe_set_union_all" && (value.refSQL != setOperationUnionAllSQL || value.chgen != "-- name: SetUnionAll :many\n"+setOperationUnionAllSQL+";") {
			return fmt.Errorf("set-operation value probe %s must use a global ORDER BY", value.name)
		}
		if value.name == "probe_set_result_vector" && (value.refSQL != setOperationResultVectorSQL || value.chgen != "-- name: SetResultVector :many\n"+setOperationResultVectorSQL+";") {
			return fmt.Errorf("set-operation value probe %s must use a global ORDER BY", value.name)
		}
		if value.name == "probe_set_precedence" && (value.refSQL != setOperationPrecedenceSQL || value.chgen != "-- name: SetPrecedence :many\n"+setOperationPrecedenceSQL+";") {
			return fmt.Errorf("set-operation value probe %s must use a global ORDER BY", value.name)
		}
		if value.name == "probe_set_low_cardinality" && (value.refSQL != setOperationLowCardinalitySQL || value.chgen != "-- name: SetLowCardinality :many\n"+setOperationLowCardinalitySQL+";") {
			return fmt.Errorf("set-operation value probe %s must use a global ORDER BY", value.name)
		}
	}
	for _, required := range []string{"probe_set_union_all", "probe_set_union_distinct", "probe_set_intersect", "probe_set_except", "probe_set_result_vector", "probe_set_precedence", "probe_set_associativity", "probe_set_low_cardinality", "probe_cte_catalog_shadow", "probe_cte_scalar_shadow", "probe_cte_relation_shadow"} {
		if !seen[required] {
			return fmt.Errorf("set-operation value probe %s is missing", required)
		}
	}
	return nil
}
