// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package keyvault

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The ref helpers and the local provider's construction/validation are
// unit-tested here (white-box) — the defensive branches the DB round-trip in
// the integration lane never exercises.

func TestRefParse_rejectsMalformed(t *testing.T) {
	ws := ids.New[ids.WorkspaceKind]()
	good, err := mintRef(ws)
	if err != nil {
		t.Fatalf("mintRef: %v", err)
	}

	malformed := map[string]Ref{
		"empty":               "",
		"wrong scheme":        Ref("xxx.1." + ws.String() + ".tok"),
		"too few parts":       Ref("mgv.1." + ws.String()),
		"too many parts":      Ref("mgv.1." + ws.String() + ".tok.extra"),
		"non-numeric version": Ref("mgv.x." + ws.String() + ".tok"),
		"zero version":        Ref("mgv.0." + ws.String() + ".tok"),
		"bad workspace":       "mgv.1.not-a-uuid.tok",
		"empty token":         Ref("mgv.1." + ws.String() + "."),
	}
	for name, ref := range malformed {
		if _, err := ref.parse(); err == nil {
			t.Errorf("%s: parse(%q) returned no error, want malformed", name, ref)
		}
		if ref.scopedTo(ws) {
			t.Errorf("%s: a malformed ref must not be scoped to any workspace", name)
		}
	}

	// The well-formed ref parses and is scoped to its own workspace only.
	p, err := good.parse()
	if err != nil {
		t.Fatalf("parse(good): %v", err)
	}
	if p.workspace != ws || p.keyVersion != currentKeyVersion || p.token == "" {
		t.Fatalf("parse(good) = %+v, want ws=%s version=%d non-empty token", p, ws, currentKeyVersion)
	}
	if !good.scopedTo(ws) {
		t.Fatal("a well-formed ref must be scoped to its own workspace")
	}
	if good.scopedTo(ids.New[ids.WorkspaceKind]()) {
		t.Fatal("a ref must not be scoped to a different workspace")
	}
}

func TestRefLogSafe(t *testing.T) {
	ws := ids.New[ids.WorkspaceKind]()
	ref, err := mintRef(ws)
	if err != nil {
		t.Fatalf("mintRef: %v", err)
	}
	safe := refLogSafe(ref)
	if !strings.Contains(safe, ws.String()) {
		t.Errorf("refLogSafe(%q) = %q, want it to name the workspace", ref, safe)
	}
	if !strings.Contains(safe, "<token>") {
		t.Errorf("refLogSafe(%q) = %q, want the token masked", ref, safe)
	}
	// The unguessable token — the capability part of the handle — must not appear.
	p, _ := ref.parse()
	if strings.Contains(safe, p.token) {
		t.Errorf("refLogSafe leaked the token: %q", safe)
	}
	if got := refLogSafe("not-a-ref"); got != "<malformed-ref>" {
		t.Errorf("refLogSafe(malformed) = %q, want <malformed-ref>", got)
	}
}

func TestNew_validatesConfig(t *testing.T) {
	// A valid key but no pool is a wiring error surfaced at construction.
	if _, err := New(Config{RootKey: testKey(t), Pool: nil}); err == nil {
		t.Fatal("New with a nil pool must error")
	}
	// A short key is rejected before the pool is even considered.
	if _, err := New(Config{RootKey: make([]byte, 16), Pool: nil}); err == nil {
		t.Fatal("New with a 16-byte key must error")
	}
}

func TestMemoryVault_healthAndForeignDeleteAreNoops(t *testing.T) {
	v := NewMemory()
	ctx := context.Background()
	if err := v.Health(ctx); err != nil {
		t.Fatalf("memory Health must succeed: %v", err)
	}
	// Deleting a ref minted for another workspace addresses nothing here — a
	// no-op, never an error (idempotent).
	foreign, err := mintRef(ids.New[ids.WorkspaceKind]())
	if err != nil {
		t.Fatalf("mintRef: %v", err)
	}
	if err := v.Delete(ctx, ids.New[ids.WorkspaceKind](), foreign); err != nil {
		t.Fatalf("deleting a foreign ref must be a no-op: %v", err)
	}
}

// The local provider's fail-fast guards return before touching the database,
// so they are exercised here with a nil pool: a zero-workspace Put, a
// foreign-ref Delete (no-op), and a malformed/foreign-ref Get (ErrNotFound)
// must never reach a query.
func TestLocalVault_guardsReturnBeforeTheDB(t *testing.T) {
	aead, err := newAEAD(testKey(t))
	if err != nil {
		t.Fatalf("newAEAD: %v", err)
	}
	v := &localVault{aead: aead, pool: nil}
	ctx := context.Background()

	if _, err := v.Put(ctx, ids.WorkspaceID{}, []byte("x")); err == nil {
		t.Fatal("Put with a zero workspace must error before any query")
	}
	foreign, err := mintRef(ids.New[ids.WorkspaceKind]())
	if err != nil {
		t.Fatalf("mintRef: %v", err)
	}
	if err := v.Delete(ctx, ids.New[ids.WorkspaceKind](), foreign); err != nil {
		t.Fatalf("Delete of a foreign ref must be a no-op before any query: %v", err)
	}
	if _, err := v.Get(ctx, ids.New[ids.WorkspaceKind](), "garbage"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get of a malformed ref: got %v, want ErrNotFound before any query", err)
	}
}
