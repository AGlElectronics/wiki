-- +goose Up
-- Notion-style databases (docs/adr/0002-databases-as-pages.md). A database
-- is a page with kind 'database'; every row is an ordinary child page, so
-- access, trash, sharing and live editing work unchanged. Properties and
-- views belong to the database page; values belong to the row pages.
ALTER TABLE pages ADD COLUMN kind TEXT NOT NULL DEFAULT 'page' CHECK (kind IN ('page', 'database'));

CREATE TABLE database_properties (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    database_id UUID NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    type        TEXT NOT NULL CHECK (type IN ('text', 'number', 'select', 'multi_select', 'date', 'person', 'checkbox', 'url')),
    -- options: {"choices": ["Todo", "Doing", "Done"]} for selects.
    options     JSONB NOT NULL DEFAULT 'null',
    position    DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX database_properties_db_idx ON database_properties (database_id, position);

CREATE TABLE page_property_values (
    page_id     UUID NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    property_id UUID NOT NULL REFERENCES database_properties(id) ON DELETE CASCADE,
    value       JSONB NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (page_id, property_id)
);
CREATE INDEX page_property_values_prop_idx ON page_property_values (property_id);

CREATE TABLE database_views (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    database_id UUID NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('table', 'board', 'calendar')),
    -- config: {"filter":[{"property":"<id>","equals":<json>}],
    --          "sort":[{"property":"<id>","desc":false}],
    --          "group_by":"<id>", "date_property":"<id>"}
    config      JSONB NOT NULL DEFAULT '{}',
    position    DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX database_views_db_idx ON database_views (database_id, position);

-- +goose Down
DROP TABLE database_views;
DROP TABLE page_property_values;
DROP TABLE database_properties;
ALTER TABLE pages DROP COLUMN kind;
