CREATE TABLE IF NOT EXISTS persistence_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    store_id TEXT NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    version INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    cwd TEXT,
    parent_session TEXT,
    seed_length INTEGER,
    origin TEXT,
    delegation_depth INTEGER,
    agent_preset TEXT,
    incarnation TEXT NOT NULL,
    revision INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS events (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    type TEXT NOT NULL,
    time INTEGER NOT NULL,
    data TEXT NOT NULL,
    source_event_seqs TEXT,
    surface_op TEXT,
    ignorable INTEGER,
    PRIMARY KEY (session_id, seq)
) STRICT;
