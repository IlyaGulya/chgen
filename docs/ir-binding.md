# Parser independent column binding

chgen now uses the IR column binder in production for a bounded SELECT family.
The binder consumes a complete lowered tree and catalog column identities,
without importing the ClickHouse parser. Type inference and SQL generation
remain in the existing engine.

## Production domain

The domain covers catalog tables, measured series functions, relation and scalar
CTEs, scalar subqueries, derived sources and JOIN chains. Each SELECT has its own
binding, including eligible bodies of CTEs, scalar subqueries and derived
relations. SELECT without FROM is also eligible. Ordinary and qualified column
references are resolved in projections, WHERE, ordinary GROUP BY, HAVING,
ORDER BY, LIMIT and OFFSET;
JOIN conditions see only the sources introduced at that point. The existing
wildcard expansion preserves column order and inclusion settings.

Complex relations first obtain typed source signatures and lexical contexts
from the existing resolver. The IR binder consumes these inputs and supplies
column lookups during the final resolution pass. CTE declaration visibility,
derived output types, JOIN common types and outer-join nullability still come
from the established engine. This is not a parser-independent type inferencer
or a complete replacement for legacy scope construction.

Scalar WITH definitions are represented as expressions rather than relation
queries. Their types, dependency ordering and lexical visibility still come from
the established resolver. Scalar subqueries retain their complete SELECT tree
and parentheses; each nested scope is bound separately. Existing single-column,
cardinality and correlation restrictions remain in force, as does scalar-result
nullability. Binding does not treat a subquery's columns as outer row bindings.

Window functions retain their OVER specification, partition and order
expressions, named-window references and frame bounds. ROWS and RANGE, bound
directions, and BETWEEN remain distinct tree properties. Named definitions are
bound in their SELECT scope. Frame validity, inheritance rules, recursive
definitions and function result types remain checked by the existing engine.

Lambdas retain their parameter names, parentheses and body. Parameters shadow
outer names and projection aliases, including inside nested lambdas; captures
are bound against the enclosing scope. Parameters are not reported as catalog
columns. The existing higher-order function rules assign parameter types and
check argument counts and body types.

Column and computed aliases are expanded during binding, including chains,
forward references and uses in WHERE, GROUP BY, HAVING and ORDER BY. Their
observations retain the referenced catalog columns and the range of each alias
use. The existing engine still infers the expression type; the binder does not assign a computed
alias the type of its input column. Cycles fail with ir-alias-cycle. An active
alias that refers to an identically named physical column resolves that column,
preserving self-reference and collision behavior. Duplicate output aliases
remain checked by the existing result validator.

Typed parent contexts preserve correlated column access and alias expressions
that are rebound against a derived query's rows. References to outer scalar
expressions carry kind scalar rather than pretending to be catalog columns.
An existing local qualifier prevents lookup from leaking into a parent source.

Special grouping modes such as ROLLUP, CUBE and TOTALS, subqueries inside
lambdas, tuple-field paths and
literal-name precedence for NULL, true and false stay
outside this domain. Queries that cannot lower completely, or fall outside the
binder domain, keep the legacy resolver. This is an explicit migration boundary,
not an opt-in to skip checks. An unknown column inside the new domain is an
invalid binding diagnostic with code ir-column-missing; it never triggers
fallback. Existing clause context and spelling suggestions are retained.

Successful bindings supply eligible root and nested scopes' column lookups
during validation and type inference. The production parser is unchanged.

## Coverage and source provenance

The coverage report adds a separate bind stage and a binding observation with
backend sqlir. Each explicit column reference identifies its catalog table,
column, ClickHouse type and available source range. Binding success alone does
not prove expression type rules, valid aggregate placement or executable SQL.
Coverage reports distinguish binding from successful resolution and generation.

IR expressions and relations retain available parser source ranges as half-open
UTF-8 byte offsets in the parsed SQL. These are provenance, not semantic tree
properties, and frontend structure comparison excludes them. A spanless
candidate can compare structure but does not establish source-position parity.

The parser adapter removes LIMIT WITH TIES from its resolution copy because the
pinned upstream AST has no node for it. Lowering refuses these queries rather
than presenting an erased modifier as a complete tree. Runtime SQL and legacy
generation of WITH TIES remain unchanged.

## Regression boundaries

Public API tests verify invalid predicate bindings and preserved generated
behavior. CLI tests verify source ranges, catalog reference identities, strict
lowering and comparison with a spanless frontend report. Existing unknown-column
suggestion and clause-validation regressions remain intact.

TestServerSeriesGeneratedRuntime executes generated filters, alias collisions,
ordinary grouping with HAVING, and a combined CTE, derived source and JOIN on
ClickHouse 25.8.29.51 with both
pinned driver versions; the independent expected value is 3. Existing wildcard
runtime tests cover column lookups after expansion.

The same runtime fixture checks a forward scalar WITH dependency and a scalar
count subquery returning 5 for row 2, plus a scalar SELECT without FROM returning
7. Public and CLI regressions retain local scalar shadowing, reject dependency
cycles and unknown columns, and preserve scalar-subquery cardinality checks.

Runtime window regressions check positions 1 through 4 and rolling sums
0, 1, 3, 5 through a named-window inheritance chain and an explicit ROWS frame.
Lambda regressions execute multiple parameters, shadowing, outer column capture
and nested lambdas. Public tests retain invalid-frame and window-placement
checks, parameter case sensitivity and lambda arity checks; CLI reports distinguish
captured columns from local parameters.

Remaining migration boundaries include special grouping modes, subqueries inside
lambdas, tuple-field paths and replacing legacy construction of typed source
signatures. Each needs parity witnesses before replacing its existing path.
None is implied by a passed bind observation today.
