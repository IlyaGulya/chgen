package conformance

import "fmt"
import "strings"

// Expectation records the server outcome that defines a statement cell.
type Expectation string

const (
	// ExpectSupported marks agreement on an accepted result vector.
	ExpectSupported Expectation = "supported"
	// ExpectRefused marks agreement on a static server refusal.
	ExpectRefused Expectation = "refused"
	// ExpectExecutionOnly marks a typed statement that only execution refuses.
	ExpectExecutionOnly Expectation = "execution_only"
	// ExpectKnownChgenRefusal marks an accepted server product with an owned support gap.
	ExpectKnownChgenRefusal Expectation = "known_chgen_refusal"
	// ExpectKnownChgenAcceptance marks an owned fail-open result.
	ExpectKnownChgenAcceptance Expectation = "known_chgen_acceptance"
	// ExpectLegal keeps the expression-oracle artifact identity.
	ExpectLegal Expectation = "legal"
	// ExpectIllegal keeps the expression-oracle artifact identity.
	ExpectIllegal Expectation = "illegal"
)

var statementContexts = []string{
	"alias_shadow", "alias_visibility", "array_join", "asof_join", "correlated_derived", "cte", "cte_where", "derived_where",
	"distinct_on", "duplicate_alias", "execution_only", "from_modifier", "group_by", "having", "join_on", "join_using", "limit",
	"limit_by", "multi_join", "offset", "order_by", "prewhere", "qualifier_case", "result_vector", "scalar_where",
	"scalar_wrapper", "settings", "ties", "union", "where", "window",
}

var statementKnownGaps = map[string]string{}

var statementKnownAcceptances = map[string]string{}

type statementRefusalBoundary struct {
	chgenError     string
	chgenResult    string
	analysisCode   int
	analysisError  string
	analysisResult string
	executionCode  int
	executionError string
}

var statementRefusalBoundaries = map[string]statementRefusalBoundary{
	"alias-shadow-refused": {chgenError: `column "grp" is not present in FROM tables`, analysisCode: 47,
		analysisError: "Identifier 'scoped.grp' cannot be resolved", executionCode: 47,
		executionError: "Identifier 'scoped.grp' cannot be resolved"},
	"array_join-refused":                 {chgenError: `column "missing" is not present in FROM tables`, analysisCode: 47, executionCode: 47},
	"asof-join-refused":                  {chgenError: "ASOF JOIN ON requires exactly one cross-input inequality condition", executionCode: 403},
	"asof-join-constant-refused":         {chgenError: "ASOF JOIN ON requires exactly one cross-input inequality condition", executionCode: 403},
	"asof-join-same-side-refused":        {chgenError: "ASOF JOIN ON requires exactly one cross-input inequality condition", executionCode: 403},
	"asof-join-two-inequalities-refused": {chgenError: "ASOF JOIN ON requires exactly one cross-input inequality condition", executionCode: 403},
	"correlated-derived-refused": {chgenError: `derived table nested: correlated derived table is not supported`, analysisCode: 48,
		analysisError: "Lateral joins are not supported", executionCode: 48, executionError: "Lateral joins are not supported"},
	"duplicate-alias-refused": {chgenError: `table alias "q" is defined more than once`, analysisCode: 179,
		analysisError: "Multiple table expressions with same alias q", executionCode: 179,
		executionError: "Multiple table expressions with same alias q"},
	"from-final-refused":                      {chgenError: "engine MergeTree", analysisCode: 181, executionCode: 181},
	"from-sample-refused":                     {chgenError: "FROM SAMPLE capability validation is not supported", analysisCode: 141, executionCode: 141},
	"having-aggregate-column-refused":         {chgenError: "HAVING aggregate coverage: GROUP BY does not cover selected column stmt_left.value", analysisCode: 215, executionCode: 215},
	"having-window-refused":                   {chgenError: "HAVING cannot contain a window function", analysisCode: 184, executionCode: 184},
	"limit-by-aggregate-column-refused":       {chgenError: "LIMIT BY aggregate coverage: GROUP BY does not cover selected column stmt_left.value", analysisCode: 10, executionCode: 10},
	"order-by-fill-step-zero-refused":         {chgenError: "ORDER BY WITH FILL STEP must be a positive numeric literal", analysisCode: 475, executionCode: 475},
	"order-by-fill-string-refused":            {chgenError: "ORDER BY WITH FILL FROM expression has type String incompatible with order key UInt64", analysisCode: 475, executionCode: 475},
	"order-by-fill-column-refused":            {chgenError: "ORDER BY WITH FILL FROM must be a measured constant literal", analysisCode: 475, executionCode: 475},
	"order-by-aggregate-column-refused":       {chgenError: "ORDER BY aggregate coverage: GROUP BY does not cover selected column stmt_left.value", analysisCode: 215, executionCode: 215},
	"order-by-group-aggregate-column-refused": {chgenError: "ORDER BY aggregate coverage: GROUP BY does not cover selected column stmt_left.value", analysisCode: 215, executionCode: 215},
	"qualifier-case-refused": {chgenError: `column "id" is not present in FROM tables`, analysisCode: 47,
		analysisError: "Unknown expression identifier `x.id`", executionCode: 47,
		executionError: "Unknown expression identifier `x.id`"},
	"scalar-wrapper-refused":                   {chgenError: `column "missing" is not present in FROM tables`, analysisCode: 47, executionCode: 47},
	"settings-max-bytes-refused":               {chgenError: "SETTINGS max_bytes_before_external_group_by must be an unsigned integer literal", analysisCode: 27, executionCode: 27},
	"settings-external-sort-refused":           {chgenError: "SETTINGS max_bytes_before_external_sort must be an unsigned integer literal", analysisCode: 27, executionCode: 27},
	"settings-overcommit-refused":              {chgenError: "SETTINGS memory_overcommit_ratio_denominator must be an unsigned integer literal", analysisCode: 27, executionCode: 27},
	"settings-overcommit-user-refused":         {chgenError: "SETTINGS memory_overcommit_ratio_denominator_for_user must be an unsigned integer literal", analysisCode: 27, executionCode: 27},
	"settings-preferred-block-refused":         {chgenError: "SETTINGS preferred_block_size_bytes must be an unsigned integer literal", analysisCode: 27, executionCode: 27},
	"settings-cache-refused":                   {chgenError: "SETTINGS use_uncompressed_cache must be 0, 1, true, or false", analysisCode: 70, executionCode: 70},
	"settings-final-merge-refused":             {chgenError: "SETTINGS do_not_merge_across_partitions_select_final must be 0, 1, true, or false", analysisCode: 70, executionCode: 70},
	"settings-execution-time-refused":          {chgenError: "SETTINGS max_execution_time must be an unsigned integer literal", analysisCode: 27, executionCode: 27},
	"settings-execution-time-max-plus-refused": {chgenError: "SETTINGS max_execution_time must be at most 9223372036854", analysisCode: 36, executionCode: 36},
	"settings-execution-time-uint-max-refused": {chgenError: "SETTINGS max_execution_time must be at most 9223372036854", analysisCode: 70, executionCode: 70},
	"settings-memory-refused":                  {chgenError: "SETTINGS max_memory_usage must be an unsigned integer literal", analysisCode: 27, executionCode: 27},
	"settings-block-refused":                   {chgenError: "SETTINGS max_block_size must be a positive unsigned integer literal", analysisCode: 36, executionCode: 36},
	"settings-log-comment-refused":             {chgenError: "SETTINGS log_comment must be a string literal", analysisCode: 170, executionCode: 170},
	"settings-case-refused":                    {chgenError: `SETTINGS name "MAX_MEMORY_USAGE" is not in the measured resolver roster`, analysisResult: "Int32", executionCode: 115},
	"settings-unknown-refused":                 {chgenError: `SETTINGS name "unknown_setting" is not in the measured resolver roster`, analysisResult: "Int32", executionCode: 115},
	"ties-limit-refused":                       {chgenError: "LIMIT WITH TIES requires ORDER BY", analysisCode: 476, executionCode: 476},
	"ties-limit-comment-refused":               {chgenError: "LIMIT WITH TIES requires ORDER BY in the same SELECT query", analysisCode: 476, executionCode: 476},
	"ties-limit-inner-order-refused":           {chgenError: "LIMIT WITH TIES requires ORDER BY in the same SELECT query", analysisCode: 476, executionCode: 476},
	"ties-limit-string-refused":                {chgenError: "LIMIT WITH TIES requires ORDER BY in the same SELECT query", analysisCode: 476, executionCode: 476},
	"ties-top-refused":                         {chgenError: "TOP WITH TIES requires ORDER BY", analysisCode: 476, executionCode: 476},
	"ties-top-domain-refused":                  {chgenError: "TOP expression has type Float64, want an unsigned integer", analysisCode: 440, executionCode: 440},
	"union-refused":                            {chgenError: "set result column 1 has no common type", analysisCode: 386, executionCode: 386},
	"window-aggregate-column-refused":          {chgenError: "GROUP BY does not cover selected column stmt_left.value", analysisCode: 215, executionCode: 215},
	"window-direct-inner-aggregate-refused":    {chgenError: "aggregate projection: GROUP BY does not cover selected column value", analysisCode: 215, executionCode: 215},
	"window-named-inner-aggregate-refused":     {chgenError: "aggregate projection: GROUP BY does not cover selected column value", analysisCode: 215, executionCode: 215},
}

// StatementContexts returns the stable set of measured query contexts.
func StatementContexts() []string {
	return append([]string(nil), statementContexts...)
}

// StatementFixtureDDL creates the two relations used by StatementMatrix.
const StatementFixtureDDL = `
CREATE TABLE stmt_left (
    id UInt64,
    grp UInt8,
    value Int32,
    label String,
    items Array(Int32)
) ENGINE = MergeTree ORDER BY tuple();
CREATE TABLE stmt_right (
    id UInt64,
    value Int32,
    payload Array(Int32)
) ENGINE = MergeTree ORDER BY tuple();
CREATE TABLE stmt_third (
    id UInt32,
    value Int32
) ENGINE = MergeTree ORDER BY tuple();
CREATE TABLE stmt_bad (
    id Array(Int32),
    value Int32
) ENGINE = MergeTree ORDER BY tuple();
CREATE TABLE stmt_replacing (
    id UInt64,
    value Int32,
    version UInt64
) ENGINE = ReplacingMergeTree(version) ORDER BY id;
`

// StatementFixtureSeed gives each relation one real row.
const StatementFixtureSeed = `
INSERT INTO stmt_left VALUES (1, 1, 10, 'left', [10, 20]);
INSERT INTO stmt_right VALUES (1, 20, [20, 30]);
INSERT INTO stmt_third VALUES (1, 30);
INSERT INTO stmt_bad VALUES ([1], 40);
INSERT INTO stmt_replacing VALUES (1, 10, 1), (1, 20, 2);
`

// StatementMatrix returns deterministic legal and illegal query shapes.
func StatementMatrix() []Input {
	const fixture = "statement-v1"
	cell := func(id, shape, scope, template, expression string, expectation Expectation) Input {
		input := Input{ID: id, Expression: expression, Query: renderStatement(template, expression),
			StatementTemplate: template, Scope: scope, StatementShape: shape, Fixture: fixture, Expectation: expectation}
		input.KnownGap = statementKnownGaps[id]
		if owner := statementKnownAcceptances[id]; owner != "" {
			input.KnownGap = owner
		}
		if boundary, ok := statementRefusalBoundaries[id]; ok {
			input.ExpectedChgenError = boundary.chgenError
			input.ExpectedChgenResult = boundary.chgenResult
			input.ExpectedAnalysisCode = boundary.analysisCode
			input.ExpectedAnalysisError = boundary.analysisError
			input.ExpectedAnalysisResult = boundary.analysisResult
			input.ExpectedExecutionCode = boundary.executionCode
			input.ExpectedExecutionError = boundary.executionError
		}
		return input
	}
	return []Input{
		cell("alias-shadow-refused", "alias_shadow", "stmt_left,stmt_right,scoped", "SELECT (SELECT max(scoped.grp) FROM stmt_right AS scoped) AS value FROM stmt_left AS {{expr}}", "scoped", ExpectRefused),
		cell("alias-shadow-outer-supported", "alias_shadow", "stmt_left,stmt_right,scoped,inner_scope", "SELECT ifNull((SELECT max(scoped.grp) FROM stmt_right AS inner_scope), 0) AS value FROM stmt_left AS {{expr}}", "scoped", ExpectSupported),
		cell("alias-group-supported", "alias_visibility", "stmt_left,Foo", "SELECT grp AS {{expr}} FROM stmt_left GROUP BY Foo", "Foo", ExpectSupported),
		cell("alias-order-case-refused", "alias_visibility", "stmt_left,Foo", "SELECT value AS Foo FROM stmt_left ORDER BY {{expr}}", "foo", ExpectRefused),
		cell("alias-order-supported", "alias_visibility", "stmt_left,Foo", "SELECT value AS Foo FROM stmt_left ORDER BY {{expr}}", "Foo", ExpectSupported),
		cell("alias-prewhere-supported", "alias_visibility", "stmt_left,Foo", "SELECT value AS {{expr}} FROM stmt_left PREWHERE Foo > 0", "Foo", ExpectSupported),
		cell("alias-where-supported", "alias_visibility", "stmt_left,Foo", "SELECT value AS {{expr}} FROM stmt_left WHERE Foo > 0", "Foo", ExpectSupported),
		cell("array_join-refused", "array_join", "stmt_left", "SELECT item AS value FROM stmt_left ARRAY JOIN {{expr}} AS item", "missing", ExpectRefused),
		cell("array_join-supported", "array_join", "stmt_left,item", "SELECT item AS value FROM stmt_left ARRAY JOIN {{expr}} AS item", "items", ExpectSupported),
		cell("asof-join-refused", "asof_join", "stmt_left,stmt_right,l,r", "SELECT l.value FROM stmt_left AS l ASOF JOIN stmt_right AS r ON {{expr}}", "l.id = r.id", ExpectRefused),
		cell("asof-join-constant-refused", "asof_join", "stmt_left,stmt_right,l,r", "SELECT l.value FROM stmt_left AS l ASOF JOIN stmt_right AS r ON l.id = r.id AND {{expr}}", "1 < 2", ExpectRefused),
		cell("asof-join-same-side-refused", "asof_join", "stmt_left,stmt_right,l,r", "SELECT l.value FROM stmt_left AS l ASOF JOIN stmt_right AS r ON l.id = r.id AND {{expr}}", "l.value >= l.grp", ExpectRefused),
		cell("asof-join-two-inequalities-refused", "asof_join", "stmt_left,stmt_right,l,r", "SELECT l.value FROM stmt_left AS l ASOF JOIN stmt_right AS r ON l.id = r.id AND l.value >= r.value AND {{expr}}", "l.id >= r.id", ExpectRefused),
		cell("asof-join-supported", "asof_join", "stmt_left,stmt_right,l,r", "SELECT l.value FROM stmt_left AS l ASOF JOIN stmt_right AS r ON l.id = r.id AND {{expr}}", "l.value >= r.value", ExpectSupported),
		cell("cte-refused", "cte", "stmt_left,scoped", "WITH scoped AS (SELECT id, value FROM stmt_left) SELECT scoped.{{expr}} AS value FROM scoped", "missing", ExpectRefused),
		cell("cte-supported", "cte", "stmt_left,scoped", "WITH scoped AS (SELECT id, value FROM stmt_left) SELECT scoped.{{expr}} AS value FROM scoped", "value", ExpectSupported),
		cell("cte-where-refused", "cte_where", "stmt_left,scoped", "WITH scoped AS (SELECT value FROM stmt_left WHERE {{expr}}) SELECT scoped.value FROM scoped", "missing = 1", ExpectRefused),
		cell("cte-where-supported", "cte_where", "stmt_left,scoped", "WITH scoped AS (SELECT value FROM stmt_left WHERE {{expr}}) SELECT scoped.value FROM scoped", "id = 1", ExpectSupported),
		cell("derived-where-refused", "derived_where", "stmt_left,nested", "SELECT nested.value FROM (SELECT value FROM stmt_left WHERE {{expr}}) AS nested", "missing = 1", ExpectRefused),
		cell("derived-where-supported", "derived_where", "stmt_left,nested", "SELECT nested.value FROM (SELECT value FROM stmt_left WHERE {{expr}}) AS nested", "id = 1", ExpectSupported),
		cell("correlated-derived-refused", "correlated_derived", "stmt_left,outer_scope,nested", "SELECT nested.value FROM stmt_left AS outer_scope CROSS JOIN (SELECT {{expr}} AS value) AS nested", "outer_scope.value", ExpectRefused),
		cell("correlated-derived-supported", "correlated_derived", "stmt_left,stmt_right,nested", "SELECT nested.value AS value FROM stmt_left CROSS JOIN (SELECT {{expr}} FROM stmt_right) AS nested", "value", ExpectSupported),
		cell("distinct-on-refused", "distinct_on", "stmt_left", "SELECT DISTINCT ON ({{expr}}) value FROM stmt_left", "missing", ExpectRefused),
		cell("distinct-on-alias-supported", "distinct_on", "stmt_left,Foo", "SELECT DISTINCT ON ({{expr}}) value AS Foo FROM stmt_left", "Foo", ExpectSupported),
		cell("distinct-on-supported", "distinct_on", "stmt_left", "SELECT DISTINCT ON ({{expr}}) value FROM stmt_left", "id", ExpectSupported),
		cell("duplicate-alias-refused", "duplicate_alias", "stmt_left,stmt_right,q", "SELECT q.value FROM stmt_left AS q CROSS JOIN stmt_right AS {{expr}}", "q", ExpectRefused),
		cell("duplicate-alias-case-supported", "duplicate_alias", "stmt_left,stmt_right,q,Q", "SELECT q.grp AS left_value, Q.payload AS right_value FROM stmt_left AS q CROSS JOIN stmt_right AS {{expr}}", "Q", ExpectSupported),
		cell("execution-only-refused", "execution_only", "stmt_left", "SELECT arrayMap((x, y) -> x + y, {{expr}}, [1]) AS value FROM stmt_left", "missing", ExpectRefused),
		cell("execution-only-runtime", "execution_only", "stmt_left", "SELECT arrayMap((x, y) -> x + y, {{expr}}, [1]) AS value FROM stmt_left", "items", ExpectExecutionOnly),
		cell("from-final-refused", "from_modifier", "stmt_left", "SELECT value FROM stmt_left {{expr}}", "FINAL", ExpectRefused),
		cell("from-final-supported", "from_modifier", "stmt_replacing", "SELECT value FROM stmt_replacing {{expr}}", "FINAL", ExpectSupported),
		cell("from-plain-supported", "from_modifier", "stmt_left", "SELECT value FROM {{expr}}", "stmt_left", ExpectSupported),
		cell("from-sample-refused", "from_modifier", "stmt_left", "SELECT value FROM stmt_left SAMPLE {{expr}}", "0.1", ExpectRefused),
		cell("group-by-refused", "group_by", "stmt_left", "SELECT grp AS value FROM stmt_left GROUP BY {{expr}}", "missing", ExpectRefused),
		cell("group-by-mixed-refused", "group_by", "stmt_left", "SELECT {{expr}} AS value FROM stmt_left GROUP BY grp", "value + count()", ExpectRefused),
		cell("group-by-nested-supported", "group_by", "stmt_left", "SELECT {{expr}} AS value FROM stmt_left GROUP BY grp", "grp + count()", ExpectSupported),
		cell("group-by-supported", "group_by", "stmt_left", "SELECT grp AS value FROM stmt_left GROUP BY {{expr}}", "grp", ExpectSupported),
		cell("having-refused", "having", "stmt_left", "SELECT count() AS value FROM stmt_left HAVING {{expr}}", "missing > 0", ExpectRefused),
		cell("having-aggregate-column-refused", "having", "stmt_left", "SELECT count() AS value FROM stmt_left HAVING {{expr}}", "stmt_left.value > 0", ExpectRefused),
		cell("having-window-refused", "having", "stmt_left", "SELECT count() AS value FROM stmt_left HAVING {{expr}}", "row_number() OVER () > 0", ExpectRefused),
		cell("having-supported", "having", "stmt_left", "SELECT count() AS value FROM stmt_left HAVING {{expr}}", "count() > 0", ExpectSupported),
		cell("join-on-domain-refused", "join_on", "stmt_left,stmt_right", "SELECT stmt_left.value FROM stmt_left INNER JOIN stmt_right ON {{expr}}", "stmt_left.value", ExpectRefused),
		cell("join-on-unknown-refused", "join_on", "stmt_left,stmt_right", "SELECT stmt_left.value FROM stmt_left INNER JOIN stmt_right ON {{expr}}", "stmt_left.missing = stmt_right.id", ExpectRefused),
		cell("join-on-supported", "join_on", "stmt_left,stmt_right", "SELECT stmt_left.value FROM stmt_left INNER JOIN stmt_right ON {{expr}}", "stmt_left.id = stmt_right.id", ExpectSupported),
		cell("join-using-refused", "join_using", "stmt_left,stmt_bad", "SELECT stmt_left.value FROM stmt_left INNER JOIN stmt_bad USING ({{expr}})", "id", ExpectRefused),
		cell("join-using-supported", "join_using", "stmt_left,stmt_third", "SELECT stmt_left.value FROM stmt_left INNER JOIN stmt_third USING ({{expr}})", "id", ExpectSupported),
		cell("join-using-output-supported", "join_using", "stmt_left,stmt_third", "SELECT {{expr}} AS value FROM stmt_left INNER JOIN stmt_third USING (id)", "id", ExpectSupported),
		cell("join-using-qualified-refused", "join_using", "stmt_left,stmt_third", "SELECT stmt_left.value FROM stmt_left INNER JOIN stmt_third USING ({{expr}})", "stmt_left.id", ExpectRefused),
		cell("limit-domain-refused", "limit", "stmt_left", "SELECT value FROM stmt_left LIMIT {{expr}}", "'x'", ExpectRefused),
		cell("limit-unknown-refused", "limit", "stmt_left", "SELECT value FROM stmt_left LIMIT {{expr}}", "missing", ExpectRefused),
		cell("limit-supported", "limit", "stmt_left", "SELECT value FROM stmt_left LIMIT {{expr}}", "1", ExpectSupported),
		cell("limit-by-domain-refused", "limit_by", "stmt_left", "SELECT value FROM stmt_left LIMIT {{expr}} BY grp", "'x'", ExpectRefused),
		cell("limit-by-unknown-refused", "limit_by", "stmt_left", "SELECT value FROM stmt_left LIMIT 1 BY {{expr}}", "missing", ExpectRefused),
		cell("limit-by-supported", "limit_by", "stmt_left", "SELECT value FROM stmt_left LIMIT 1 BY {{expr}}", "grp", ExpectSupported),
		cell("limit-by-aggregate-column-refused", "limit_by", "stmt_left", "SELECT count() AS value FROM stmt_left LIMIT 1 BY {{expr}}", "stmt_left.value", ExpectRefused),
		cell("multi-join-bad-first", "multi_join", "stmt_left,stmt_right,stmt_third,l,r,t", "SELECT l.value AS value FROM stmt_left AS l INNER JOIN stmt_right AS r ON l.{{expr}} = r.id INNER JOIN stmt_third AS t ON r.id = t.id", "missing", ExpectRefused),
		cell("multi-join-bad-second", "multi_join", "stmt_left,stmt_right,stmt_third,l,r,t", "SELECT l.value AS value FROM stmt_left AS l INNER JOIN stmt_right AS r ON l.id = r.id INNER JOIN stmt_third AS t ON r.{{expr}} = t.id", "missing", ExpectRefused),
		cell("multi-join-domain-refused", "multi_join", "stmt_left,stmt_right,stmt_third,l,r,t", "SELECT l.value AS value FROM stmt_left AS l INNER JOIN stmt_right AS r ON l.{{expr}} INNER JOIN stmt_third AS t ON r.id = t.id", "value", ExpectRefused),
		cell("multi-join-supported", "multi_join", "stmt_left,stmt_right,stmt_third,l,r,t", "SELECT l.value AS value FROM stmt_left AS l INNER JOIN stmt_right AS r ON l.{{expr}} = r.id INNER JOIN stmt_third AS t ON r.id = t.id", "id", ExpectSupported),
		cell("offset-domain-refused", "offset", "stmt_left", "SELECT value FROM stmt_left LIMIT 1 OFFSET {{expr}}", "'x'", ExpectRefused),
		cell("offset-unknown-refused", "offset", "stmt_left", "SELECT value FROM stmt_left LIMIT 1 OFFSET {{expr}}", "missing", ExpectRefused),
		cell("offset-supported", "offset", "stmt_left", "SELECT value FROM stmt_left LIMIT 1 OFFSET {{expr}}", "0", ExpectSupported),
		cell("order-by-refused", "order_by", "stmt_left", "SELECT value FROM stmt_left ORDER BY {{expr}}", "missing", ExpectRefused),
		cell("order-by-fill-refused", "order_by", "stmt_left", "SELECT id FROM stmt_left ORDER BY id WITH FILL FROM {{expr}} TO 3 STEP 1", "missing", ExpectRefused),
		cell("order-by-fill-step-zero-refused", "order_by", "stmt_left", "SELECT id FROM stmt_left ORDER BY id WITH FILL FROM 0 TO 3 STEP {{expr}}", "0", ExpectRefused),
		cell("order-by-fill-string-refused", "order_by", "stmt_left", "SELECT id FROM stmt_left ORDER BY id WITH FILL FROM {{expr}} TO 3 STEP 1", "'x'", ExpectRefused),
		cell("order-by-fill-column-refused", "order_by", "stmt_left", "SELECT id FROM stmt_left ORDER BY id WITH FILL FROM {{expr}} TO 3 STEP 1", "id", ExpectRefused),
		cell("order-by-fill-supported", "order_by", "stmt_left", "SELECT id FROM stmt_left ORDER BY id WITH FILL FROM {{expr}} TO 3 STEP 1", "0", ExpectSupported),
		cell("order-by-supported", "order_by", "stmt_left", "SELECT value FROM stmt_left ORDER BY {{expr}}", "id", ExpectSupported),
		cell("order-by-aggregate-column-refused", "order_by", "stmt_left", "SELECT count() AS value FROM stmt_left ORDER BY {{expr}}", "stmt_left.value", ExpectRefused),
		cell("order-by-aggregate-supported", "order_by", "stmt_left", "SELECT count() AS value FROM stmt_left ORDER BY {{expr}}", "value", ExpectSupported),
		cell("order-by-group-aggregate-column-refused", "order_by", "stmt_left", "SELECT count() AS value FROM stmt_left GROUP BY id ORDER BY {{expr}}", "stmt_left.value", ExpectRefused),
		cell("order-by-group-aggregate-supported", "order_by", "stmt_left", "SELECT count() AS value FROM stmt_left GROUP BY id ORDER BY {{expr}}", "id", ExpectSupported),
		cell("prewhere-domain-refused", "prewhere", "stmt_left", "SELECT value FROM stmt_left PREWHERE {{expr}}", "label", ExpectRefused),
		cell("prewhere-unknown-refused", "prewhere", "stmt_left", "SELECT value FROM stmt_left PREWHERE {{expr}}", "missing = 1", ExpectRefused),
		cell("prewhere-supported", "prewhere", "stmt_left", "SELECT value FROM stmt_left PREWHERE {{expr}}", "id = 1", ExpectSupported),
		cell("qualifier-case-refused", "qualifier_case", "stmt_left,X", "SELECT {{expr}}.id AS value FROM stmt_left AS X", "x", ExpectRefused),
		cell("qualifier-case-supported", "qualifier_case", "stmt_left,X", "SELECT {{expr}}.id AS value FROM stmt_left AS X", "X", ExpectSupported),
		cell("result-vector-refused", "result_vector", "stmt_left", "SELECT id AS first, {{expr}} AS second FROM stmt_left", "missing", ExpectRefused),
		cell("result-vector-supported", "result_vector", "stmt_left", "SELECT id AS first, {{expr}} AS second FROM stmt_left", "value", ExpectSupported),
		cell("scalar-where-refused", "scalar_where", "stmt_left,stmt_right", "SELECT (SELECT value FROM stmt_right WHERE {{expr}} LIMIT 1) AS value FROM stmt_left", "missing = 1", ExpectRefused),
		cell("scalar-where-supported", "scalar_where", "stmt_left,stmt_right", "SELECT (SELECT value FROM stmt_right WHERE {{expr}} LIMIT 1) AS value FROM stmt_left", "id = 1", ExpectSupported),
		cell("scalar-wrapper-supported-direct", "scalar_wrapper", "stmt_left,stmt_right", "SELECT (SELECT value FROM stmt_right WHERE {{expr}} LIMIT 1) + 1 AS value FROM stmt_left", "id = 0", ExpectSupported),
		cell("scalar-wrapper-refused", "scalar_wrapper", "stmt_left,stmt_right", "SELECT (SELECT value FROM stmt_right WHERE {{expr}} LIMIT 1) + 1 AS value FROM stmt_left", "missing = 0", ExpectRefused),
		cell("scalar-wrapper-supported", "scalar_wrapper", "stmt_left,stmt_right", "SELECT ifNull((SELECT value FROM stmt_right WHERE {{expr}} LIMIT 1), 0) + 1 AS value FROM stmt_left", "id = 0", ExpectSupported),
		cell("settings-refused", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS max_threads = {{expr}}", "'x'", ExpectRefused),
		cell("settings-supported", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS max_threads = {{expr}}", "1", ExpectSupported),
		cell("settings-max-bytes-refused", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS max_bytes_before_external_group_by = {{expr}}", "'x'", ExpectRefused),
		cell("settings-max-bytes-supported", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS max_bytes_before_external_group_by = {{expr}}", "17", ExpectSupported),
		cell("settings-external-sort-refused", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS max_bytes_before_external_sort = {{expr}}", "'x'", ExpectRefused),
		cell("settings-external-sort-supported", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS max_bytes_before_external_sort = {{expr}}", "19", ExpectSupported),
		cell("settings-overcommit-refused", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS memory_overcommit_ratio_denominator = {{expr}}", "'x'", ExpectRefused),
		cell("settings-overcommit-supported", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS memory_overcommit_ratio_denominator = {{expr}}", "0", ExpectSupported),
		cell("settings-overcommit-user-refused", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS memory_overcommit_ratio_denominator_for_user = {{expr}}", "'x'", ExpectRefused),
		cell("settings-overcommit-user-supported", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS memory_overcommit_ratio_denominator_for_user = {{expr}}", "0", ExpectSupported),
		cell("settings-preferred-block-refused", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS preferred_block_size_bytes = {{expr}}", "'x'", ExpectRefused),
		cell("settings-preferred-block-supported", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS preferred_block_size_bytes = {{expr}}", "23", ExpectSupported),
		cell("settings-cache-refused", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS use_uncompressed_cache = {{expr}}", "2", ExpectRefused),
		cell("settings-cache-supported", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS use_uncompressed_cache = {{expr}}", "0", ExpectSupported),
		cell("settings-final-merge-refused", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS do_not_merge_across_partitions_select_final = {{expr}}", "2", ExpectRefused),
		cell("settings-final-merge-supported", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS do_not_merge_across_partitions_select_final = {{expr}}", "0", ExpectSupported),
		cell("settings-execution-time-refused", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS max_execution_time = {{expr}}", "'x'", ExpectRefused),
		cell("settings-execution-time-supported", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS max_execution_time = {{expr}}", "600", ExpectSupported),
		cell("settings-execution-time-max-supported", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS max_execution_time = {{expr}}", "9223372036854", ExpectSupported),
		cell("settings-execution-time-max-plus-refused", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS max_execution_time = {{expr}}", "9223372036855", ExpectRefused),
		cell("settings-execution-time-uint-max-refused", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS max_execution_time = {{expr}}", "18446744073709551615", ExpectRefused),
		cell("settings-memory-refused", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS max_memory_usage = {{expr}}", "'x'", ExpectRefused),
		cell("settings-memory-supported", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS max_memory_usage = {{expr}}", "31", ExpectSupported),
		cell("settings-block-refused", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS max_block_size = {{expr}}", "0", ExpectRefused),
		cell("settings-block-supported", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS max_block_size = {{expr}}", "37", ExpectSupported),
		cell("settings-log-comment-refused", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS log_comment = {{expr}}", "1", ExpectRefused),
		cell("settings-log-comment-supported", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS log_comment = {{expr}}", "'statement_probe'", ExpectSupported),
		cell("settings-duplicate-supported", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS max_memory_usage = 1, max_memory_usage = {{expr}}", "2", ExpectSupported),
		cell("settings-case-refused", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS {{expr}} = 1", "MAX_MEMORY_USAGE", ExpectRefused),
		cell("settings-unknown-refused", "settings", "stmt_left", "SELECT value FROM stmt_left SETTINGS {{expr}} = 1", "unknown_setting", ExpectRefused),
		cell("ties-limit-refused", "ties", "stmt_left", "SELECT value FROM stmt_left LIMIT {{expr}} WITH TIES", "1", ExpectRefused),
		cell("ties-limit-comment-refused", "ties", "stmt_left", "SELECT value FROM stmt_left /* ORDER BY id */ LIMIT {{expr}} WITH TIES", "1", ExpectRefused),
		cell("ties-limit-inner-order-refused", "ties", "stmt_left,stmt_right", "SELECT value FROM stmt_left WHERE id IN (SELECT id FROM stmt_right ORDER BY id) LIMIT {{expr}} WITH TIES", "1", ExpectRefused),
		cell("ties-limit-string-refused", "ties", "stmt_left", "SELECT {{expr}} AS value FROM stmt_left LIMIT 1 WITH TIES", "'ORDER BY'", ExpectRefused),
		cell("ties-limit-supported", "ties", "stmt_left", "SELECT value FROM stmt_left ORDER BY id LIMIT {{expr}} WITH TIES", "1", ExpectSupported),
		cell("ties-top-refused", "ties", "stmt_left", "SELECT TOP {{expr}} WITH TIES value FROM stmt_left", "1", ExpectRefused),
		cell("ties-top-domain-refused", "ties", "stmt_left", "SELECT TOP {{expr}} value FROM stmt_left", "1.5", ExpectRefused),
		cell("ties-top-supported", "ties", "stmt_left", "SELECT TOP {{expr}} WITH TIES value FROM stmt_left ORDER BY id", "1", ExpectSupported),
		cell("ties-top-plain-supported", "ties", "stmt_left", "SELECT TOP {{expr}} value FROM stmt_left", "1", ExpectSupported),
		cell("union-refused", "union", "stmt_left,stmt_right", "SELECT value FROM stmt_left UNION ALL SELECT {{expr}} AS value FROM stmt_right", "payload", ExpectRefused),
		cell("union-supported", "union", "stmt_left,stmt_right", "SELECT value FROM stmt_left UNION ALL SELECT {{expr}} AS value FROM stmt_right", "value", ExpectSupported),
		cell("where-domain-refused", "where", "stmt_left", "SELECT value FROM stmt_left WHERE {{expr}}", "value", ExpectRefused),
		cell("where-unknown-refused", "where", "stmt_left", "SELECT value FROM stmt_left WHERE {{expr}}", "missing = 1", ExpectRefused),
		cell("where-supported", "where", "stmt_left", "SELECT value FROM stmt_left WHERE {{expr}}", "id = 1", ExpectSupported),
		cell("where-aggregate-refused", "where", "stmt_left", "SELECT value FROM stmt_left WHERE {{expr}}", "count() > 0", ExpectRefused),
		cell("where-window-refused", "where", "stmt_left", "SELECT value FROM stmt_left WHERE {{expr}}", "row_number() OVER () > 0", ExpectRefused),
		cell("window-refused", "window", "stmt_left", "SELECT row_number() OVER (ORDER BY {{expr}}) AS value FROM stmt_left", "missing", ExpectRefused),
		cell("window-aggregate-column-refused", "window", "stmt_left,w", "SELECT count() AS total, row_number() OVER w AS value FROM stmt_left WINDOW w AS (ORDER BY {{expr}})", "stmt_left.value", ExpectRefused),
		cell("window-aggregate-group-supported", "window", "stmt_left,w", "SELECT count() AS total, row_number() OVER w AS value FROM stmt_left GROUP BY id WINDOW w AS (ORDER BY {{expr}})", "id", ExpectSupported),
		cell("window-direct-inner-aggregate-refused", "window", "stmt_left", "SELECT first_value(value) OVER (ORDER BY {{expr}}) AS value FROM stmt_left", "count()", ExpectRefused),
		cell("window-direct-inner-aggregate-group-supported", "window", "stmt_left", "SELECT row_number() OVER (ORDER BY {{expr}}) AS value FROM stmt_left", "count()", ExpectSupported),
		cell("window-named-inner-aggregate-refused", "window", "stmt_left,w", "SELECT first_value(value) OVER w AS value FROM stmt_left WINDOW w AS (ORDER BY {{expr}})", "count()", ExpectRefused),
		cell("window-named-inner-aggregate-group-supported", "window", "stmt_left,w", "SELECT row_number() OVER w AS value FROM stmt_left WINDOW w AS (ORDER BY {{expr}})", "count()", ExpectSupported),
		cell("window-supported", "window", "stmt_left", "SELECT row_number() OVER (ORDER BY {{expr}}) AS value FROM stmt_left", "id", ExpectSupported),
	}
}

// ShrinkStatement replaces only the expression slot of a statement cell.
func ShrinkStatement(input Input, candidates []string, keepsFinding func(Input) bool) Input {
	if input.StatementTemplate == "" || !strings.Contains(input.StatementTemplate, "{{expr}}") {
		return input
	}
	best := input
	for _, candidate := range candidates {
		trial := input
		trial.Expression = candidate
		trial.Query = renderStatement(input.StatementTemplate, candidate)
		if keepsFinding(trial) {
			best = trial
		}
	}
	return best
}

func renderStatement(template, expression string) string {
	return strings.ReplaceAll(template, "{{expr}}", expression)
}

// ValidateStatementCoverage checks every named agreement lane.
func ValidateStatementCoverage(report Report) error {
	type count struct{ accepted, rejected int }
	counts := make(map[string]*count, len(statementContexts))
	seenKnownGaps := make(map[string]bool, len(statementKnownGaps))
	seenKnownAcceptances := make(map[string]bool, len(statementKnownAcceptances))
	for _, context := range statementContexts {
		counts[context] = &count{}
	}
	for _, cell := range report.Cells {
		if cell.Query == "" {
			continue
		}
		current, ok := counts[cell.StatementShape]
		if !ok {
			return fmt.Errorf("statement cell %q has unknown context %q", cell.ID, cell.StatementShape)
		}
		if cell.Scope == "" || cell.Fixture == "" || cell.StatementTemplate == "" {
			return fmt.Errorf("statement cell %q has incomplete context identity", cell.ID)
		}
		if cell.Expectation != ExpectKnownChgenRefusal && cell.Expectation != ExpectKnownChgenAcceptance && cell.KnownGap != "" {
			return fmt.Errorf("statement cell %q has an unexpected known gap %q", cell.ID, cell.KnownGap)
		}
		if cell.ExpectedChgenError != "" && !strings.Contains(cell.Chgen.Error, cell.ExpectedChgenError) {
			return fmt.Errorf("statement cell %q chgen error is %q, want boundary %q", cell.ID, cell.Chgen.Error, cell.ExpectedChgenError)
		}
		if cell.ExpectedChgenResult != "" && !hasExactSingleResult(cell.Chgen, cell.ExpectedChgenResult) {
			return fmt.Errorf("statement cell %q chgen result is not exactly %s", cell.ID, cell.ExpectedChgenResult)
		}
		if cell.ExpectedAnalysisCode != 0 && cell.Analysis.ErrorCode != cell.ExpectedAnalysisCode {
			return fmt.Errorf("statement cell %q analysis code is %d, want %d", cell.ID, cell.Analysis.ErrorCode, cell.ExpectedAnalysisCode)
		}
		if cell.ExpectedAnalysisError != "" && !strings.Contains(cell.Analysis.Error, cell.ExpectedAnalysisError) {
			return fmt.Errorf("statement cell %q analysis error is %q, want boundary %q", cell.ID, cell.Analysis.Error, cell.ExpectedAnalysisError)
		}
		if cell.ExpectedAnalysisResult != "" && !hasExactSingleResult(cell.Analysis, cell.ExpectedAnalysisResult) {
			return fmt.Errorf("statement cell %q analysis result is not exactly %s", cell.ID, cell.ExpectedAnalysisResult)
		}
		if cell.ExpectedExecutionCode != 0 && cell.Execution.ErrorCode != cell.ExpectedExecutionCode {
			return fmt.Errorf("statement cell %q execution code is %d, want %d", cell.ID, cell.Execution.ErrorCode, cell.ExpectedExecutionCode)
		}
		if cell.ExpectedExecutionError != "" && !strings.Contains(cell.Execution.Error, cell.ExpectedExecutionError) {
			return fmt.Errorf("statement cell %q execution error is %q, want boundary %q", cell.ID, cell.Execution.Error, cell.ExpectedExecutionError)
		}
		if cell.Expectation == ExpectKnownChgenRefusal && cell.ExpectedChgenError == "" {
			return fmt.Errorf("known chgen refusal %q has no pinned chgen boundary", cell.ID)
		}
		if cell.Expectation == ExpectKnownChgenAcceptance {
			if cell.ExpectedChgenResult == "" {
				return fmt.Errorf("known chgen acceptance %q has no pinned chgen result", cell.ID)
			}
			if cell.Analysis.Error != "" && (cell.ExpectedAnalysisCode == 0 || cell.ExpectedAnalysisError == "" ||
				cell.ExpectedExecutionCode == 0 || cell.ExpectedExecutionError == "") {
				return fmt.Errorf("known chgen acceptance %q has an incomplete server refusal boundary", cell.ID)
			}
			if cell.Analysis.Error == "" && cell.ExpectedAnalysisResult == "" {
				return fmt.Errorf("known chgen acceptance %q has no pinned analysis result", cell.ID)
			}
		}
		switch cell.Expectation {
		case ExpectSupported:
			current.accepted++
			if !cell.Execution.Ran || len(cell.Analysis.Results) == 0 {
				return fmt.Errorf("supported statement cell %q was rejected", cell.ID)
			}
			if err := compareResultVectors(cell.Chgen, cell.Analysis); err != nil {
				return fmt.Errorf("supported statement cell %q: %w", cell.ID, err)
			}
		case ExpectRefused:
			current.rejected++
			if cell.Execution.Ran || cell.Execution.Error == "" || !cell.Analysis.hasAnswer() {
				return fmt.Errorf("refused statement cell %q was accepted", cell.ID)
			}
			if cell.Chgen.Error == "" {
				return fmt.Errorf("refused statement cell %q was typed by chgen", cell.ID)
			}
		case ExpectExecutionOnly:
			current.accepted++
			if cell.Execution.Ran || cell.Execution.Error == "" || len(cell.Analysis.Results) == 0 {
				return fmt.Errorf("execution-only statement cell %q did not fail only at execution", cell.ID)
			}
			if err := compareResultVectors(cell.Chgen, cell.Analysis); err != nil {
				return fmt.Errorf("execution-only statement cell %q: %w", cell.ID, err)
			}
		case ExpectKnownChgenRefusal:
			current.accepted++
			wantGap, ok := statementKnownGaps[cell.ID]
			if !ok || cell.KnownGap != wantGap {
				return fmt.Errorf("known chgen refusal %q has gap %q, want %q", cell.ID, cell.KnownGap, wantGap)
			}
			seenKnownGaps[cell.ID] = true
			if !cell.Execution.Ran || len(cell.Analysis.Results) == 0 {
				return fmt.Errorf("known chgen refusal %q was rejected by the server", cell.ID)
			}
			if cell.Chgen.Error == "" {
				return fmt.Errorf("known chgen refusal %q is now typed; remove the support gap", cell.ID)
			}
		case ExpectKnownChgenAcceptance:
			current.rejected++
			wantGap, ok := statementKnownAcceptances[cell.ID]
			if !ok || cell.KnownGap != wantGap {
				return fmt.Errorf("known chgen acceptance %q has gap %q, want %q", cell.ID, cell.KnownGap, wantGap)
			}
			seenKnownAcceptances[cell.ID] = true
			if cell.Chgen.Error != "" || len(cell.Chgen.Results) == 0 {
				return fmt.Errorf("known chgen acceptance %q was not typed by chgen", cell.ID)
			}
			if cell.Analysis.Error != "" {
				if cell.Execution.Ran || cell.Execution.Error == "" {
					return fmt.Errorf("known chgen acceptance %q has no matching server refusal witness", cell.ID)
				}
				break
			}
			if !cell.Execution.Ran || len(cell.Analysis.Results) == 0 {
				return fmt.Errorf("known chgen acceptance %q has no accepted server result witness", cell.ID)
			}
			if err := compareResultVectors(cell.Chgen, cell.Analysis); err == nil {
				return fmt.Errorf("known chgen acceptance %q now agrees with the server; remove the support gap", cell.ID)
			}
		default:
			return fmt.Errorf("statement cell %q has no support lane", cell.ID)
		}
	}
	for id := range statementKnownGaps {
		if !seenKnownGaps[id] {
			return fmt.Errorf("known chgen refusal %q is missing from the measured report", id)
		}
	}
	for id := range statementKnownAcceptances {
		if !seenKnownAcceptances[id] {
			return fmt.Errorf("known chgen acceptance %q is missing from the measured report", id)
		}
	}
	for _, context := range statementContexts {
		current := counts[context]
		if current.accepted == 0 || current.rejected == 0 {
			return fmt.Errorf("statement context %q has no accepted and refused server pair", context)
		}
	}
	return nil
}

func hasExactSingleResult(result TypeResult, raw string) bool {
	return len(result.Results) == 1 && result.Results[0].Name == "value" && result.Results[0].Raw == raw
}

func compareResultVectors(chgen, analysis TypeResult) error {
	if chgen.Error != "" {
		return fmt.Errorf("chgen refused: %s", chgen.Error)
	}
	if len(chgen.Results) != len(analysis.Results) {
		return fmt.Errorf("chgen has %d results, analysis has %d", len(chgen.Results), len(analysis.Results))
	}
	for index := range chgen.Results {
		left, right := chgen.Results[index], analysis.Results[index]
		if left.Name != right.Name {
			return fmt.Errorf("result %d name is %q, want %q", index, left.Name, right.Name)
		}
		if !left.Canonical.Equal(right.Canonical) {
			return fmt.Errorf("result %d type is %s, want %s", index, left.Raw, right.Raw)
		}
	}
	return nil
}

func hasStatementCells(cells []Cell) bool {
	for _, cell := range cells {
		if cell.Query != "" {
			return true
		}
	}
	return false
}
