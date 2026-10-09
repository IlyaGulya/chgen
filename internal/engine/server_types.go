package engine

import (
	"fmt"
	"strconv"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// In server mode ClickHouse validates timezone constructors. Offline measured
// constructor domains must not reject server-confirmed metadata or parameters.
func parseServerCHType(columnType clickhouse.ColumnType) (CHType, error) {
	switch typ := columnType.(type) {
	case *clickhouse.TypeWithParams:
		name := canonicalCHTypeName(typ.Name.Name)
		if name != "DateTime" && name != "DateTime64" {
			return parseCHType(columnType)
		}
		var params []string
		for _, param := range typ.Params {
			params = append(params, clickhouse.Format(param))
		}
		if name == "DateTime" {
			if len(params) != 1 || !isQuotedTypeString(params[0]) {
				return CHType{}, fmt.Errorf("DateTime expects a timezone string")
			}
		} else {
			if len(params) < 1 || len(params) > 2 {
				return CHType{}, fmt.Errorf("DateTime64 expects precision and optional timezone")
			}
			precision, err := strconv.Atoi(params[0])
			if err != nil || precision < 0 || precision > 9 {
				return CHType{}, fmt.Errorf("DateTime64 precision must be from 0 through 9")
			}
			if len(params) == 2 && !isQuotedTypeString(params[1]) {
				return CHType{}, fmt.Errorf("DateTime64 timezone must be a string")
			}
		}
		return CHType{Name: name, LiteralParams: params}, nil
	case *clickhouse.ComplexType:
		result := CHType{Name: canonicalCHTypeName(typ.Name.Name)}
		for _, param := range typ.Params {
			parsed, err := parseServerCHType(param)
			if err != nil {
				return CHType{}, err
			}
			result.Params = append(result.Params, parsed)
		}
		if _, err := goType(result); err != nil {
			return CHType{}, err
		}
		return result, nil
	default:
		return parseCHType(columnType)
	}
}

func parseServerType(typeText string) (CHType, error) {
	_, contract, err := parseTypeContractWith(resultCHTypeDirective+" value "+strings.TrimSpace(typeText), 1, parseServerCHType)
	return contract.typeOf, err
}
