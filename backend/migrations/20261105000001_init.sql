-- Wiki — initial schema: organizations, users keyed by the OIDC
-- (issuer, subject) pair, private and team spaces, the page tree with
-- trash, space and page grants, and the Den pairing row. Later migrations
-- add page bodies (Yjs snapshots and segments in S3), revisions, search,
-- files, comments, share links, templates, databases and offboarding.

-- +goose Up
-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS pgcrypto;
-- +goose StatementEnd

CREATE TABLE organizations (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug         TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (slug = lower(slug) AND slug ~ '^[a-z0-9][a-z0-9-]*[a-z0-9]$')
);

-- Roles are NOT stored here: they come from IdP groups via the access
-- config on every request. `groups` is the caller's IdP groups as of their
-- last request; it only feeds the group picker in the share dialog, never
-- authorization (that always uses the groups of the current token).
CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    issuer        TEXT NOT NULL,
    subject       TEXT NOT NULL,
    display_name  TEXT NOT NULL,
    email         TEXT,
    groups        TEXT[] NOT NULL DEFAULT '{}',
    last_login_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (issuer, subject)
);

CREATE INDEX users_email_lower_idx ON users (lower(email));

-- ----------------------------------------------------------------------------
-- SPACES
-- ----------------------------------------------------------------------------
-- A private space belongs to one person (created on their first visit).
-- A team space is shared through space_grants and, optionally, a level for
-- everyone signed in (everyone_level; NULL = closed).

CREATE TABLE spaces (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    kind            TEXT NOT NULL CHECK (kind IN ('private', 'team')),
    owner_id        UUID REFERENCES users(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    icon            TEXT NOT NULL DEFAULT '',
    description     TEXT NOT NULL DEFAULT '',
    everyone_level  TEXT CHECK (everyone_level IN ('view', 'comment', 'edit', 'full')),
    position        INTEGER NOT NULL DEFAULT 0,
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((kind = 'private') = (owner_id IS NOT NULL)),
    CHECK (kind = 'team' OR everyone_level IS NULL)
);

CREATE UNIQUE INDEX spaces_private_owner_idx ON spaces (owner_id) WHERE kind = 'private';

-- principal is a users.id (as text) for kind 'user', or an IdP group name.
CREATE TABLE space_grants (
    space_id       UUID NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
    principal_kind TEXT NOT NULL CHECK (principal_kind IN ('user', 'group')),
    principal      TEXT NOT NULL,
    level          TEXT NOT NULL CHECK (level IN ('view', 'comment', 'edit', 'full')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (space_id, principal_kind, principal)
);

-- ----------------------------------------------------------------------------
-- PAGES
-- ----------------------------------------------------------------------------
-- Pages inherit their parent's access (the space's for top-level pages). A
-- restricted page stops inheritance: only its own grants apply to it and
-- its children. Trash = deleted_at on the trashed page itself; its
-- children stay attached and come back with it.
-- position is a float so a drag writes one row: the new position is the
-- midpoint of its neighbours.

CREATE TABLE pages (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    space_id    UUID NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
    parent_id   UUID REFERENCES pages(id) ON DELETE CASCADE,
    position    DOUBLE PRECISION NOT NULL DEFAULT 0,
    title       TEXT NOT NULL DEFAULT '',
    icon        TEXT NOT NULL DEFAULT '',
    restricted  BOOLEAN NOT NULL DEFAULT FALSE,
    created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ,
    deleted_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    CHECK (parent_id IS NULL OR parent_id <> id)
);

CREATE INDEX pages_space_idx  ON pages (space_id, parent_id, position);
CREATE INDEX pages_parent_idx ON pages (parent_id);

CREATE TABLE page_grants (
    page_id        UUID NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    principal_kind TEXT NOT NULL CHECK (principal_kind IN ('user', 'group')),
    principal      TEXT NOT NULL,
    level          TEXT NOT NULL CHECK (level IN ('view', 'comment', 'edit', 'full')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (page_id, principal_kind, principal)
);

CREATE INDEX page_grants_principal_idx ON page_grants (principal_kind, principal);

-- ----------------------------------------------------------------------------
-- DEN PAIRING
-- ----------------------------------------------------------------------------

CREATE TABLE den_link (
    id            SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    den_base_url  TEXT NOT NULL,
    credential    TEXT NOT NULL,
    module_id     TEXT NOT NULL DEFAULT '',
    paired_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    synced_at     TIMESTAMPTZ,
    last_error    TEXT NOT NULL DEFAULT '',
    bundle        JSONB
);

INSERT INTO organizations (id, slug, display_name)
VALUES ('00000000-0000-0000-0000-000000000010', 'demo-org', 'Demo Organization')
ON CONFLICT (slug) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS den_link;
DROP TABLE IF EXISTS page_grants;
DROP TABLE IF EXISTS pages;
DROP TABLE IF EXISTS space_grants;
DROP TABLE IF EXISTS spaces;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS organizations;
