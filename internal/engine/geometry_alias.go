package engine

import "strings"

// withoutGeometryAliases exposes the structural Array/Tuple type used by
// computed results and argument-domain checks. Direct column declarations
// retain their aliases; this is not a global catalog normalization.
func withoutGeometryAliases(value CHType) CHType {
	point := CHType{Name: "Tuple", Params: []CHType{{Name: "Float64"}, {Name: "Float64"}}}
	array := func(inner CHType) CHType {
		return CHType{Name: "Array", Params: []CHType{inner}}
	}
	switch strings.ToLower(value.Name) {
	case "point":
		return point
	case "ring", "linestring":
		return array(point)
	case "polygon", "multilinestring":
		return array(array(point))
	case "multipolygon":
		return array(array(array(point)))
	}
	if len(value.Params) == 0 {
		return value
	}
	result := value
	result.Params = make([]CHType, len(value.Params))
	for index, parameter := range value.Params {
		result.Params[index] = withoutGeometryAliases(parameter)
	}
	return result
}
