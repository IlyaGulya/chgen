// Package drivercompat defines the generated-code compile matrix.
package drivercompat

import (
	"fmt"
	"regexp"
	"sort"
)

// Cell is one supported generated-code compile target.
// This target makes no claim about query execution.
type Cell struct {
	MinimumGo     string
	DriverVersion string
}

// Supported returns the complete generated-code compile matrix.
func Supported() []Cell {
	return []Cell{
		{MinimumGo: "1.24.0", DriverVersion: "v2.42.0"},
		{MinimumGo: "1.25.0", DriverVersion: "v2.47.0"},
	}
}

var (
	goVersionPattern     = regexp.MustCompile(`^[1-9][0-9]*\.[0-9]+\.0$`)
	driverVersionPattern = regexp.MustCompile(`^v2\.[0-9]+\.0$`)
)

// Validate checks one complete matrix.
func Validate(cells []Cell) error {
	if len(cells) == 0 {
		return fmt.Errorf("driver compatibility matrix is empty")
	}
	seen := make(map[Cell]struct{}, len(cells))
	for index, cell := range cells {
		if !goVersionPattern.MatchString(cell.MinimumGo) {
			return fmt.Errorf("matrix cell %d has invalid minimum Go version %q", index, cell.MinimumGo)
		}
		if !driverVersionPattern.MatchString(cell.DriverVersion) {
			return fmt.Errorf("matrix cell %d has invalid driver version %q", index, cell.DriverVersion)
		}
		if _, exists := seen[cell]; exists {
			return fmt.Errorf("matrix has duplicate Go %s and driver %s", cell.MinimumGo, cell.DriverVersion)
		}
		seen[cell] = struct{}{}
	}
	if !sort.SliceIsSorted(cells, func(left, right int) bool {
		if cells[left].MinimumGo != cells[right].MinimumGo {
			return cells[left].MinimumGo < cells[right].MinimumGo
		}
		return cells[left].DriverVersion < cells[right].DriverVersion
	}) {
		return fmt.Errorf("driver compatibility matrix is not sorted")
	}
	return nil
}

// Find returns one exact supported pair.
func Find(minimumGo, driverVersion string) (Cell, error) {
	cells := Supported()
	if err := Validate(cells); err != nil {
		return Cell{}, err
	}
	for _, cell := range cells {
		if cell.MinimumGo == minimumGo && cell.DriverVersion == driverVersion {
			return cell, nil
		}
	}
	return Cell{}, fmt.Errorf("Go %s and driver %s are not a supported pair", minimumGo, driverVersion)
}
