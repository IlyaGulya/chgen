-- name: ReadPage :many
-- chgen:table Source events archived_events
WITH halves AS (
  SELECT id, chgen.assumeType(intDiv(id, toUInt64(2)), 'UInt64') AS half
  FROM chgen.table('Source')
)
SELECT id, half FROM halves WHERE 1
-- chgen:if Requested
AND id IN (SELECT id FROM chgen.external('Keys', requested_ids))
-- chgen:end
-- chgen:if After
AND half > chgen.arg('AfterHalf')
-- chgen:end
ORDER BY id LIMIT chgen.arg('PageRows');
