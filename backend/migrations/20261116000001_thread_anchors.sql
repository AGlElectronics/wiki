-- +goose Up
-- A thread is either anchored to text (a comment mark in the page's Yjs
-- document, written by the browser) or a page-level discussion with no
-- anchor. Anchoring changes the document, which needs edit access; the
-- server cannot check a mark without decoding Yjs (ADR 0001), so
-- comment-level people start page-level discussions only. Every thread
-- before this migration came from the editor and is anchored.
ALTER TABLE comment_threads ADD COLUMN anchored BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose Down
ALTER TABLE comment_threads DROP COLUMN anchored;
