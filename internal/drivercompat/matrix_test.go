package drivercompat

import (
	"reflect"
	"strings"
	"testing"
)

func TestSupportedMatrixIsPinned(t *testing.T) {
	want := []Cell{
		{MinimumGo: "1.24.0", DriverVersion: "v2.42.0"},
		{MinimumGo: "1.25.0", DriverVersion: "v2.47.0"},
	}
	got := Supported()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("supported matrix = %#v, want %#v", got, want)
	}
	if err := Validate(got); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsMatrixDrift(t *testing.T) {
	tests := []struct {
		name  string
		cells []Cell
		want  string
	}{
		{name: "empty", want: "matrix is empty"},
		{
			name:  "invalid Go version",
			cells: []Cell{{MinimumGo: "1.24", DriverVersion: "v2.42.0"}},
			want:  "invalid minimum Go version",
		},
		{
			name:  "floating driver version",
			cells: []Cell{{MinimumGo: "1.24.0", DriverVersion: "latest"}},
			want:  "invalid driver version",
		},
		{
			name: "duplicate",
			cells: []Cell{
				{MinimumGo: "1.24.0", DriverVersion: "v2.42.0"},
				{MinimumGo: "1.24.0", DriverVersion: "v2.42.0"},
			},
			want: "duplicate",
		},
		{
			name: "unsorted",
			cells: []Cell{
				{MinimumGo: "1.25.0", DriverVersion: "v2.47.0"},
				{MinimumGo: "1.24.0", DriverVersion: "v2.42.0"},
			},
			want: "not sorted",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Validate(test.cells)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want text %q", err, test.want)
			}
		})
	}
}

func TestFindRefusesUnknownPair(t *testing.T) {
	if _, err := Find("1.24.0", "v2.47.0"); err == nil {
		t.Fatal("Find() accepted an unknown pair")
	}
	if _, err := Find("1.24.0", "v2.42.0"); err != nil {
		t.Fatal(err)
	}
}
