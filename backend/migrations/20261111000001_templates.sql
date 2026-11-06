-- +goose Up
-- Templates are ordinary pages flagged as such; "new from template"
-- duplicates the page server-side (snapshot and segments copied as bytes,
-- never decoded).
ALTER TABLE pages ADD COLUMN is_template BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE pages DROP COLUMN is_template;
