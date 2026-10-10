package argumentcoverage_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen/internal/argumentcoverage"
	"github.com/IlyaGulya/chgen/internal/functionrules"
)

func TestArgumentCoveragePreservesRefusalsAndBuildDifferences(t *testing.T) {
	data, err := os.ReadFile("../../testdata/clickhouse-function-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := functionrules.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	report, err := argumentcoverage.ArgumentCoverage(evidence)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, cell := range report.Cells {
		switch cell.Function + "/" + cell.ID {
		case "sin/Int32":
			if cell.Status != "supported" || cell.ChgenType != "Float64" {
				t.Fatalf("supported: %+v", cell)
			}
			seen["supported"] = true
		case "sin/String":
			if cell.Status != "both_refuse" || cell.Diagnostic == "" || cell.AnalysisCode != 43 {
				t.Fatalf("refusal: %+v", cell)
			}
			seen["refusal"] = true
		case "exp/Float32":
			if cell.Status != "build_dependent_refusal" || !strings.Contains(cell.Diagnostic, "build-dependent") || cell.BuildDifference == nil {
				t.Fatalf("build difference: %+v", cell)
			}
			seen["difference"] = true
		}
	}
	if len(seen) != 3 || report.Total != len(report.Cells) || report.Counts["build_dependent_refusal"] != 21 {
		t.Fatalf("incomplete coverage: %v, %v", seen, report.Counts)
	}
	broken := evidence
	broken.Functions = nil
	if _, err := argumentcoverage.ArgumentCoverage(broken); err == nil {
		t.Fatal("missing evidence accepted")
	}
	broken.Functions = evidence.Functions[:1]
	if _, err := argumentcoverage.ArgumentCoverage(broken); err == nil {
		t.Fatal("partial function roster accepted")
	}
}

func TestWrapperCoverageKeepsMeasuredCoordinatesAndRejectsCorruption(t *testing.T) {
	data, err := os.ReadFile("../../testdata/wrapper_grid.golden")
	if err != nil {
		t.Fatal(err)
	}
	report, err := argumentcoverage.WrapperCoverage(data)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 10629 || report.Counts["CHGEN_REFUSES_SERVER_ACCEPTS"] != 819 {
		t.Fatalf("lost cells: %+v", report.Counts)
	}
	if report.EntryCounts["fn/reverse"]["AGREE"] == 0 {
		t.Fatal("grid per-function coverage missing")
	}
	for _, mutation := range []string{
		strings.Replace(string(data), "# cells\t10629", "# cells\t1", 1),
		strings.Replace(string(data), "fn/abs/bare/arr\tBOTH_REFUSE", "fn/abs/bare/arr\tUNKNOWN", 1),
		strings.Replace(string(data), "BOTH_REFUSE\t<refused>\t43\t<refused>", "BOTH_REFUSE\tFloat64\t43\t<refused>", 1),
		string(data) + "fn/abs/bare/arr\tBOTH_REFUSE\t<refused>\t43\t<refused>\tabs(c_bare_arr)\n",
	} {
		if _, err := argumentcoverage.WrapperCoverage([]byte(mutation)); err == nil {
			t.Fatal("corrupt evidence accepted")
		}
	}
	again, err := argumentcoverage.WrapperCoverage(data)
	if err != nil || !reflect.DeepEqual(report, again) {
		t.Fatal("report is not deterministic")
	}
}

func TestCoverageConsistencyNeverWaivesKnownAcceptance(t *testing.T) {
	for _, status := range []string{"type_mismatch", "chgen_accepts_server_refuses", "execution_refusal", "analysis_execution_mismatch", "build_dependent_acceptance"} {
		report := argumentcoverage.ArgumentReport{Counts: map[string]int{status: 1}}
		if report.Consistent() {
			t.Errorf("unsafe %s must fail", status)
		}
	}
	for _, status := range []string{"MISMATCH", "CHGEN_TYPES_SERVER_REFUSES", "CHGEN_TYPES_SERVER_REFUSES_PAIR"} {
		report := argumentcoverage.ArgumentReport{WrapperGrid: &argumentcoverage.WrapperReport{Counts: map[string]int{status: 1}}}
		if report.Consistent() {
			t.Errorf("unsafe grid %s must fail", status)
		}
	}
}

func TestCoverageDoesNotCallAnalysisOnlyAcceptanceAUsableGap(t *testing.T) {
	data, err := os.ReadFile("../../testdata/clickhouse-string-function-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := functionrules.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	report, err := argumentcoverage.ArgumentCoverage(evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, cell := range report.Cells {
		if cell.Function == "lowerUTF8" && cell.Input == "Nullable(FixedString(8))" {
			if cell.Status != "chgen_refuses_execution_refuses" || cell.ExecutionCode != 36 || cell.Analysis == "" || cell.Diagnostic == "" {
				t.Fatalf("analysis success must not hide execution refusal: %+v", cell)
			}
			return
		}
	}
	t.Fatal("missing measured boundary")
}

func TestArgumentCoverageDoesNotWaiveWrongTypes(t *testing.T) {
	data, err := os.ReadFile("../../testdata/clickhouse-function-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := functionrules.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	for i := range evidence.Functions {
		if evidence.Functions[i].Name != "sin" {
			continue
		}
		for j := range evidence.Functions[i].Cells {
			cell := &evidence.Functions[i].Cells[j]
			if cell.ID == "Int32" {
				cell.Analysis, cell.Execution = "String", "String"
			}
		}
	}
	report, err := argumentcoverage.ArgumentCoverage(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if report.Consistent() || report.Counts["type_mismatch"] != 1 {
		t.Fatal("wrong inferred type was hidden")
	}
	if report.FunctionCounts["sin"]["type_mismatch"] != 1 {
		t.Fatal("per-function boundaries missing")
	}
}
