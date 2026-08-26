package conformance

import (
	"fmt"
	"strings"
)

const nestedScopeIDRoster = `
scalar/if-null
scalar/nullability
scalar/correlated
scalar/correlated-limit
scalar/two-results
exists/uncorrelated
exists/two-results
exists/correlated
in/uncorrelated
in/correlated
in/two-results
derived/projection
derived/correlated
binding/shadow
binding/case-mismatch
binding/duplicate
binding/missing
alias/outer-where-supported
alias/outer-where
alias/derived-where
alias/inner-hidden
alias/forward
alias/self-physical
alias/pure-cycle
alias/masked-cycle
alias/physical-collision
alias/join-on
alias/having
alias/limit-by
alias/window-direct
alias/window-named
alias/child-rebind
using/multi-input-refused
using/left-unmatched
using/right-unmatched
using/full-unmatched
join-on/unqualified-left-bias
alias/duplicate-identical-generator-refusal
alias/duplicate-different-refused
cte/duplicate-relation-refused
cte/duplicate-scalar-refused
cte/cross-namespace
cte/relation-case-variants
cte/scalar-exact
cte/scalar-case-refused
cte/scalar-case-variants
cte/direct-scalar
cte/forward-relation-gap
cte/forward-scalar-gap
cte/relation-cycle-refused
cte/scalar-cycle-refused
cte/direct-scalar-cycle-refused
scalar/empty-limit
scalar/plain-limit-one
scalar/limit-with-ties-refused
scalar/real-from-limit-zero
scalar/multiple-rows-refused
scalar/container-unsuppressed
scalar/container-where-refused
scalar/container-having-refused
scalar/container-limit-zero-refused
scalar/container-offset-refused
scalar/container-aggregate-unsuppressed
scalar/tuple-unsuppressed
scalar/tuple-where-refused
scalar/low-cardinality-unsuppressed
scalar/low-cardinality-where-refused
`

// NestedScopeCell records one nested query boundary.
type NestedScopeCell struct {
	ID            string `json:"id"`
	Form          string `json:"form"`
	Role          string `json:"role"`
	Binding       string `json:"binding"`
	Query         string `json:"query"`
	Chgen         string `json:"chgen"`
	ChgenType     string `json:"chgen_type,omitempty"`
	ChgenError    string `json:"chgen_error,omitempty"`
	Analysis      string `json:"analysis"`
	AnalysisType  string `json:"analysis_type,omitempty"`
	AnalysisCode  int    `json:"analysis_code,omitempty"`
	Execution     string `json:"execution"`
	ExecutionCode int    `json:"execution_code,omitempty"`
	ResultName    string `json:"result_name,omitempty"`
	Owner         string `json:"owner,omitempty"`
	ValueProbe    string `json:"value_probe,omitempty"`
}

// NestedScopeArtifact records the measured nested query matrix.
type NestedScopeArtifact struct {
	Version           int               `json:"version"`
	ClickHouseVersion string            `json:"clickhouse_version"`
	Fixture           string            `json:"fixture"`
	Cells             []NestedScopeCell `json:"cells"`
}

// NestedScopeFixtureDDL creates real columns for nested scope checks.
const NestedScopeFixtureDDL = `
CREATE TABLE ns_outer (
    id UInt64,
    value Int32,
    label String
) ENGINE = Memory;
CREATE TABLE ns_inner (
    id UInt64,
    value Int32,
    label String
) ENGINE = Memory;
`

// NestedScopeFixtureSeed makes result values differ by query form.
const NestedScopeFixtureSeed = `
INSERT INTO ns_outer VALUES (1, 10, 'outer'), (2, 11, 'outer-2');
INSERT INTO ns_inner VALUES (1, 20, 'inner'), (3, 30, 'inner-3');
`

// CurrentNestedScopeArtifact returns the pinned nested query matrix.
func CurrentNestedScopeArtifact(clickHouseVersion string) NestedScopeArtifact {
	const owner = "nested-scope-known-gap"
	return NestedScopeArtifact{
		Version: 1, ClickHouseVersion: clickHouseVersion, Fixture: "nested-scope-v1",
		Cells: []NestedScopeCell{
			{ID: "scalar/if-null", Form: "scalar", Role: "value", Binding: "uncorrelated", Query: "SELECT id, ifNull((SELECT max(value) FROM ns_inner), toInt32(0)) AS result FROM ns_outer ORDER BY id", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "result", ValueProbe: "probe_nested_scalar"},
			{ID: "scalar/nullability", Form: "scalar", Role: "value", Binding: "uncorrelated", Query: "SELECT (SELECT toInt32(7)) AS result FROM ns_outer ORDER BY id", Chgen: "accept", ChgenType: "Nullable(Int32)", Analysis: "accept", AnalysisType: "Nullable(Int32)", Execution: "accept", ResultName: "result", ValueProbe: "probe_nested_scalar_nullable_value"},
			{ID: "scalar/correlated", Form: "scalar", Role: "value", Binding: "correlated", Query: "SELECT (SELECT max(inner_scope.value) FROM ns_inner AS inner_scope WHERE inner_scope.id = outer_scope.id) AS result FROM ns_outer AS outer_scope ORDER BY id", Chgen: "accept", ChgenType: "Nullable(Int32)", Analysis: "accept", AnalysisType: "Nullable(Int32)", Execution: "accept", ResultName: "result"},
			{ID: "scalar/correlated-limit", Form: "scalar", Role: "value", Binding: "correlated", Query: "SELECT (SELECT inner_scope.value FROM ns_inner AS inner_scope WHERE inner_scope.id = outer_scope.id LIMIT 1) AS result FROM ns_outer AS outer_scope ORDER BY id", Chgen: "refuse", ChgenError: "correlated scalar subquery with LIMIT is not supported", Analysis: "refuse", AnalysisCode: 48, Execution: "refuse", ExecutionCode: 48},
			{ID: "scalar/two-results", Form: "scalar", Role: "value", Binding: "width_two", Query: "SELECT (SELECT id, value FROM ns_inner LIMIT 1) AS result FROM ns_outer", Chgen: "refuse", ChgenError: "scalar subquery must select exactly one expression", Analysis: "accept", AnalysisType: "Tuple(id UInt64, value Int32)", Execution: "accept", ResultName: "result", Owner: owner},
			{ID: "exists/uncorrelated", Form: "exists", Role: "existence", Binding: "uncorrelated", Query: "SELECT id, EXISTS(SELECT 1 FROM ns_inner WHERE ns_inner.id = 1) AS result FROM ns_outer ORDER BY id", Chgen: "accept", ChgenType: "UInt8", Analysis: "accept", AnalysisType: "UInt8", Execution: "accept", ResultName: "result", ValueProbe: "probe_nested_exists"},
			{ID: "exists/two-results", Form: "exists", Role: "existence", Binding: "width_two", Query: "SELECT EXISTS(SELECT 1, 2 FROM ns_inner) AS result FROM ns_outer", Chgen: "accept", ChgenType: "UInt8", Analysis: "accept", AnalysisType: "UInt8", Execution: "accept", ResultName: "result"},
			{ID: "exists/correlated", Form: "exists", Role: "existence", Binding: "correlated", Query: "SELECT EXISTS(SELECT 1 FROM ns_inner AS inner_scope WHERE inner_scope.id = outer_scope.id) AS result FROM ns_outer AS outer_scope ORDER BY id", Chgen: "accept", ChgenType: "UInt8", Analysis: "accept", AnalysisType: "UInt8", Execution: "accept", ResultName: "result"},
			{ID: "in/uncorrelated", Form: "in", Role: "membership", Binding: "uncorrelated", Query: "SELECT id, id IN (SELECT id FROM ns_inner) AS result FROM ns_outer ORDER BY id", Chgen: "accept", ChgenType: "UInt8", Analysis: "accept", AnalysisType: "UInt8", Execution: "accept", ResultName: "result", ValueProbe: "probe_nested_in"},
			{ID: "in/correlated", Form: "in", Role: "membership", Binding: "correlated", Query: "SELECT outer_scope.id IN (SELECT inner_scope.id FROM ns_inner AS inner_scope WHERE inner_scope.value > outer_scope.value) AS result FROM ns_outer AS outer_scope ORDER BY id", Chgen: "refuse", ChgenError: "correlated IN subquery is not supported", Analysis: "refuse", AnalysisCode: 48, Execution: "refuse", ExecutionCode: 48},
			{ID: "in/two-results", Form: "in", Role: "membership", Binding: "width_two", Query: "SELECT id IN (SELECT id, value FROM ns_inner) AS result FROM ns_outer", Chgen: "refuse", ChgenError: "IN set projection has 2 expressions, want 1", Analysis: "accept", AnalysisType: "UInt8", Execution: "refuse", ExecutionCode: 20, ResultName: "result", Owner: owner},
			{ID: "derived/projection", Form: "derived", Role: "relation", Binding: "uncorrelated", Query: "SELECT nested.inner_value AS result FROM (SELECT value AS inner_value FROM ns_inner) AS nested ORDER BY result", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "result", ValueProbe: "probe_nested_derived"},
			{ID: "derived/correlated", Form: "derived", Role: "relation", Binding: "correlated", Query: "SELECT nested.inner_value AS result FROM ns_outer AS outer_scope CROSS JOIN (SELECT outer_scope.value AS inner_value) AS nested", Chgen: "refuse", ChgenError: "derived table nested: correlated derived table is not supported", Analysis: "refuse", AnalysisCode: 48, Execution: "refuse", ExecutionCode: 48},
			{ID: "binding/shadow", Form: "scalar", Role: "value", Binding: "shadow", Query: "SELECT ifNull((SELECT max(same_name.value) FROM ns_inner AS same_name), toInt32(0)) AS result FROM ns_outer AS same_name ORDER BY id", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "result", ValueProbe: "probe_nested_shadow"},
			{ID: "binding/case-mismatch", Form: "scalar", Role: "value", Binding: "case_mismatch", Query: "SELECT (SELECT max(Inner_scope.value) FROM ns_inner AS inner_scope) AS result FROM ns_outer ORDER BY id", Chgen: "refuse", ChgenError: "function max argument: column \"value\" is not present in FROM tables", Analysis: "refuse", AnalysisCode: 47, Execution: "refuse", ExecutionCode: 47},
			{ID: "binding/duplicate", Form: "derived", Role: "relation", Binding: "duplicate", Query: "SELECT q.value AS result FROM ns_outer AS q CROSS JOIN ns_inner AS q", Chgen: "refuse", ChgenError: "table alias \"q\" is defined more than once", Analysis: "refuse", AnalysisCode: 179, Execution: "refuse", ExecutionCode: 179},
			{ID: "binding/missing", Form: "derived", Role: "relation", Binding: "missing", Query: "SELECT nested.missing AS result FROM (SELECT value FROM ns_inner) AS nested", Chgen: "refuse", ChgenError: "column \"missing\" is not present in FROM tables", Analysis: "refuse", AnalysisCode: 47, Execution: "refuse", ExecutionCode: 47},
			{ID: "alias/outer-where-supported", Form: "scalar", Role: "value", Binding: "alias_visible", Query: "SELECT value AS result FROM ns_outer WHERE result > 10 ORDER BY id", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "result"},
			{ID: "alias/outer-where", Form: "scalar", Role: "value", Binding: "alias_visible", Query: "SELECT (SELECT max(value) FROM ns_inner) AS result FROM ns_outer WHERE result > 0 ORDER BY id", Chgen: "accept", ChgenType: "Nullable(Int32)", Analysis: "accept", AnalysisType: "Nullable(Int32)", Execution: "accept", ResultName: "result"},
			{ID: "alias/derived-where", Form: "derived", Role: "relation", Binding: "alias_visible", Query: "SELECT nested.inner_alias AS result FROM (SELECT value AS inner_alias FROM ns_inner WHERE inner_alias > 20) AS nested", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "result"},
			{ID: "alias/inner-hidden", Form: "scalar", Role: "value", Binding: "alias_hidden", Query: "SELECT (SELECT max(value) AS inner_alias FROM ns_inner) AS result FROM ns_outer ORDER BY inner_alias", Chgen: "refuse", ChgenError: "ORDER BY expression: column \"inner_alias\" is not present in FROM tables", Analysis: "refuse", AnalysisCode: 47, Execution: "refuse", ExecutionCode: 47},
			{ID: "alias/forward", Form: "scalar", Role: "alias_dependency", Binding: "alias_visible", Query: "SELECT x + 1 AS result, value AS x FROM ns_outer", Chgen: "accept", ChgenType: "Int64", Analysis: "accept", AnalysisType: "Int64", Execution: "accept", ResultName: "result"},
			{ID: "alias/self-physical", Form: "scalar", Role: "alias_dependency", Binding: "alias_visible", Query: "SELECT value + 1 AS value FROM ns_outer", Chgen: "accept", ChgenType: "Int64", Analysis: "accept", AnalysisType: "Int64", Execution: "accept", ResultName: "value"},
			{ID: "alias/pure-cycle", Form: "scalar", Role: "alias_dependency", Binding: "alias_hidden", Query: "SELECT y AS x, x AS y FROM ns_outer", Chgen: "refuse", ChgenError: "not present in FROM tables", Analysis: "refuse", AnalysisCode: 47, Execution: "refuse", ExecutionCode: 47},
			{ID: "alias/masked-cycle", Form: "scalar", Role: "alias_dependency", Binding: "alias_visible", Query: "SELECT id AS value, value AS id FROM ns_outer", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "value"},
			{ID: "alias/physical-collision", Form: "scalar", Role: "alias_dependency", Binding: "alias_visible", Query: "SELECT value AS id FROM ns_outer WHERE id = 10", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "id"},
			{ID: "alias/join-on", Form: "derived", Role: "join_on", Binding: "alias_visible", Query: "SELECT o.value AS x FROM ns_outer AS o JOIN ns_inner AS i ON i.value = x", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "x"},
			{ID: "alias/having", Form: "scalar", Role: "having", Binding: "alias_visible", Query: "SELECT count() AS result FROM ns_outer HAVING result > 0", Chgen: "accept", ChgenType: "UInt64", Analysis: "accept", AnalysisType: "UInt64", Execution: "accept", ResultName: "result"},
			{ID: "alias/limit-by", Form: "scalar", Role: "limit_by", Binding: "alias_visible", Query: "SELECT value AS result FROM ns_outer LIMIT 1 BY result", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "result"},
			{ID: "alias/window-direct", Form: "scalar", Role: "window", Binding: "alias_visible", Query: "SELECT value AS result, row_number() OVER (ORDER BY result) AS rn FROM ns_outer", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "result"},
			{ID: "alias/window-named", Form: "scalar", Role: "window", Binding: "alias_visible", Query: "SELECT value AS result, row_number() OVER w AS rn FROM ns_outer WINDOW w AS (ORDER BY result)", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "result"},
			{ID: "alias/child-rebind", Form: "scalar", Role: "alias_dependency", Binding: "alias_visible", Query: "SELECT value AS x, (SELECT max(r.value) FROM ns_inner AS r WHERE r.value = x) AS result FROM ns_outer", Chgen: "accept", ChgenType: "Nullable(Int32)", Analysis: "accept", AnalysisType: "Nullable(Int32)", Execution: "accept", ResultName: "result"},
			{ID: "using/multi-input-refused", Form: "derived", Role: "join_using", Binding: "duplicate", Query: "SELECT s.id AS result FROM ns_outer AS o JOIN ns_inner AS i ON o.id = i.id JOIN (SELECT toUInt16(1) AS id) AS s USING (id)", Chgen: "refuse", ChgenError: "multi-input JOIN USING name resolution is not supported", Analysis: "refuse", AnalysisCode: 207, Execution: "refuse", ExecutionCode: 207},
			{ID: "using/left-unmatched", Form: "derived", Role: "join_using", Binding: "unmatched", Query: "SELECT id AS result FROM (SELECT toUInt64(2) AS id) AS l LEFT JOIN (SELECT toUInt32(3) AS id) AS r USING (id)", Chgen: "accept", ChgenType: "UInt64", Analysis: "accept", AnalysisType: "UInt64", Execution: "accept", ResultName: "result", ValueProbe: "probe_using_left_unmatched"},
			{ID: "using/right-unmatched", Form: "derived", Role: "join_using", Binding: "unmatched", Query: "SELECT id AS result FROM (SELECT toUInt64(2) AS id) AS l RIGHT JOIN (SELECT toUInt32(3) AS id) AS r USING (id)", Chgen: "accept", ChgenType: "UInt64", Analysis: "accept", AnalysisType: "UInt64", Execution: "accept", ResultName: "result", ValueProbe: "probe_using_right_unmatched"},
			{ID: "using/full-unmatched", Form: "derived", Role: "join_using", Binding: "unmatched", Query: "SELECT id AS result FROM (SELECT toUInt64(2) AS id) AS l FULL JOIN (SELECT toUInt32(3) AS id) AS r USING (id) ORDER BY result", Chgen: "accept", ChgenType: "UInt64", Analysis: "accept", AnalysisType: "UInt64", Execution: "accept", ResultName: "result", ValueProbe: "probe_using_full_unmatched"},
			{ID: "join-on/unqualified-left-bias", Form: "derived", Role: "join_on", Binding: "duplicate", Query: "SELECT value AS result FROM (SELECT toInt32(11) AS value, toUInt8(1) AS id) AS l JOIN (SELECT toInt16(22) AS value, toUInt8(1) AS id) AS r ON l.id = r.id", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "result", ValueProbe: "probe_join_on_left_bias"},
			{ID: "alias/duplicate-identical-generator-refusal", Form: "scalar", Role: "alias_dependency", Binding: "duplicate", Query: "SELECT value AS result, value AS result FROM ns_outer", Chgen: "refuse", ChgenError: "both generate Go field Result", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "result", Owner: owner},
			{ID: "alias/duplicate-different-refused", Form: "scalar", Role: "alias_dependency", Binding: "duplicate", Query: "SELECT value AS result, id AS result FROM ns_outer", Chgen: "refuse", ChgenError: "projection alias \"result\" has different expressions", Analysis: "refuse", AnalysisCode: 179, Execution: "refuse", ExecutionCode: 179},
			{ID: "cte/duplicate-relation-refused", Form: "derived", Role: "cte", Binding: "duplicate", Query: "WITH q AS (SELECT value FROM ns_outer), q AS (SELECT value FROM ns_inner) SELECT value AS result FROM q", Chgen: "refuse", ChgenError: "relation CTE \"q\" is defined more than once", Analysis: "refuse", AnalysisCode: 179, Execution: "refuse", ExecutionCode: 179},
			{ID: "cte/duplicate-scalar-refused", Form: "scalar", Role: "cte", Binding: "duplicate", Query: "WITH (SELECT 1) AS q, (SELECT 2) AS q SELECT q AS result", Chgen: "refuse", ChgenError: "scalar CTE \"q\" is defined more than once", Analysis: "refuse", AnalysisCode: 179, Execution: "refuse", ExecutionCode: 179},
			{ID: "cte/cross-namespace", Form: "derived", Role: "cte", Binding: "shadow", Query: "WITH q AS (SELECT value FROM ns_outer), (SELECT 1) AS q SELECT value AS result FROM q WHERE q = 1", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "result"},
			{ID: "cte/relation-case-variants", Form: "derived", Role: "cte", Binding: "case_mismatch", Query: "WITH q AS (SELECT id, value FROM ns_outer), Q AS (SELECT id, value FROM ns_inner) SELECT q.value AS result FROM q JOIN Q ON q.id = Q.id", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "result"},
			{ID: "cte/scalar-exact", Form: "scalar", Role: "cte", Binding: "case_mismatch", Query: "WITH (SELECT 1) AS q SELECT q AS result", Chgen: "accept", ChgenType: "Nullable(UInt8)", Analysis: "accept", AnalysisType: "Nullable(UInt8)", Execution: "accept", ResultName: "result"},
			{ID: "cte/scalar-case-refused", Form: "scalar", Role: "cte", Binding: "case_mismatch", Query: "WITH (SELECT 1) AS q SELECT Q AS result", Chgen: "refuse", ChgenError: "column \"Q\" is not present in FROM tables", Analysis: "refuse", AnalysisCode: 47, Execution: "refuse", ExecutionCode: 47},
			{ID: "cte/scalar-case-variants", Form: "scalar", Role: "cte", Binding: "case_mismatch", Query: "WITH (SELECT 1) AS q, (SELECT 2) AS Q SELECT q AS result", Chgen: "accept", ChgenType: "Nullable(UInt8)", Analysis: "accept", AnalysisType: "Nullable(UInt8)", Execution: "accept", ResultName: "result"},
			{ID: "cte/direct-scalar", Form: "scalar", Role: "cte", Binding: "case_mismatch", Query: "WITH toInt32(7) AS q SELECT q AS result", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "result", ValueProbe: "probe_nested_direct_scalar_cte"},
			{ID: "cte/forward-relation-gap", Form: "derived", Role: "cte", Binding: "missing", Query: "WITH q AS (SELECT value FROM later), later AS (SELECT value FROM ns_outer) SELECT value AS result FROM q", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "result"},
			{ID: "cte/forward-scalar-gap", Form: "scalar", Role: "cte", Binding: "missing", Query: "WITH later AS q, toInt32(7) AS later SELECT q AS result", Chgen: "accept", ChgenType: "Int32", Analysis: "accept", AnalysisType: "Int32", Execution: "accept", ResultName: "result"},
			{ID: "cte/relation-cycle-refused", Form: "derived", Role: "cte", Binding: "missing", Query: "WITH q AS (SELECT value FROM later), later AS (SELECT value FROM q) SELECT value AS result FROM q", Chgen: "refuse", ChgenError: "table \"later\" is not present in the schema or the query scope", Analysis: "refuse", AnalysisCode: 60, Execution: "refuse", ExecutionCode: 60},
			{ID: "cte/scalar-cycle-refused", Form: "scalar", Role: "cte", Binding: "missing", Query: "WITH (SELECT q) AS q SELECT q AS result", Chgen: "refuse", ChgenError: "scalar CTE \"q\" is not resolved", Analysis: "refuse", AnalysisCode: 47, Execution: "refuse", ExecutionCode: 47},
			{ID: "cte/direct-scalar-cycle-refused", Form: "scalar", Role: "cte", Binding: "missing", Query: "WITH r AS q, q AS r SELECT q AS result", Chgen: "refuse", ChgenError: "scalar CTE \"r\" is not resolved", Analysis: "refuse", AnalysisCode: 47, Execution: "refuse", ExecutionCode: 47},
			{ID: "scalar/empty-limit", Form: "scalar", Role: "cardinality", Binding: "uncorrelated", Query: "SELECT (SELECT value FROM ns_inner WHERE id = 999 LIMIT 1) AS result", Chgen: "accept", ChgenType: "Nullable(Int32)", Analysis: "accept", AnalysisType: "Nullable(Int32)", Execution: "accept", ResultName: "result", ValueProbe: "probe_nested_scalar_nullable_null"},
			{ID: "scalar/plain-limit-one", Form: "scalar", Role: "cardinality", Binding: "uncorrelated", Query: "SELECT (SELECT value FROM ns_inner ORDER BY id % 1 LIMIT 1) AS result", Chgen: "accept", ChgenType: "Nullable(Int32)", Analysis: "accept", AnalysisType: "Nullable(Int32)", Execution: "accept", ResultName: "result"},
			{ID: "scalar/limit-with-ties-refused", Form: "scalar", Role: "cardinality", Binding: "uncorrelated", Query: "SELECT (SELECT value FROM ns_inner ORDER BY id % 1 LIMIT 1 WITH TIES) AS result", Chgen: "refuse", ChgenError: "nested LIMIT WITH TIES is not supported", Analysis: "refuse", AnalysisCode: 125, Execution: "refuse", ExecutionCode: 125},
			{ID: "scalar/real-from-limit-zero", Form: "scalar", Role: "cardinality", Binding: "uncorrelated", Query: "SELECT (SELECT value FROM ns_inner LIMIT 0) AS result", Chgen: "accept", ChgenType: "Nullable(Int32)", Analysis: "accept", AnalysisType: "Nullable(Int32)", Execution: "accept", ResultName: "result", ValueProbe: "probe_nested_scalar_limit_zero"},
			{ID: "scalar/multiple-rows-refused", Form: "scalar", Role: "cardinality", Binding: "uncorrelated", Query: "SELECT (SELECT value FROM ns_inner) AS result", Chgen: "refuse", ChgenError: "scalar subquery row cardinality is not statically bounded", Analysis: "refuse", AnalysisCode: 125, Execution: "refuse", ExecutionCode: 125},
			{ID: "scalar/container-unsuppressed", Form: "scalar", Role: "cardinality", Binding: "uncorrelated", Query: "SELECT (SELECT [toInt32(1)]) AS result", Chgen: "accept", ChgenType: "Array(Int32)", Analysis: "accept", AnalysisType: "Array(Int32)", Execution: "accept", ResultName: "result"},
			{ID: "scalar/container-where-refused", Form: "scalar", Role: "cardinality", Binding: "uncorrelated", Query: "SELECT (SELECT [toInt32(1)] WHERE 0) AS result", Chgen: "refuse", ChgenError: "cannot put Array(Int32) inside Nullable", Analysis: "refuse", AnalysisCode: 125, Execution: "refuse", ExecutionCode: 125},
			{ID: "scalar/container-having-refused", Form: "scalar", Role: "cardinality", Binding: "uncorrelated", Query: "SELECT (SELECT groupArray(value) FROM ns_inner HAVING 0) AS result", Chgen: "refuse", ChgenError: "cannot put Array(Int32) inside Nullable", Analysis: "refuse", AnalysisCode: 125, Execution: "refuse", ExecutionCode: 125},
			{ID: "scalar/container-limit-zero-refused", Form: "scalar", Role: "cardinality", Binding: "uncorrelated", Query: "SELECT (SELECT [toInt32(1)] LIMIT 0) AS result", Chgen: "refuse", ChgenError: "cannot put Array(Int32) inside Nullable", Analysis: "refuse", AnalysisCode: 125, Execution: "refuse", ExecutionCode: 125},
			{ID: "scalar/container-offset-refused", Form: "scalar", Role: "cardinality", Binding: "uncorrelated", Query: "SELECT (SELECT [toInt32(1)] LIMIT 1 OFFSET 1) AS result", Chgen: "refuse", ChgenError: "cannot put Array(Int32) inside Nullable", Analysis: "refuse", AnalysisCode: 125, Execution: "refuse", ExecutionCode: 125},
			{ID: "scalar/container-aggregate-unsuppressed", Form: "scalar", Role: "cardinality", Binding: "uncorrelated", Query: "SELECT (SELECT groupArray(value) FROM ns_inner) AS result", Chgen: "accept", ChgenType: "Array(Int32)", Analysis: "accept", AnalysisType: "Array(Int32)", Execution: "accept", ResultName: "result"},
			{ID: "scalar/tuple-unsuppressed", Form: "scalar", Role: "cardinality", Binding: "uncorrelated", Query: "SELECT (SELECT tuple(toInt32(1))) AS result", Chgen: "refuse", ChgenError: "ClickHouse type Tuple(Int32) has no Go mapping", Analysis: "accept", AnalysisType: "Tuple(Int32)", Execution: "accept", ResultName: "result", Owner: owner},
			{ID: "scalar/tuple-where-refused", Form: "scalar", Role: "cardinality", Binding: "uncorrelated", Query: "SELECT (SELECT tuple(toInt32(1)) WHERE 0) AS result", Chgen: "refuse", ChgenError: "cannot put Tuple(Int32) inside Nullable", Analysis: "refuse", AnalysisCode: 125, Execution: "refuse", ExecutionCode: 125},
			{ID: "scalar/low-cardinality-unsuppressed", Form: "scalar", Role: "cardinality", Binding: "uncorrelated", Query: "SELECT (SELECT CAST('x' AS LowCardinality(String))) AS result", Chgen: "accept", ChgenType: "LowCardinality(String)", Analysis: "accept", AnalysisType: "LowCardinality(String)", Execution: "accept", ResultName: "result"},
			{ID: "scalar/low-cardinality-where-refused", Form: "scalar", Role: "cardinality", Binding: "uncorrelated", Query: "SELECT (SELECT CAST('x' AS LowCardinality(String)) WHERE 0) AS result", Chgen: "refuse", ChgenError: "cannot put LowCardinality(String) inside Nullable", Analysis: "refuse", AnalysisCode: 125, Execution: "refuse", ExecutionCode: 125},
		},
	}
}

// ValidateNestedScopeArtifact checks all nested query axes and owned gaps.
func ValidateNestedScopeArtifact(artifact NestedScopeArtifact) error {
	if artifact.Version != 1 || artifact.ClickHouseVersion == "" || artifact.Fixture == "" || len(artifact.Cells) == 0 {
		return fmt.Errorf("nested scope matrix identity is incomplete")
	}
	seen := make(map[string]bool, len(artifact.Cells))
	forms := make(map[string]bool)
	bindings := make(map[string]bool)
	probes := make(map[string]bool)
	for _, cell := range artifact.Cells {
		if cell.ID == "" || seen[cell.ID] || cell.Query == "" {
			return fmt.Errorf("nested scope matrix has an empty or duplicate cell %q", cell.ID)
		}
		seen[cell.ID] = true
		forms[cell.Form] = true
		bindings[cell.Binding] = true
		if !nestedLane(cell.Chgen) || !nestedLane(cell.Analysis) || !nestedLane(cell.Execution) {
			return fmt.Errorf("nested scope cell %s has an invalid lane", cell.ID)
		}
		if cell.Chgen == "accept" && cell.ChgenType == "" || cell.Chgen == "refuse" && cell.ChgenError == "" {
			return fmt.Errorf("nested scope cell %s has no chgen boundary", cell.ID)
		}
		if cell.Analysis == "accept" && cell.AnalysisType == "" || cell.Analysis == "refuse" && cell.AnalysisCode == 0 {
			return fmt.Errorf("nested scope cell %s has no analysis boundary", cell.ID)
		}
		if cell.Analysis == "accept" && cell.ResultName == "" {
			return fmt.Errorf("nested scope cell %s has no exact result name", cell.ID)
		}
		if cell.Execution == "refuse" && cell.ExecutionCode == 0 {
			return fmt.Errorf("nested scope cell %s has no execution boundary", cell.ID)
		}
		disagrees := cell.Chgen != cell.Analysis || cell.Chgen == "accept" && cell.Analysis == "accept" && cell.ChgenType != cell.AnalysisType
		if disagrees && cell.Owner == "" {
			return fmt.Errorf("nested scope cell %s has an unowned disagreement", cell.ID)
		}
		if !disagrees && cell.Owner != "" {
			return fmt.Errorf("nested scope cell %s has an owner without a chgen disagreement", cell.ID)
		}
		if cell.ValueProbe != "" {
			if cell.Execution != "accept" || probes[cell.ValueProbe] {
				return fmt.Errorf("nested scope cell %s has an invalid value probe", cell.ID)
			}
			probes[cell.ValueProbe] = true
		}
	}
	wantIDs := strings.Fields(nestedScopeIDRoster)
	if len(seen) != len(wantIDs) {
		return fmt.Errorf("nested scope matrix has %d IDs, want %d", len(seen), len(wantIDs))
	}
	for _, id := range wantIDs {
		if !seen[id] {
			return fmt.Errorf("nested scope matrix is missing cell %s", id)
		}
	}
	for _, form := range []string{"scalar", "exists", "in", "derived"} {
		if !forms[form] {
			return fmt.Errorf("nested scope matrix has no %s form", form)
		}
	}
	for _, binding := range []string{"correlated", "shadow", "case_mismatch", "duplicate", "missing", "alias_visible", "alias_hidden"} {
		if !bindings[binding] {
			return fmt.Errorf("nested scope matrix has no %s binding", binding)
		}
	}
	for _, probe := range []string{
		"probe_nested_scalar", "probe_nested_exists", "probe_nested_in", "probe_nested_derived", "probe_nested_shadow",
		"probe_using_left_unmatched", "probe_using_right_unmatched", "probe_using_full_unmatched",
		"probe_join_on_left_bias",
		"probe_nested_scalar_nullable_null", "probe_nested_scalar_nullable_value", "probe_nested_scalar_limit_zero",
		"probe_nested_direct_scalar_cte",
	} {
		if !probes[probe] {
			return fmt.Errorf("nested scope matrix has no generated value probe %s", probe)
		}
	}
	return nil
}

func nestedLane(value string) bool { return value == "accept" || value == "refuse" }
