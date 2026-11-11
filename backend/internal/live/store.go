package live

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/AGlElectronics/wiki/backend/internal/httperror"
	"github.com/AGlElectronics/wiki/backend/internal/wiki"
)

// Objects is the slice of object storage the live store needs;
// *files.Client implements it.
type Objects interface {
	Put(ctx context.Context, key string, data []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
	Remove(ctx context.Context, key string) error
	RemovePrefix(ctx context.Context, prefix string) error
}

// revisionEvery is the minimum time between two automatic revisions.
const revisionEvery = 10 * time.Minute

// Store persists page content: the disk buffer, S3 objects and their
// Postgres index.
type Store struct {
	pool    *pgxpool.Pool
	objects Objects
	dir     string
	log     zerolog.Logger
}

// NewStore creates the buffer directory if needed.
func NewStore(pool *pgxpool.Pool, objects Objects, dir string, log zerolog.Logger) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create buffer dir: %w", err)
	}
	return &Store{pool: pool, objects: objects, dir: dir, log: log}, nil
}

func pagePrefix(id uuid.UUID) string { return "wiki/pages/" + id.String() + "/" }

func currentKey(id uuid.UUID) string { return pagePrefix(id) + "current.ydoc" }

func segmentKey(id uuid.UUID, from, to int64) string {
	return fmt.Sprintf("%supdates/%012d-%012d.bin", pagePrefix(id), from, to)
}

func revisionKey(id, rev uuid.UUID) string {
	return pagePrefix(id) + "revisions/" + rev.String() + ".ydoc"
}

// Buffer files: <page>.log is being appended to; <page>.<n>.ship files
// were rotated out and wait to be uploaded.
func (s *Store) logPath(id uuid.UUID) string { return filepath.Join(s.dir, id.String()+".log") }

func (s *Store) shipFiles(id uuid.UUID) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(s.dir, id.String()+".*.ship"))
	sort.Strings(files)
	return files, err
}

// loaded is a page's document as stored: the snapshot and every update
// after it, in sequence order.
type loaded struct {
	snapshot []byte
	snapUpto int64
	records  []record
	segments int
}

func (l loaded) lastSeq() int64 {
	if n := len(l.records); n > 0 {
		return l.records[n-1].seq
	}
	return l.snapUpto
}

// load reads the snapshot and segments from S3 and anything still in the
// local buffer.
func (s *Store) load(ctx context.Context, id uuid.UUID) (loaded, error) {
	var out loaded
	var key string
	err := s.pool.QueryRow(ctx, `SELECT storage_key, upto_seq FROM page_snapshots WHERE page_id = $1`, id).Scan(&key, &out.snapUpto)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return out, err
	default:
		if out.snapshot, err = s.objects.Get(ctx, key); err != nil {
			return out, err
		}
	}

	rows, err := s.pool.Query(ctx, `
		SELECT storage_key FROM page_segments WHERE page_id = $1 AND to_seq > $2 ORDER BY from_seq`, id, out.snapUpto)
	if err != nil {
		return out, err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return out, err
	}
	bySeq := map[int64][]byte{}
	for _, k := range keys {
		data, err := s.objects.Get(ctx, k)
		if err != nil {
			return out, err
		}
		for _, r := range parseRecords(data) {
			bySeq[r.seq] = r.data
		}
	}
	out.segments = len(keys)

	local, err := s.shipFiles(id)
	if err != nil {
		return out, err
	}
	local = append(local, s.logPath(id))
	for _, f := range local {
		data, err := os.ReadFile(f)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return out, err
		}
		for _, r := range parseRecords(data) {
			bySeq[r.seq] = r.data
		}
	}

	for seq, data := range bySeq {
		if seq > out.snapUpto {
			out.records = append(out.records, record{seq: seq, data: data})
		}
	}
	sort.Slice(out.records, func(i, j int) bool { return out.records[i].seq < out.records[j].seq })
	return out, nil
}

// rotate moves the active buffer file aside for shipping.
func (s *Store) rotate(id uuid.UUID) error {
	err := os.Rename(s.logPath(id), filepath.Join(s.dir, fmt.Sprintf("%s.%d.ship", id, time.Now().UnixNano())))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ship uploads a page's rotated buffer files as S3 segments and deletes
// them. It returns the number of segments and bytes shipped.
func (s *Store) ship(ctx context.Context, id uuid.UUID) (segments int, shipped int64, err error) {
	files, err := s.shipFiles(id)
	if err != nil {
		return 0, 0, err
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return segments, shipped, err
		}
		recs := parseRecords(data)
		if len(recs) == 0 {
			_ = os.Remove(f)
			continue
		}
		from, to := recs[0].seq, recs[len(recs)-1].seq
		key := segmentKey(id, from, to)
		if err := s.objects.Put(ctx, key, data, "application/octet-stream"); err != nil {
			return segments, shipped, err
		}
		_, err = s.pool.Exec(ctx, `
			INSERT INTO page_segments (page_id, from_seq, to_seq, storage_key, size_bytes) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (page_id, from_seq) DO NOTHING`, id, from, to, key, len(data))
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			// The page was deleted forever meanwhile.
			_ = s.objects.Remove(ctx, key)
		} else if err != nil {
			return segments, shipped, err
		} else {
			segments++
		}
		if err := os.Remove(f); err != nil {
			return segments, shipped, err
		}
		shipped += int64(len(data))
	}
	return segments, shipped, nil
}

// recover ships every buffer file left from a previous run, before the
// server accepts connections.
func (s *Store) recover(ctx context.Context) error {
	logs, err := filepath.Glob(filepath.Join(s.dir, "*.log"))
	if err != nil {
		return err
	}
	pages := map[uuid.UUID]bool{}
	for _, f := range logs {
		if id, err := uuid.Parse(strings.TrimSuffix(filepath.Base(f), ".log")); err == nil {
			if err := s.rotate(id); err != nil {
				return err
			}
			pages[id] = true
		}
	}
	ships, err := filepath.Glob(filepath.Join(s.dir, "*.ship"))
	if err != nil {
		return err
	}
	for _, f := range ships {
		if id, err := uuid.Parse(strings.SplitN(filepath.Base(f), ".", 2)[0]); err == nil {
			pages[id] = true
		}
	}
	var errs []error
	for id := range pages {
		n, _, err := s.ship(ctx, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("page %s: %w", id, err))
			continue
		}
		s.log.Info().Str("page", id.String()).Int("segments", n).Msg("shipped buffered edits from the previous run")
	}
	return errors.Join(errs...)
}

// saveSnapshot stores a compacted document covering every update up to
// upto, keeps the previous one as a revision (at most one per
// revisionEvery), records the search text and links, and deletes the
// segments it covers. It returns how many segments remain.
func (s *Store) saveSnapshot(ctx context.Context, id uuid.UUID, prev []byte, prevUpto, upto int64, state []byte, h snapshotHeader) (int, error) {
	if prev != nil {
		var last *time.Time
		if err := s.pool.QueryRow(ctx, `SELECT max(created_at) FROM page_revisions WHERE page_id = $1`, id).Scan(&last); err != nil {
			return 0, err
		}
		if last == nil || time.Since(*last) >= revisionEvery {
			rev := uuid.New()
			if err := s.objects.Put(ctx, revisionKey(id, rev), prev, "application/octet-stream"); err != nil {
				return 0, err
			}
			if _, err := s.pool.Exec(ctx, `
				INSERT INTO page_revisions (id, page_id, storage_key, size_bytes, upto_seq) VALUES ($1, $2, $3, $4, $5)`,
				rev, id, revisionKey(id, rev), len(prev), prevUpto); err != nil {
				return 0, err
			}
		}
	}

	if err := s.objects.Put(ctx, currentKey(id), state, "application/octet-stream"); err != nil {
		return 0, err
	}

	links := []uuid.UUID{}
	for _, l := range h.Links {
		if u, err := uuid.Parse(l); err == nil && u != id {
			links = append(links, u)
		}
	}
	text := h.Text
	if len(text) > maxTextBytes {
		text = strings.ToValidUTF8(text[:maxTextBytes], "")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `
		INSERT INTO page_snapshots (page_id, storage_key, upto_seq, size_bytes) VALUES ($1, $2, $3, $4)
		ON CONFLICT (page_id) DO UPDATE
		   SET storage_key = EXCLUDED.storage_key, upto_seq = EXCLUDED.upto_seq,
		       size_bytes = EXCLUDED.size_bytes, updated_at = now()`,
		id, currentKey(id), upto, len(state)); err != nil {
		return 0, err
	}
	// The title column is maintained by the page service; only the body
	// text comes from the compacting browser.
	if _, err := tx.Exec(ctx, `
		INSERT INTO page_search (page_id, title, body_text)
		SELECT p.id, p.title, $2 FROM pages p WHERE p.id = $1
		ON CONFLICT (page_id) DO UPDATE SET body_text = EXCLUDED.body_text, updated_at = now()`, id, text); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM page_links WHERE from_page = $1`, id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO page_links (from_page, to_page) SELECT $1, p.id FROM pages p WHERE p.id = ANY($2)
		ON CONFLICT DO NOTHING`, id, links); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `DELETE FROM page_segments WHERE page_id = $1 AND to_seq <= $2 RETURNING storage_key`, id, upto)
	if err != nil {
		return 0, err
	}
	covered, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	var remaining int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM page_segments WHERE page_id = $1`, id).Scan(&remaining); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	for _, k := range covered {
		if err := s.objects.Remove(ctx, k); err != nil {
			s.log.Warn().Err(err).Str("key", k).Msg("could not remove compacted segment")
		}
	}
	return remaining, nil
}

// pruneBatch bounds one prune query; prune loops until a batch comes back
// short.
const pruneBatch = 500

// pruneRevisions deletes revisions created before cutoff: the S3 object
// first, then its row, so a failed delete leaves the row to retry next
// time instead of an object nobody points at. It returns how many went.
func (s *Store) pruneRevisions(ctx context.Context, cutoff time.Time) (int, error) {
	pruned := 0
	failed := map[uuid.UUID]bool{}
	for {
		rows, err := s.pool.Query(ctx, `
			SELECT id, storage_key FROM page_revisions
			 WHERE created_at < $1 AND NOT (id = ANY($2))
			 ORDER BY created_at LIMIT $3`, cutoff, mapKeys(failed), pruneBatch)
		if err != nil {
			return pruned, err
		}
		type rev struct {
			id  uuid.UUID
			key string
		}
		revs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (rev, error) {
			var v rev
			err := r.Scan(&v.id, &v.key)
			return v, err
		})
		if err != nil {
			return pruned, err
		}
		for _, v := range revs {
			if err := s.objects.Remove(ctx, v.key); err != nil {
				s.log.Warn().Err(err).Str("key", v.key).Msg("could not remove an expired revision; retrying later")
				failed[v.id] = true
				continue
			}
			if _, err := s.pool.Exec(ctx, `DELETE FROM page_revisions WHERE id = $1`, v.id); err != nil {
				return pruned, err
			}
			pruned++
		}
		if len(revs) < pruneBatch {
			return pruned, nil
		}
	}
}

func mapKeys(m map[uuid.UUID]bool) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// revisionMeta returns the page and storage key of a revision.
func (s *Store) revisionMeta(ctx context.Context, rev uuid.UUID) (page uuid.UUID, key string, err error) {
	err = s.pool.QueryRow(ctx, `SELECT page_id, storage_key FROM page_revisions WHERE id = $1`, rev).Scan(&page, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", httperror.NotFound("revision")
	}
	return page, key, err
}

// restore makes a revision the page's current document. The pre-restore
// snapshot is kept as a 'restore' revision. Update segments and buffered
// edits belong to the replaced timeline, so they are dropped, and the new
// snapshot's upto_seq jumps past every sequence number ever written: a
// stray old record can never be applied on a later load. The manager has
// already closed the page's hub, so nothing holds the buffer files open.
//
// Server-side limitation (the server cannot merge Yjs): the kept
// pre-restore revision is the last compacted snapshot; edits newer than it
// are not in the history.
func (s *Store) restore(ctx context.Context, page, rev uuid.UUID) error {
	var revKey string
	err := s.pool.QueryRow(ctx, `SELECT storage_key FROM page_revisions WHERE id = $1 AND page_id = $2`, rev, page).Scan(&revKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return httperror.NotFound("revision")
	}
	if err != nil {
		return err
	}
	restored, err := s.objects.Get(ctx, revKey)
	if err != nil {
		return err
	}

	// Keep the replaced state in the history.
	var curKey string
	var curUpto int64
	err = s.pool.QueryRow(ctx, `SELECT storage_key, upto_seq FROM page_snapshots WHERE page_id = $1`, page).Scan(&curKey, &curUpto)
	hasCurrent := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if hasCurrent {
		cur, err := s.objects.Get(ctx, curKey)
		if err != nil {
			return err
		}
		keep := uuid.New()
		if err := s.objects.Put(ctx, revisionKey(page, keep), cur, "application/octet-stream"); err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO page_revisions (id, page_id, storage_key, size_bytes, upto_seq, reason)
			VALUES ($1, $2, $3, $4, $5, 'restore')`, keep, page, revisionKey(page, keep), len(cur), curUpto); err != nil {
			return err
		}
	}

	// New timeline: start above everything ever written for this page.
	var maxSeq int64
	if err := s.pool.QueryRow(ctx, `
		SELECT GREATEST(COALESCE((SELECT max(to_seq) FROM page_segments WHERE page_id = $1), 0), $2)`,
		page, curUpto).Scan(&maxSeq); err != nil {
		return err
	}
	upto := maxSeq + 1

	if err := s.objects.Put(ctx, currentKey(page), restored, "application/octet-stream"); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `
		INSERT INTO page_snapshots (page_id, storage_key, upto_seq, size_bytes) VALUES ($1, $2, $3, $4)
		ON CONFLICT (page_id) DO UPDATE
		   SET storage_key = EXCLUDED.storage_key, upto_seq = EXCLUDED.upto_seq,
		       size_bytes = EXCLUDED.size_bytes, updated_at = now()`,
		page, currentKey(page), upto, len(restored)); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `DELETE FROM page_segments WHERE page_id = $1 RETURNING storage_key`, page)
	if err != nil {
		return err
	}
	dropped, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	files, _ := s.shipFiles(page)
	for _, f := range append(files, s.logPath(page)) {
		_ = os.Remove(f)
	}
	for _, k := range dropped {
		if err := s.objects.Remove(ctx, k); err != nil {
			s.log.Warn().Err(err).Str("key", k).Msg("could not remove a replaced segment")
		}
	}
	return nil
}

// copyContent duplicates a page's stored document (snapshot and segments,
// bytes only) under a new page id. The source keeps everything.
func (s *Store) copyContent(ctx context.Context, from, to uuid.UUID) error {
	var key string
	var upto, size int64
	err := s.pool.QueryRow(ctx, `SELECT storage_key, upto_seq, size_bytes FROM page_snapshots WHERE page_id = $1`, from).
		Scan(&key, &upto, &size)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Never compacted; segments may still exist.
	case err != nil:
		return err
	default:
		data, err := s.objects.Get(ctx, key)
		if err != nil {
			return err
		}
		if err := s.objects.Put(ctx, currentKey(to), data, "application/octet-stream"); err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO page_snapshots (page_id, storage_key, upto_seq, size_bytes) VALUES ($1, $2, $3, $4)`,
			to, currentKey(to), upto, size); err != nil {
			return err
		}
	}
	rows, err := s.pool.Query(ctx, `
		SELECT from_seq, to_seq, storage_key, size_bytes FROM page_segments WHERE page_id = $1 ORDER BY from_seq`, from)
	if err != nil {
		return err
	}
	type seg struct {
		from, to, size int64
		key            string
	}
	segs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (seg, error) {
		var sg seg
		err := r.Scan(&sg.from, &sg.to, &sg.key, &sg.size)
		return sg, err
	})
	if err != nil {
		return err
	}
	for _, sg := range segs {
		data, err := s.objects.Get(ctx, sg.key)
		if err != nil {
			return err
		}
		newKey := segmentKey(to, sg.from, sg.to)
		if err := s.objects.Put(ctx, newKey, data, "application/octet-stream"); err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO page_segments (page_id, from_seq, to_seq, storage_key, size_bytes) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (page_id, from_seq) DO NOTHING`, to, sg.from, sg.to, newKey, sg.size); err != nil {
			return err
		}
	}
	// The buffer (edits of the last few seconds) is not copied; the next
	// open of the source ships it, and the copy starts from what is stored.
	return nil
}

// forget drops everything stored for a page deleted forever: the buffer
// files, the page content under wiki/pages/, and its uploads under
// wiki/files/ (their metadata rows cascaded with the page).
func (s *Store) forget(ctx context.Context, id uuid.UUID) error {
	files, _ := s.shipFiles(id)
	for _, f := range append(files, s.logPath(id)) {
		_ = os.Remove(f)
	}
	if err := s.objects.RemovePrefix(ctx, wiki.FilesPrefix(id)); err != nil {
		return err
	}
	return s.objects.RemovePrefix(ctx, pagePrefix(id))
}
