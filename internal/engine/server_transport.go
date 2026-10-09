package engine

import "fmt"

// Temporal values travel as exact integers, not calendar strings. Explicit SQL
// constructors preserve pre-epoch fractions and avoid server timezone parsing.
func serverTransportType(t CHType) CHType {
	switch t.Name {
	case "DateTime", "DateTime64":
		return CHType{Name: "Int64"}
	case "Date", "Date32":
		return CHType{Name: "String"}
	case "LowCardinality":
		return serverTransportType(t.Params[0])
	case "SimpleAggregateFunction":
		return serverTransportType(t.Params[1])
	}
	result := t
	result.Params = make([]CHType, len(t.Params))
	for i, param := range t.Params {
		result.Params[i] = serverTransportType(param)
	}
	return result
}

func serverTransportSQL(t CHType, input string) string {
	if !serverTypeContains(t, "DateTime64") {
		return input
	}
	switch t.Name {
	case "DateTime64":
		return "fromUnixTimestamp64Nano(" + input + ", 'UTC')"
	case "Nullable", "LowCardinality":
		return serverTransportSQL(t.Params[0], input)
	case "SimpleAggregateFunction":
		return serverTransportSQL(t.Params[1], input)
	case "Array":
		return fmt.Sprintf("arrayMap(item -> %s, %s)", serverTransportSQL(t.Params[0], "item"), input)
	case "Map":
		return fmt.Sprintf("mapApply((key, item) -> (%s, %s), %s)", serverTransportSQL(t.Params[0], "key"), serverTransportSQL(t.Params[1], "item"), input)
	}
	return input
}
