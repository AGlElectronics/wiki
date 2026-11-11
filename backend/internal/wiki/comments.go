package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AGlElectronics/wiki/backend/internal/domain"
	"github.com/AGlElectronics/wiki/backend/internal/httperror"
)

// Access rules mirror BlockNote's DefaultThreadStoreAuth so the UI and the
// server agree: comment level replies, resolves and reacts; authors edit
// their own comments; comment authors or editors delete comments; editors
// delete whole threads.
//
// Starting a thread: an anchored (inline) thread is tied to a comment mark
// the browser writes into the page's Yjs document, and the relay drops
// document updates from connections below edit (ADR 0001: the server never
// decodes Yjs, so it cannot let only the mark through). Anchored threads
// therefore need edit access; comment level starts page-level discussions,
// which have no anchor.

// Reaction is one emoji on a comment with everyone who sent it.
type Reaction struct {
	Emoji     string    `json:"emoji"`
	CreatedAt time.Time `json:"created_at"`
	UserIDs   []string  `json:"user_ids"`
}

// Comment is one message in a thread. A deleted comment keeps its row
// (deleted_at set, body null) so the thread keeps its shape.
type Comment struct {
	ID        uuid.UUID       `json:"id"`
	UserID    *uuid.UUID      `json:"user_id"`
	Body      json.RawMessage `json:"body"`
	Metadata  json.RawMessage `json:"metadata"`
	Reactions []Reaction      `json:"reactions"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	DeletedAt *time.Time      `json:"deleted_at"`
}

// Thread is one discussion on a page.
type Thread struct {
	ID     uuid.UUID `json:"id"`
	PageID uuid.UUID `json:"page_id"`
	// Anchored threads are tied to text in the page; the others are
	// page-level discussions.
	Anchored   bool            `json:"anchored"`
	Resolved   bool            `json:"resolved"`
	ResolvedBy *uuid.UUID      `json:"resolved_by"`
	ResolvedAt *time.Time      `json:"resolved_at"`
	Metadata   json.RawMessage `json:"metadata"`
	Comments   []Comment       `json:"comments"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// pageLevel is the caller's level on a page, as a 404 when below view.
func (s *Service) pageLevel(ctx context.Context, c Caller, pageID uuid.UUID) (domain.Level, error) {
	p, err := s.GetPage(ctx, c, pageID)
	if err != nil {
		return domain.LevelNone, err
	}
	return p.Access, nil
}

// threadPage finds the page a thread belongs to.
func (s *Service) threadPage(ctx context.Context, threadID uuid.UUID) (uuid.UUID, error) {
	var pageID uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT page_id FROM comment_threads WHERE id = $1`, threadID).Scan(&pageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, httperror.NotFound("thread")
	}
	return pageID, err
}

// commentRow is what comment-level checks need.
type commentRow struct {
	ThreadID  uuid.UUID
	PageID    uuid.UUID
	CreatedBy *uuid.UUID
	DeletedAt *time.Time
}

func (s *Service) commentRow(ctx context.Context, commentID uuid.UUID) (commentRow, error) {
	var row commentRow
	err := s.pool.QueryRow(ctx, `
		SELECT c.thread_id, t.page_id, c.created_by, c.deleted_at
		  FROM comments c JOIN comment_threads t ON t.id = c.thread_id
		 WHERE c.id = $1`, commentID).Scan(&row.ThreadID, &row.PageID, &row.CreatedBy, &row.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, httperror.NotFound("comment")
	}
	return row, err
}

// hide maps a page 404 onto the thing being asked for, so responses never
// confirm an invisible page exists.
func hide(err error, what string) error {
	var he *httperror.Error
	if errors.As(err, &he) && he.Code == httperror.CodeNotFound {
		return httperror.NotFound(what)
	}
	return err
}

var errBadBody = httperror.Validation("invalid comment", map[string]string{"body": "required"})

func validBody(body json.RawMessage) error {
	if len(body) == 0 || string(body) == "null" || len(body) > 64<<10 {
		return errBadBody
	}
	return nil
}

// Threads returns a page's discussions, oldest first (view access).
func (s *Service) Threads(ctx context.Context, c Caller, pageID uuid.UUID) ([]Thread, error) {
	if _, err := s.pageLevel(ctx, c, pageID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, page_id, anchored, resolved, resolved_by, resolved_at, metadata, created_at, updated_at
		  FROM comment_threads WHERE page_id = $1 ORDER BY created_at, id`, pageID)
	if err != nil {
		return nil, err
	}
	threads, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Thread, error) {
		var t Thread
		err := r.Scan(&t.ID, &t.PageID, &t.Anchored, &t.Resolved, &t.ResolvedBy, &t.ResolvedAt, &t.Metadata, &t.CreatedAt, &t.UpdatedAt)
		t.Comments = []Comment{}
		return t, err
	})
	if err != nil {
		return nil, err
	}
	if len(threads) == 0 {
		return []Thread{}, nil
	}
	byID := map[uuid.UUID]*Thread{}
	for i := range threads {
		byID[threads[i].ID] = &threads[i]
	}
	rows, err = s.pool.Query(ctx, `
		SELECT c.id, c.thread_id, c.created_by, c.body, c.metadata, c.created_at, c.updated_at, c.deleted_at
		  FROM comments c JOIN comment_threads t ON t.id = c.thread_id
		 WHERE t.page_id = $1 ORDER BY c.created_at, c.id`, pageID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var cm Comment
		var threadID uuid.UUID
		if err := rows.Scan(&cm.ID, &threadID, &cm.UserID, &cm.Body, &cm.Metadata, &cm.CreatedAt, &cm.UpdatedAt, &cm.DeletedAt); err != nil {
			rows.Close()
			return nil, err
		}
		cm.Reactions = []Reaction{}
		if t := byID[threadID]; t != nil {
			t.Comments = append(t.Comments, cm)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Index comments only once the slices are final (append reallocates).
	commentIndex := map[uuid.UUID]*Comment{}
	for i := range threads {
		for j := range threads[i].Comments {
			commentIndex[threads[i].Comments[j].ID] = &threads[i].Comments[j]
		}
	}
	rows, err = s.pool.Query(ctx, `
		SELECT r.comment_id, r.emoji, min(r.created_at), array_agg(r.user_id::text ORDER BY r.created_at)
		  FROM comment_reactions r
		  JOIN comments c ON c.id = r.comment_id
		  JOIN comment_threads t ON t.id = c.thread_id
		 WHERE t.page_id = $1
		 GROUP BY r.comment_id, r.emoji ORDER BY 3`, pageID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var commentID uuid.UUID
		var re Reaction
		if err := rows.Scan(&commentID, &re.Emoji, &re.CreatedAt, &re.UserIDs); err != nil {
			rows.Close()
			return nil, err
		}
		if cm := commentIndex[commentID]; cm != nil {
			cm.Reactions = append(cm.Reactions, re)
		}
	}
	rows.Close()
	return threads, rows.Err()
}

// thread reloads one thread with its comments.
func (s *Service) thread(ctx context.Context, c Caller, threadID uuid.UUID) (Thread, error) {
	pageID, err := s.threadPage(ctx, threadID)
	if err != nil {
		return Thread{}, err
	}
	threads, err := s.Threads(ctx, c, pageID)
	if err != nil {
		return Thread{}, hide(err, "thread")
	}
	for _, t := range threads {
		if t.ID == threadID {
			return t, nil
		}
	}
	return Thread{}, httperror.NotFound("thread")
}

// CreateThread starts a discussion with its first comment: anchored to
// text with edit access, page-level with comment access.
func (s *Service) CreateThread(ctx context.Context, c Caller, pageID uuid.UUID, body, metadata json.RawMessage, anchored bool) (Thread, error) {
	l, err := s.pageLevel(ctx, c, pageID)
	if err != nil {
		return Thread{}, err
	}
	want := domain.LevelComment
	if anchored {
		want = domain.LevelEdit
	}
	if err := need(l, want); err != nil {
		if anchored && l >= domain.LevelComment {
			return Thread{}, httperror.New(httperror.CodeForbidden, "comments on text need edit access; start a page discussion instead (anchored: false)")
		}
		return Thread{}, err
	}
	if err := validBody(body); err != nil {
		return Thread{}, err
	}
	if len(metadata) == 0 {
		metadata = json.RawMessage("null")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Thread{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var threadID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO comment_threads (page_id, created_by, metadata, anchored) VALUES ($1, $2, $3, $4) RETURNING id`,
		pageID, c.UserID, metadata, anchored).Scan(&threadID); err != nil {
		return Thread{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO comments (thread_id, created_by, body) VALUES ($1, $2, $3)`,
		threadID, c.UserID, body); err != nil {
		return Thread{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Thread{}, err
	}
	return s.thread(ctx, c, threadID)
}

// AddComment replies to a thread (comment access).
func (s *Service) AddComment(ctx context.Context, c Caller, threadID uuid.UUID, body json.RawMessage) (Thread, error) {
	pageID, err := s.threadPage(ctx, threadID)
	if err != nil {
		return Thread{}, err
	}
	l, err := s.pageLevel(ctx, c, pageID)
	if err != nil {
		return Thread{}, hide(err, "thread")
	}
	if err := need(l, domain.LevelComment); err != nil {
		return Thread{}, err
	}
	if err := validBody(body); err != nil {
		return Thread{}, err
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO comments (thread_id, created_by, body) VALUES ($1, $2, $3)`,
		threadID, c.UserID, body); err != nil {
		return Thread{}, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE comment_threads SET updated_at = now() WHERE id = $1`, threadID); err != nil {
		return Thread{}, err
	}
	return s.thread(ctx, c, threadID)
}

// UpdateComment edits a comment's body (author only).
func (s *Service) UpdateComment(ctx context.Context, c Caller, commentID uuid.UUID, body json.RawMessage) (Thread, error) {
	row, err := s.commentRow(ctx, commentID)
	if err != nil {
		return Thread{}, err
	}
	if _, err := s.pageLevel(ctx, c, row.PageID); err != nil {
		return Thread{}, hide(err, "comment")
	}
	if row.DeletedAt != nil {
		return Thread{}, httperror.NotFound("comment")
	}
	if row.CreatedBy == nil || *row.CreatedBy != c.UserID {
		return Thread{}, httperror.New(httperror.CodeForbidden, "only the author may edit a comment")
	}
	if err := validBody(body); err != nil {
		return Thread{}, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE comments SET body = $2, updated_at = now() WHERE id = $1`, commentID, body); err != nil {
		return Thread{}, err
	}
	return s.thread(ctx, c, row.ThreadID)
}

// DeleteComment soft-deletes a comment (author, or edit access). When the
// whole thread is deleted comments go with it.
func (s *Service) DeleteComment(ctx context.Context, c Caller, commentID uuid.UUID) (Thread, error) {
	row, err := s.commentRow(ctx, commentID)
	if err != nil {
		return Thread{}, err
	}
	l, err := s.pageLevel(ctx, c, row.PageID)
	if err != nil {
		return Thread{}, hide(err, "comment")
	}
	author := row.CreatedBy != nil && *row.CreatedBy == c.UserID
	if !author && l < domain.LevelEdit {
		return Thread{}, httperror.New(httperror.CodeForbidden, "needs edit access, or being the author")
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE comments SET body = NULL, deleted_at = now(), updated_at = now() WHERE id = $1`, commentID); err != nil {
		return Thread{}, err
	}
	return s.thread(ctx, c, row.ThreadID)
}

// DeleteThread removes a discussion and its comments (edit access).
func (s *Service) DeleteThread(ctx context.Context, c Caller, threadID uuid.UUID) error {
	pageID, err := s.threadPage(ctx, threadID)
	if err != nil {
		return err
	}
	l, err := s.pageLevel(ctx, c, pageID)
	if err != nil {
		return hide(err, "thread")
	}
	if err := need(l, domain.LevelEdit); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM comment_threads WHERE id = $1`, threadID)
	return err
}

// SetResolved resolves or reopens a thread (comment access).
func (s *Service) SetResolved(ctx context.Context, c Caller, threadID uuid.UUID, resolved bool) (Thread, error) {
	pageID, err := s.threadPage(ctx, threadID)
	if err != nil {
		return Thread{}, err
	}
	l, err := s.pageLevel(ctx, c, pageID)
	if err != nil {
		return Thread{}, hide(err, "thread")
	}
	if err := need(l, domain.LevelComment); err != nil {
		return Thread{}, err
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE comment_threads
		   SET resolved = $2,
		       resolved_by = CASE WHEN $2 THEN $3::uuid ELSE NULL END,
		       resolved_at = CASE WHEN $2 THEN now() ELSE NULL END,
		       updated_at = now()
		 WHERE id = $1`, threadID, resolved, c.UserID); err != nil {
		return Thread{}, err
	}
	return s.thread(ctx, c, threadID)
}

// SetReaction adds or removes one emoji by the caller (comment access).
func (s *Service) SetReaction(ctx context.Context, c Caller, commentID uuid.UUID, emoji string, add bool) (Thread, error) {
	if emoji == "" || len(emoji) > 32 {
		return Thread{}, httperror.Validation("invalid reaction", map[string]string{"emoji": "one emoji"})
	}
	row, err := s.commentRow(ctx, commentID)
	if err != nil {
		return Thread{}, err
	}
	l, err := s.pageLevel(ctx, c, row.PageID)
	if err != nil {
		return Thread{}, hide(err, "comment")
	}
	if err := need(l, domain.LevelComment); err != nil {
		return Thread{}, err
	}
	if add {
		_, err = s.pool.Exec(ctx, `
			INSERT INTO comment_reactions (comment_id, user_id, emoji) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, commentID, c.UserID, emoji)
	} else {
		_, err = s.pool.Exec(ctx, `
			DELETE FROM comment_reactions WHERE comment_id = $1 AND user_id = $2 AND emoji = $3`,
			commentID, c.UserID, emoji)
	}
	if err != nil {
		return Thread{}, err
	}
	return s.thread(ctx, c, row.ThreadID)
}
