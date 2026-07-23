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

	// cleanupOrphanedRef deletes the orphaned ref (the lost-race path) and
	// answers ErrIncumbentAlreadyConnected.
	err = svc.cleanupOrphanedRef(context.Background(), ws, ref)
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

// noOwnerEmailsForUnitTest is a minimal OwnerEmailResolver for
// constructing a MirrorStore in a unit test that never actually
// resolves an owner — distinct from testsupport_integration.go's
// noOwnerEmails, which lives behind the //go:build integration tag this
// file does not carry.
type noOwnerEmailsForUnitTest struct{}

func (noOwnerEmailsForUnitTest) OwnerEmail(_ context.Context, ownerExternalID string) (string, error) {
	return "", errors.New("test: no owner with external id " + ownerExternalID)
}
