package engine

import clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"

// CallContext owns inference results for one call in one lexical scope.
// Errors are cached too: validation and result inference must see the same
// placeholder or diagnostic. Lambda bodies use their own scopes, never this cache.
type CallContext struct {
	args  []clickhouse.Expr
	scope queryScope
	types []callArgumentType
}

type callArgumentType struct {
	ready bool
	value CHType
	err   error
}

func newCallContext(args []clickhouse.Expr, scope queryScope) *CallContext {
	return &CallContext{args: args, scope: scope}
}

func (call *CallContext) argumentType(index int) (CHType, error) {
	if call.types == nil {
		call.types = make([]callArgumentType, len(call.args))
	}
	result := &call.types[index]
	if !result.ready {
		result.value, result.err = inferExprType(call.args[index], call.scope)
		result.ready = true
	}
	return result.value, result.err
}
