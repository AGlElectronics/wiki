-- +goose Up
-- Offboarding (decision WIKI-13): when HR deletes someone's account it
-- calls POST /api/internal/offboarded; the wiki stamps their private
-- space. Wiki admins can then see and export it; a daily sweep deletes it
-- (content included) after WIKI_OFFBOARD_GRACE_DAYS.
ALTER TABLE spaces ADD COLUMN orphaned_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE spaces DROP COLUMN orphaned_at;
