package engine

import "strings"

// portableFunctionInput rejects measured unstable primitives through wrappers,
// and unstable aggregate markers regardless of the marker's aggregate name.
// It deliberately does not mistake Nullable(Float64) for bare Float64.
func portableFunctionInput(value CHType, excluded []string) bool {
	base := domainBaseType(value)
	inner, aggregate := simpleAggregateWrapperInner(base)
	for _, input := range excluded {
		if strings.EqualFold(base.String(), input) {
			return false
		}
		if aggregate {
			if strings.EqualFold(domainBaseType(inner).String(), input) {
				return false
			}
			if strings.HasPrefix(input, "SimpleAggregateFunction(anyLast, ") {
				want := strings.TrimSuffix(strings.TrimPrefix(input, "SimpleAggregateFunction(anyLast, "), ")")
				if strings.EqualFold(inner.String(), want) {
					return false
				}
			}
		}
	}
	return true
}
