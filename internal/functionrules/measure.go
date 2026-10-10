// Package functionrules measures bounded, side-effect-free function recipes.
// Measurements are candidates, never an unrestricted inference rule.
package functionrules

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IlyaGulya/chgen/internal/apiinventory"
)

const Version = "25.8.29.51"
const Profile = "numeric-unary-v1"

// The recipe allowlist is deliberate: discovering a name in system.functions
// does not authorize executing file, network, sleep, or stateful functions.
var names = []string{"acos", "asin", "atan", "cbrt", "cos", "cosh", "erf", "erfc", "exp", "exp10", "exp2", "lgamma", "log", "log10", "log2", "sign", "sin", "sinh", "sqrt", "tan", "tanh", "tgamma"}
var numeric = []string{"Int8", "Int16", "Int32", "Int64", "Int128", "Int256", "UInt8", "UInt16", "UInt32", "UInt64", "UInt128", "UInt256", "Float32", "Float64", "Bool"}

type Report struct {
	Format    int                 `json:"format"`
	Profile   string              `json:"profile"`
	Source    apiinventory.Source `json:"source"`
	Functions []Function          `json:"functions"`
}

type Function struct {
	Name            string `json:"name"`
	CaseInsensitive bool   `json:"case_insensitive"`
	Cells           []Cell `json:"cells"`
}

type Cell struct {
	ID            string   `json:"id"`
	Input         string   `json:"input"`
	Expression    string   `json:"expression"`
	Values        []string `json:"values"`
	Analysis      string   `json:"analysis,omitempty"`
	Execution     string   `json:"execution,omitempty"`
	AnalysisCode  int      `json:"analysis_code,omitzero"`
	ExecutionCode int      `json:"execution_code,omitzero"`
	ExecutionRows int      `json:"execution_rows,omitzero"`
}

type probe struct {
	id, input, expression string
	values                []string
}

func Names() []string { return slices.Clone(names) }

func numericCases() []string {
	result := slices.Clone(numeric)
	for _, width := range []struct{ bits, precision int }{{32, 9}, {64, 18}, {128, 38}, {256, 76}} {
		for scale := 0; scale <= width.precision; scale++ {
			result = append(result, fmt.Sprintf("Decimal%d(%d)", width.bits, scale))
		}
	}
	return append(result, "Decimal(9, 2)", "Decimal(18, 4)", "Decimal(38, 6)", "Decimal(76, 8)")
}

func wrappers(base string) []string {
	inputs := []string{base, "Nullable(" + base + ")", "SimpleAggregateFunction(anyLast, " + base + ")", "SimpleAggregateFunction(anyLast, Nullable(" + base + "))"}
	// ClickHouse rejects LowCardinality(Decimal) as a column type, before
	// function analysis. Do not mistake an invalid fixture for a type rule.
	if !strings.HasPrefix(base, "Decimal") {
		inputs = append(inputs, "LowCardinality("+base+")", "LowCardinality(Nullable("+base+"))")
	}
	return inputs
}

func plan(name string) []probe {
	var probes []probe
	for _, base := range numericCases() {
		values := []string{"1", "0", "2", "0"}
		if strings.HasPrefix(base, "Decimal") {
			scaleText := base[strings.LastIndexAny(base, "(,")+1 : len(base)-1]
			scale, _ := strconv.Atoi(strings.TrimSpace(scaleText))
			if scale > 0 {
				fraction := "0." + strings.Repeat("0", scale-1)
				values[0] = "'" + fraction + "1'"
				values[2] = "'" + fraction + "2'"
			}
		}
		for _, input := range wrappers(base) {
			rowValues := slices.Clone(values)
			if strings.Contains(input, "Nullable(") {
				rowValues[3] = "NULL"
			}
			probes = append(probes, probe{id: input, input: input, expression: name + "(c)", values: rowValues})
		}
	}
	for _, input := range []string{"String", "Nullable(String)", "LowCardinality(String)", "Date", "Date32", "DateTime64(3)", "Array(Int32)", "Tuple(Int32, String)", "UUID", "IPv4"} {
		seed := "'1'"
		switch input {
		case "Array(Int32)":
			seed = "[1]"
		case "Tuple(Int32, String)":
			seed = "(1, 'x')"
		case "UUID":
			seed = "'00000000-0000-0000-0000-000000000001'"
		case "IPv4":
			seed = "'127.0.0.1'"
		case "Date", "Date32":
			seed = "'2020-01-01'"
		case "DateTime64(3)":
			seed = "'2020-01-01 00:00:00'"
		}
		probes = append(probes, probe{id: input, input: input, expression: name + "(c)", values: []string{seed, seed, seed, seed}})
	}
	for _, shape := range []struct{ id, expression string }{
		{"arity-zero", name + "()"}, {"arity-two", name + "(c, c)"},
		{"over", name + "(c) OVER ()"}, {"parameters", name + "(1)(c)"},
	} {
		probes = append(probes, probe{id: shape.id, input: "Float64", expression: shape.expression, values: []string{"1", "0", "2", "0"}})
	}
	return probes
}

type server struct {
	endpoint string
	client   *http.Client
}

func (s server) query(ctx context.Context, sql string) (string, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, strings.NewReader(sql))
	if err != nil {
		return "", 0, fmt.Errorf("create measurement request")
	}
	response, err := s.client.Do(request)
	if err != nil {
		return "", 0, fmt.Errorf("measurement transport failed: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", 0, fmt.Errorf("read measurement response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		code, _ := strconv.Atoi(response.Header.Get("X-ClickHouse-Exception-Code"))
		if code == 0 {
			return "", 0, fmt.Errorf("measurement HTTP status %d", response.StatusCode)
		}
		return "", code, nil
	}
	return string(data), 0, nil
}

// Measure uses real Memory-table columns on an explicitly supplied disposable
// server. It creates and removes only its uniquely named fixture table.
func Measure(ctx context.Context, endpoint string, selected []string) (report Report, err error) {
	return MeasureProfile(ctx, endpoint, Profile, selected)
}

// MeasureProfile measures only the explicit safe recipes of a known profile.
func MeasureProfile(ctx context.Context, endpoint, profile string, selected []string) (report Report, err error) {
	allowed, err := profileNames(profile)
	if err != nil {
		return report, err
	}
	address, err := url.Parse(endpoint)
	if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Host == "" || address.User != nil || address.RawQuery != "" || address.Fragment != "" || (address.Path != "" && address.Path != "/") {
		return report, fmt.Errorf("use an explicit HTTP endpoint without credentials, query, or path")
	}
	if len(selected) == 0 {
		selected = allowed
	}
	selected = slices.Clone(selected)
	slices.Sort(selected)
	for i, name := range selected {
		if !slices.Contains(allowed, name) || (i > 0 && selected[i-1] == name) {
			return report, fmt.Errorf("unknown or repeated safe recipe %q", name)
		}
	}
	inventory, err := apiinventory.NewCollector(endpoint).Collect(ctx)
	if err != nil {
		return report, fmt.Errorf("collect measurement API: %w", err)
	}
	if inventory.Source.Version != Version {
		return report, fmt.Errorf("server version %s, require %s", inventory.Source.Version, Version)
	}
	report = Report{Format: 1, Profile: profile, Source: inventory.Source}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return report, err
	}
	table := "chgen_functionrules_" + hex.EncodeToString(nonce[:])
	s := server{endpoint: endpoint, client: &http.Client{Timeout: 30 * time.Second}}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, code, cleanupErr := s.query(cleanup, "DROP TABLE IF EXISTS "+table)
		if cleanupErr != nil || code != 0 {
			err = errors.Join(err, fmt.Errorf("remove owned measurement fixture: code=%d error=%v", code, cleanupErr))
		}
	}()
	columns := make(map[string]string)
	var declarations []string
	rows := make([][]string, 4)
	for _, probe := range profilePlan(profile, selected[0]) {
		if _, exists := columns[probe.input]; exists {
			continue
		}
		column := fmt.Sprintf("c%d", len(columns))
		columns[probe.input] = column
		declarations = append(declarations, column+" "+probe.input)
		for i := range rows {
			rows[i] = append(rows[i], probe.values[i])
		}
	}
	var tuples []string
	for _, row := range rows {
		tuples = append(tuples, "("+strings.Join(row, ",")+")")
	}
	for _, sql := range []string{"CREATE TABLE " + table + " (" + strings.Join(declarations, ",") + ") ENGINE=Memory SETTINGS allow_suspicious_low_cardinality_types=1", "INSERT INTO " + table + " VALUES " + strings.Join(tuples, ",")} {
		_, code, err := s.query(ctx, sql)
		if err != nil || code != 0 {
			return report, fmt.Errorf("prepare shared fixture: code=%d error=%v", code, err)
		}
	}
	for _, name := range selected {
		var metadata *apiinventory.Function
		for i := range inventory.Functions.BuiltIn.Canonical {
			if inventory.Functions.BuiltIn.Canonical[i].Name == name {
				metadata = &inventory.Functions.BuiltIn.Canonical[i]
				break
			}
		}
		if metadata == nil || metadata.IsAggregate {
			return report, fmt.Errorf("safe scalar recipe %s is not a canonical server function", name)
		}
		function := Function{Name: name, CaseInsensitive: metadata.CaseInsensitive}
		probes := profilePlan(profile, name)
		positiveBases := numericCases()
		if profile == StringProfile {
			positiveBases = stringBases
		}
		positiveProbeCount := 0
		for _, base := range positiveBases {
			positiveProbeCount += len(wrappers(base))
		}
		retryIndividuallyUntil := 0
		for start := 0; start < len(probes); {
			end := min(start+64, positiveProbeCount)
			if start < end && start >= retryIndividuallyUntil {
				cells, ok, err := s.batch(ctx, table, columns, probes[start:end])
				if err != nil {
					return report, err
				}
				if ok {
					function.Cells = append(function.Cells, cells...)
					start = end
					continue
				}
				retryIndividuallyUntil = end
			}
			probe := probes[start]
			start++
			expression := fixtureExpression(probe.expression, columns[probe.input])
			cell := Cell{ID: probe.id, Input: probe.input, Expression: probe.expression, Values: slices.Clone(probe.values)}
			analysis, code, err := s.query(ctx, "DESCRIBE TABLE (SELECT "+expression+" AS result FROM "+table+") FORMAT JSON")
			if err != nil {
				return report, err
			}
			cell.AnalysisCode = code
			if code == 0 {
				var describe struct {
					Data []struct {
						Type string `json:"type"`
					} `json:"data"`
				}
				if err := json.Unmarshal([]byte(analysis), &describe); err != nil || len(describe.Data) != 1 || describe.Data[0].Type == "" {
					return report, fmt.Errorf("invalid DESCRIBE witness for %s", name)
				}
				cell.Analysis = describe.Data[0].Type
			}
			execution, code, err := s.query(ctx, "SELECT toTypeName("+expression+"), ignore("+expression+") FROM "+table+" FORMAT TabSeparated")
			if err != nil {
				return report, err
			}
			cell.ExecutionCode = code
			if code == 0 {
				rows := strings.Split(strings.TrimSpace(execution), "\n")
				if len(rows) != len(probe.values) {
					return report, fmt.Errorf("execution row count differs for %s", name)
				}
				for _, row := range rows {
					fields := strings.Split(row, "\t")
					if len(fields) != 2 || fields[0] == "" || fields[1] != "0" || (cell.Execution != "" && cell.Execution != fields[0]) {
						return report, fmt.Errorf("invalid execution witness for %s", name)
					}
					cell.Execution = fields[0]
				}
				cell.ExecutionRows = len(rows)
			}
			function.Cells = append(function.Cells, cell)
		}
		report.Functions = append(report.Functions, function)
	}
	return report, nil
}
