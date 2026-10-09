-- name: FingerprintV2FactRows :many
SELECT fact_id, span_id, projected_at, normalized_id, display_id
FROM fact_ci_spans_v3
WHERE repository_key = chgen.arg('RepositoryKey');
