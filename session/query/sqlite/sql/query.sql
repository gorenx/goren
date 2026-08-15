-- name: GetIndexGeneration :one
SELECT generation FROM index_state WHERE singleton = 1;

-- name: IncrementIndexGeneration :exec
UPDATE index_state SET generation = generation + 1 WHERE singleton = 1;

-- name: ListIndexedSessions :many
SELECT id, version, created_at, cwd, parent_session, seed_length, origin,
       delegation_depth, agent_preset, live, persisted, source_revision, generation
FROM indexed_sessions;

-- name: GetIndexedSession :one
SELECT id, version, created_at, cwd, parent_session, seed_length, origin,
       delegation_depth, agent_preset, live, persisted, source_revision, generation
FROM indexed_sessions
WHERE id = ?;

-- name: UpsertIndexedSession :exec
INSERT INTO indexed_sessions (
    id, version, created_at, cwd, parent_session, seed_length, origin,
    delegation_depth, agent_preset, live, persisted, source_revision, generation
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    version = excluded.version,
    created_at = excluded.created_at,
    cwd = excluded.cwd,
    parent_session = excluded.parent_session,
    seed_length = excluded.seed_length,
    origin = excluded.origin,
    delegation_depth = excluded.delegation_depth,
    agent_preset = excluded.agent_preset,
    live = excluded.live,
    persisted = excluded.persisted,
    source_revision = excluded.source_revision,
    generation = excluded.generation;

-- name: DeleteIndexedSession :exec
DELETE FROM indexed_sessions WHERE id = ?;

-- name: DeleteIndexedDocuments :exec
DELETE FROM indexed_documents WHERE session_id = ?;

-- name: InsertIndexedDocument :exec
INSERT INTO indexed_documents (text, session_id, seq, type, time, surface)
VALUES (?, ?, ?, ?, ?, ?);
