CREATE TABLE IF NOT EXISTS index_state (
    singleton  INTEGER PRIMARY KEY CHECK (singleton = 1),
    generation INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS indexed_sessions (
    id                TEXT PRIMARY KEY,
    version           INTEGER NOT NULL,
    created_at        INTEGER NOT NULL,
    cwd               TEXT,
    parent_session    TEXT,
    seed_length       INTEGER,
    origin            TEXT NOT NULL,
    delegation_depth  INTEGER,
    agent_preset      TEXT,
    live              INTEGER NOT NULL CHECK (live IN (0, 1)),
    persisted         INTEGER NOT NULL CHECK (persisted IN (0, 1)),
    source_revision   TEXT NOT NULL,
    generation        INTEGER NOT NULL
) STRICT;

CREATE VIRTUAL TABLE IF NOT EXISTS indexed_documents USING fts5(
    text,
    session_id UNINDEXED,
    seq UNINDEXED,
    type UNINDEXED,
    time UNINDEXED,
    surface UNINDEXED,
    tokenize = 'unicode61'
);

INSERT OR IGNORE INTO index_state (singleton, generation) VALUES (1, 0);
