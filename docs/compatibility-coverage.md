# SQL compatibility coverage

`chgen coverage` reports what happens to complete SQL inputs, independently of
the supported-function roster. It writes JSON to stdout, not generated files.

```sh
chgen coverage -corpus testdata/syntax-corpus.json
chgen coverage -corpus testdata/syntax-corpus.json \
  -server http://localhost:8123 -database test_schema
```

Without `-server` there is no network access. Cases continue after a refusal.
The optional server analysis is read-only and bounded; it never applies schema
DDL or submits parser-only scripts. Prepare the fixture database separately.

## Corpus and evidence

A corpus requires `version: 1`, `clickhouse_version`, and nonempty `cases`.
Each case needs a unique `id`, `family`, `source`, `sql`, and `expected_server`
(`accept`, `refuse`, or `unknown`). Optional `schema` supplies local DDL only.
Default `scope: query` requires one unannotated SELECT. `scope: parse` accepts
complete scripts for parser testing only. Duplicate JSON keys, unknown fields,
duplicate IDs and trailing JSON are refused.

Reports identify the exact corpus SHA-256 and pinned parser dependency. Stages
are distinct: `parse`, `catalog`, `resolve` (binding and inference), `generate`,
`server_analysis`, and complete ordered `type_comparison`. Unvisited stages
remain `not_run`. `lower` records whether the entire SELECT can be represented
in the parser-independent observation tree. Unsupported properties refuse
lowering rather than disappearing. `execution` is always `not_run`: formatting generated Go does
not demonstrate compilation or runtime correctness.

Declared server expectations are not measurements, and a local refusal does
not prove that ClickHouse rejects SQL. Live analysis must match the exact corpus
server version. Permission/authentication/readonly refusals are `blocked`;
other refusals may mean that the prepared fixture is missing, not invalid SQL.
There is deliberately no percentage of "all ClickHouse SQL". Keep parser-only
scripts separate from typed-query cases when interpreting the counts.

Exit codes: `0` means a report without compared mismatches, including
unknown or blocked coverage; `1` means a structure or result-vector mismatch (JSON
is still emitted); `2` means an invalid corpus or run. Unknown coverage must
not be read as support or as proof that a workload is safe.

## Comparing frontend observations

`chgen coverage -corpus corpus.json -candidate candidate.json` compares a
separately produced report against local observations. Corpus digest, format,
server version, case IDs, scope and source must agree. Complete lowered trees
are compared with a JSON-pointer diagnostic for the first difference; available
ordered result vectors are compared separately. An artifact with no comparable
complete trees is refused. Candidate artifacts never authorize generation.

The observation tree currently covers ordinary projections, table relations,
series function calls, relation CTEs, numeric expressions, WHERE, GROUP BY,
HAVING, ORDER BY and LIMIT/OFFSET. Any unmodeled nonzero parser property rejects
the entire lowering. This is not a SQL-equivalence proof or a replacement
frontend: source positions, the production binder and the generator still use
the existing parser. A matching self-generated report is only a comparison
mechanism test, not independent evidence of correctness.

## Bundled corpus

The 107 input units come from ClickHouse commit
`54df9137dcfc1ef0b307f264199e004a05a9a5dd`, tag `v25.8.29.51-lts`:
16 targeted stateless scripts, 64 scripts sampled by sorting stateless SQL Git
blob hashes and taking the first 64, and 27 exact single-line series SELECTs.
Each case links to its source. Full scripts retain setup and negative cases;
their server expectation is deliberately `unknown`. This starter corpus is not
an unbiased sample of application workloads.

These imported tests are Apache-2.0, not chgen's MIT license. See
[the corpus license](../testdata/syntax-corpus-LICENSE.txt).

Add complete, source-linked client queries and schemas to the corpus. Use the
report to identify missing families, then add public-interface regressions and
generated-runtime witnesses before implementing them. CI publishes the offline
report; it does not treat absence of mismatches as full compatibility.

Measured family boundaries include [series relations](series-table-evidence.md)
and [wildcard projections](wildcard-evidence.md). Their runtime witnesses are
separate from this command's execution stage, which remains not_run.
