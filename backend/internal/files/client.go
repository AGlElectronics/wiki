// Package files wraps an S3-compatible object storage client. In local
// development this points at MinIO; in production it MUST point at a
// managed S3-compatible bucket.
//
// The adapter is deliberately provider-neutral; feature code never imports
// the minio-go types directly, only the Client surface defined here.
// Storage is optional: New returns a nil *Client when S3_ENDPOINT is empty,
// and callers treat nil as "uploads unavailable".
package files

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/AGlElectronics/wiki/backend/internal/config"
)

// Client wraps *minio.Client plus the configured bucket. Everything is
// served through the backend (uploads, downloads, page content); there are
// no presigned URLs, so page and file access always goes through the
// wiki's own access checks.
type Client struct {
	mc     *minio.Client
	bucket string
}

// New constructs a Client and ensures the configured bucket exists. It
// returns (nil, nil) when S3_ENDPOINT is not configured. The Wiki default
// bucket is `wiki-data-dev` for local dev — override in staging/prod with
// a managed bucket.
func New(ctx context.Context, cfg *config.Config) (*Client, error) {
	if !cfg.S3Enabled() {
		return nil, nil
	}
	endpointURL, err := url.Parse(cfg.S3Endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse S3_ENDPOINT: %w", err)
	}
	host := endpointURL.Host
	if host == "" {
		host = endpointURL.Path
	}
	useSSL := endpointURL.Scheme == "https"

	bucketLookup := minio.BucketLookupAuto
	if cfg.S3UsePathStyle {
		bucketLookup = minio.BucketLookupPath
	}

	mc, err := minio.New(host, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.S3AccessKeyID, cfg.S3SecretAccessKey, ""),
		Secure:       useSSL,
		Region:       cfg.S3Region,
		BucketLookup: bucketLookup,
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}

	exists, err := mc.BucketExists(ctx, cfg.S3Bucket)
	if err != nil {
		return nil, fmt.Errorf("check bucket: %w", err)
	}
	if !exists {
		if err := mc.MakeBucket(ctx, cfg.S3Bucket, minio.MakeBucketOptions{Region: cfg.S3Region}); err != nil {
			return nil, fmt.Errorf("create bucket: %w", err)
		}
	}

	return &Client{
		mc:     mc,
		bucket: cfg.S3Bucket,
	}, nil
}

// PutObject streams bytes into the bucket. Pass -1 as size if unknown; the
// SDK buffers in memory up to its default part size.
func (c *Client) PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := c.mc.PutObject(ctx, c.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	return nil
}

// GetObject opens a streaming reader for the object. Caller must Close. A
// Stat probe runs first so a missing key surfaces as an error here rather
// than mid-stream where it is hard to turn into an HTTP 404.
func (c *Client) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get object: %w", err)
	}
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		return nil, fmt.Errorf("stat object: %w", err)
	}
	return obj, nil
}

// Remove deletes an object. Called when a file metadata row is deleted.
func (c *Client) Remove(ctx context.Context, key string) error {
	return c.mc.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{})
}

// Put stores a small object from memory.
func (c *Client) Put(ctx context.Context, key string, data []byte, contentType string) error {
	return c.PutObject(ctx, key, bytes.NewReader(data), int64(len(data)), contentType)
}

// Get reads a whole object into memory.
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get object: %w", err)
	}
	defer obj.Close()
	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, fmt.Errorf("read object %s: %w", key, err)
	}
	return data, nil
}

// RemovePrefix deletes every object under prefix.
func (c *Client) RemovePrefix(ctx context.Context, prefix string) error {
	objects := c.mc.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true})
	for e := range c.mc.RemoveObjects(ctx, c.bucket, objects, minio.RemoveObjectsOptions{}) {
		if e.Err != nil {
			return fmt.Errorf("remove %s: %w", e.ObjectName, e.Err)
		}
	}
	return nil
}

// Bucket returns the configured bucket name (for health/log display).
func (c *Client) Bucket() string { return c.bucket }
