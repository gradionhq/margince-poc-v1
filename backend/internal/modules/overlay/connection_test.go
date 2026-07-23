// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// cleanupOrphanedRef's own unit-level proof: it touches only s.vault,
// never the pool, so it is provable with an in-memory keyvault and no
// real Postgres — unlike the rest of connection.go's Service methods
// (connection_integration_test.go), which need real RLS-scoped rows.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestCleanupOrphanedRefDeletesAndAnswersAlreadyConnected(t *testing.T) {
	vault := keyvault.NewMemory()
	svc := NewService(nil, vault, NewMirrorStore(nil, noOwnerEmailsForUnitTest{}))

	ws := ids.NewV7()
	ref, err := vault.Put(context.Background(), ids.From[ids.WorkspaceKind](ws), []byte("pat-secret-token"))
	if err != nil {
		t.Fatalf("seeding a vault ref: %v", err)
	}

	// The concurrent-race cause is a UNIQUE(workspace_id) violation; cleanup
	// deletes the orphaned ref and maps that cause to ErrIncumbentAlreadyConnected.
	uniqueViolation := &pgconn.PgError{Code: "23505"}
	err = svc.cleanupOrphanedRef(context.Background(), ws, ref, uniqueViolation)
	if !errors.Is(err, apperrors.ErrIncumbentAlreadyConnected) {
		t.Fatalf("cleanupOrphanedRef err = %v, want errors.Is(_, ErrIncumbentAlreadyConnected)", err)
	}

	// The orphaned ref must actually be gone — Get resolving it now
	// answers not-found, proving the cleanup ran rather than being a
	// no-op that happens to return the right sentinel.
	if _, getErr := vault.Get(context.Background(), ids.From[ids.WorkspaceKind](ws), ref); !errors.Is(getErr, keyvault.ErrNotFound) {
		t.Fatalf("vault.Get after cleanup = %v, want ErrNotFound (the orphaned ref must be deleted)", getErr)
	}
}

// TestCleanupOrphanedRefSurfacesANonUniqueCause proves the other cleanup path:
// when the insert failed for a reason OTHER than the concurrent-race unique
// violation (a DB error, a cancelled context), cleanup still deletes the sealed
// ref but surfaces the original cause verbatim rather than the misleading
// "already connected".
func TestCleanupOrphanedRefSurfacesANonUniqueCause(t *testing.T) {
	vault := keyvault.NewMemory()
	svc := NewService(nil, vault, NewMirrorStore(nil, noOwnerEmailsForUnitTest{}))

	ws := ids.NewV7()
	ref, err := vault.Put(context.Background(), ids.From[ids.WorkspaceKind](ws), []byte("pat-secret-token"))
	if err != nil {
		t.Fatalf("seeding a vault ref: %v", err)
	}

	cause := errors.New("insert failed: context canceled")
	err = svc.cleanupOrphanedRef(context.Background(), ws, ref, cause)
	if !errors.Is(err, cause) {
		t.Fatalf("cleanupOrphanedRef err = %v, want the original cause surfaced", err)
	}
	if errors.Is(err, apperrors.ErrIncumbentAlreadyConnected) {
		t.Error("a non-unique cause must NOT be mapped to ErrIncumbentAlreadyConnected")
	}
	if _, getErr := vault.Get(context.Background(), ids.From[ids.WorkspaceKind](ws), ref); !errors.Is(getErr, keyvault.ErrNotFound) {
		t.Fatalf("vault.Get after cleanup = %v, want ErrNotFound (the ref must be deleted on every failure path)", getErr)
	}
}

// noOwnerEmailsForUnitTest is a minimal OwnerEmailResolver for
// constructing a MirrorStore in a unit test that never actually
// resolves an owner — distinct from testsupport_integration.go's
// noOwnerEmails, which lives behind the //go:build integration tag this
// file does not carry.
type noOwnerEmailsForUnitTest struct{}

func (noOwnerEmailsForUnitTest) OwnerEmail(_ context.Context, ownerExternalID string) (string, error) {
	return "", errors.New("test: no owner with external id " + ownerExternalID)
}
