package engine

import (
	"fmt"

	"github.com/IlyaGulya/chgen/internal/diagnostic"
)

// QueryDiagnostics exposes actual escape-hatch use, not merely annotations.
// A matching inferred type or a setting promoted into the roster needs no trust.
func QueryDiagnostics(query Query) []diagnostic.Detail {
	var result []diagnostic.Detail
	add := func(code, stage, message, hint string) {
		result = append(result, diagnostic.Detail{
			Code: code, Status: diagnostic.Unknown, Stage: stage, Message: message, Hint: hint,
			File: query.File, Line: query.Line, Query: query.Name,
		})
	}
	for _, name := range query.unverifiedResults {
		add("result-type-asserted", "contract", fmt.Sprintf("result %s uses a client-asserted ClickHouse type", name),
			"Verify the contract against your server. Generated read-side metadata checks detect type mismatches, not incorrect values or query semantics.")
	}
	for _, expression := range query.unverifiedExpressions {
		add("expression-type-asserted", "contract", fmt.Sprintf("expression %s uses a client-asserted ClickHouse type", expression),
			"Verify this intermediate expression against your server. Output metadata checks cannot prove intermediate types or predicate semantics. Use check -require-confirmed to forbid assumptions.")
	}
	for _, name := range query.uncheckedSettings {
		add("setting-unchecked", "settings", fmt.Sprintf("SETTINGS %s is explicitly excluded from type resolution", name),
			"The SQL retains this setting. Reverify its effect on result types when upgrading ClickHouse.")
	}
	return result
}
