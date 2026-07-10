// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package keyvault_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The memory fake carries the Vault contract for hermetic unit tests; the
// same behaviours are proven against the local provider on real Postgres in
// the integration lane. Every property here is one the local provider must
// honour identically.

func TestMemoryVault_roundTrip(t *testing.T) {
	v := keyvault.NewMemory()
	ctx := context.Background()
	ws := ids.New[ids.WorkspaceKind]()
	secret := []byte("imap-password-and-cursor-bundle")

	ref, err := v.Put(ctx, ws, secret)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref == "" {
		t.Fatal("Put returned an empty ref")
	}

	got, err := v.Get(ctx, ws, ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("Get returned %q, want %q", got, secret)
	}
}

// A ref is safe to persist in a domain row and to log — it must never carry
// the plaintext it addresses.
func TestMemoryVault_refDoesNotLeakTheSecret(t *testing.T) {
	v := keyvault.NewMemory()
	ctx := context.Background()
	ws := ids.New[ids.WorkspaceKind]()
	secret := []byte("super-secret-value-xyzzy")

	ref, err := v.Put(ctx, ws, secret)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if strings.Contains(string(ref), "xyzzy") {
		t.Fatalf("ref %q contains the plaintext secret", ref)
	}
}

// The load-bearing isolation property: a ref minted under one workspace does
// not resolve under another — a stolen ref is inert across the tenant edge.
func TestMemoryVault_refDoesNotResolveUnderAnotherWorkspace(t *testing.T) {
	v := keyvault.NewMemory()
	ctx := context.Background()
	wsA := ids.New[ids.WorkspaceKind]()
	wsB := ids.New[ids.WorkspaceKind]()

	ref, err := v.Put(ctx, wsA, []byte("tenant-a-secret"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if _, err := v.Get(ctx, wsB, ref); !errors.Is(err, keyvault.ErrNotFound) {
		t.Fatalf("Get under the wrong workspace: got %v, want ErrNotFound", err)
	}
}

func TestMemoryVault_getMissingIsNotFound(t *testing.T) {
	v := keyvault.NewMemory()
	ctx := context.Background()
	ws := ids.New[ids.WorkspaceKind]()

	ref, err := v.Put(ctx, ws, []byte("value"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := v.Delete(ctx, ws, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := v.Get(ctx, ws, ref); !errors.Is(err, keyvault.ErrNotFound) {
		t.Fatalf("Get after Delete: got %v, want ErrNotFound", err)
	}
}

// Delete is idempotent so an erasure crash-retry is safe: deleting an absent
// ref, or the same ref twice, is not an error.
func TestMemoryVault_deleteIsIdempotent(t *testing.T) {
	v := keyvault.NewMemory()
	ctx := context.Background()
	ws := ids.New[ids.WorkspaceKind]()

	ref, err := v.Put(ctx, ws, []byte("value"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := v.Delete(ctx, ws, ref); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	if err := v.Delete(ctx, ws, ref); err != nil {
		t.Fatalf("second Delete (idempotent): %v", err)
	}
}

// A zero workspace id is a wiring bug, never a legitimate tenant — Put must
// refuse it loudly rather than mint an unscoped ref.
func TestMemoryVault_putRejectsZeroWorkspace(t *testing.T) {
	v := keyvault.NewMemory()
	if _, err := v.Put(context.Background(), ids.WorkspaceID{}, []byte("value")); err == nil {
		t.Fatal("Put with a zero workspace id must return an error")
	}
}
