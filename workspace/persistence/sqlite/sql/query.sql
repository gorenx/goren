-- name: GetWorkspaceState :one
SELECT initialized, workspace_ids, archived_session_ids
FROM workspace_state
WHERE singleton = 1;

-- name: ListWorkspaces :many
SELECT id, path, title, session_ids, created_at, updated_at
FROM workspaces;

-- name: InsertWorkspace :exec
INSERT INTO workspaces (id, path, title, session_ids, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: UpdateWorkspace :execrows
UPDATE workspaces SET
    path = ?,
    title = ?,
    session_ids = ?,
    created_at = ?,
    updated_at = ?
WHERE id = ?;

-- name: DeleteWorkspace :execrows
DELETE FROM workspaces WHERE id = ?;

-- name: DeleteAllWorkspaces :exec
DELETE FROM workspaces;

-- name: PutWorkspaceState :exec
INSERT INTO workspace_state (singleton, initialized, workspace_ids, archived_session_ids)
VALUES (1, ?, ?, ?)
ON CONFLICT(singleton) DO UPDATE SET
    initialized = excluded.initialized,
    workspace_ids = excluded.workspace_ids,
    archived_session_ids = excluded.archived_session_ids;

-- name: SetWorkspaceOrder :execrows
UPDATE workspace_state SET workspace_ids = ? WHERE singleton = 1;

-- name: SetArchivedSessionIDs :execrows
UPDATE workspace_state SET archived_session_ids = ? WHERE singleton = 1;
