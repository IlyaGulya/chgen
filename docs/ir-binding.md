# Parser independent column binding

chgen now uses the IR column binder in production for a bounded SELECT family.
The binder consumes a complete lowered tree and catalog column identities,
without importing the ClickHouse parser. Type inference and SQL generation
remain in the existing engine.

## Production domain

The domain has exactly one FROM relation and no CTE, GROUP BY, HAVING or
correlated subquery. The relation can be a catalog table or
a measured series table function. Ordinary and qualified column references are
resolved in projections, WHERE, ORDER BY, LIMIT and OFFSET. Wildcards are
validated against the source; the existing expansion preserves column order
and inclusion settings.

Direct column aliases such as e.id AS event_id are resolved back to their
catalog column, including uses in WHERE and ORDER BY. Their observations retain
the source column identity and the range of each alias use. Computed aliases,
alias chains, duplicate aliases and names that collide with source columns
remain in the legacy resolver; their substitution and precedence rules are
not part of this domain.

Tuple-field paths and literal-name precedence for NULL, true and false stay
outside this domain. Queries that cannot lower completely, or fall outside the
binder domain, keep the legacy resolver. This is an explicit migration boundary,
not an opt-in to skip checks. An unknown column inside the new domain is an
invalid binding diagnostic with code ir-column-missing; it never triggers
fallback. Existing clause context and spelling suggestions are retained.

Successful bindings supply the root scope's column lookups during validation
and type inference. Legacy lexical rules for joins, alias expansion, CTEs and
correlation remain isolated from that scope. The production parser is unchanged.

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

TestServerSeriesGeneratedRuntime executes a generated filter with WHERE,
ORDER BY, LIMIT and OFFSET on ClickHouse 25.8.29.51 with both pinned driver
versions; its independent expected result is number=3. Existing wildcard runtime
tests cover root column lookups after expansion.

Next steps are computed alias substitution, relation CTE visibility, derived
scopes and JOIN binding. Each needs its own domain and parity witnesses before
replacing the legacy path. None is implied by a passed bind observation today.
