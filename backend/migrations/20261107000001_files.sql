-- +goose Up
-- Uploads (images and attachments) referenced from page blocks. The bytes
-- live in S3 under wiki/files/<page_id>/<file_id><ext>; this table is the
-- metadata and the access path (file -> page -> page access). Deleting a
-- page forever cascades here; the S3 prefix is removed by the live store.
CREATE TABLE files (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    page_id      UUID NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    storage_key  TEXT NOT NULL,
    filename     TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size_bytes   BIGINT NOT NULL,
    uploaded_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX files_page_idx ON files (page_id);

-- +goose Down
DROP TABLE files;
