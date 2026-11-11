-- +goose Up
-- Group grants are stored in a normalised form and matched against the
-- token's groups normalised the same way: trimmed, one leading '/'
-- removed, lower-cased. Full paths stay full paths, so '/org/dept-x' never
-- matches a plain 'dept-x'. principal_display keeps the name as it was
-- typed, for the share dialog. The SQL below mirrors groupKey in
-- internal/wiki/access.go.

ALTER TABLE space_grants ADD COLUMN principal_display TEXT NOT NULL DEFAULT '';
ALTER TABLE page_grants  ADD COLUMN principal_display TEXT NOT NULL DEFAULT '';

-- Two grants that normalise to the same group (say 'Dept-X' and
-- '/dept-x') become one: the higher level wins, then the first name.
DELETE FROM space_grants g
 USING space_grants h
 WHERE g.principal_kind = 'group' AND h.principal_kind = 'group'
   AND g.space_id = h.space_id AND g.principal <> h.principal
   AND lower(regexp_replace(btrim(g.principal), '^/', '')) = lower(regexp_replace(btrim(h.principal), '^/', ''))
   AND (array_position(ARRAY['view','comment','edit','full'], g.level) < array_position(ARRAY['view','comment','edit','full'], h.level)
        OR (g.level = h.level AND g.principal COLLATE "C" > h.principal COLLATE "C"));

DELETE FROM page_grants g
 USING page_grants h
 WHERE g.principal_kind = 'group' AND h.principal_kind = 'group'
   AND g.page_id = h.page_id AND g.principal <> h.principal
   AND lower(regexp_replace(btrim(g.principal), '^/', '')) = lower(regexp_replace(btrim(h.principal), '^/', ''))
   AND (array_position(ARRAY['view','comment','edit','full'], g.level) < array_position(ARRAY['view','comment','edit','full'], h.level)
        OR (g.level = h.level AND g.principal COLLATE "C" > h.principal COLLATE "C"));

UPDATE space_grants
   SET principal_display = btrim(principal),
       principal = lower(regexp_replace(btrim(principal), '^/', ''))
 WHERE principal_kind = 'group';

UPDATE page_grants
   SET principal_display = btrim(principal),
       principal = lower(regexp_replace(btrim(principal), '^/', ''))
 WHERE principal_kind = 'group';

-- +goose Down
UPDATE space_grants SET principal = principal_display WHERE principal_kind = 'group' AND principal_display <> '';
UPDATE page_grants  SET principal = principal_display WHERE principal_kind = 'group' AND principal_display <> '';
ALTER TABLE page_grants  DROP COLUMN principal_display;
ALTER TABLE space_grants DROP COLUMN principal_display;
