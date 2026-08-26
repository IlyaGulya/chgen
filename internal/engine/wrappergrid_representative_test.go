package engine

// The reachability gates of the representative chooser.
//
// These tests need NO server. They guard the property that the regression
// restored: an entry whose accepted domain holds a container base must
// MEASURE one. Before that ticket the grid took its representatives by
// position in gridBases order, the containers are last in that list, and
// so every entry that also accepted a scalar spent both slots on scalars.
// The census then reported health for a class it could not address.
//
// A count alone cannot guard this. The old grid had 3978 cells and looked
// large while 204 (entry, wrapper) pairs were starved. The gates below
// therefore test the REACHABILITY of a kind, not the size of the census.

import (
	"strings"
	"testing"
)

// gridStarvedKinds reports the (entry, wrapper) pairs whose accepted
// domain holds a base of one kind while the chosen representatives hold
// none of that kind.
//
// It walks the fn and sized lanes only. Those are the two lanes that ask
// gridProbeColumnsFor for a domain-filtered candidate list, thus they are
// the two lanes that the chooser governs. The hof lane names its arrays
// directly and the op, simplestate and var lanes name their bases
// directly, so no cap can starve them.
func gridStarvedKinds(t *testing.T, kind string) []string {
	t.Helper()
	schema := gridTestSchema(t)
	var starved []string
	check := func(lane, name string, domain *argumentDomain) {
		for _, wrapper := range gridWrappers {
			candidates := gridProbeColumnsFor(domain, wrapper.key, schema)
			offered := false
			for _, cell := range candidates {
				if gridBaseKindOf(cell.base) == kind {
					offered = true
					break
				}
			}
			if !offered {
				continue
			}
			taken := false
			for _, cell := range gridRepresentativeCells(candidates, domain, schema) {
				if gridBaseKindOf(cell.base) == kind {
					taken = true
					break
				}
			}
			if !taken {
				starved = append(starved, lane+"/"+name+"/"+wrapper.key)
			}
		}
	}
	for _, name := range gridSortedKeys(functionRegistry) {
		spec := functionRegistry[name]
		if spec.gen == nil || gridRecipeIsNullary(*spec.gen) {
			continue
		}
		check("fn", name, spec.domain)
	}
	for _, name := range gridSortedKeys(sizedConstructors) {
		check("sized", name, sizedConstructors[name].domain)
	}
	return starved
}

// An offered base must be a measured base.
//
// This is the exact defect of the regression stated as a property. If a domain
// accepts a container and the chooser drops it, the cell exists in the
// alphabet but no measurement addresses it, and the golden then says
// nothing about that class while looking complete.
func TestGridChoosesEveryOfferedKind(t *testing.T) {
	// Every container base is its own kind, thus the loop names the
	// bases. A base that is added to gridBases without being named here
	// is caught by TestGridBaseKindAgreesWithTheContainerList instead.
	for _, kind := range []string{"arr", "tup", "map", "narr", "lcarr"} {
		starved := gridStarvedKinds(t, kind)
		if len(starved) == 0 {
			continue
		}
		shown := starved
		if len(shown) > 10 {
			shown = shown[:10]
		}
		t.Errorf("%d (entry, wrapper) pairs accept a %s base and measure none.\n"+
			"The chooser is discarding a class that the alphabet spells, thus the "+
			"census reports health for cells nobody measured.\nFirst offenders: %s",
			len(starved), kind, strings.Join(shown, " "))
	}
}

// The scalar cap must still bind.
//
// The cure for the starvation must not become "take every base". The
// whole base list per (entry, wrapper) pair would multiply the grid by
// eight and the census would stop being a reviewable number. This test
// holds the scalar kind to gridBasesPerCell and each container kind to
// one, which is the bound that the growth argument of the regression rests on.
func TestGridRepresentativeCapsStillBind(t *testing.T) {
	schema := gridTestSchema(t)
	check := func(lane, name string, domain *argumentDomain) {
		for _, wrapper := range gridWrappers {
			chosen := gridRepresentativeCells(
				gridProbeColumnsFor(domain, wrapper.key, schema), domain, schema)
			counts := map[string]int{}
			for _, cell := range chosen {
				counts[gridBaseKindOf(cell.base)]++
			}
			for kind, got := range counts {
				// The scalar kind keeps the two-representative rule.
				// Every other kind is one container base, and it takes
				// exactly one slot.
				limit := 1
				if kind == gridScalarKind {
					limit = gridBasesPerCell
				}
				if got > limit {
					t.Errorf("%s/%s/%s takes %d bases of kind %q; the cap is %d",
						lane, name, wrapper.key, got, kind, limit)
				}
			}
		}
	}
	for _, name := range gridSortedKeys(functionRegistry) {
		spec := functionRegistry[name]
		if spec.gen == nil || gridRecipeIsNullary(*spec.gen) {
			continue
		}
		check("fn", name, spec.domain)
	}
	for _, name := range gridSortedKeys(sizedConstructors) {
		check("sized", name, sizedConstructors[name].domain)
	}
}

// Every base of the grid must fall into a kind that the chooser knows.
//
// gridBaseKindOf answers "scalar" for anything it does not name. That
// default is right for a new scalar base and WRONG for a new container
// base: a new container would silently join the scalar kind, spend a
// scalar slot and lose its own. The list of container bases already
// exists as gridBaseIsContainer, thus the two can be compared and cannot
// drift apart in silence.
func TestGridBaseKindAgreesWithTheContainerList(t *testing.T) {
	for _, base := range gridBases {
		kind := gridBaseKindOf(base.key)
		isContainerKind := kind != gridScalarKind
		if isContainerKind != gridBaseIsContainer(base.key) {
			t.Errorf("base %q has kind %q but gridBaseIsContainer says %v; "+
				"a container that falls into the scalar kind loses its own slot",
				base.key, kind, gridBaseIsContainer(base.key))
		}
	}
}
