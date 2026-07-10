// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package blobstore owns object-bytes I/O — the durable store behind the
// object keys the schema already commits to (attachment.storage_key,
// organization.logo_object_key). It is a peer of platform/events and
// platform/jobs: technical plumbing that owns no domain. The DB row stays
// the system of record and the tenant anchor; the store holds only opaque
// bytes at a workspace-prefixed key. See decisions/0022-blobstore-seam.md.
package blobstore

import (
	"context"
	"errors"
	"io"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ErrNotFound reports that no object exists at the given key. Callers
// errors.Is against it (a missing object on Delete is not an error; a
// missing object on Get is ErrNotFound).
var ErrNotFound = errors.New("blobstore: object not found")

// Store is the object-bytes seam. Keys are opaque to the store and are
// derived by the caller through WorkspaceKey so that tenant isolation is a
// property of the key, never of the store.
type Store interface {
	// Put writes size bytes read from r at key, recording contentType.
	// An existing object at key is overwritten.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error

	// Get opens the object at key for reading; the caller closes the
	// reader. Returns ErrNotFound if no object exists at key.
	Get(ctx context.Context, key string) (io.ReadCloser, Object, error)

	// Delete removes the object at key. It is idempotent: deleting a key
	// with no object is not an error, so a crash-retry is safe.
	Delete(ctx context.Context, key string) error

	// Stat returns the object's metadata without its bytes; ErrNotFound if
	// absent.
	Stat(ctx context.Context, key string) (Object, error)
}

// Object is the stored bytes' metadata.
type Object struct {
	Key         string
	Size        int64
	ContentType string
}

// WorkspaceKey derives the storage key for one entity's object. The key is
// prefixed by the workspace id so a tenant physically cannot address
// another tenant's object; kind is the entity discriminator (e.g.
// "attachment") and id its identifier.
func WorkspaceKey(ws ids.WorkspaceID, kind, id string) string {
	return ws.String() + "/" + kind + "/" + id
}
