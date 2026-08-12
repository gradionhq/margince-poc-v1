// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package database

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A resolver that cannot answer must surface, not be swallowed into a
// transaction that then runs unbound. The installation is unresolvable in two
// real states — not bootstrapped yet, and more than one live workspace — and
// both must reach the caller saying which step failed.
func TestAResolverThatCannotAnswerStopsTheTransaction(t *testing.T) {
	t.Parallel()
	refused := errors.New("more than one active workspace")
	db := Bind(nil, func(context.Context) (ids.WorkspaceID, error) { return ids.WorkspaceID{}, refused })

	err := db.Tx(context.Background(), func(pgx.Tx) error {
		t.Fatal("the transaction body ran with no workspace resolved")
		return nil
	})
	if !errors.Is(err, refused) {
		t.Fatalf("Tx = %v, want the resolver's own error to reach the caller", err)
	}
	if !strings.Contains(err.Error(), "resolving the installation's workspace") {
		t.Errorf("the wrap must say which step failed, got %q", err)
	}
}

// A store built with no handle answers ErrNoWorkspace rather than panicking.
// The sentinel is load-bearing: callers distinguish "the gate denied me" from
// "the gate admitted me and the probe reached a database it could not bind" by
// exactly this error.
func TestAnUninjectedHandleAnswersTheNoWorkspaceSentinel(t *testing.T) {
	t.Parallel()
	var db *DB

	err := db.Tx(context.Background(), func(pgx.Tx) error { return nil })
	if !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("Tx on a nil handle = %v, want ErrNoWorkspace", err)
	}
	if !strings.Contains(err.Error(), "compose") {
		t.Errorf("the refusal must name where the handle comes from, got %q", err)
	}
}

// BindTo pins the workspace it is given — the shape bootstrap and every
// multi-tenant fixture rely on, where no installation exists for a resolver to
// find or several do.
func TestBindToPinsTheWorkspaceItIsGiven(t *testing.T) {
	t.Parallel()
	ws := ids.From[ids.WorkspaceKind](ids.NewV7())

	got, err := BindTo(nil, ws).Workspace(context.Background())
	if err != nil {
		t.Fatalf("resolving a pinned handle: %v", err)
	}
	if got != ws {
		t.Errorf("pinned workspace = %s, want %s", got, ws)
	}
}
