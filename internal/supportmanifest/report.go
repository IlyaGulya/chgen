package supportmanifest

import "sort"

// Report contains coverage counts without source-code interpretation.
type Report struct {
	Total             int                     `json:"total"`
	Applicable        int                     `json:"applicable"`
	Measured          int                     `json:"measured"`
	MeasuredPct       float64                 `json:"measured_percent"`
	SupportedMeasured int                     `json:"supported_measured"`
	PartiallyMeasured int                     `json:"partially_measured"`
	SupportedPct      float64                 `json:"supported_percent"`
	StatusCounts      map[Status]int          `json:"status_counts"`
	KindCounts        map[Kind]map[Status]int `json:"kind_counts"`
}

// Coverage derives the support totals from a validated manifest.
func Coverage(manifest Manifest) Report {
	report := Report{
		Total:        len(manifest.Entries),
		StatusCounts: make(map[Status]int),
		KindCounts:   make(map[Kind]map[Status]int),
	}
	for _, entry := range manifest.Entries {
		report.StatusCounts[entry.Status]++
		if report.KindCounts[entry.Kind] == nil {
			report.KindCounts[entry.Kind] = make(map[Status]int)
		}
		report.KindCounts[entry.Kind][entry.Status]++
		if entry.Status != NotApplicable {
			report.Applicable++
		}
		if entry.Status == SupportedMeasured || entry.Status == ExplicitlyRefused || entry.Status == PartiallyMeasured {
			report.Measured++
		}
		if entry.Status == SupportedMeasured {
			report.SupportedMeasured++
		}
		if entry.Status == PartiallyMeasured {
			report.PartiallyMeasured++
		}
	}
	if report.Applicable > 0 {
		report.MeasuredPct = float64(report.Measured) * 100 / float64(report.Applicable)
		report.SupportedPct = float64(report.SupportedMeasured) * 100 / float64(report.Applicable)
	}
	return report
}

// SortedKinds returns all report kinds in stable order.
func (r Report) SortedKinds() []Kind {
	kinds := make([]Kind, 0, len(r.KindCounts))
	for kind := range r.KindCounts {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(a, b int) bool { return kinds[a] < kinds[b] })
	return kinds
}
