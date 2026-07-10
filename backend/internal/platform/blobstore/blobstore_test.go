// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package blobstore_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestMemoryStorePutGetRoundTrip(t *testing.T) {
	store := blobstore.NewMemory()
	ctx := t.Context()
	body := []byte("the quick brown fox")

	if err := store.Put(ctx, "ws/attachment/a", bytes.NewReader(body), int64(len(body)), "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	r, obj, err := store.Get(ctx, "ws/attachment/a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() {
		if cerr := r.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	}()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("bytes = %q, want %q", got, body)
	}
	if obj.Size != int64(len(body)) {
		t.Errorf("Object.Size = %d, want %d", obj.Size, len(body))
	}
	if obj.ContentType != "text/plain" {
		t.Errorf("Object.ContentType = %q, want text/plain", obj.ContentType)
	}
}

func TestMemoryStoreGetMissingReturnsNotFound(t *testing.T) {
	store := blobstore.NewMemory()

	_, _, err := store.Get(t.Context(), "ws/attachment/absent")
	if !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("Get on a missing key: err = %v, want ErrNotFound", err)
	}
}

func TestMemoryStoreDeleteIsIdempotent(t *testing.T) {
	store := blobstore.NewMemory()
	ctx := t.Context()

	// Deleting a key that was never written is not an error.
	if err := store.Delete(ctx, "ws/attachment/absent"); err != nil {
		t.Fatalf("Delete on a missing key: %v", err)
	}

	if err := store.Put(ctx, "ws/attachment/a", bytes.NewReader([]byte("x")), 1, ""); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Delete(ctx, "ws/attachment/a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := store.Get(ctx, "ws/attachment/a"); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("Get after Delete: err = %v, want ErrNotFound", err)
	}
	// A second Delete of the now-gone key is still a no-op (crash-retry safety).
	if err := store.Delete(ctx, "ws/attachment/a"); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
}

func TestMemoryStoreGetReturnsAnIndependentCopy(t *testing.T) {
	store := blobstore.NewMemory()
	ctx := t.Context()
	if err := store.Put(ctx, "k", bytes.NewReader([]byte("original")), 8, ""); err != nil {
		t.Fatalf("Put: %v", err)
	}

	r, _, err := store.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(r)
	_ = r.Close()
	// Mutating the returned slice must not corrupt the stored object.
	for i := range got {
		got[i] = 'X'
	}

	r2, _, err := store.Get(ctx, "k")
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	got2, _ := io.ReadAll(r2)
	_ = r2.Close()
	if string(got2) != "original" {
		t.Errorf("stored object was mutated through a returned reader: got %q", got2)
	}
}

func TestWorkspaceKeyIsolatesWorkspaces(t *testing.T) {
	a := ids.New[ids.WorkspaceKind]()
	b := ids.New[ids.WorkspaceKind]()

	keyA := blobstore.WorkspaceKey(a, "attachment", "same-id")
	keyB := blobstore.WorkspaceKey(b, "attachment", "same-id")

	if keyA == keyB {
		t.Fatalf("keys for distinct workspaces collided: %q", keyA)
	}
	// The key is prefixed by the workspace so one tenant cannot address
	// another tenant's object.
	wantPrefix := a.String() + "/"
	if got := keyA[:len(wantPrefix)]; got != wantPrefix {
		t.Errorf("WorkspaceKey prefix = %q, want %q", got, wantPrefix)
	}
}
