-- name: ListDefinitions :many
SELECT name, revision, definition_json
FROM bound_definitions
ORDER BY name;

-- name: InsertDefinition :execrows
INSERT INTO bound_definitions (
    name,
    revision,
    definition_json
) VALUES (?, ?, ?)
ON CONFLICT(name) DO NOTHING;

-- name: ReplaceDefinition :execrows
UPDATE bound_definitions
SET
    revision = ?,
    definition_json = ?
WHERE name = ? AND revision = ?;

-- name: DefinitionRevision :one
SELECT revision
FROM bound_definitions
WHERE name = ?;
