package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/conformance"
	"github.com/IlyaGulya/chgen/internal/supportmanifest"
)

const functionProbeCatalogVersion = 5

var (
	functionProbeCatalogPath  = moduleRootPath("testdata", "clickhouse-function-probes.json")
	functionProbeEvidencePath = moduleRootPath("testdata", "clickhouse-function-probe-evidence.json")
)

type functionProbeCatalog struct {
	Version     int                  `json:"version"`
	CHVersion   string               `json:"clickhouse_version"`
	FixtureHash string               `json:"fixture_hash"`
	Functions   []functionProbeEntry `json:"functions"`
}

type functionProbeEntry struct {
	Name         string          `json:"name"`
	Family       string          `json:"family"`
	ProbeRecipe  string          `json:"probe_recipe"`
	RequiredAxes []string        `json:"required_axes"`
	Spelling     string          `json:"spelling"`
	Signature    probeSignature  `json:"signature"`
	Positions    []probePosition `json:"positions"`
	Probes       []functionProbe `json:"probes"`
}

type probeSignature struct {
	Forms          []probeSignatureForm     `json:"forms"`
	ParameterForms []probeSignatureForm     `json:"parameter_forms"`
	Relations      []probeSignatureRelation `json:"relations,omitempty"`
	ConstantRanges []probeConstantRange     `json:"constant_ranges,omitempty"`
	Placement      string                   `json:"placement"`
	AllowBare      bool                     `json:"allow_bare"`
	AllowOver      bool                     `json:"allow_over"`
	Evidence       string                   `json:"evidence"`
}

type probeSignatureForm struct {
	Prefix     []string `json:"prefix,omitempty"`
	Repeat     []string `json:"repeat,omitempty"`
	Suffix     []string `json:"suffix,omitempty"`
	MinRepeats int      `json:"min_repeats,omitempty"`
	MaxRepeats int      `json:"max_repeats,omitempty"`
}

type probeSignatureRelation struct {
	Kind  string `json:"kind"`
	Left  int    `json:"left"`
	Right int    `json:"right"`
}

type probeConstantRange struct {
	Parameter bool    `json:"parameter,omitempty"`
	Position  int     `json:"position"`
	Minimum   float64 `json:"minimum"`
	Maximum   float64 `json:"maximum"`
}

type probePosition struct {
	Arity          int    `json:"arity"`
	Position       int    `json:"position"`
	Sort           string `json:"sort"`
	LegalProbeID   string `json:"legal_probe_id"`
	IllegalProbeID string `json:"illegal_probe_id,omitempty"`
}

type functionProbe struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Arity         int    `json:"arity"`
	Position      int    `json:"position,omitempty"`
	SQL           string `json:"sql"`
	ExpectedChgen string `json:"expected_chgen"`
}

func TestFunctionProbeCatalogIsCurrent(t *testing.T) {
	catalog := buildFunctionProbeCatalog(t)
	data := marshalFunctionProbeCatalog(t, catalog)
	if os.Getenv("CHGEN_UPDATE_FUNCTION_PROBES") == "1" {
		if err := os.WriteFile(functionProbeCatalogPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	current, err := os.ReadFile(functionProbeCatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, data) {
		t.Fatal("function probe catalog is stale; run CHGEN_UPDATE_FUNCTION_PROBES=1 go test ./internal/engine -run '^TestFunctionProbeCatalogIsCurrent$'")
	}
}

func TestAggregateSizeProbesUseThePositiveBoundary(t *testing.T) {
	catalog := buildFunctionProbeCatalog(t)
	wanted := map[string]bool{
		"grouparray":       false,
		"grouparrayif":     false,
		"groupuniqarray":   false,
		"groupuniqarrayif": false,
	}
	for _, function := range catalog.Functions {
		if _, found := wanted[function.Name]; !found {
			continue
		}
		legal := false
		illegal := false
		for _, probe := range function.Probes {
			switch probe.Kind {
			case "legal_parameter":
				legal = strings.Contains(probe.SQL, "(1)(")
			case "illegal_parameter_value":
				illegal = strings.Contains(probe.SQL, "(0)(")
			}
		}
		if !legal || !illegal {
			t.Errorf("function %s probes do not hold the positive-size boundary", function.Name)
		}
		wanted[function.Name] = true
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("function probe catalog has no %s entry", name)
		}
	}
}

func TestFunctionProbeCatalogRejectsMissingPosition(t *testing.T) {
	catalog := buildFunctionProbeCatalog(t)
	mutated := catalog
	mutated.Functions = append([]functionProbeEntry(nil), catalog.Functions...)
	for index := range mutated.Functions {
		if len(mutated.Functions[index].Positions) == 0 {
			continue
		}
		mutated.Functions[index].Positions = mutated.Functions[index].Positions[1:]
		if err := validateFunctionProbeCatalog(t, mutated); err == nil {
			t.Fatal("probe catalog gate accepted a missing argument position")
		}
		return
	}
	t.Fatal("probe catalog has no position to mutate")
}

func TestFunctionProbeCatalogRejectsMissingArityBoundary(t *testing.T) {
	catalog := buildFunctionProbeCatalog(t)
	mutated := catalog
	mutated.Functions = append([]functionProbeEntry(nil), catalog.Functions...)
	for functionIndex := range mutated.Functions {
		for probeIndex, probe := range mutated.Functions[functionIndex].Probes {
			if probe.Kind != "illegal_arity" {
				continue
			}
			probes := append([]functionProbe(nil), mutated.Functions[functionIndex].Probes...)
			mutated.Functions[functionIndex].Probes = append(probes[:probeIndex], probes[probeIndex+1:]...)
			if err := validateFunctionProbeCatalog(t, mutated); err == nil {
				t.Fatal("probe catalog gate accepted a missing arity boundary")
			}
			return
		}
	}
	t.Fatal("probe catalog has no arity boundary to mutate")
}

func TestFunctionProbeCatalogRejectsFamilyRecipeDrift(t *testing.T) {
	catalog := cloneFunctionProbeCatalog(t, buildFunctionProbeCatalog(t))
	catalog.Functions[0].ProbeRecipe = ""
	if err := validateFunctionProbeCatalog(t, catalog); err == nil {
		t.Fatal("probe catalog gate accepted a missing family probe recipe")
	}
}

func TestFunctionProbeCatalogRejectsTypedPolicyMutation(t *testing.T) {
	catalog := buildFunctionProbeCatalog(t)
	saved := functionSemanticFamilies[semanticFamilyPredicate]
	mutated := saved
	mutated.probePolicy = probePolicyWindowSignature
	mutated.probeAxes = familyDefinition("mutated", mutated.resultPolicy, mutated.probePolicy).probeAxes
	functionSemanticFamilies[semanticFamilyPredicate] = mutated
	t.Cleanup(func() { functionSemanticFamilies[semanticFamilyPredicate] = saved })
	if err := validateFunctionProbeCatalog(t, catalog); err == nil {
		t.Fatal("probe catalog accepted a non-empty typed probe policy mutation")
	}
}

func TestEachTypedProbeAxisChangesGeneratedCells(t *testing.T) {
	original := buildFunctionProbeCatalog(t)
	originalBytes := marshalFunctionProbeCatalog(t, original)
	tests := []struct {
		axis      functionProbeAxis
		family    functionSemanticFamily
		function  string
		probeKind string
	}{
		{axis: probeAxisArity, family: semanticFamilyDedicated, function: "abs", probeKind: "illegal_arity"},
		{axis: probeAxisArgumentDomain, family: semanticFamilyDedicated, function: "abs", probeKind: "legal_domain"},
		{axis: probeAxisResultType, family: semanticFamilyDedicated, function: "abs", probeKind: "legal_result"},
		{axis: probeAxisPlacement, family: semanticFamilyContextDependent, function: "and", probeKind: "legal_placement"},
		{axis: probeAxisLambda, family: semanticFamilyLambdaPredicate, function: "arrayexists", probeKind: "legal_lambda"},
		{axis: probeAxisParameters, family: semanticFamilyParametric, function: "array", probeKind: "legal_parameters"},
	}
	for _, test := range tests {
		test := test
		t.Run(functionProbeAxisName(test.axis), func(t *testing.T) {
			originalEntry := functionProbeEntryByName(t, original, test.function)
			if !hasProbeKind(originalEntry.Probes, test.probeKind) {
				t.Fatalf("function %s has no baseline %s probe", test.function, test.probeKind)
			}
			saved := functionSemanticFamilies[test.family]
			mutated := saved
			mutated.probeAxes = nil
			for _, candidate := range saved.probeAxes {
				if candidate != test.axis {
					mutated.probeAxes = append(mutated.probeAxes, candidate)
				}
			}
			functionSemanticFamilies[test.family] = mutated
			defer func() { functionSemanticFamilies[test.family] = saved }()
			catalog := buildFunctionProbeCatalog(t)
			functionSemanticFamilies[test.family] = saved
			mutatedEntry := functionProbeEntryByName(t, catalog, test.function)
			if hasProbeKind(mutatedEntry.Probes, test.probeKind) {
				t.Fatalf("function %s kept its %s probe after axis removal", test.function, test.probeKind)
			}
			encoded := marshalFunctionProbeCatalog(t, catalog)
			if bytes.Equal(encoded, originalBytes) || countFunctionProbes(catalog) >= countFunctionProbes(original) {
				t.Fatal("removing a typed probe axis did not remove generated cells")
			}
			path := t.TempDir() + "/catalog.json"
			if err := os.WriteFile(path, encoded, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := supportmanifest.LoadFunctionEvidence(path, functionProbeEvidencePath); err == nil {
				t.Fatal("live evidence accepted a catalog with one typed axis removed")
			}
		})
	}
}

func TestSourceFamilyMutationsFailAgainstLiveEvidence(t *testing.T) {
	tests := []struct {
		name     string
		function string
		family   functionSemanticFamily
	}{
		{name: "trim fixed result to dedicated", function: "trim", family: semanticFamilyDedicated},
		{name: "count aggregate fixed result to dedicated", function: "count", family: semanticFamilyAggregateDedicated},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := make(map[string]functionSpec, len(functionSemanticSpecs))
			for name, spec := range functionSemanticSpecs {
				source[name] = spec
			}
			mutated := source[test.function]
			mutated.family = test.family
			source[test.function] = mutated
			built := mustBuildFunctionRegistry(source)
			saved := functionRegistry
			functionRegistry = built
			defer func() { functionRegistry = saved }()
			catalog := buildFunctionProbeCatalog(t)
			functionRegistry = saved
			path := t.TempDir() + "/catalog.json"
			if err := os.WriteFile(path, marshalFunctionProbeCatalog(t, catalog), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := supportmanifest.LoadFunctionEvidence(path, functionProbeEvidencePath); err == nil {
				t.Fatal("live evidence accepted a rebuilt catalog after a source family mutation")
			}
		})
	}
}

func countFunctionProbes(catalog functionProbeCatalog) int {
	count := 0
	for _, function := range catalog.Functions {
		count += len(function.Probes)
	}
	return count
}

func functionProbeEntryByName(t *testing.T, catalog functionProbeCatalog, name string) functionProbeEntry {
	t.Helper()
	for _, function := range catalog.Functions {
		if function.Name == name {
			return function
		}
	}
	t.Fatalf("function probe catalog has no function %s", name)
	return functionProbeEntry{}
}

func hasProbeKind(probes []functionProbe, kind string) bool {
	for _, probe := range probes {
		if probe.Kind == kind {
			return true
		}
	}
	return false
}

func TestFunctionProbeCatalogRejectsMissingLegalAndIllegalProbes(t *testing.T) {
	catalog := buildFunctionProbeCatalog(t)
	for _, kind := range []string{"legal_result", "illegal_domain"} {
		t.Run(kind, func(t *testing.T) {
			mutated := cloneFunctionProbeCatalog(t, catalog)
			for functionIndex := range mutated.Functions {
				for probeIndex, probe := range mutated.Functions[functionIndex].Probes {
					if probe.Kind != kind {
						continue
					}
					probes := mutated.Functions[functionIndex].Probes
					mutated.Functions[functionIndex].Probes = append(probes[:probeIndex:probeIndex], probes[probeIndex+1:]...)
					if err := validateFunctionProbeCatalog(t, mutated); err == nil {
						t.Fatalf("probe catalog gate accepted a missing %s probe", kind)
					}
					return
				}
			}
			t.Fatalf("probe catalog has no %s probe to mutate", kind)
		})
	}
	t.Run("illegal position", func(t *testing.T) {
		mutated := cloneFunctionProbeCatalog(t, catalog)
		for functionIndex := range mutated.Functions {
			for positionIndex := range mutated.Functions[functionIndex].Positions {
				position := &mutated.Functions[functionIndex].Positions[positionIndex]
				if position.IllegalProbeID == "" {
					continue
				}
				position.IllegalProbeID = ""
				if err := validateFunctionProbeCatalog(t, mutated); err == nil {
					t.Fatal("probe catalog gate accepted a missing illegal position")
				}
				return
			}
		}
		t.Fatal("probe catalog has no illegal position to mutate")
	})
}

func TestFunctionProbeCatalogRejectsSignatureMutation(t *testing.T) {
	catalog := buildFunctionProbeCatalog(t)
	checks := []struct {
		name   string
		mutate func(*functionProbeEntry) bool
	}{
		{"optional form", func(entry *functionProbeEntry) bool {
			if entry.Name != "laginframe" || len(entry.Signature.Forms) < 2 {
				return false
			}
			entry.Signature.Forms = entry.Signature.Forms[1:]
			return true
		}},
		{"repeated group", func(entry *functionProbeEntry) bool {
			if entry.Name != "map" || len(entry.Signature.Forms[0].Repeat) < 2 {
				return false
			}
			entry.Signature.Forms[0].Repeat = entry.Signature.Forms[0].Repeat[1:]
			return true
		}},
		{"constant argument", func(entry *functionProbeEntry) bool {
			if entry.Name != "datediff" || len(entry.Signature.Forms[0].Prefix) == 0 {
				return false
			}
			entry.Signature.Forms[0].Prefix[0] = "value"
			return true
		}},
		{"linked argument", func(entry *functionProbeEntry) bool {
			if entry.Name != "laginframe" || len(entry.Signature.Relations) == 0 {
				return false
			}
			entry.Signature.Relations = nil
			return true
		}},
		{"aggregate parameter", func(entry *functionProbeEntry) bool {
			if entry.Name != "quantile" || len(entry.Signature.ParameterForms) < 2 {
				return false
			}
			entry.Signature.ParameterForms = entry.Signature.ParameterForms[:1]
			return true
		}},
		{"placement", func(entry *functionProbeEntry) bool {
			if entry.Name != "abs" {
				return false
			}
			entry.Signature.AllowOver = true
			return true
		}},
		{"constant range", func(entry *functionProbeEntry) bool {
			if entry.Name != "uniqcombined" || len(entry.Signature.ConstantRanges) == 0 {
				return false
			}
			entry.Signature.ConstantRanges[0].Minimum = 1
			return true
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			mutated := cloneFunctionProbeCatalog(t, catalog)
			for index := range mutated.Functions {
				if !check.mutate(&mutated.Functions[index]) {
					continue
				}
				if err := validateFunctionProbeCatalog(t, mutated); err == nil {
					t.Fatalf("probe catalog gate accepted a %s mutation", check.name)
				}
				return
			}
			t.Fatalf("probe catalog has no target for %s", check.name)
		})
	}
}

func TestFunctionProbeCatalogCoversMeasuredFunctionSupport(t *testing.T) {
	catalog := buildFunctionProbeCatalog(t)
	covered := make(map[string]struct{}, len(catalog.Functions))
	for _, function := range catalog.Functions {
		covered[function.Name] = struct{}{}
	}
	manifest, err := supportmanifest.Load(supportManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest.Entries {
		if entry.Status != supportmanifest.SupportedMeasured ||
			(entry.Kind != supportmanifest.KindFunction && entry.Kind != supportmanifest.KindFunctionAlias) {
			continue
		}
		if _, found := covered[strings.ToLower(entry.Name)]; !found {
			t.Errorf("supported function %s has no function probe entry", entry.Name)
		}
	}
}

func buildFunctionProbeCatalog(t *testing.T) functionProbeCatalog {
	t.Helper()
	columns := orderedOracleFixtureColumns(t)
	index := buildDrawIndex(columns)
	prepareFunctionProbeIndex(index, columns)
	names := make([]string, 0, len(index))
	for name := range index {
		names = append(names, name)
	}
	sort.Strings(names)
	catalog := functionProbeCatalog{
		Version: functionProbeCatalogVersion, CHVersion: MeasuredCHVersion,
		FixtureHash: conformance.StableHash(oracleSchemaDDL),
	}
	for _, name := range names {
		candidate := index[name]
		family := functionSemanticFamilies[functionRegistry[name].family]
		entry := functionProbeEntry{
			Name: name, Family: family.name, ProbeRecipe: functionProbePolicyName(family.probePolicy),
			RequiredAxes: functionProbeAxisNames(family.probeAxes),
			Spelling:     candidate.spec.spelling, Signature: probeSignatureOf(candidate.signature),
		}
		axes := functionProbeAxisSet(family.probeAxes)
		for _, arity := range candidate.signature.representativeArities() {
			sorts, accepted := candidate.signature.sortsForArity(arity)
			if !accepted {
				t.Fatalf("signature for %s lost representative arity %d", candidate.name, arity)
			}
			if axes[probeAxisResultType] {
				entry.Probes = append(entry.Probes, legalAxisFunctionProbe(t, candidate, arity, "result"))
			}
			if !axes[probeAxisArgumentDomain] {
				continue
			}
			for position := 0; position < arity; position++ {
				legal := legalFunctionProbe(t, candidate, arity, position)
				legal.Kind = "legal_domain"
				coverage := probePosition{
					Arity: arity, Position: position, Sort: argumentSortName(sorts[position]),
					LegalProbeID: legal.ID,
				}
				entry.Probes = append(entry.Probes, legal)
				if argumentSortUsesPool(sorts[position]) &&
					position < len(candidate.pools) && len(candidate.pools[position].illegal) > 0 {
					illegal := illegalDomainFunctionProbe(t, candidate, arity, position)
					coverage.IllegalProbeID = illegal.ID
					entry.Probes = append(entry.Probes, illegal)
				}
				entry.Positions = append(entry.Positions, coverage)
			}
		}
		if axes[probeAxisArity] {
			for _, arity := range candidate.signature.rejectedBoundaryArities() {
				entry.Probes = append(entry.Probes, illegalArityFunctionProbe(t, candidate, arity))
			}
		}
		if axes[probeAxisPlacement] {
			arity := candidate.signature.representativeArities()[0]
			entry.Probes = append(entry.Probes, legalAxisFunctionProbe(t, candidate, arity, "placement"))
			if candidate.signature.place == placementWindow {
				entry.Probes = append(entry.Probes, bareWindowPlacementProbe(t, candidate, arity))
			}
		}
		if axes[probeAxisLambda] {
			entry.Probes = append(entry.Probes, legalAxisFunctionProbe(t, candidate, candidate.signature.representativeArities()[0], "lambda"))
		}
		if axes[probeAxisParameters] {
			entry.Probes = append(entry.Probes, legalAxisFunctionProbe(t, candidate, candidate.signature.representativeArities()[0], "parameters"))
			parameterSignature := functionSignature{forms: candidate.signature.parameterForms}
			for _, arity := range parameterSignature.representativeArities() {
				if arity == 0 {
					continue
				}
				entry.Probes = append(entry.Probes, parameterFunctionProbe(t, candidate, arity, false))
				entry.Probes = append(entry.Probes, parameterFunctionProbe(t, candidate, arity, true))
				if len(candidate.signature.constantRanges) > 0 {
					entry.Probes = append(entry.Probes, illegalParameterValueFunctionProbe(t, candidate, arity))
				}
			}
			if len(candidate.signature.parameterForms) > 1 {
				for _, arity := range parameterSignature.rejectedBoundaryArities() {
					entry.Probes = append(entry.Probes, illegalParameterArityFunctionProbe(t, candidate, arity))
				}
			}
		}
		if name == "arraypartialsort" || name == "arraypartialreversesort" {
			entry.Probes = append(entry.Probes, functionProbe{
				ID: probeID(name, "legal_bool_limit", 2, 0), Kind: "legal_bool_limit",
				Arity: 2, Position: 0, SQL: candidate.spec.spelling + "(b, arr_i)", ExpectedChgen: "accept",
			})
		}
		catalog.Functions = append(catalog.Functions, entry)
	}
	if err := validateFunctionProbeCatalog(t, catalog); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func functionProbeAxisSet(axes []functionProbeAxis) map[functionProbeAxis]bool {
	result := make(map[functionProbeAxis]bool, len(axes))
	for _, axis := range axes {
		result[axis] = true
	}
	return result
}

func prepareFunctionProbeIndex(index map[string]genDrawCandidate, columns []fixtureColumn) {
	setPool := func(name string, position int, accepts func(CHType) bool) {
		candidate, found := index[name]
		if !found || position >= len(candidate.pools) {
			return
		}
		candidate.pools[position] = splitByDomain(&argumentDomain{name: name, accepts: accepts}, columns)
		index[name] = candidate
	}
	setPool("arrayelement", 0, arrayArgumentDomain.accepts)
	setPool("arrayexists", 1, arrayArgumentDomain.accepts)
	setPool("arraysort", 0, arrayArgumentDomain.accepts)
	setPool("arraysort", 1, arrayArgumentDomain.accepts)
	setPool("mapkeys", 0, func(value CHType) bool { return domainBaseType(value).normalizedName() == "map" })
	setPool("mapvalues", 0, func(value CHType) bool { return domainBaseType(value).normalizedName() == "map" })
	setPool("tupleelement", 0, func(value CHType) bool { return domainBaseType(value).normalizedName() == "tuple" })
	setPool("tostartofinterval", 0, func(value CHType) bool {
		name := domainBaseType(value).normalizedName()
		return name == "datetime" || name == "datetime64"
	})
	setPool("totimezone", 0, func(value CHType) bool {
		name := domainBaseType(value).normalizedName()
		return name == "datetime" || name == "datetime64"
	})
}

func orderedOracleFixtureColumns(t *testing.T) []fixtureColumn {
	t.Helper()
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatal(err)
	}
	var columns []fixtureColumn
	tableNames := make([]string, 0, len(schema.Tables))
	for tableName := range schema.Tables {
		tableNames = append(tableNames, tableName)
	}
	sort.Strings(tableNames)
	for _, tableName := range tableNames {
		table := schema.Tables[tableName]
		for _, columnName := range table.ColumnOrder {
			column := table.Columns[columnName]
			columns = append(columns, fixtureColumn{name: column.Name, columnType: column.Type})
		}
	}
	return columns
}

func legalFunctionProbe(t *testing.T, candidate genDrawCandidate, arity, position int) functionProbe {
	t.Helper()
	call, ok := candidate.renderCallArity(probeRand(candidate.name, "legal", arity, position), arity, -1)
	if !ok {
		t.Fatalf("cannot render legal probe for %s arity %d position %d", candidate.name, arity, position)
	}
	id := probeID(candidate.name, "legal", arity, position)
	expectation := "accept"
	if arity == 0 && (candidate.name == "array" || candidate.name == "tuple" || candidate.name == "map") {
		expectation = "refuse_unsupported_result"
	}
	return functionProbe{
		ID: id, Kind: "legal", Arity: arity, Position: position,
		SQL: placeProbe(call, candidate.signature.place), ExpectedChgen: expectation,
	}
}

func legalAxisFunctionProbe(t *testing.T, candidate genDrawCandidate, arity int, axis string) functionProbe {
	probe := legalFunctionProbe(t, candidate, arity, -1)
	probe.ID = probeID(candidate.name, "legal_"+axis, arity, -1)
	probe.Kind = "legal_" + axis
	return probe
}

func bareWindowPlacementProbe(t *testing.T, candidate genDrawCandidate, arity int) functionProbe {
	t.Helper()
	call, ok := candidate.renderCallArity(probeRand(candidate.name, "bare_placement", arity, -1), arity, -1)
	if !ok {
		t.Fatalf("cannot render bare placement probe for %s arity %d", candidate.name, arity)
	}
	kind := "illegal_bare_placement"
	expectation := "refuse"
	if candidate.signature.allowBare {
		kind = "legal_bare_placement"
		expectation = "accept"
	}
	return functionProbe{
		ID: probeID(candidate.name, kind, arity, -1), Kind: kind,
		Arity: arity, Position: -1, SQL: call, ExpectedChgen: expectation,
	}
}

func illegalDomainFunctionProbe(t *testing.T, candidate genDrawCandidate, arity, position int) functionProbe {
	t.Helper()
	call, ok := candidate.renderCallArity(probeRand(candidate.name, "illegal_domain", arity, position), arity, position)
	if !ok {
		t.Fatalf("cannot render illegal domain probe for %s arity %d position %d", candidate.name, arity, position)
	}
	return functionProbe{
		ID: probeID(candidate.name, "illegal_domain", arity, position), Kind: "illegal_domain",
		Arity: arity, Position: position, SQL: placeProbe(call, candidate.signature.place), ExpectedChgen: "refuse",
	}
}

func illegalArityFunctionProbe(t *testing.T, candidate genDrawCandidate, arity int) functionProbe {
	t.Helper()
	arguments := make([]string, 0, arity)
	random := probeRand(candidate.name, "illegal_arity", arity, -1)
	for position := 0; position < arity; position++ {
		argumentSort, found := candidate.argumentSort(position)
		if !found {
			argumentSort = argSortValue
		}
		argument, ok := candidate.renderArgumentSort(random, position, argumentSort, false)
		if !ok && argumentSort == argSortValue {
			argument, ok = "i8", true
		}
		if !ok {
			t.Fatalf("cannot render rejected-arity argument %d for %s", position, candidate.name)
		}
		arguments = append(arguments, argument)
	}
	call := candidate.spec.spelling + "(" + strings.Join(arguments, ", ") + ")"
	return functionProbe{
		ID: probeID(candidate.name, "illegal_arity", arity, -1), Kind: "illegal_arity",
		Arity: arity, Position: -1, SQL: placeProbe(call, candidate.signature.place), ExpectedChgen: "refuse",
	}
}

func parameterFunctionProbe(t *testing.T, candidate genDrawCandidate, arity int, illegal bool) functionProbe {
	t.Helper()
	parameterSignature := functionSignature{forms: candidate.signature.parameterForms}
	sorts, accepted := parameterSignature.sortsForArity(arity)
	if !accepted {
		t.Fatalf("signature for %s rejects parameter arity %d", candidate.name, arity)
	}
	parameters := make([]string, 0, arity)
	random := probeRand(candidate.name, "parameter", arity, -1)
	for position, sort := range sorts {
		parameter, ok := candidate.renderArgumentSort(random, position, sort, false)
		for _, valueRange := range candidate.signature.constantRanges {
			if valueRange.parameter && valueRange.position == position {
				value := (valueRange.minimum + valueRange.maximum) / 2
				if sort == argSortConstInt {
					// Use the lower accepted integer boundary. A midpoint of a
					// large range can use exponent notation, which is not an
					// integer literal and can request an impractical allocation.
					value = valueRange.minimum
				}
				parameter = strconv.FormatFloat(value, 'f', -1, 64)
			}
		}
		if illegal {
			parameter, ok = "i8", true
		}
		if !ok {
			t.Fatalf("cannot render parameter %d for %s", position, candidate.name)
		}
		parameters = append(parameters, parameter)
	}
	call := callWithFunctionParameters(t, candidate, parameters)
	kind := "legal_parameter"
	expectation := "accept"
	if illegal {
		kind = "illegal_parameter_domain"
		expectation = "refuse"
	}
	return functionProbe{
		ID: probeID(candidate.name, kind, arity, -1), Kind: kind, Arity: arity, Position: -1,
		SQL: placeProbe(call, candidate.signature.place), ExpectedChgen: expectation,
	}
}

func illegalParameterValueFunctionProbe(t *testing.T, candidate genDrawCandidate, arity int) functionProbe {
	t.Helper()
	parameterSignature := functionSignature{forms: candidate.signature.parameterForms}
	sorts, accepted := parameterSignature.sortsForArity(arity)
	if !accepted {
		t.Fatalf("signature for %s rejects parameter arity %d", candidate.name, arity)
	}
	parameters := make([]string, arity)
	random := probeRand(candidate.name, "parameter_value", arity, -1)
	for position, sort := range sorts {
		value, ok := candidate.renderArgumentSort(random, position, sort, false)
		if !ok {
			t.Fatalf("cannot render parameter %d for %s", position, candidate.name)
		}
		parameters[position] = value
	}
	for _, valueRange := range candidate.signature.constantRanges {
		if valueRange.parameter && valueRange.position < len(parameters) {
			parameters[valueRange.position] = strconv.FormatFloat(valueRange.minimum-1, 'f', -1, 64)
		}
	}
	call := callWithFunctionParameters(t, candidate, parameters)
	return functionProbe{
		ID: probeID(candidate.name, "illegal_parameter_value", arity, -1), Kind: "illegal_parameter_value",
		Arity: arity, Position: -1, SQL: placeProbe(call, candidate.signature.place), ExpectedChgen: "refuse",
	}
}

func illegalParameterArityFunctionProbe(t *testing.T, candidate genDrawCandidate, arity int) functionProbe {
	t.Helper()
	parameters := make([]string, arity)
	for position := range parameters {
		parameters[position] = "1"
	}
	call := callWithFunctionParameters(t, candidate, parameters)
	return functionProbe{
		ID: probeID(candidate.name, "illegal_parameter_arity", arity, -1), Kind: "illegal_parameter_arity",
		Arity: arity, Position: -1, SQL: placeProbe(call, candidate.signature.place), ExpectedChgen: "refuse",
	}
}

func callWithFunctionParameters(t *testing.T, candidate genDrawCandidate, parameters []string) string {
	t.Helper()
	argumentArities := candidate.signature.representativeArities()
	if len(argumentArities) == 0 {
		t.Fatalf("signature for %s has no argument arity", candidate.name)
	}
	call, ok := candidate.renderCallArity(probeRand(candidate.name, "parameter_call", len(parameters), -1), argumentArities[0], -1)
	if !ok {
		t.Fatalf("cannot render parameter call for %s", candidate.name)
	}
	prefix := candidate.spec.spelling + "("
	if !strings.HasPrefix(call, prefix) {
		t.Fatalf("parameter call for %s has unsupported syntax %s", candidate.name, call)
	}
	return candidate.spec.spelling + "(" + strings.Join(parameters, ", ") + ")(" + strings.TrimPrefix(call, prefix)
}

func placeProbe(call string, place placement) string {
	if place == placementWindow {
		return call + " OVER (ORDER BY i32)"
	}
	return call
}

func probeRand(name, kind string, arity, position int) *rand.Rand {
	return rand.New(zeroRandSource{})
}

type zeroRandSource struct{}

func (zeroRandSource) Int63() int64 { return 0 }

func (zeroRandSource) Seed(int64) {}

func probeID(name, kind string, arity, position int) string {
	return fmt.Sprintf("%s/%s/a%d/p%d", name, kind, arity, position)
}

func argumentSortName(value argSort) string {
	switch value {
	case argSortValue:
		return "value"
	case argSortNumber:
		return "numeric_value"
	case argSortOffset:
		return "offset_value"
	case argSortIntegerOffset:
		return "integer_offset_value"
	case argSortIndex:
		return "index_value"
	case argSortPredicate:
		return "predicate"
	case argSortConstString:
		return "constant_string"
	case argSortConstInt:
		return "constant_integer"
	case argSortConstNumber:
		return "constant_number"
	case argSortInterval:
		return "interval"
	case argSortLambda:
		return "lambda"
	case argSortTypeName:
		return "type_name"
	default:
		return "unknown"
	}
}

func signatureRelationName(value signatureRelationKind) string {
	switch value {
	case signatureRelationDefaultKeepsFirstType:
		return "default_keeps_first_type"
	case signatureRelationArrayElementCommonType:
		return "array_element_common_type"
	default:
		return "unknown"
	}
}

func probeSignatureFormOf(form signatureForm) probeSignatureForm {
	result := probeSignatureForm{MinRepeats: form.minRepeats, MaxRepeats: form.maxRepeats}
	for _, sort := range form.prefix {
		result.Prefix = append(result.Prefix, argumentSortName(sort))
	}
	for _, sort := range form.repeat {
		result.Repeat = append(result.Repeat, argumentSortName(sort))
	}
	for _, sort := range form.suffix {
		result.Suffix = append(result.Suffix, argumentSortName(sort))
	}
	return result
}

func probeSignatureOf(signature functionSignature) probeSignature {
	result := probeSignature{
		Placement: placementName(signature.place), AllowBare: signature.allowBare,
		AllowOver: signature.allowOver, Evidence: string(signature.evidence),
	}
	for _, form := range signature.forms {
		result.Forms = append(result.Forms, probeSignatureFormOf(form))
	}
	for _, form := range signature.parameterForms {
		result.ParameterForms = append(result.ParameterForms, probeSignatureFormOf(form))
	}
	for _, relation := range signature.relations {
		result.Relations = append(result.Relations, probeSignatureRelation{
			Kind: signatureRelationName(relation.kind), Left: relation.left, Right: relation.right,
		})
	}
	for _, valueRange := range signature.constantRanges {
		result.ConstantRanges = append(result.ConstantRanges, probeConstantRange{
			Parameter: valueRange.parameter, Position: valueRange.position,
			Minimum: valueRange.minimum, Maximum: valueRange.maximum,
		})
	}
	return result
}

func placementName(value placement) string {
	switch value {
	case placementScalar:
		return "scalar"
	case placementAggregate:
		return "aggregate"
	case placementWindow:
		return "window"
	default:
		return "unknown"
	}
}

func validateFunctionProbeCatalog(t *testing.T, catalog functionProbeCatalog) error {
	t.Helper()
	if catalog.Version != functionProbeCatalogVersion || catalog.CHVersion != MeasuredCHVersion || catalog.FixtureHash != conformance.StableHash(oracleSchemaDDL) {
		return fmt.Errorf("function probe catalog identity is stale")
	}
	columns := orderedOracleFixtureColumns(t)
	expected := buildDrawIndex(columns)
	prepareFunctionProbeIndex(expected, columns)
	seen := make(map[string]struct{}, len(catalog.Functions))
	for _, function := range catalog.Functions {
		candidate, found := expected[function.Name]
		if !found {
			return fmt.Errorf("function probe catalog has stale function %s", function.Name)
		}
		if _, duplicate := seen[function.Name]; duplicate {
			return fmt.Errorf("function probe catalog repeats function %s", function.Name)
		}
		seen[function.Name] = struct{}{}
		family := functionSemanticFamilies[functionRegistry[function.Name].family]
		axes := functionProbeAxisSet(family.probeAxes)
		if function.Family != family.name || function.ProbeRecipe != functionProbePolicyName(family.probePolicy) ||
			!reflect.DeepEqual(function.RequiredAxes, functionProbeAxisNames(family.probeAxes)) {
			return fmt.Errorf("function %s semantic family recipe is stale", function.Name)
		}
		if function.Spelling != candidate.spec.spelling || !reflect.DeepEqual(function.Signature, probeSignatureOf(candidate.signature)) {
			return fmt.Errorf("function %s signature is stale", function.Name)
		}
		probeIDs := make(map[string]struct{}, len(function.Probes))
		for _, probe := range function.Probes {
			if probe.ID == "" || probe.SQL == "" {
				return fmt.Errorf("function %s has an empty probe", function.Name)
			}
			if probe.ExpectedChgen != "accept" && probe.ExpectedChgen != "refuse" && probe.ExpectedChgen != "refuse_unsupported_result" {
				return fmt.Errorf("function %s probe %s has an unknown chgen expectation", function.Name, probe.ID)
			}
			if _, duplicate := probeIDs[probe.ID]; duplicate {
				return fmt.Errorf("function %s repeats probe %s", function.Name, probe.ID)
			}
			probeIDs[probe.ID] = struct{}{}
		}
		positions := make(map[string]probePosition)
		for _, position := range function.Positions {
			key := fmt.Sprintf("%d/%d", position.Arity, position.Position)
			if _, duplicate := positions[key]; duplicate {
				return fmt.Errorf("function %s repeats position %s", function.Name, key)
			}
			positions[key] = position
			if _, found := probeIDs[position.LegalProbeID]; !found {
				return fmt.Errorf("function %s position %s has no legal probe", function.Name, key)
			}
			if position.IllegalProbeID != "" {
				if _, found := probeIDs[position.IllegalProbeID]; !found {
					return fmt.Errorf("function %s position %s names a missing illegal probe", function.Name, key)
				}
			}
		}
		for _, arity := range candidate.signature.representativeArities() {
			sorts, accepted := candidate.signature.sortsForArity(arity)
			if !accepted {
				return fmt.Errorf("function %s lost representative arity %d", function.Name, arity)
			}
			if axes[probeAxisResultType] && !hasProbe(function.Probes, "legal_result", arity) {
				return fmt.Errorf("function %s has no result-type probe at arity %d", function.Name, arity)
			}
			if !axes[probeAxisArgumentDomain] {
				continue
			}
			for position := 0; position < arity; position++ {
				key := fmt.Sprintf("%d/%d", arity, position)
				coverage, found := positions[key]
				if !found {
					return fmt.Errorf("function %s has no coverage for position %s", function.Name, key)
				}
				if argumentSortUsesPool(sorts[position]) &&
					position < len(candidate.pools) && len(candidate.pools[position].illegal) > 0 && coverage.IllegalProbeID == "" {
					return fmt.Errorf("function %s position %s has no illegal complement probe", function.Name, key)
				}
			}
		}
		if axes[probeAxisArity] {
			for _, arity := range candidate.signature.rejectedBoundaryArities() {
				if !hasProbe(function.Probes, "illegal_arity", arity) {
					return fmt.Errorf("function %s has no rejected arity %d", function.Name, arity)
				}
			}
		}
		if axes[probeAxisPlacement] {
			arity := candidate.signature.representativeArities()[0]
			if !hasProbe(function.Probes, "legal_placement", arity) {
				return fmt.Errorf("function %s has no placement probe", function.Name)
			}
			if candidate.signature.place == placementWindow {
				kind := "illegal_bare_placement"
				if candidate.signature.allowBare {
					kind = "legal_bare_placement"
				}
				if !hasProbe(function.Probes, kind, arity) {
					return fmt.Errorf("function %s has no bare placement complement probe", function.Name)
				}
			}
		}
		if axes[probeAxisLambda] && !hasProbe(function.Probes, "legal_lambda", candidate.signature.representativeArities()[0]) {
			return fmt.Errorf("function %s has no lambda probe", function.Name)
		}
		if axes[probeAxisParameters] {
			if !hasProbe(function.Probes, "legal_parameters", candidate.signature.representativeArities()[0]) {
				return fmt.Errorf("function %s has no result-parameter probe", function.Name)
			}
			parameterSignature := functionSignature{forms: candidate.signature.parameterForms}
			for _, arity := range parameterSignature.representativeArities() {
				if arity == 0 {
					continue
				}
				if !hasProbe(function.Probes, "legal_parameter", arity) ||
					!hasProbe(function.Probes, "illegal_parameter_domain", arity) {
					return fmt.Errorf("function %s has incomplete parameter coverage at arity %d", function.Name, arity)
				}
				if len(candidate.signature.constantRanges) > 0 && !hasProbe(function.Probes, "illegal_parameter_value", arity) {
					return fmt.Errorf("function %s has no parameter value boundary at arity %d", function.Name, arity)
				}
			}
			if len(candidate.signature.parameterForms) > 1 {
				for _, arity := range parameterSignature.rejectedBoundaryArities() {
					if !hasProbe(function.Probes, "illegal_parameter_arity", arity) {
						return fmt.Errorf("function %s has no rejected parameter arity %d", function.Name, arity)
					}
				}
			}
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("function probe catalog covers %d functions, want %d", len(seen), len(expected))
	}
	return nil
}

func hasProbe(probes []functionProbe, kind string, arity int) bool {
	for _, probe := range probes {
		if probe.Kind == kind && probe.Arity == arity {
			return true
		}
	}
	return false
}

func isLegalFunctionProbeKind(kind string) bool {
	return strings.HasPrefix(kind, "legal")
}

func marshalFunctionProbeCatalog(t *testing.T, catalog functionProbeCatalog) []byte {
	t.Helper()
	data, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func cloneFunctionProbeCatalog(t *testing.T, catalog functionProbeCatalog) functionProbeCatalog {
	t.Helper()
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	var result functionProbeCatalog
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
