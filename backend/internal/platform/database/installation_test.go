// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package database

// The process-wide installation binding (ADR-0091 §9 step 3): which workspace
// a transaction binds when the context carries none.
//
// Both directions matter and they pull against each other. Before boot has
// resolved an installation there is nothing to guess at, so a query must fail
// loudly — the worker polls exactly that state until the API bootstraps. After
// boot, a context with no workspace binds the installation rather than failing,
// which is what lets a worker loop stop carrying a workspace it could only ever
// have set to the same value.
//
// No database is needed to prove either: the decision happens before any SQL
// runs, which is the point of resolving it there.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// bindForTest sets the process-wide installation and restores whatever was
// there afterwards, so these cases cannot leak into each other or into the
// integration lane running in the same binary.
func bindForTest(t *testing.T, wsID *ids.UUID) {
	t.Helper()
	previous := installation.Load()
	installation.Store(wsID)
	t.Cleanup(func() { installation.Store(previous) })
}

func TestATransactionWithNoWorkspaceFailsBeforeBootstrap(t *testing.T) {
	bindForTest(t, nil)

	_, err := resolveWorkspace(context.Background())
	if !errors.Is(err, ErrNoWorkspace) {
		t.Errorf("err = %v, want ErrNoWorkspace — a query before bootstrap must not guess an installation", err)
	}
}

func TestAContextWithNoWorkspaceBindsTheInstallationAfterBoot(t *testing.T) {
	bound := ids.NewV7()
	bindForTest(t, &bound)

	got, err := resolveWorkspace(context.Background())
	if err != nil {
		t.Fatalf("a context with no workspace failed after boot bound one: %v", err)
	}
	if got != bound {
		t.Errorf("resolved %v, want the bound installation %v", got, bound)
	}
}

func TestAContextsOwnWorkspaceWinsOverTheInstallation(t *testing.T) {
	bound := ids.NewV7()
	bindForTest(t, &bound)

	// The fallback is a fallback. A context that names a workspace binds THAT
	// one — the property every cross-tenant isolation test depends on, and the
	// reason this step can land while RLS is still armed.
	other := ids.NewV7()
	got, err := resolveWorkspace(principal.WithWorkspaceID(context.Background(), other))
	if err != nil {
		t.Fatal(err)
	}
	if got != other {
		t.Errorf("resolved %v, want the context's own workspace %v — the fallback must not override it", got, other)
	}
}
