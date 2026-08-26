-- Every value is a bare placeholder, thus the generator can use the native
-- PrepareBatch/Append path, which is the fast path and never converts the
-- values to text.
-- name: InsertAuditRow :exec
INSERT INTO audit_log (id, actor, note, written_at)
VALUES (chgen.arg('ID'), chgen.arg('Actor'), chgen.arg('Note'), chgen.arg('WrittenAt'));

-- NOTE on the want.go of this case. The output does NOT declare
-- chgenNullableParam, although the Note parameter is a *string: this query
-- uses only the native batch path, which gives the pointer straight to
-- batch.Append and never wraps it. hasNullableParams() asks whether some
-- call site on the TEXT path wraps a parameter, thus a batch-only package
-- emits no helper. See insert_exec_text_nullable for the text path, which
-- does emit and call the helper.
