-- +goose Up
-- Search gains the page title (weighted above body text) and favourites.
-- The title is maintained by the page service on create and rename; the
-- body text still comes from the browser that compacts the document.
ALTER TABLE page_search ADD COLUMN title TEXT NOT NULL DEFAULT '';
UPDATE page_search s SET title = p.title FROM pages p WHERE p.id = s.page_id;
ALTER TABLE page_search DROP COLUMN tsv;
ALTER TABLE page_search ADD COLUMN tsv TSVECTOR GENERATED ALWAYS AS
    (setweight(to_tsvector('simple', title), 'A') || to_tsvector('simple', body_text)) STORED;
CREATE INDEX page_search_tsv_idx ON page_search USING GIN (tsv);

-- Every page gets a search row. Before this migration a page only had one
-- after its first compaction.
INSERT INTO page_search (page_id, title)
SELECT id, title FROM pages
ON CONFLICT (page_id) DO NOTHING;

CREATE TABLE favorites (
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    page_id    UUID NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    position   DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, page_id)
);
CREATE INDEX favorites_page_idx ON favorites (page_id);

-- +goose Down
DROP TABLE favorites;
ALTER TABLE page_search DROP COLUMN tsv;
ALTER TABLE page_search DROP COLUMN title;
ALTER TABLE page_search ADD COLUMN tsv TSVECTOR GENERATED ALWAYS AS
    (to_tsvector('simple', body_text)) STORED;
CREATE INDEX page_search_tsv_idx ON page_search USING GIN (tsv);
