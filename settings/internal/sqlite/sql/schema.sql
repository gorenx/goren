CREATE TABLE IF NOT EXISTS settings_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    revision INTEGER NOT NULL CHECK (revision >= 0),
    schema_version INTEGER NOT NULL CHECK (schema_version = 1)
) STRICT;

CREATE TABLE IF NOT EXISTS settings_sections (
    namespace TEXT PRIMARY KEY,
    revision INTEGER NOT NULL CHECK (revision >= 0),
    overrides_json TEXT NOT NULL
) STRICT;
