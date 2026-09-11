-- name: InitializeState :exec
INSERT INTO settings_state (singleton, revision, schema_version)
VALUES (1, 0, 1) ON CONFLICT DO NOTHING;

-- name: ReadState :one
SELECT revision, schema_version FROM settings_state WHERE singleton = 1;

-- name: ReadSections :many
SELECT namespace, revision, overrides_json FROM settings_sections ORDER BY namespace;

-- name: CompareRevision :execrows
UPDATE settings_state SET revision = sqlc.arg(next_revision)
WHERE singleton = 1 AND revision = sqlc.arg(expected_revision);

-- name: WriteSection :exec
INSERT INTO settings_sections (namespace, revision, overrides_json) VALUES (?, ?, ?)
ON CONFLICT (namespace) DO UPDATE SET revision = excluded.revision, overrides_json = excluded.overrides_json;
