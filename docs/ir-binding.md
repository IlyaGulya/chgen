# Parser independent column binding

chgen now uses the IR column binder in production for a bounded SELECT family.
The binder consumes a complete lowered tree and catalog column identities,
without importing the ClickHouse parser. Type inference and SQL generation
remain in the existing engine.

## Production domain

The domain covers catalog tables, measured series functions, relation CTEs,
derived sources and JOIN chains. Each SELECT has its own binding, including
eligible bodies of CTEs and derived relations. Ordinary and qualified column
references are resolved in projections, WHERE, ORDER BY, LIMIT and OFFSET;
JOIN conditions see only the sources introduced at that point. The existing
wildcard expansion preserves column order and inclusion settings.

Complex relations first obtain typed source signatures and lexical contexts
from the existing resolver. The IR binder consumes these inputs and supplies
column lookups during the final resolution pass. CTE declaration visibility,
derived output types, JOIN common types and outer-join nullability still come
from the established engine. This is not a parser-independent type inferencer
or a complete replacement for legacy scope construction.

Column and computed aliases are expanded during binding, including chains,
forward references and uses in WHERE and ORDER BY. Their observations retain
the referenced catalog columns and the range of each alias use. The existing
engine still infers the expression type; the binder does not assign a computed
alias the type of its input column. Cycles fail with ir-alias-cycle. An active
alias that refers to an identically named physical column resolves that column,
preserving self-reference and collision behavior. Duplicate output aliases
remain checked by the existing result validator.

Typed parent contexts preserve correlated column access and alias expressions
that are rebound against a derived query's rows. References to outer scalar
expressions carry kind scalar rather than pretending to be catalog columns.
An existing local qualifier prevents lookup from leaking into a parent source.

GROUP BY, HAVING, scalar subquery syntax, lambda scopes, tuple-field paths and
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
The unchanged corpus has 24 successful IR binding observations among 27 query
cases; only 17 of those query cases resolve and generate.

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

TestServerSeriesGeneratedRuntime executes generated filters, alias collisions
and a combined CTE, derived source and JOIN on ClickHouse 25.8.29.51 with both
pinned driver versions; the independent expected value is 3. Existing wildcard
runtime tests cover column lookups after expansion.

Remaining migration boundaries include grouping, scalar subqueries, scalar
CTEs, windows, lambda scopes and replacing legacy construction of typed source
signatures. Each needs parity witnesses before replacing its existing path.
None is implied by a passed bind observation today.
