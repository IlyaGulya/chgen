package functionrules

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/IlyaGulya/chgen/internal/apiinventory"
	"github.com/IlyaGulya/chgen/internal/supportmanifest"
)

// GapReport distinguishes discovery from proof; it never executes discovered names.
type GapReport struct {
	FormatVersion int                 `json:"format_version"`
	Source        apiinventory.Source `json:"source"`
	Total         int                 `json:"total"`
	Supported     int                 `json:"supported"`
	Gaps          []Gap               `json:"gaps"`
}

// Gap retains exact server metadata and the current support verdict.
type Gap struct {
	Name            string                 `json:"name"`
	AliasTo         string                 `json:"alias_to,omitempty"`
	Origin          string                 `json:"origin"`
	Aggregate       bool                   `json:"aggregate"`
	CaseInsensitive bool                   `json:"case_insensitive"`
	Status          supportmanifest.Status `json:"status"`
	Reason          string                 `json:"reason"`
}

// Gaps joins discovery with measured support without executing any function.
func Gaps(inventory apiinventory.Inventory, manifest supportmanifest.Manifest) (GapReport, error) {
	if err := inventory.Validate(); err != nil {
		return GapReport{}, err
	}
	if inventory.Source.Version != Version || inventory.Source.Version != manifest.Source.Version || inventory.Source.Revision != manifest.Source.Revision {
		return GapReport{}, fmt.Errorf("function gaps require inventory and support measured on ClickHouse %s", Version)
	}
	statuses := make(map[string]supportmanifest.Status)
	for _, entry := range manifest.Entries {
		if entry.Kind == supportmanifest.KindFunction || entry.Kind == supportmanifest.KindFunctionAlias {
			statuses[entry.Name] = entry.Status
		}
	}
	report := GapReport{FormatVersion: 1, Source: inventory.Source, Gaps: []Gap{}}
	for _, set := range []apiinventory.FunctionSet{inventory.Functions.BuiltIn, inventory.Functions.UserDefined} {
		for _, list := range [][]apiinventory.Function{set.Canonical, set.Aliases} {
			for _, function := range list {
				report.Total++
				status := statuses[function.Name]
				if status == supportmanifest.SupportedMeasured {
					report.Supported++
					continue
				}
				if status == "" {
					status = supportmanifest.NotMeasured
				}
				reason := "no measured production rule"
				switch status {
				case supportmanifest.ExplicitlyRefused:
					reason = "explicitly refused; review measured refusal before expansion"
				case supportmanifest.PartiallyMeasured:
					reason = "only some argument products are measured"
				case supportmanifest.NotApplicable:
					reason = "outside the built-in support contract"
				}
				report.Gaps = append(report.Gaps, Gap{Name: function.Name, AliasTo: function.AliasTo, Origin: function.Origin, Aggregate: function.IsAggregate, CaseInsensitive: function.CaseInsensitive, Status: status, Reason: reason})
			}
		}
	}
	slices.SortFunc(report.Gaps, func(a, b Gap) int { return cmp.Compare(a.Name, b.Name) })
	return report, nil
}
