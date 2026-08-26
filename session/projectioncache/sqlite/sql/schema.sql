CREATE TABLE IF NOT EXISTS session_projection_checkpoints (
    session_id TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL,
    cwd        TEXT,
    rows_json  BLOB NOT NULL CHECK (json_valid(rows_json))
) STRICT;
