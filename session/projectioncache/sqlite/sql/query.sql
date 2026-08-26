-- name: ListCheckpoints :many
SELECT session_id, created_at, cwd, rows_json
FROM session_projection_checkpoints
ORDER BY session_id;

-- name: ReplaceCheckpoint :exec
INSERT INTO session_projection_checkpoints (
    session_id,
    created_at,
    cwd,
    rows_json
) VALUES (?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
    created_at = excluded.created_at,
    cwd = excluded.cwd,
    rows_json = excluded.rows_json;
