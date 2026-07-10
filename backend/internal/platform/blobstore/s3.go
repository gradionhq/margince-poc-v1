// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package blobstore

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Config is the S3-compatible store's wiring, populated from operator
// config in cmd. These are the store's OWN infrastructure credentials — not
// tenant secrets and never sourced from the keyvault (a cold boot must reach
// the store before any vault exists).
type Config struct {
	Endpoint  string // host:port, no scheme (e.g. "localhost:59000")
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string // default "us-east-1"
	UseSSL    bool   // false for local MinIO
}

// s3Store is the S3-compatible Store (MinIO in dev). It holds only bytes;
// isolation lives in the workspace-prefixed key the caller supplies.
type s3Store struct {
	client *minio.Client
	bucket string
}

// New builds an S3-compatible store and ensures its bucket exists. It
// tolerates a not-yet-ready backend (MinIO can still be starting when
// `make db-up` returns) with a bounded connect retry, so readiness is the
// store's responsibility, not the caller's.
//
//nolint:ireturn // the seam has two providers (memory + s3) behind one Store; returning the interface is the design.
func New(ctx context.Context, cfg Config) (Store, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, fmt.Errorf("blobstore: endpoint and bucket are required")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("blobstore: new client: %w", err)
	}
	s := &s3Store{client: client, bucket: cfg.Bucket}
	if err := s.ensureBucket(ctx, region); err != nil {
		return nil, err
	}
	return s, nil
}

// ensureBucket creates the bucket if it is absent, retrying while the
// backend comes up. The retry is bounded by ctx (or ~30s if ctx has no
// deadline) so a permanently-unreachable store fails the boot loudly.
func (s *s3Store) ensureBucket(ctx context.Context, region string) error {
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	var lastErr error
	for attempt := 0; ; attempt++ {
		exists, err := s.client.BucketExists(ctx, s.bucket)
		switch {
		case err == nil && exists:
			return nil
		case err == nil && !exists:
			mkErr := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: region})
			if mkErr == nil {
				return nil
			}
			// A concurrent creator winning the race is success, not failure.
			if existsErr, _ := s.client.BucketExists(ctx, s.bucket); existsErr {
				return nil
			}
			lastErr = fmt.Errorf("blobstore: create bucket %q: %w", s.bucket, mkErr)
		default:
			lastErr = fmt.Errorf("blobstore: reach store: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("blobstore: store not ready within deadline: %w", lastErr)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("blobstore: waiting for store: %w", ctx.Err())
		case <-time.After(backoff(attempt)):
		}
	}
}

// backoff grows from 200ms toward a 2s cap.
func backoff(attempt int) time.Duration {
	d := 200 * time.Millisecond * time.Duration(attempt+1)
	if d > 2*time.Second {
		return 2 * time.Second
	}
	return d
}

func (s *s3Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if _, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType}); err != nil {
		return fmt.Errorf("blobstore: put %q: %w", key, err)
	}
	return nil
}

func (s *s3Store) Get(ctx context.Context, key string) (io.ReadCloser, Object, error) {
	// Stat first so a missing object is ErrNotFound before we hand back a
	// lazy reader, and so the metadata is populated.
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return nil, Object{}, mapErr(key, err)
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, Object{}, fmt.Errorf("blobstore: get %q: %w", key, err)
	}
	return obj, objectInfoTo(key, info), nil
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	// RemoveObject is idempotent: removing an absent object is not an error,
	// so an erasure crash-retry is safe.
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("blobstore: delete %q: %w", key, err)
	}
	return nil
}

func (s *s3Store) Stat(ctx context.Context, key string) (Object, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return Object{}, mapErr(key, err)
	}
	return objectInfoTo(key, info), nil
}

func (s *s3Store) Health(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("blobstore: health: %w", err)
	}
	if !exists {
		return fmt.Errorf("blobstore: bucket %q missing", s.bucket)
	}
	return nil
}

// mapErr turns MinIO's not-found response into the package sentinel; every
// other error is wrapped without leaking bucket/endpoint internals to a
// client caller.
func mapErr(key string, err error) error {
	if minio.ToErrorResponse(err).Code == "NoSuchKey" {
		return fmt.Errorf("blobstore: %q: %w", key, ErrNotFound)
	}
	return fmt.Errorf("blobstore: stat %q: %w", key, err)
}

func objectInfoTo(key string, info minio.ObjectInfo) Object {
	return Object{Key: key, Size: info.Size, ContentType: info.ContentType}
}

var _ Store = (*s3Store)(nil)
