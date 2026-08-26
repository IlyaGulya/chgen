package engine

import "testing"

// A Nullable temporal column generates *time.Time. If the pointer form does
// not reach the import decision, the generated file loses "time" and cannot
// build. Guard the pointer path for every import-bearing leaf.
func TestPointerGoTypeStillDrivesImports(t *testing.T) {
	for _, leaf := range []string{"time.Time", "decimal.Decimal", "net.IP", "uuid.UUID"} {
		use := collectGoTypeUse([]Query{{Results: []Result{{GoType: "*" + leaf}}}})
		if !use.inParamsOrResults(leaf) {
			t.Errorf("*%s did not reach the import decision", leaf)
		}
	}
	// Nested through a slice too: []*time.Time.
	use := collectGoTypeUse([]Query{{Results: []Result{{GoType: "[]*time.Time"}}}})
	if !use.inParamsOrResults("time.Time") {
		t.Error("[]*time.Time did not reach the import decision")
	}
}
