//go:build execoracle

package engine

import (
	"fmt"
	"testing"

	"github.com/IlyaGulya/chgen/internal/conformance"
)

const finalSetProbeSQL = "SELECT result FROM (SELECT value AS result FROM final_probe_rows FINAL UNION ALL SELECT toInt32(7) AS result) ORDER BY result"

func buildFinalValueProbes() []probe {
	return []probe{
		{name: "probe_final_replacing", chgen: "-- name: FinalReplacing :many\nSELECT value AS result FROM final_probe_rows FINAL ORDER BY id;", call: "func(ctx context.Context) (any, error) { return q.FinalReplacing(ctx, gen.FinalReplacingParams{}) }", refSQL: "SELECT value AS result FROM final_probe_rows FINAL ORDER BY id", typ: scalar("int32"), col: 0, ncols: 1, note: "generated ReplacingMergeTree FINAL value lane"},
		{name: "probe_final_summing", chgen: "-- name: FinalSumming :many\nSELECT value AS result FROM final_probe_summing FINAL ORDER BY id;", call: "func(ctx context.Context) (any, error) { return q.FinalSumming(ctx, gen.FinalSummingParams{}) }", refSQL: "SELECT value AS result FROM final_probe_summing FINAL ORDER BY id", typ: scalar("int32"), col: 0, ncols: 1, note: "generated SummingMergeTree FINAL value lane"},
		{name: "probe_final_collapsing", chgen: "-- name: FinalCollapsing :many\nSELECT value AS result FROM final_probe_collapsing FINAL ORDER BY id;", call: "func(ctx context.Context) (any, error) { return q.FinalCollapsing(ctx, gen.FinalCollapsingParams{}) }", refSQL: "SELECT value AS result FROM final_probe_collapsing FINAL ORDER BY id", typ: scalar("int32"), col: 0, ncols: 1, note: "generated CollapsingMergeTree FINAL value lane"},
		{name: "probe_final_join", chgen: "-- name: FinalJoin :many\nSELECT o.value AS result FROM final_probe_rows AS r FINAL INNER JOIN final_probe_other AS o FINAL ON r.id = o.id ORDER BY r.id;", call: "func(ctx context.Context) (any, error) { return q.FinalJoin(ctx, gen.FinalJoinParams{}) }", refSQL: "SELECT o.value AS result FROM final_probe_rows AS r FINAL INNER JOIN final_probe_other AS o FINAL ON r.id = o.id ORDER BY r.id", typ: scalar("int16"), col: 0, ncols: 1, note: "generated two-input FINAL value lane"},
		{name: "probe_final_cte", chgen: "-- name: FinalCTE :many\nWITH q AS (SELECT id, value FROM final_probe_rows FINAL) SELECT value AS result FROM q ORDER BY id;", call: "func(ctx context.Context) (any, error) { return q.FinalCTE(ctx, gen.FinalCTEParams{}) }", refSQL: "WITH q AS (SELECT id, value FROM final_probe_rows FINAL) SELECT value AS result FROM q ORDER BY id", typ: scalar("int32"), col: 0, ncols: 1, note: "generated CTE FINAL value lane"},
		{name: "probe_final_set", chgen: "-- name: FinalSet :many\n" + finalSetProbeSQL + ";", call: "func(ctx context.Context) (any, error) { return q.FinalSet(ctx, gen.FinalSetParams{}) }", refSQL: finalSetProbeSQL, typ: scalar("int32"), col: 0, ncols: 1, note: "generated set-branch FINAL value lane"},
	}
}

func TestFinalValueProbeLinks(t *testing.T) {
	if err := validateFinalValueProbes(buildFinalValueProbes()); err != nil {
		t.Fatal(err)
	}
	known := make(map[string]bool)
	for _, value := range buildFinalValueProbes() {
		known[value.name] = true
	}
	for _, cell := range conformance.CurrentFinalArtifact(MeasuredCHVersion).Cells {
		if cell.ValueProbe != "" && !known[cell.ValueProbe] {
			t.Errorf("FINAL cell %s links missing probe %s", cell.ID, cell.ValueProbe)
		}
	}
}

func TestEveryFinalValueProbeDeletionFails(t *testing.T) {
	probes := buildFinalValueProbes()
	for remove := range probes {
		mutated := append([]probe(nil), probes[:remove]...)
		mutated = append(mutated, probes[remove+1:]...)
		if err := validateFinalValueProbes(mutated); err == nil {
			t.Errorf("deletion of %s passed", probes[remove].name)
		}
	}
}

func TestFinalSetProbeUsesGlobalOrder(t *testing.T) {
	for _, value := range buildFinalValueProbes() {
		if value.name != "probe_final_set" {
			continue
		}
		if value.refSQL != finalSetProbeSQL {
			t.Fatalf("the FINAL set probe is not globally ordered: %s", value.refSQL)
		}
		if value.chgen != "-- name: FinalSet :many\n"+finalSetProbeSQL+";" {
			t.Fatalf("the generated FINAL set query is not globally ordered: %s", value.chgen)
		}
		return
	}
	t.Fatal("the FINAL set probe is missing")
}

func TestFinalSetProbeRejectsBranchLocalOrder(t *testing.T) {
	probes := buildFinalValueProbes()
	for index := range probes {
		if probes[index].name != "probe_final_set" {
			continue
		}
		probes[index].refSQL = "SELECT value AS result FROM final_probe_rows FINAL UNION ALL SELECT toInt32(7) AS result ORDER BY result"
		probes[index].chgen = "-- name: FinalSet :many\n" + probes[index].refSQL + ";"
		if err := validateFinalValueProbes(probes); err == nil {
			t.Fatal("the branch-local ORDER BY mutation passed")
		}
		return
	}
	t.Fatal("the FINAL set probe is missing")
}

func validateFinalValueProbes(probes []probe) error {
	seen := make(map[string]bool, len(probes))
	for _, value := range probes {
		if value.name == "" || value.chgen == "" || value.call == "" || value.refSQL == "" || value.typ == nil || value.ncols == 0 || seen[value.name] {
			return fmt.Errorf("FINAL value probe is incomplete or duplicate")
		}
		seen[value.name] = true
		if value.name == "probe_final_set" && (value.refSQL != finalSetProbeSQL || value.chgen != "-- name: FinalSet :many\n"+finalSetProbeSQL+";") {
			return fmt.Errorf("FINAL value probe %s must use a global ORDER BY", value.name)
		}
	}
	for _, required := range []string{"probe_final_replacing", "probe_final_summing", "probe_final_collapsing", "probe_final_join", "probe_final_cte", "probe_final_set"} {
		if !seen[required] {
			return fmt.Errorf("FINAL value probe %s is missing", required)
		}
	}
	return nil
}
