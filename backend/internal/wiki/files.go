package wiki

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AGlElectronics/wiki/backend/internal/domain"
	"github.com/AGlElectronics/wiki/backend/internal/httperror"
)

// Upload limits, same as Stash.
const (
	MaxImageBytes = int64(25) << 20
	MaxFileBytes  = int64(50) << 20
)

// MaxUploadBytes is the size limit for one upload of the given content type.
func MaxUploadBytes(contentType string) int64 {
	if strings.HasPrefix(contentType, "image/") {
		return MaxImageBytes
	}
	return MaxFileBytes
}

// FilesPrefix is the S3 prefix of a page's uploads; the live store removes
// it when the page is deleted forever.
func FilesPrefix(page uuid.UUID) string { return "wiki/files/" + page.String() + "/" }

// FileObjects is the slice of object storage uploads need; *files.Client
// implements it.
type FileObjects interface {
	PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	GetObject(ctx context.Context, key string) (io.ReadCloser, error)
	Remove(ctx context.Context, key string) error
}

// File is one upload's metadata. URL is the backend download path; the
// bytes are only served after the page access check.
type File struct {
	ID          uuid.UUID `json:"id"`
	PageID      uuid.UUID `json:"page_id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	CreatedAt   time.Time `json:"created_at"`
	URL         string    `json:"url"`
}

// FileURL is the download path of a file (relative to the API base).
func FileURL(id uuid.UUID) string { return "/api/v1/files/" + id.String() }

// Files stores and serves page uploads. Access rides on the page: edit to
// upload, view to download, and a missing page (or no access) is a 404.
type Files struct {
	svc     *Service
	objects FileObjects
}

// NewFiles wires the uploads service.
func NewFiles(svc *Service, objects FileObjects) *Files {
	return &Files{svc: svc, objects: objects}
}

func cleanFilename(name string) string {
	name = strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	if len(name) > 200 {
		name = name[len(name)-200:]
	}
	return name
}

// Upload stores one file on a page the caller can edit.
func (f *Files) Upload(ctx context.Context, c Caller, pageID uuid.UUID, filename, contentType string, size int64, r io.Reader) (File, error) {
	p, err := f.svc.GetPage(ctx, c, pageID)
	if err != nil {
		return File{}, err
	}
	if err := need(p.Access, domain.LevelEdit); err != nil {
		return File{}, err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if size <= 0 {
		return File{}, httperror.Validation("invalid upload", map[string]string{"file": "empty"})
	}
	if limit := MaxUploadBytes(contentType); size > limit {
		return File{}, httperror.Validation("file too large", map[string]string{
			"file": fmt.Sprintf("at most %d MB", limit>>20)})
	}
	filename = cleanFilename(filename)
	id := uuid.New()
	ext := strings.ToLower(filepath.Ext(filename))
	if len(ext) > 12 || strings.ContainsAny(ext, "/\\%?#") {
		ext = ""
	}
	key := FilesPrefix(pageID) + id.String() + ext
	if err := f.objects.PutObject(ctx, key, io.LimitReader(r, size), size, contentType); err != nil {
		return File{}, err
	}
	out := File{ID: id, PageID: pageID, Filename: filename, ContentType: contentType, SizeBytes: size, URL: FileURL(id)}
	err = f.svc.pool.QueryRow(ctx, `
		INSERT INTO files (id, page_id, storage_key, filename, content_type, size_bytes, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at`,
		id, pageID, key, filename, contentType, size, c.UserID).Scan(&out.CreatedAt)
	if err != nil {
		_ = f.objects.Remove(ctx, key)
		return File{}, err
	}
	return out, nil
}

// Open returns a file's metadata and a reader of its bytes when the caller
// can view its page. An invisible page reads as a missing file.
func (f *Files) Open(ctx context.Context, c Caller, id uuid.UUID) (File, io.ReadCloser, error) {
	var out File
	var key string
	err := f.svc.pool.QueryRow(ctx, `
		SELECT id, page_id, storage_key, filename, content_type, size_bytes, created_at
		  FROM files WHERE id = $1`, id).
		Scan(&out.ID, &out.PageID, &key, &out.Filename, &out.ContentType, &out.SizeBytes, &out.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, nil, httperror.NotFound("file")
	}
	if err != nil {
		return File{}, nil, err
	}
	if _, err := f.svc.GetPage(ctx, c, out.PageID); err != nil {
		var he *httperror.Error
		if errors.As(err, &he) && he.Code == httperror.CodeNotFound {
			return File{}, nil, httperror.NotFound("file")
		}
		return File{}, nil, err
	}
	out.URL = FileURL(out.ID)
	rc, err := f.objects.GetObject(ctx, key)
	if err != nil {
		return File{}, nil, err
	}
	return out, rc, nil
}
