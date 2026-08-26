package apiinventory

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Difference contains all API names that differ between two inventories.
type Difference struct {
	ReferenceSource Source          `json:"reference_source"`
	CandidateSource Source          `json:"candidate_source"`
	Sections        []SectionChange `json:"sections"`
}

// SectionChange names additions, removals, and metadata changes in one class.
type SectionChange struct {
	Section  string   `json:"section"`
	Added    []string `json:"added,omitempty"`
	Removed  []string `json:"removed,omitempty"`
	Modified []string `json:"modified,omitempty"`
}

// Identical reports whether the two inventories have the same source and API.
func (d Difference) Identical() bool {
	return d.ReferenceSource == d.CandidateSource && len(d.Sections) == 0
}

// Compare returns a name-based difference for every inventory class.
func Compare(before, after Inventory) (Difference, error) {
	if err := before.Validate(); err != nil {
		return Difference{}, fmt.Errorf("validate reference inventory: %w", err)
	}
	if err := after.Validate(); err != nil {
		return Difference{}, fmt.Errorf("validate candidate inventory: %w", err)
	}
	difference := Difference{ReferenceSource: before.Source, CandidateSource: after.Source}
	sections := []struct {
		name   string
		before any
		after  any
	}{
		{"functions.built_in.canonical", before.Functions.BuiltIn.Canonical, after.Functions.BuiltIn.Canonical},
		{"functions.built_in.aliases", before.Functions.BuiltIn.Aliases, after.Functions.BuiltIn.Aliases},
		{"functions.user_defined.canonical", before.Functions.UserDefined.Canonical, after.Functions.UserDefined.Canonical},
		{"functions.user_defined.aliases", before.Functions.UserDefined.Aliases, after.Functions.UserDefined.Aliases},
		{"data_type_families.canonical", before.DataTypeFamilies.Canonical, after.DataTypeFamilies.Canonical},
		{"data_type_families.aliases", before.DataTypeFamilies.Aliases, after.DataTypeFamilies.Aliases},
		{"aggregate_function_combinators", before.AggregateFunctionCombinators, after.AggregateFunctionCombinators},
		{"table_functions", before.TableFunctions, after.TableFunctions},
		{"settings", before.Settings, after.Settings},
	}
	for _, section := range sections {
		change, err := compareSection(section.name, section.before, section.after)
		if err != nil {
			return Difference{}, err
		}
		if len(change.Added)+len(change.Removed)+len(change.Modified) > 0 {
			difference.Sections = append(difference.Sections, change)
		}
	}
	return difference, nil
}

func compareSection(name string, before, after any) (SectionChange, error) {
	beforeRows, err := rowsByName(before)
	if err != nil {
		return SectionChange{}, fmt.Errorf("read reference section %s: %w", name, err)
	}
	afterRows, err := rowsByName(after)
	if err != nil {
		return SectionChange{}, fmt.Errorf("read candidate section %s: %w", name, err)
	}
	change := SectionChange{Section: name}
	for itemName, beforeRow := range beforeRows {
		afterRow, found := afterRows[itemName]
		if !found {
			change.Removed = append(change.Removed, itemName)
			continue
		}
		if string(beforeRow) != string(afterRow) {
			change.Modified = append(change.Modified, itemName)
		}
	}
	for itemName := range afterRows {
		if _, found := beforeRows[itemName]; !found {
			change.Added = append(change.Added, itemName)
		}
	}
	sort.Strings(change.Added)
	sort.Strings(change.Removed)
	sort.Strings(change.Modified)
	return change, nil
}

func rowsByName(rows any) (map[string]json.RawMessage, error) {
	data, err := json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	var objects []map[string]json.RawMessage
	if err := json.Unmarshal(data, &objects); err != nil {
		return nil, err
	}
	result := make(map[string]json.RawMessage, len(objects))
	for _, object := range objects {
		var name string
		if err := json.Unmarshal(object["name"], &name); err != nil {
			return nil, err
		}
		row, err := json.Marshal(object)
		if err != nil {
			return nil, err
		}
		result[name] = row
	}
	return result, nil
}

// String returns a stable, concise API difference report.
func (d Difference) String() string {
	var output strings.Builder
	if d.Identical() {
		return "IDENTICAL\n"
	}
	if d.ReferenceSource != d.CandidateSource {
		output.WriteString("SOURCE CHANGED\n")
		fmt.Fprintf(&output, "  - version=%s revision=%d build_id=%s\n",
			d.ReferenceSource.Version, d.ReferenceSource.Revision, d.ReferenceSource.BuildID)
		fmt.Fprintf(&output, "  + version=%s revision=%d build_id=%s\n",
			d.CandidateSource.Version, d.CandidateSource.Revision, d.CandidateSource.BuildID)
	}
	for _, section := range d.Sections {
		fmt.Fprintf(&output, "%s\n", section.Section)
		writeNames(&output, "+", section.Added)
		writeNames(&output, "-", section.Removed)
		writeNames(&output, "~", section.Modified)
	}
	return output.String()
}

func writeNames(output *strings.Builder, marker string, names []string) {
	for _, name := range names {
		fmt.Fprintf(output, "  %s %s\n", marker, name)
	}
}
