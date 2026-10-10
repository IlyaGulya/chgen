package functionrules

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
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
	var report Report
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return report, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return report, fmt.Errorf("evidence must contain exactly one JSON document")
	}
	if report.Format != 1 || report.Profile != Profile || report.Source.Version != Version || report.Source.Revision == 0 || report.Source.BuildID == "" || len(report.Functions) == 0 {
		return report, fmt.Errorf("incomplete or incompatible measurement provenance")
	}
	seen := make(map[string]bool)
	for _, function := range report.Functions {
		if !slices.Contains(names, function.Name) || seen[function.Name] {
			return report, fmt.Errorf("unknown or duplicate function recipe %q", function.Name)
		}
		seen[function.Name] = true
		probes := plan(function.Name)
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
	quoted := make([]string, len(accepted))
	for i, name := range accepted {
		quoted[i] = strconv.Quote(name)
	}
	spelling := ""
	if !function.CaseInsensitive {
		spelling = "exactSpelling: " + strconv.Quote(function.Name) + ","
	}
	return fmt.Sprintf(`%q: {
%s
family: semanticFamilyFixedResult,
rule: fixedFunctionType(%q),
class: wrapperTransparent,
strategy: argsIndependent,
domain: &argumentDomain{
name: "measured numeric domain",
accepts: func(value CHType) bool {
if arithmeticDecimalType(value) { return true }
if len(value.Params) != 0 { return false }
switch value.normalizedName() {
case %s: return true
}
return false
},
expected: %q,
},
gen: scalarCall(%q, 1),
resultMode: resultRuleGeneric,
domainMode: argumentDomainRestricted,
domainArgs: []int{0},
parameterPolicy: parameterResultCurated,
evidence: %q,
},`, function.Name, spelling, result, strings.Join(quoted, ", "), strings.Join(accepted, ", ")+", Decimal", function.Name, "functionrules/"+Profile+"/"+digest), nil
}

func owns(entry ast.Expr) bool {
	pair, ok := entry.(*ast.KeyValueExpr)
	if !ok {
		return false
	}
	value, ok := pair.Value.(*ast.CompositeLit)
	if !ok {
		return false
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
		return err == nil && strings.HasPrefix(evidence, "functionrules/"+Profile+"/")
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
	hash := sha256.Sum256(encoded)
	digest := hex.EncodeToString(hash[:])
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
		measured[function.Name] = true
	}
	existing := make(map[string]int)
	for i, element := range literal.Elts {
		if entry, ok := element.(*ast.KeyValueExpr); ok {
			if key, ok := entry.Key.(*ast.BasicLit); ok {
				name, _ := strconv.Unquote(key.Value)
				if owns(element) && !measured[name] {
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
		text, err := specification(function, digest)
		if err != nil {
			if i, exists := existing[function.Name]; exists && owns(literal.Elts[i]) {
				return nil, nil, fmt.Errorf("generated rule %s no longer satisfies its contract: %w", function.Name, err)
			}
			continue
		}
		if i, found := existing[function.Name]; found {
			if !owns(literal.Elts[i]) {
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
