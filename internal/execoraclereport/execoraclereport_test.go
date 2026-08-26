package execoraclereport

import (
	"strings"
	"testing"
)

func TestGateRefusesUnknownDateTime64Row(t *testing.T) {
	known := Finding{
		Case: "rand_57", CHType: "DateTime64(3)", Direction: "scan",
		RowID: 2, Class: "runtime_error",
	}
	knownSignature := Signature(known)
	baseline := Baseline{
		CHVersion: "25.8.29.51",
		Accepted:  []string{knownSignature},
		Reasons: map[string]string{
			knownSignature: "The driver wraps the measured value from 2299 into 1715.",
		},
	}
	report := &Report{
		CHVersion: "25.8.29.51", Cases: 1,
		Findings: []Finding{{
			Case: "rand_295", CHType: "DateTime64(3)", Direction: "scan",
			RowID: 2, Class: "runtime_error",
		}},
	}

	result, err := baseline.Gate(report)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK() {
		t.Fatal("the gate accepted an unknown DateTime64 signature")
	}
	if len(result.New) != 1 || !strings.Contains(result.New[0], "rand_295") {
		t.Fatalf("new signatures = %v, want rand_295", result.New)
	}
}

func TestSignatureIncludesMeasuredTypeAndRow(t *testing.T) {
	base := Finding{Case: "rand_57", CHType: "DateTime64(3)", Direction: "scan", RowID: 2, Class: "runtime_error"}
	changedRow := base
	changedRow.RowID = 1
	changedType := base
	changedType.CHType = "Nullable(DateTime64(3))"
	if Signature(base) == Signature(changedRow) {
		t.Fatal("a row change did not change the signature")
	}
	if Signature(base) == Signature(changedType) {
		t.Fatal("a type change did not change the signature")
	}
}

func TestBaselineNeedsOneReasonForEachSignature(t *testing.T) {
	baseline := Baseline{Accepted: []string{"a", "b"}, Reasons: map[string]string{"a": "measured"}}
	if _, err := baseline.Marshal(); err == nil || !strings.Contains(err.Error(), "has no measured reason") {
		t.Fatalf("Marshal error = %v, want missing measured reason", err)
	}
}

func TestMandatoryGateRefusesExecutionAndUnsupportedWitnessLoss(t *testing.T) {
	execution := "canary_null_zero:probe:value_distortion:row=0:type=Nullable(Int32)"
	unsupported := "unsupported_tuple:generation:chgen_unsupported:row=-1:type=Tuple(Int32, String)"
	baseline := Baseline{Mandatory: []string{execution, unsupported}}
	valid := &Report{Findings: []Finding{
		{Case: "canary_null_zero", Direction: "probe", Class: "value_distortion", RowID: 0, CHType: "Nullable(Int32)"},
		{Case: "unsupported_tuple", Direction: "generation", Class: "chgen_unsupported", RowID: -1, CHType: "Tuple(Int32, String)"},
	}}
	if err := baseline.RequireMandatory(valid); err != nil {
		t.Fatal(err)
	}
	for name, findings := range map[string][]Finding{
		"execution witness":   valid.Findings[1:],
		"unsupported witness": valid.Findings[:1],
	} {
		t.Run(name, func(t *testing.T) {
			mutated := *valid
			mutated.Findings = findings
			if err := baseline.RequireMandatory(&mutated); err == nil {
				t.Fatal("mandatory gate accepted a missing witness")
			}
		})
	}
}
