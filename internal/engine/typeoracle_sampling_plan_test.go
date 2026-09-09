//go:build fuzzoracle

package engine

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	currentSamplingPlanID = "current-combined-v1"
	samplingRetryLimit    = 512
	combinatorQuota       = 100
)

type samplingPlanStats struct {
	Attempts map[string]int
	Accepted map[string]int
	Fallback map[string]int
}

type samplingRegistryShape struct {
	Scalar           int
	Aggregate        int
	Window           int
	IllegalScalar    int
	IllegalAggregate int
}

var samplingRegistryBaseline = samplingRegistryShape{
	Scalar: 149, Aggregate: 38, Window: 7, IllegalScalar: 124, IllegalAggregate: 22,
}

// currentScalarSamplingExpansion names the measured scalar classes that were
// added after the first current-combined-v1 shape was recorded. The array
// entries come from the dedicated higher-order family. toYYYYMMDD comes from
// the conversion family.
var currentScalarSamplingExpansion = []string{
	"arrayall", "arrayavg", "arraycount", "arraycumsum", "arraycumsumnonnegative",
	"arrayfill", "arrayfilter", "arrayfirst", "arrayfirstindex", "arrayfirstornull",
	"arrayfold", "arraylast", "arraylastindex", "arraylastornull", "arraymap",
	"arraymax", "arraymin", "arraypartialreversesort", "arraypartialsort", "arrayproduct",
	"arrayreversefill", "arrayreversesort", "arrayreversesplit", "arraysplit", "arraysum",
	"toyyyymm", "toyyyymmdd", "toyyyymmddhhmmss",
	"bitshiftright", "fromunixtimestamp64milli",
}

// currentWindowSamplingExpansion names the measured window classes that were
// added after the first current-combined-v1 shape was recorded.
var currentWindowSamplingExpansion = []string{
	"denserank", "percent_rank", "percentrank", "lag", "lead", "nth_value", "ntile",
	"first_value_respect_nulls", "firstvaluerespectnulls",
	"last_value_respect_nulls", "lastvaluerespectnulls",
}

var requiredSamplingSemanticFamilies = []string{
	"common-supertype-scalar", "context-dependent-scalar", "conversion-scalar",
	"dedicated-aggregate", "dedicated-lambda", "dedicated-parametric-aggregate",
	"dedicated-scalar", "first-argument-aggregate", "first-argument-lambda",
	"first-argument-scalar", "first-argument-window", "fixed-result-aggregate",
	"fixed-result-parametric-aggregate", "fixed-result-scalar", "fixed-result-window",
	"parametric-scalar", "predicate-lambda", "predicate-scalar",
}

func expectedCurrentSamplingRegistryShape() samplingRegistryShape {
	return samplingRegistryShape{
		Scalar:           samplingRegistryBaseline.Scalar + len(currentScalarSamplingExpansion),
		Aggregate:        samplingRegistryBaseline.Aggregate,
		Window:           samplingRegistryBaseline.Window + len(currentWindowSamplingExpansion),
		IllegalScalar:    samplingRegistryBaseline.IllegalScalar + len(currentScalarSamplingExpansion) + 1,
		IllegalAggregate: samplingRegistryBaseline.IllegalAggregate,
	}
}

func actualSamplingRegistryShape(source *oracleGen) samplingRegistryShape {
	return samplingRegistryShape{
		Scalar: len(source.drawScalar), Aggregate: len(source.drawAggregate), Window: len(source.drawWindow),
		IllegalScalar: len(source.drawIllegalScalar), IllegalAggregate: len(source.drawIllegalAggregate),
	}
}

func newSamplingPlanStats() samplingPlanStats {
	return samplingPlanStats{Attempts: map[string]int{}, Accepted: map[string]int{}, Fallback: map[string]int{}}
}

func samplingLaneRand(seed int64, lane string) *rand.Rand {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", currentSamplingPlanID, seed, lane)))
	return rand.New(rand.NewSource(int64(binary.LittleEndian.Uint64(sum[:8]))))
}

func cloneOracleGenForLane(source *oracleGen, seed int64, lane string) *oracleGen {
	return &oracleGen{
		r:                    samplingLaneRand(seed, lane),
		drawIndex:            source.drawIndex,
		drawScalar:           source.drawScalar,
		drawAggregate:        source.drawAggregate,
		drawWindow:           source.drawWindow,
		drawIllegalScalar:    source.drawIllegalScalar,
		drawIllegalAggregate: source.drawIllegalAggregate,
	}
}

type samplingCollector struct {
	seen  map[string]bool
	exprs []*fexpr
	stats samplingPlanStats
}

func (c *samplingCollector) ensureGroup(group string) {
	if _, ok := c.stats.Attempts[group]; !ok {
		c.stats.Attempts[group] = 0
	}
	if _, ok := c.stats.Accepted[group]; !ok {
		c.stats.Accepted[group] = 0
	}
	if _, ok := c.stats.Fallback[group]; !ok {
		c.stats.Fallback[group] = 0
	}
}

func (c *samplingCollector) accept(group string, expr *fexpr) bool {
	c.ensureGroup(group)
	c.stats.Attempts[group]++
	expr.sql = stripOuterParens(expr.sql)
	if c.seen[expr.sql] {
		return false
	}
	c.seen[expr.sql] = true
	c.exprs = append(c.exprs, expr)
	c.stats.Accepted[group]++
	return true
}

func (c *samplingCollector) cover(group string, expr *fexpr) {
	c.ensureGroup(group)
	c.stats.Attempts[group]++
	expr.sql = stripOuterParens(expr.sql)
	if !c.seen[expr.sql] {
		c.seen[expr.sql] = true
		c.exprs = append(c.exprs, expr)
	}
	c.stats.Accepted[group]++
}

func (c *samplingCollector) fill(group string, quota int, build func() (*fexpr, bool)) error {
	c.ensureGroup(group)
	limit := quota * samplingRetryLimit
	for c.stats.Accepted[group] < quota && c.stats.Attempts[group] < limit {
		expr, fallback := build()
		if fallback {
			c.stats.Attempts[group]++
			c.stats.Fallback[group]++
			continue
		}
		c.accept(group, expr)
	}
	if c.stats.Accepted[group] != quota {
		return fmt.Errorf("sampling group %q accepted %d of %d expressions after %d attempts and %d fallbacks",
			group, c.stats.Accepted[group], quota, c.stats.Attempts[group], c.stats.Fallback[group])
	}
	return nil
}

func curatedProductionBuilders(g *oracleGen) map[string]func() *fexpr {
	return map[string]func() *fexpr{
		"curatedCoverage":               func() *fexpr { return g.curatedCoverage(1) },
		"curatedDateDifference":         g.curatedDateDifference,
		"curatedLCArithmetic":           g.curatedLCArithmetic,
		"curatedLCStringComparison":     g.curatedLCStringComparison,
		"curatedPlainNumericComparison": g.curatedPlainNumericComparison,
		"curatedNullableLogic":          func() *fexpr { return g.curatedNullableLogic(3) },
		"curatedFallbackOperators":      g.curatedFallbackOperators,
		"curatedDoubleEquals":           g.curatedDoubleEquals,
		"curatedRegexp":                 g.curatedRegexp,
		"curatedCastOperator":           g.curatedCastOperator,
		"curatedBareLambda":             g.curatedBareLambda,
		"curatedNumeric":                func() *fexpr { return g.curatedNumeric(3) },
		"curatedString":                 func() *fexpr { return g.curatedString(3) },
		"curatedBoolean":                func() *fexpr { return g.curatedBoolean(3) },
		"curatedTemporal":               func() *fexpr { return g.curatedTemporal(3) },
		"curatedArray":                  func() *fexpr { return g.curatedArray(3) },
		"curatedAggregate":              func() *fexpr { return g.curatedAggregate(2) },
		"curatedWindow":                 func() *fexpr { return g.curatedWindow(3) },
		"curatedTuple":                  func() *fexpr { return g.curatedTuple(3) },
	}
}

func expressionHasKind(expr *fexpr, target string) bool {
	if expr.kind == target {
		return true
	}
	for _, child := range expr.children {
		if expressionHasKind(child, target) {
			return true
		}
	}
	return false
}

func addCompulsoryCurated(seed int64, source *oracleGen, collector *samplingCollector) error {
	productionNames := make([]string, 0, len(curatedProductionMutationKinds))
	for name := range curatedProductionMutationKinds {
		productionNames = append(productionNames, name)
	}
	sort.Strings(productionNames)
	for _, name := range productionNames {
		group := "curated-production:" + name
		collector.ensureGroup(group)
		g := cloneOracleGenForLane(source, seed, group)
		build := curatedProductionBuilders(g)[name]
		remaining := map[string]bool{}
		for _, kind := range curatedProductionMutationKinds[name] {
			remaining[kind] = true
		}
		limit := samplingRetryLimit * len(remaining)
		for attempts := 0; len(remaining) > 0 && attempts < limit; attempts++ {
			expr := build()
			collector.stats.Attempts[group]++
			var matched []string
			for kind := range remaining {
				if expressionHasKind(expr, kind) {
					matched = append(matched, kind)
				}
			}
			if len(matched) > 0 {
				expr.sql = stripOuterParens(expr.sql)
				if !collector.seen[expr.sql] {
					collector.seen[expr.sql] = true
					collector.exprs = append(collector.exprs, expr)
					collector.stats.Accepted[group]++
					for _, kind := range matched {
						delete(remaining, kind)
					}
				}
			}
		}
		if len(remaining) != 0 {
			missing := make([]string, 0, len(remaining))
			for kind := range remaining {
				missing = append(missing, kind)
			}
			sort.Strings(missing)
			return fmt.Errorf("compulsory production %q did not reach kinds %v", name, missing)
		}
	}
	return nil
}

func topLevelLaneByName(name string) (topLevelLane, bool) {
	for _, lane := range topLevelLanes {
		if lane.name == name {
			return lane, true
		}
	}
	return topLevelLane{}, false
}

func addCuratedLaneQuotas(seed int64, source *oracleGen, collector *samplingCollector) error {
	names := make([]string, 0, len(curatedAcceptedLaneQuotas))
	for name := range curatedAcceptedLaneQuotas {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		lane, ok := topLevelLaneByName(name)
		if !ok {
			return fmt.Errorf("curated quota names unknown lane %q", name)
		}
		group := "curated-lane:" + name
		g := cloneOracleGenForLane(source, seed, group)
		if err := collector.fill(group, curatedAcceptedLaneQuotas[name], func() (*fexpr, bool) {
			return lane.build(g, 3), false
		}); err != nil {
			return err
		}
	}
	return nil
}

func illegalPositions(candidate genDrawCandidate) []int {
	var positions []int
	sorts, accepted := candidate.signature.sortsForArity(candidate.spec.minArity)
	if !accepted {
		return nil
	}
	for position, sortKind := range sorts {
		if argumentSortUsesPool(sortKind) &&
			position < len(candidate.pools) && len(candidate.pools[position].illegal) > 0 {
			positions = append(positions, position)
		}
	}
	return positions
}

func candidateExpression(candidate genDrawCandidate, call string, illegal bool) *fexpr {
	kind := "registry-fn-" + candidate.spec.spelling
	if illegal {
		kind = "registry-illegal-" + candidate.spec.spelling
	}
	aggregate := candidate.spec.place != placementScalar
	if candidate.spec.place == placementWindow {
		call += " OVER (ORDER BY i32)"
	}
	return node(kind, call, aggregate)
}

func addCompulsoryRegistry(seed int64, source *oracleGen, collector *samplingCollector) error {
	names := make([]string, 0, len(source.drawIndex))
	for name, candidate := range source.drawIndex {
		if candidate.writable() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		candidate := source.drawIndex[name]
		group := "registry-legal:" + name
		collector.ensureGroup(group)
		r := samplingLaneRand(seed, group)
		call, ok := candidate.renderCall(r, -1)
		if !ok {
			collector.stats.Attempts[group]++
			collector.stats.Fallback[group]++
			return fmt.Errorf("registry candidate %q cannot render its compulsory legal call", name)
		}
		collector.cover(group, candidateExpression(candidate, call, false))
		for _, position := range illegalPositions(candidate) {
			group = fmt.Sprintf("registry-illegal:%s:%d", name, position)
			r = samplingLaneRand(seed, group)
			call, ok = candidate.renderCall(r, position)
			if !ok {
				collector.ensureGroup(group)
				collector.stats.Attempts[group]++
				collector.stats.Fallback[group]++
				return fmt.Errorf("registry candidate %q cannot render illegal position %d", name, position)
			}
			collector.cover(group, candidateExpression(candidate, call, true))
		}
	}
	return nil
}

func addCombinatorQuota(seed int64, source *oracleGen, collector *samplingCollector) error {
	group := "registry-combinator"
	g := cloneOracleGenForLane(source, seed, group)
	return collector.fill(group, combinatorQuota, func() (*fexpr, bool) {
		before := g.laneDraws["combinator-fallback"]
		expr := g.combinatorDraw()
		return expr, g.laneDraws["combinator-fallback"] != before
	})
}

func addSamplingFiller(seed int64, count int, source *oracleGen, collector *samplingCollector) error {
	var schedule []topLevelLane
	for _, lane := range topLevelLanes {
		for draw := 0; draw < lane.weight/5; draw++ {
			schedule = append(schedule, lane)
		}
	}
	generators := make(map[string]*oracleGen, len(topLevelLanes))
	for _, lane := range topLevelLanes {
		generators[lane.name] = cloneOracleGenForLane(source, seed, "filler:"+lane.name)
	}
	start := len(collector.exprs)
	limit := count * samplingRetryLimit
	for attempt := 0; len(collector.exprs)-start < count && attempt < limit; attempt++ {
		lane := schedule[attempt%len(schedule)]
		group := "filler:" + lane.name
		g := generators[lane.name]
		before := 0
		for key, value := range g.laneDraws {
			if strings.HasSuffix(key, "-fallback") {
				before += value
			}
		}
		expr := lane.build(g, 3)
		after := 0
		for key, value := range g.laneDraws {
			if strings.HasSuffix(key, "-fallback") {
				after += value
			}
		}
		if after != before {
			collector.stats.Attempts[group]++
			collector.stats.Fallback[group]++
			continue
		}
		collector.accept(group, expr)
	}
	if got := len(collector.exprs) - start; got != count {
		return fmt.Errorf("sampling filler accepted %d of %d expressions before the retry limit", got, count)
	}
	return nil
}

func buildCurrentSamplingPlan(seed int64, fillerCount int, source *oracleGen, initial []*fexpr) ([]*fexpr, samplingPlanStats, error) {
	collector := &samplingCollector{seen: map[string]bool{}, stats: newSamplingPlanStats()}
	for _, expr := range initial {
		collector.seen[expr.sql] = true
		collector.exprs = append(collector.exprs, expr)
	}
	if err := addCompulsoryCurated(seed, source, collector); err != nil {
		return nil, collector.stats, err
	}
	if err := addCuratedLaneQuotas(seed, source, collector); err != nil {
		return nil, collector.stats, err
	}
	if err := addCompulsoryRegistry(seed, source, collector); err != nil {
		return nil, collector.stats, err
	}
	if err := addCombinatorQuota(seed, source, collector); err != nil {
		return nil, collector.stats, err
	}
	if err := addSamplingFiller(seed, fillerCount, source, collector); err != nil {
		return nil, collector.stats, err
	}
	return collector.exprs, collector.stats, nil
}

func currentSamplingPlanHash(source *oracleGen) string {
	var lines []string
	lines = append(lines, currentSamplingPlanID, strconv.Itoa(samplingRetryLimit), strconv.Itoa(combinatorQuota), strconv.Itoa(registryIllegalShare))
	for _, name := range sortedSignalKeys(curatedAcceptedLaneQuotas) {
		lines = append(lines, fmt.Sprintf("quota:%s:%d", name, curatedAcceptedLaneQuotas[name]))
	}
	productionNames := make([]string, 0, len(curatedProductionMutationKinds))
	for name := range curatedProductionMutationKinds {
		productionNames = append(productionNames, name)
	}
	sort.Strings(productionNames)
	for _, name := range productionNames {
		kinds := append([]string(nil), curatedProductionMutationKinds[name]...)
		sort.Strings(kinds)
		lines = append(lines, "production:"+name+":"+strings.Join(kinds, ","))
	}
	for _, lane := range topLevelLanes {
		lines = append(lines, fmt.Sprintf("lane:%s:%d", lane.name, lane.weight))
	}
	names := make([]string, 0, len(source.drawIndex))
	for name, candidate := range source.drawIndex {
		if candidate.writable() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		candidate := source.drawIndex[name]
		signature, err := json.Marshal(probeSignatureOf(candidate.signature))
		if err != nil {
			panic(err)
		}
		lines = append(lines, fmt.Sprintf(
			"candidate:%s:spelling=%q:min=%d:max=%d:sorts=%v:place=%d:template=%q:illegal=%v:signature=%s",
			name, candidate.spec.spelling, candidate.spec.minArity, candidate.spec.maxArity,
			candidate.spec.argSorts, candidate.spec.place, candidate.spec.template,
			illegalPositions(candidate), signature,
		))
		for position, pool := range candidate.pools {
			var legal, illegal []string
			for _, column := range pool.legal {
				if strings.Contains(column.name, "(") {
					continue
				}
				legal = append(legal, column.name+":"+column.columnType.String())
			}
			for _, column := range pool.illegal {
				if strings.Contains(column.name, "(") {
					continue
				}
				illegal = append(illegal, column.name+":"+column.columnType.String())
			}
			lines = append(lines, fmt.Sprintf("pool:%s:%d:legal=%v:illegal=%v", name, position, legal, illegal))
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return fmt.Sprintf("%x", sum)
}

func samplingPlacementForFamily(family string) placement {
	switch {
	case strings.HasSuffix(family, "-window"):
		return placementWindow
	case strings.HasSuffix(family, "-aggregate"):
		return placementAggregate
	default:
		return placementScalar
	}
}

func generatedSamplingNamesByPlacement(source *oracleGen) (map[placement][]string, map[string]bool, error) {
	byPlacement := map[placement][]string{
		placementScalar: {}, placementAggregate: {}, placementWindow: {},
	}
	all := make(map[string]bool, len(generatedFunctionSemanticFamilies)+len(sizedConstructors))
	for name, family := range generatedFunctionSemanticFamilies {
		all[name] = true
		if candidate, present := source.drawIndex[name]; present && candidate.writable() {
			place := samplingPlacementForFamily(family)
			byPlacement[place] = append(byPlacement[place], name)
		}
	}
	for name := range sizedConstructors {
		if !all[name] {
			all[name] = true
			if candidate, present := source.drawIndex[name]; present && candidate.writable() {
				byPlacement[placementScalar] = append(byPlacement[placementScalar], name)
			}
		}
	}
	for place := range byPlacement {
		sort.Strings(byPlacement[place])
	}
	if len(all) == 0 {
		return nil, nil, fmt.Errorf("generated sampling source is empty")
	}
	return byPlacement, all, nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validateRequiredSamplingFamilies(source *oracleGen) error {
	for _, family := range requiredSamplingSemanticFamilies {
		reached := false
		for name, candidateFamily := range generatedFunctionSemanticFamilies {
			if candidateFamily != family {
				continue
			}
			candidate, present := source.drawIndex[name]
			if !present || !candidate.writable() || candidate.spec.place != samplingPlacementForFamily(family) {
				continue
			}
			if _, rendered := candidate.renderCall(samplingLaneRand(1, "family:"+family+":"+name), -1); rendered {
				reached = true
				break
			}
		}
		if !reached {
			return fmt.Errorf("required sampling family %q has no reachable generated candidate", family)
		}
	}
	return nil
}

func validateSamplingRegistryContract(source *oracleGen) error {
	if err := validateRequiredSamplingFamilies(source); err != nil {
		return err
	}
	expectedByPlacement, expectedNames, err := generatedSamplingNamesByPlacement(source)
	if err != nil {
		return err
	}
	for name := range source.drawIndex {
		if !expectedNames[name] {
			return fmt.Errorf("sampling candidate %q is not present in the generated semantic source", name)
		}
	}
	for name := range expectedNames {
		_, present := source.drawIndex[name]
		if !present {
			return fmt.Errorf("generated sampling candidate %q is missing from the draw index", name)
		}
	}
	actualByPlacement := map[placement][]string{
		placementScalar: source.drawScalar, placementAggregate: source.drawAggregate, placementWindow: source.drawWindow,
	}
	for _, place := range []placement{placementScalar, placementAggregate, placementWindow} {
		if !sameStrings(actualByPlacement[place], expectedByPlacement[place]) {
			return fmt.Errorf("sampling placement %d has %v, want generated roster %v", place, actualByPlacement[place], expectedByPlacement[place])
		}
	}
	expectedIllegalScalar := drawNamesWithIllegal(source.drawIndex, placementScalar)
	if !sameStrings(source.drawIllegalScalar, expectedIllegalScalar) {
		return fmt.Errorf("illegal scalar roster has %v, want domain-derived roster %v", source.drawIllegalScalar, expectedIllegalScalar)
	}
	expectedIllegalAggregate := drawNamesWithIllegal(source.drawIndex, placementAggregate)
	if !sameStrings(source.drawIllegalAggregate, expectedIllegalAggregate) {
		return fmt.Errorf("illegal aggregate roster has %v, want domain-derived roster %v", source.drawIllegalAggregate, expectedIllegalAggregate)
	}
	return nil
}

func cloneSamplingSource(source *oracleGen) *oracleGen {
	result := *source
	result.drawIndex = make(map[string]genDrawCandidate, len(source.drawIndex))
	for name, candidate := range source.drawIndex {
		result.drawIndex[name] = candidate
	}
	result.drawScalar = append([]string(nil), source.drawScalar...)
	result.drawAggregate = append([]string(nil), source.drawAggregate...)
	result.drawWindow = append([]string(nil), source.drawWindow...)
	result.drawIllegalScalar = append([]string(nil), source.drawIllegalScalar...)
	result.drawIllegalAggregate = append([]string(nil), source.drawIllegalAggregate...)
	return &result
}

func TestCurrentSamplingPlanMeetsCompulsoryContract(t *testing.T) {
	for _, seed := range samplingProfileSeeds {
		source := newCurrentOracleGenerator(t, seed)
		if err := validateSamplingRegistryContract(source); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if got, want := actualSamplingRegistryShape(source), expectedCurrentSamplingRegistryShape(); got != want {
			t.Fatalf("seed %d: unexpected registry index shape: got %+v, want baseline plus measured expansions %+v", seed, got, want)
		}
		exprs, stats, err := buildCurrentSamplingPlan(seed, 200, source, canaryExprs)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if len(exprs) <= len(canaryExprs)+200 {
			t.Fatalf("seed %d: the compulsory passes added no population", seed)
		}
		kinds := map[string]int{}
		for _, expr := range exprs {
			walkSignalKinds(expr, kinds)
		}
		for production, requiredKinds := range curatedProductionMutationKinds {
			for _, kind := range requiredKinds {
				if kinds[kind] == 0 {
					t.Errorf("seed %d production %s: accepted population lost kind %s", seed, production, kind)
				}
			}
		}
		for lane, quota := range curatedAcceptedLaneQuotas {
			if got := stats.Accepted["curated-lane:"+lane]; got != quota {
				t.Errorf("seed %d lane %s: accepted %d, need %d", seed, lane, got, quota)
			}
		}
		for name, candidate := range source.drawIndex {
			if !candidate.writable() {
				continue
			}
			if stats.Accepted["registry-legal:"+name] != 1 {
				t.Errorf("seed %d candidate %s: no accepted legal cell", seed, name)
			}
			for _, position := range illegalPositions(candidate) {
				group := fmt.Sprintf("registry-illegal:%s:%d", name, position)
				if stats.Accepted[group] != 1 {
					t.Errorf("seed %d candidate %s position %d: no accepted illegal cell", seed, name, position)
				}
			}
		}
		if registryIllegalShare > 6 {
			t.Fatalf("registry filler has an illegal share below 1/6: denominator=%d", registryIllegalShare)
		}
		if stats.Accepted["registry-combinator"] != combinatorQuota {
			t.Errorf("seed %d: accepted %d combinators, need %d", seed, stats.Accepted["registry-combinator"], combinatorQuota)
		}
	}
}

func TestCurrentSamplingExpansionIsIntendedAndReachable(t *testing.T) {
	source := newCurrentOracleGenerator(t, 42)
	for _, name := range currentScalarSamplingExpansion {
		candidate, present := source.drawIndex[name]
		if !present {
			t.Errorf("scalar expansion %s is absent from the draw index", name)
			continue
		}
		if candidate.spec.place != placementScalar || !candidate.writable() {
			t.Errorf("scalar expansion %s is not a writable scalar candidate", name)
		}
		family := generatedFunctionSemanticFamilies[name]
		if name == "bitshiftright" {
			if family != "dedicated-scalar" {
				t.Errorf("scalar expansion %s has family %q", name, family)
			}
		} else if name == "fromunixtimestamp64milli" {
			if family != "context-dependent-scalar" {
				t.Errorf("scalar expansion %s has family %q", name, family)
			}
		} else if strings.HasPrefix(name, "toyyyymm") {
			if family != "conversion-scalar" {
				t.Errorf("scalar expansion %s has family %q", name, family)
			}
		} else if family != "dedicated-lambda" {
			t.Errorf("scalar expansion %s has family %q", name, family)
		}
		if positions := illegalPositions(candidate); len(positions) == 0 {
			t.Errorf("scalar expansion %s has no measured negative domain", name)
		}
		if call, rendered := candidate.renderCall(samplingLaneRand(42, "expansion:"+name), -1); !rendered || call == "" {
			t.Errorf("scalar expansion %s cannot render a legal call", name)
		}
	}
	for _, name := range currentWindowSamplingExpansion {
		candidate, present := source.drawIndex[name]
		if !present {
			t.Errorf("window expansion %s is absent from the draw index", name)
			continue
		}
		if candidate.spec.place != placementWindow || !candidate.writable() {
			t.Errorf("window expansion %s is not a writable window candidate", name)
		}
		family := generatedFunctionSemanticFamilies[name]
		if family != "fixed-result-window" && family != "first-argument-window" {
			t.Errorf("window expansion %s has family %q", name, family)
		}
		if call, rendered := candidate.renderCall(samplingLaneRand(42, "expansion:"+name), -1); !rendered || call == "" {
			t.Errorf("window expansion %s cannot render a legal call", name)
		}
	}
	if got := drawNamesWithIllegal(source.drawIndex, placementWindow); !sameStrings(got, []string{"nth_value"}) {
		t.Fatalf("window negative roster = %v, want the compulsory nth_value offset boundary", got)
	}
	xor := source.drawIndex["xor"]
	if functionRegistry["xor"].domain != &xorArgumentDomain || len(illegalPositions(xor)) == 0 {
		t.Fatal("xor does not carry its measured narrow domain into the illegal scalar lane")
	}
	if got, want := actualSamplingRegistryShape(source), expectedCurrentSamplingRegistryShape(); got != want {
		t.Fatalf("sampling expansion shape = %+v, want %+v", got, want)
	}
}

func TestSamplingRegistryContractRejectsMissingFamilyAndWidening(t *testing.T) {
	source := newCurrentOracleGenerator(t, 42)

	missingFamily := cloneSamplingSource(source)
	for name, family := range generatedFunctionSemanticFamilies {
		if family != "fixed-result-window" {
			continue
		}
		delete(missingFamily.drawIndex, name)
	}
	missingFamily.drawWindow = drawNamesOfPlace(missingFamily.drawIndex, placementWindow)
	if err := validateSamplingRegistryContract(missingFamily); err == nil || !strings.Contains(err.Error(), "required sampling family") {
		t.Fatalf("missing required family error = %v", err)
	}

	widened := cloneSamplingSource(source)
	accidental := source.drawIndex["abs"]
	accidental.name = "accidental-widening"
	widened.drawIndex[accidental.name] = accidental
	widened.drawScalar = append(widened.drawScalar, accidental.name)
	sort.Strings(widened.drawScalar)
	if err := validateSamplingRegistryContract(widened); err == nil || !strings.Contains(err.Error(), "not present in the generated semantic source") {
		t.Fatalf("accidental widening error = %v", err)
	}

	illegalWidening := cloneSamplingSource(source)
	illegalWidening.drawIllegalScalar = append(illegalWidening.drawIllegalScalar, "isnull")
	sort.Strings(illegalWidening.drawIllegalScalar)
	if err := validateSamplingRegistryContract(illegalWidening); err == nil || !strings.Contains(err.Error(), "illegal scalar roster") {
		t.Fatalf("illegal-lane widening error = %v", err)
	}
}

func TestCurrentSamplingPlanIsStableAndLaneLocal(t *testing.T) {
	firstSource := newCurrentOracleGenerator(t, 42)
	secondSource := newCurrentOracleGenerator(t, 42)
	firstExprs, firstStats, err := buildCurrentSamplingPlan(42, 50, firstSource, canaryExprs)
	if err != nil {
		t.Fatal(err)
	}
	secondExprs, secondStats, err := buildCurrentSamplingPlan(42, 50, secondSource, canaryExprs)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(firstStats) != fmt.Sprint(secondStats) {
		t.Fatalf("the same seed gave different plan statistics:\nfirst: %v\nsecond: %v", firstStats, secondStats)
	}
	if len(firstExprs) != len(secondExprs) {
		t.Fatalf("the same seed gave %d and %d expressions", len(firstExprs), len(secondExprs))
	}
	for index := range firstExprs {
		if firstExprs[index].sql != secondExprs[index].sql || firstExprs[index].kind != secondExprs[index].kind {
			t.Fatalf("the same seed changed at expression %d", index)
		}
	}
	if firstSourceHash, secondSourceHash := currentSamplingPlanHash(firstSource), currentSamplingPlanHash(secondSource); firstSourceHash == "" || firstSourceHash != secondSourceHash {
		t.Fatalf("the plan hash is empty or unstable: %q %q", firstSourceHash, secondSourceHash)
	}
	first := samplingLaneRand(42, "curated-lane:num").Int63()
	second := samplingLaneRand(42, "curated-lane:num").Int63()
	otherLane := samplingLaneRand(42, "curated-lane:str").Int63()
	if first != second || first == otherLane {
		t.Fatal("the lane random streams are not stable and separate")
	}
}

func TestSamplingPlanRefusesAnExhaustedGroup(t *testing.T) {
	collector := &samplingCollector{seen: map[string]bool{}, stats: newSamplingPlanStats()}
	err := collector.fill("finite", 2, func() (*fexpr, bool) {
		return lit("finite", "i32"), false
	})
	if err == nil {
		t.Fatal("the plan accepted a quota above the unique population")
	}
	if got, want := collector.stats.Attempts["finite"], 2*samplingRetryLimit; got != want {
		t.Fatalf("the retry bound changed: got %d attempts, want %d", got, want)
	}
	if collector.stats.Accepted["finite"] != 1 || collector.stats.Fallback["finite"] != 0 {
		t.Fatalf("unexpected exhausted-group counts: %+v", collector.stats)
	}
}

func TestSamplingPlanHashCoversTheRegistryDrawContract(t *testing.T) {
	source := newCurrentOracleGenerator(t, 42)
	before := currentSamplingPlanHash(source)
	for _, seed := range samplingProfileSeeds {
		if got := currentSamplingPlanHash(newCurrentOracleGenerator(t, seed)); got != before {
			t.Fatalf("seed %d changed the plan hash: got %s, want %s", seed, got, before)
		}
	}
	candidate := source.drawIndex["abs"]
	candidate.spec.spelling = "changedAbs"
	source.drawIndex["abs"] = candidate
	after := currentSamplingPlanHash(source)
	if before == after {
		t.Fatal("the plan hash did not change with the registry draw contract")
	}
	candidate = source.drawIndex["abs"]
	candidate.pools[0].legal = candidate.pools[0].legal[1:]
	source.drawIndex["abs"] = candidate
	if currentSamplingPlanHash(source) == after {
		t.Fatal("the plan hash did not change with the accepted column pool")
	}
}
