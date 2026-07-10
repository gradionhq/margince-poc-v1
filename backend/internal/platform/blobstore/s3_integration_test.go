// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package blobstore_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// newS3Store builds a real MinIO-backed store from the MARGINCE_TEST_BLOBSTORE_*
// contract. Like every integration test here, it fails loudly without its
// dependency — it never skips (a skipped blobstore test looks exactly like a
// passing one).
func newS3Store(t *testing.T) blobstore.Store {
	t.Helper()
	endpoint := os.Getenv("MARGINCE_TEST_BLOBSTORE_ENDPOINT")
	if endpoint == "" {
		t.Fatal("MARGINCE_TEST_BLOBSTORE_ENDPOINT not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	store, err := blobstore.New(t.Context(), blobstore.Config{
		Endpoint:  endpoint,
		AccessKey: os.Getenv("MARGINCE_TEST_BLOBSTORE_ACCESS_KEY"),
		SecretKey: os.Getenv("MARGINCE_TEST_BLOBSTORE_SECRET_KEY"),
		Bucket:    os.Getenv("MARGINCE_TEST_BLOBSTORE_BUCKET"),
	})
	if err != nil {
		t.Fatalf("blobstore.New: %v", err)
	}
	return store
}

func TestS3StoreRoundTrip(t *testing.T) {
	store := newS3Store(t)
	ctx := t.Context()
	// A fresh workspace id keeps this test's keys clear of every other
	// test sharing the bucket.
	key := blobstore.WorkspaceKey(ids.New[ids.WorkspaceKind](), "attachment", "roundtrip")
	body := []byte("s3 round trip payload")

	if err := store.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "application/octet-stream"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Delete(context.Background(), key); err != nil {
			t.Errorf("cleanup Delete: %v", err)
		}
	})

	r, obj, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(r)
	if cerr := r.Close(); cerr != nil {
		t.Errorf("Close: %v", cerr)
	}
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("bytes = %q, want %q", got, body)
	}
	if obj.Size != int64(len(body)) {
		t.Errorf("Object.Size = %d, want %d", obj.Size, len(body))
	}
	if obj.ContentType != "application/octet-stream" {
		t.Errorf("Object.ContentType = %q, want application/octet-stream", obj.ContentType)
	}

	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := store.Get(ctx, key); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("Get after Delete: err = %v, want ErrNotFound", err)
	}
}

func TestS3StoreGetMissingReturnsNotFound(t *testing.T) {
	store := newS3Store(t)
	key := blobstore.WorkspaceKey(ids.New[ids.WorkspaceKind](), "attachment", "absent")

	_, _, err := store.Get(t.Context(), key)
	if !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("Get on a missing key: err = %v, want ErrNotFound", err)
	}
}

func TestS3StoreDeleteIsIdempotent(t *testing.T) {
	store := newS3Store(t)
	key := blobstore.WorkspaceKey(ids.New[ids.WorkspaceKind](), "attachment", "never-written")

	// Removing an object that was never written is a no-op, so a crash-retry
	// of an erasure is safe.
	if err := store.Delete(t.Context(), key); err != nil {
		t.Fatalf("Delete on a missing key: %v", err)
	}
}

func TestS3StoreHealthReadyAgainstLiveMinIO(t *testing.T) {
	if err := newS3Store(t).Health(t.Context()); err != nil {
		t.Fatalf("Health against live MinIO: %v", err)
	}
}

func TestS3StoreWorkspaceKeyIsolation(t *testing.T) {
	store := newS3Store(t)
	ctx := t.Context()
	// The same logical entity id under two workspaces must not collide:
	// putting under A's key leaves B's key empty.
	wsA := ids.New[ids.WorkspaceKind]()
	wsB := ids.New[ids.WorkspaceKind]()
	keyA := blobstore.WorkspaceKey(wsA, "attachment", "shared-id")
	keyB := blobstore.WorkspaceKey(wsB, "attachment", "shared-id")

	if err := store.Put(ctx, keyA, bytes.NewReader([]byte("A's bytes")), 9, ""); err != nil {
		t.Fatalf("Put A: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Delete(context.Background(), keyA); err != nil {
			t.Errorf("cleanup Delete: %v", err)
		}
	})

	if _, _, err := store.Get(ctx, keyB); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("workspace B key resolved after only A was written: err = %v, want ErrNotFound", err)
	}
}
