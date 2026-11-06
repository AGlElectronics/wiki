-- +goose Up
-- Page bodies are Yjs documents in S3. These tables only point at them:
-- the current snapshot, the update segments shipped since, and the
-- revisions kept for history. Sequence numbers count a page's updates.

CREATE TABLE page_snapshots (
    page_id     UUID PRIMARY KEY REFERENCES pages(id) ON DELETE CASCADE,
    storage_key TEXT NOT NULL,
    upto_seq    BIGINT NOT NULL,
    size_bytes  BIGINT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE page_segments (
    page_id     UUID NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    from_seq    BIGINT NOT NULL,
    to_seq      BIGINT NOT NULL,
    storage_key TEXT NOT NULL,
    size_bytes  BIGINT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (page_id, from_seq),
    CHECK (to_seq >= from_seq)
);

CREATE TABLE page_revisions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    page_id     UUID NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    storage_key TEXT NOT NULL,
    size_bytes  BIGINT NOT NULL,
    upto_seq    BIGINT NOT NULL,
    reason      TEXT NOT NULL DEFAULT 'auto' CHECK (reason IN ('auto', 'restore', 'manual')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX page_revisions_page_idx ON page_revisions (page_id, created_at DESC);

-- Plain text of each page, written by the browser that compacts it.
CREATE TABLE page_search (
    page_id    UUID PRIMARY KEY REFERENCES pages(id) ON DELETE CASCADE,
    body_text  TEXT NOT NULL DEFAULT '',
    tsv        TSVECTOR GENERATED ALWAYS AS (to_tsvector('simple', body_text)) STORED,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX page_search_tsv_idx ON page_search USING GIN (tsv);

CREATE TABLE page_links (
    from_page UUID NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    to_page   UUID NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    PRIMARY KEY (from_page, to_page)
);
CREATE INDEX page_links_to_idx ON page_links (to_page);

-- +goose Down
DROP TABLE page_links;
DROP TABLE page_search;
DROP TABLE page_revisions;
DROP TABLE page_segments;
DROP TABLE page_snapshots;
