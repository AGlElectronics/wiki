-- +goose Up
-- Pages deleted forever still have content in S3 (the document, history
-- and uploads). Deleting the rows and queueing the page here happen in one
-- transaction; the row is removed once the S3 objects are gone, and the
-- backend retries what is left. So a crash between the two never leaves
-- S3 objects nobody owns. No foreign key: the page row is already gone.
CREATE TABLE content_deletions (
    page_id    UUID PRIMARY KEY,
    queued_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts   INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE content_deletions;
