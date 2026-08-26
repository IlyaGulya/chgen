-- An expression wraps a placeholder, thus the native batch path is not
-- usable and the generator must emit conn.Exec. On that path a TYPED nil
-- pointer makes the driver panic, thus every pointer parameter must go
-- through chgenNullableParam, which turns it into an UNTYPED nil.
-- name: InsertAuditRowWithDefault :exec
INSERT INTO audit_log (id, actor, note, written_at)
VALUES (chgen.arg('ID'), upper(chgen.arg('Actor')), chgen.arg('Note'), now64(3));
