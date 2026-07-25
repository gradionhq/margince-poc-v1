// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The unit-level proofs for connection.go's two vault-cleanup paths —
// cleanupOrphanedRef (a lost connect race) and deleteUnreferencedRef (the
// post-commit delete disconnect and reconnect share). Both touch only
// s.vault, never the pool, so both are provable with an in-memory keyvault
// and no real Postgres — unlike the rest of connection.go's Service methods
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

// cancelAwareVault is the in-memory keyvault plus the one behaviour a map
// does not have and every real vault backend does: it refuses work whose
// context is already cancelled. Without it, a cleanup handed the caller's
// cancelled context would still delete the blob and the test below would
// pass while the defect it guards remains — the map never looks at ctx.
type cancelAwareVault struct{ keyvault.Vault }

func (v cancelAwareVault) Delete(ctx context.Context, ws ids.WorkspaceID, ref keyvault.Ref) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return v.Vault.Delete(ctx, ws, ref)
}

func TestDeleteUnreferencedRefOutlivesTheCancelledRequestThatTriggeredIt(t *testing.T) {
	vault := cancelAwareVault{keyvault.NewMemory()}
	svc := NewService(nil, vault, NewMirrorStore(nil, noOwnerEmailsForUnitTest{}))

	ws := ids.NewV7()
	ref, err := vault.Put(context.Background(), ids.From[ids.WorkspaceKind](ws), []byte("pat-secret-token"))
	if err != nil {
		t.Fatalf("seeding a vault ref: %v", err)
	}

	// The client hung up the instant it had its response: the disconnect (or
	// reconnect) transaction has committed, and the request context is already
	// cancelled by the time the post-commit cleanup runs. That cleanup is the
	// only chance this blob ever gets — no retry can re-reach it, because a
	// second disconnect finds no active connection.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	svc.deleteUnreferencedRef(ctx, ws, ref, "disconnect")

	if _, getErr := vault.Get(context.Background(), ids.From[ids.WorkspaceKind](ws), ref); !errors.Is(getErr, keyvault.ErrNotFound) {
		t.Fatalf("vault.Get after cleanup = %v, want ErrNotFound (a cancelled request must not strand the unreferenced credential)", getErr)
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
