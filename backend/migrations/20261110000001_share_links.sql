-- +goose Up
-- Public share links: a page (optionally with its sub-pages) readable by
-- anyone who has the link, without an account. Only the SHA-256 of the
-- token is stored; the token itself is shown once, like Den's module
-- credentials. The feature is off unless Den's wiki_public_links setting
-- (or PUBLIC_LINKS_ENABLED standalone) switches it on.
CREATE TABLE page_share_links (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    page_id          UUID NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    token_hash       TEXT NOT NULL UNIQUE,
    include_children BOOLEAN NOT NULL DEFAULT FALSE,
    created_by       UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at       TIMESTAMPTZ,
    revoked_at       TIMESTAMPTZ,
    last_used_at     TIMESTAMPTZ
);
CREATE INDEX page_share_links_page_idx ON page_share_links (page_id);

-- +goose Down
DROP TABLE page_share_links;
