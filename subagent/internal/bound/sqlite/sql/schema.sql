CREATE TABLE IF NOT EXISTS bound_definitions (
    name            TEXT PRIMARY KEY,
    revision        INTEGER NOT NULL CHECK (revision > 0),
    definition_json BLOB NOT NULL CHECK (json_valid(definition_json))
) STRICT;
