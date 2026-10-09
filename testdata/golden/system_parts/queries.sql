-- name: ReadFactTableChangeSignature :one
-- result: Signature signature
SELECT toUInt64(sum(cityHash64(partition_id, max_block, mutation))) AS signature
FROM (
    SELECT
        partition_id,
        max(max_block_number) AS max_block,
        max(if(data_version > max_block_number, data_version, 0)) AS mutation
    FROM system.parts
    WHERE database = currentDatabase() AND table = chgen.arg('Table') AND active
    GROUP BY partition_id
)
SETTINGS log_comment = 'serving_read_change_signature';
