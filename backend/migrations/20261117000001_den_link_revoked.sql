-- +goose Up
-- Shared denlink (con-services shared/go/denlink, SchemaUpgrade): Den can
-- reject the module credential; the link is then marked revoked until an
-- admin pairs again. Without this column every pairing query fails.
ALTER TABLE den_link ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE den_link DROP COLUMN IF EXISTS revoked_at;
