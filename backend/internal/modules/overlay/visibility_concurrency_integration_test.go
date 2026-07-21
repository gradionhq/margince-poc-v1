// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TestConcurrentRemapsLeaveExactlyOneOwnerVisible proves the visibility
// projection is race-free under concurrent remaps of the SAME user. Two
// goroutines remap Bob to two different owners at once; each owner owns one
// record, and both owners carry Bob's email so both remaps pass the
// email check. Whichever remap wins last, Bob must end up seeing EXACTLY
// ONE of the two records — never both (a stale grant the losing remap left
// behind) and never neither. Without the per-(workspace,user,incumbent)
// advisory serialization in UpsertUserMap and the FOR UPDATE row-lock in
// the visibility recompute, the two clear-then-grant recomputes interleave
// and can leave Bob granted on both owners' records. The assertion is on
// the invariant (exactly one visible), not on timing, so it never flakes —
// the same shape identity's TestConcurrentLastAdminDeactivationsKeepOneAdmin
// uses.
func TestConcurrentRemapsLeaveExactlyOneOwnerVisible(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	// Both owners resolve to Bob's email, so either remap passes the
	// UpsertUserMap email check — the honest concurrency scenario.
	store := NewMirrorStore(pool, stubOwnerEmails{
		"owner-a": "bob@example.com",
		"owner-b": "bob@example.com",
	})
	ctxBob, bobRaw := testWorkspaceCtxAsUser(t, ws, "bob@example.com")
	bob := ids.From[ids.UserKind](bobRaw)

	const objectClass = "contact"
	const recA, recB = "ext-owned-by-a", "ext-owned-by-b"
	for owner, external := range map[string]string{"owner-a": recA, "owner-b": recB} {
		if err := store.Ingest(ctx, Record{
			ObjectClass: objectClass, ExternalID: external,
			Fields: map[string]any{"firstname": "Rec"}, ModifiedAt: time.Now(), OwnerExternalID: owner,
		}); err != nil {
			t.Fatalf("ingesting the %s record: %v", owner, err)
		}
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, owner := range []string{"owner-a", "owner-b"} {
		wg.Add(1)
		go func(i int, owner string) {
			defer wg.Done()
			errs[i] = store.UpsertUserMap(ctx, bob, "hubspot", owner, "email")
		}(i, owner)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("a concurrent remap failed: %v", err)
		}
	}

	visibleA := recordVisible(ctxBob, t, store, objectClass, recA)
	visibleB := recordVisible(ctxBob, t, store, objectClass, recB)
	if visibleA == visibleB {
		t.Fatalf("after concurrent remaps Bob must see EXACTLY ONE record; saw recA=%v recB=%v (both or neither = a visibility race)", visibleA, visibleB)
	}
}

// recordVisible reports whether ctx's principal can read (objectClass,
// external) through the mirror store's visibility deny-join — true for a
// hit, false for the existence-hiding not-found, and a fatal for any other
// error (which would mean the probe itself is broken, not that the row is
// hidden).
func recordVisible(ctx context.Context, t *testing.T, store *MirrorStore, objectClass, external string) bool {
	t.Helper()
	_, err := store.Get(ctx, objectClass, external)
	switch {
	case err == nil:
		return true
	case errors.Is(err, apperrors.ErrNotFound):
		return false
	default:
		t.Fatalf("visibility probe for %s/%s errored: %v", objectClass, external, err)
		return false
	}
}
