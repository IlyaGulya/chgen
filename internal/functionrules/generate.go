package functionrules

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"slices"
	"strconv"
	"strings"
)

// Decode refuses partial or foreign evidence before any source is written.
func Decode(data []byte) (Report, error) {
	var header struct {
		Format int `json:"format"`
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&header); err != nil {
		return Report{}, err
	}
	if header.Format == 2 {
		return decodeCompact(data)
	}
	return decodeExpanded(data)
}

func decodeExpanded(data []byte) (Report, error) {
	var report Report
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return report, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return report, fmt.Errorf("evidence must contain exactly one JSON document")
	}
	allowed, profileErr := profileNames(report.Profile)
	if profileErr != nil || report.Format != 1 || report.Source.Version != Version || report.Source.Revision == 0 || report.Source.BuildID == "" || len(report.Functions) == 0 {
		return report, fmt.Errorf("incomplete or incompatible measurement provenance")
	}
	seen := make(map[string]bool)
	for _, function := range report.Functions {
		if !slices.Contains(allowed, function.Name) || seen[function.Name] {
			return report, fmt.Errorf("unknown or duplicate function recipe %q", function.Name)
		}
		seen[function.Name] = true
		probes := profilePlan(report.Profile, function.Name)
		if len(function.Cells) != len(probes) {
			return report, fmt.Errorf("%s has an incomplete probe matrix", function.Name)
		}
		for i, cell := range function.Cells {
			probe := probes[i]
			if cell.ID != probe.id || cell.Input != probe.input || cell.Expression != probe.expression || !slices.Equal(cell.Values, probe.values) {
				return report, fmt.Errorf("%s has a foreign or reordered probe %s", function.Name, cell.ID)
			}
			if (cell.Analysis == "") != (cell.AnalysisCode > 0) || (cell.Execution == "") != (cell.ExecutionCode > 0) || cell.AnalysisCode < 0 || cell.ExecutionCode < 0 {
				return report, fmt.Errorf("%s has missing analysis or execution evidence", function.Name)
			}
			if (cell.ExecutionCode == 0 && cell.ExecutionRows != len(probe.values)) || (cell.ExecutionCode != 0 && cell.ExecutionRows != 0) {
				return report, fmt.Errorf("%s has missing execution row witnesses", function.Name)
			}
		}
	}
	return report, nil
}

func expectedWrapper(input, result string) string {
	nullable := strings.Contains(input, "Nullable(")
	if nullable {
		result = "Nullable(" + result + ")"
	}
	if strings.HasPrefix(input, "LowCardinality(") {
		result = "LowCardinality(" + result + ")"
	}
	return result
}

func specification(function Function, digest string) (string, error) {
	excluded, err := nonPortableInputs(function)
	if err != nil {
		return "", err
	}
	byID := make(map[string]Cell)
	for _, cell := range function.Cells {
		byID[cell.ID] = cell
	}
	for _, id := range []string{"arity-zero", "arity-two", "over", "parameters"} {
		cell := byID[id]
		if !slices.Contains([]int{42, 63, 309}, cell.AnalysisCode) || !slices.Contains([]int{42, 63, 309}, cell.ExecutionCode) {
			return "", fmt.Errorf("%s is accepted; unary scalar signature is not proved", id)
		}
	}
	numericInputs := make(map[string]bool)
	for _, base := range numericCases() {
		for _, input := range wrappers(base) {
			numericInputs[input] = true
		}
	}
	for _, probe := range plan(function.Name) {
		if slices.Contains([]string{"arity-zero", "arity-two", "over", "parameters"}, probe.id) {
			continue
		}
		if numericInputs[probe.input] {
			continue
		}
		cell := byID[probe.id]
		if cell.AnalysisCode != 43 || cell.ExecutionCode != 43 {
			return "", fmt.Errorf("negative domain %s is not proved", probe.input)
		}
	}
	result := ""
	var accepted []string
	for _, base := range numericCases() {
		cell := byID[base]
		if slices.Contains(excluded, base) {
			// This is a legal server argument, not a negative-domain witness.
			// Its build-dependent result is refused by the full-input guard.
			accepted = append(accepted, strings.ToLower(base))
			continue
		}
		if cell.AnalysisCode != 0 {
			if strings.HasPrefix(base, "Decimal") {
				return "", fmt.Errorf("incomplete Decimal domain at %s", base)
			}
			for _, input := range wrappers(base) {
				refusal := byID[input]
				if refusal.AnalysisCode != 43 || refusal.ExecutionCode != 43 {
					return "", fmt.Errorf("type-domain refusal is not proved for %s", input)
				}
			}
			continue
		}
		if cell.Analysis != cell.Execution {
			return "", fmt.Errorf("analysis/execution disagree for %s", base)
		}
		if cell.Analysis != "Float64" && cell.Analysis != "Int8" {
			return "", fmt.Errorf("result %s needs another family", cell.Analysis)
		}
		if result == "" {
			result = cell.Analysis
		}
		if result != cell.Analysis {
			return "", fmt.Errorf("type-dependent result needs another family")
		}
		for _, input := range wrappers(base) {
			wrapper := byID[input]
			if excludedCell(function.Name, wrapper) {
				continue
			}
			want := expectedWrapper(input, result)
			if wrapper.Analysis != want || wrapper.Execution != want || wrapper.AnalysisCode != 0 || wrapper.ExecutionCode != 0 {
				return "", fmt.Errorf("wrapper transport is not proved for %s", input)
			}
		}
		if !strings.HasPrefix(base, "Decimal") {
			accepted = append(accepted, strings.ToLower(base))
		}
	}
	if result == "" || !slices.Contains(accepted, "int32") || !slices.Contains(accepted, "float64") {
		return "", fmt.Errorf("missing representative legal numeric types")
	}
	slices.Sort(accepted)
	domain := fmt.Sprintf("primitives: %q,\ndecimal: true,", strings.Join(accepted, " "))
	text := renderSpecification(function, digest, Profile, result, domain, strings.Join(accepted, ", ")+", Decimal", "")
	if len(excluded) != 0 {
		var quoted []string
		for _, input := range excluded {
			quoted = append(quoted, strconv.Quote(input))
		}
		text = strings.Replace(text, "spelling:", "nonPortableInputs: []string{"+strings.Join(quoted, ",")+"},\nspelling:", 1)
	}
	return text, nil
}

func profileSpecification(profile string, function Function, digest string) (string, error) {
	if profile == StringProfile {
		return stringSpecification(function, digest)
	}
	return specification(function, digest)
}

// DeclineReason explains why measured behavior cannot become a production rule.
func DeclineReason(profile string, function Function) string {
	_, err := profileSpecification(profile, function, "")
	if err == nil {
		return ""
	}
	return err.Error()
}

func renderSpecification(function Function, digest, profile, result, domain, expected, transport string) string {
	spelling := ""
	if !function.CaseInsensitive {
		spelling = "exactSpelling: " + strconv.Quote(function.Name) + ",\n"
	}
	return fmt.Sprintf(`%q: measuredUnarySpec(measuredUnaryFamily{
result: %q,
%s
expected: %q,
%s
}, measuredUnaryMember{
spelling: %q,
%s
evidence: %q,
}),`, strings.ToLower(function.Name), result, domain, expected, transport, function.Name, spelling, "functionrules/"+profile+"/"+digest)
}

func owns(entry ast.Expr, profile string) bool {
	pair, ok := entry.(*ast.KeyValueExpr)
	if !ok {
		return false
	}
	value, ok := pair.Value.(*ast.CompositeLit)
	if !ok {
		call, valid := pair.Value.(*ast.CallExpr)
		if !valid || len(call.Args) != 2 {
			return false
		}
		helper, valid := call.Fun.(*ast.Ident)
		if !valid || helper.Name != "measuredUnarySpec" {
			return false
		}
		value, ok = call.Args[1].(*ast.CompositeLit)
		if !ok {
			return false
		}
	}
	for _, element := range value.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		name, ok := field.Key.(*ast.Ident)
		if !ok || name.Name != "evidence" {
			continue
		}
		literal, ok := field.Value.(*ast.BasicLit)
		if !ok {
			return false
		}
		evidence, err := strconv.Unquote(literal.Value)
		return err == nil && strings.HasPrefix(evidence, "functionrules/"+profile+"/")
	}
	return false
}

// Generate adds eligible measured specifications to the existing central
// registry. It refuses to overwrite hand-written rules. Its output is a review
// candidate, not proof that compilation and generated execution have passed.
func Generate(source []byte, report Report) ([]byte, []string, error) {
	encoded, err := json.Marshal(report)
	if err != nil {
		return nil, nil, err
	}
	if _, err := Decode(encoded); err != nil {
		return nil, nil, err
	}
	// Semantic identity excludes build IDs; actual witnesses retain provenance.
	// The restriction policy is part of the generated contract as well.
	portable := report
	portable.Functions = make([]Function, len(report.Functions))
	for i, function := range report.Functions {
		portable.Functions[i] = Function{Name: function.Name, CaseInsensitive: function.CaseInsensitive}
		for _, cell := range function.Cells {
			if report.Profile != Profile || !excludedCell(function.Name, cell) {
				portable.Functions[i].Cells = append(portable.Functions[i].Cells, cell)
			}
		}
	}
	semanticDigest := identity(portable).SemanticDigest
	witness, err := buildVariants()
	if err != nil {
		return nil, nil, err
	}
	policyDigest := digest(struct {
		Version, Profile, Plan string
		Cells                  []CellDifference
	}{SemanticsVersion, witness.Profile, witness.Expected.PlanDigest, witness.Cells})
	digest := digest(struct{ Semantics, Policy string }{semanticDigest, policyDigest})
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, "registry.go", source, parser.ParseComments)
	if err != nil {
		return nil, nil, err
	}
	var literal *ast.CompositeLit
	ast.Inspect(file, func(node ast.Node) bool {
		decl, ok := node.(*ast.ValueSpec)
		if ok && len(decl.Names) == 1 && decl.Names[0].Name == "functionSemanticSpecs" && len(decl.Values) == 1 {
			literal, _ = decl.Values[0].(*ast.CompositeLit)
		}
		return true
	})
	if literal == nil {
		return nil, nil, fmt.Errorf("central functionSemanticSpecs declaration is missing")
	}
	measured := make(map[string]bool)
	for _, function := range report.Functions {
		measured[strings.ToLower(function.Name)] = true
	}
	existing := make(map[string]int)
	for i, element := range literal.Elts {
		if entry, ok := element.(*ast.KeyValueExpr); ok {
			if key, ok := entry.Key.(*ast.BasicLit); ok {
				name, _ := strconv.Unquote(key.Value)
				if owns(element, report.Profile) && !measured[name] {
					return nil, nil, fmt.Errorf("missing evidence for owned rule %s; measure the complete generated roster", name)
				}
				existing[name] = i
			}
		}
	}
	type edit struct {
		start, end int
		text       string
	}
	var edits []edit
	var additions strings.Builder
	var added []string
	for _, function := range report.Functions {
		text, err := profileSpecification(report.Profile, function, digest)
		key := strings.ToLower(function.Name)
		if err != nil {
			if i, exists := existing[key]; exists && owns(literal.Elts[i], report.Profile) {
				return nil, nil, fmt.Errorf("generated rule %s no longer satisfies its contract: %w", function.Name, err)
			}
			continue
		}
		if i, found := existing[key]; found {
			if !owns(literal.Elts[i], report.Profile) {
				return nil, nil, fmt.Errorf("refuse to overwrite existing manual rule %s", function.Name)
			}
			start := files.Position(literal.Elts[i].Pos()).Offset
			prefix := ""
			line := bytes.LastIndexByte(source[:start], '\n') + 1
			if len(bytes.TrimSpace(source[line:start])) != 0 {
				prefix = "\n"
			}
			edits = append(edits, edit{start, files.Position(literal.Elts[i].End()).Offset, prefix + strings.TrimSuffix(text, ",")})
		} else {
			additions.WriteString(text + "\n")
		}
		added = append(added, function.Name)
	}
	if len(added) == 0 {
		return nil, nil, fmt.Errorf("no functions satisfy the measured fixed-result contract")
	}
	if additions.Len() != 0 {
		position := files.Position(literal.Rbrace).Offset
		prefix := ""
		line := bytes.LastIndexByte(source[:position], '\n') + 1
		if len(bytes.TrimSpace(source[line:position])) != 0 {
			prefix = "\n"
		}
		edits = append(edits, edit{position, position, prefix + additions.String()})
	}
	slices.SortFunc(edits, func(a, b edit) int {
		return cmp.Compare(b.start, a.start)
	})
	output := slices.Clone(source)
	for _, edit := range edits {
		output = append(append(append([]byte(nil), output[:edit.start]...), edit.text...), output[edit.end:]...)
	}
	formatted, err := format.Source(output)
	return formatted, added, err
}
