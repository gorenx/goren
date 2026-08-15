-- name: GetStoreIdentity :one
SELECT store_id FROM persistence_state WHERE singleton = 1;

-- name: GetSession :one
SELECT id, version, created_at, cwd, parent_session, seed_length, origin,
       delegation_depth, agent_preset, incarnation, revision
FROM sessions
WHERE id = ?;

-- name: GetSessionRevision :one
SELECT incarnation, revision FROM sessions WHERE id = ?;

-- name: ListSessions :many
SELECT id, version, created_at, cwd, parent_session, seed_length, origin,
       delegation_depth, agent_preset, incarnation, revision
FROM sessions;

-- name: ListEvents :many
SELECT seq, type, time, data, source_event_seqs, surface_op, ignorable
FROM events
WHERE session_id = ?
ORDER BY seq;

-- name: ListEventsFrom :many
SELECT seq, type, time, data, source_event_seqs, surface_op, ignorable
FROM events
WHERE session_id = ? AND seq >= ?
ORDER BY seq;

-- name: InsertSession :exec
INSERT INTO sessions (
    id, version, created_at, cwd, parent_session, seed_length, origin,
    delegation_depth, agent_preset, incarnation, revision
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0);

-- name: InsertEvent :exec
INSERT INTO events (
    session_id, seq, type, time, data, source_event_seqs, surface_op, ignorable
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: IncrementRevision :execrows
UPDATE sessions SET revision = revision + 1 WHERE id = ?;

-- name: DeleteEventsFrom :exec
DELETE FROM events WHERE session_id = ? AND seq >= ?;
