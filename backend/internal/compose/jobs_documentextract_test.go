// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The worker's own wiring, which has exactly one interesting property: it can
// reach the bytes.

import (
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// A worker assembled without the object store fails EVERY document reading in
// the installation with "this installation stores no document bytes" — a true
// sentence about the store and a false one about the installation, since the
// bytes are there and the role that was meant to read them was built without a
// handle to them. It is invisible in every unit test that stops at the store,
// and it reads to an operator as a configuration they have not got wrong.
func TestTheDocumentWorkerCanReachTheBytes(t *testing.T) {
	worker := newDocumentExtractWorker(nil, nil, blobstore.NewMemory(), discardLogger())
	store, ok := worker.activities.(*activities.Store)
	if !ok {
		t.Fatalf("worker store is %T, want the activities store", worker.activities)
	}
	if !store.HasBlobstore() {
		t.Fatal("the document worker was assembled with an object store and cannot reach it")
	}
}

// A role genuinely without one still says so honestly rather than pretending.
func TestADocumentWorkerWithNoObjectStoreSaysSo(t *testing.T) {
	worker := newDocumentExtractWorker(nil, nil, nil, discardLogger())
	store, ok := worker.activities.(*activities.Store)
	if !ok {
		t.Fatalf("worker store is %T, want the activities store", worker.activities)
	}
	if store.HasBlobstore() {
		t.Fatal("a worker built with no object store reports one")
	}
}

// A reading queued on an installation with no model lane FAILS visibly. Leaving
// it queued is the failure mode: to the rep watching the panel, a row nobody
// will ever pick up is indistinguishable from one still being worked.
func TestAReadingWithNoModelLaneFailsWithAReasonRatherThanWaiting(t *testing.T) {
	store := &fakeReadStore{claimedAt: time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)}
	worker := &documentExtractWorker{activities: store, log: discardLogger()}

	if err := worker.declineUnread(t.Context(), DocumentExtractArgs{}); err != nil {
		t.Fatalf("declineUnread: %v", err)
	}
	if store.outcome == nil || store.outcome.Status != activities.ExtractionReadFailed {
		t.Fatalf("outcome = %+v, want failed", store.outcome)
	}
	if !strings.Contains(store.outcome.Detail, "no AI model configured") {
		t.Errorf("detail = %q, want the reason an operator can act on", store.outcome.Detail)
	}
	// Under this worker's own claim, like every other write.
	if !store.outcome.ClaimedAt.Equal(store.claimedAt) {
		t.Errorf("closed under claim %v, want %v", store.outcome.ClaimedAt, store.claimedAt)
	}
}

// A reading somebody else already holds is not this worker's to decline.
func TestDecliningAReadingAnotherWorkerHoldsWritesNothing(t *testing.T) {
	store := &fakeReadStore{beginErr: apperrors.ErrConflict}
	worker := &documentExtractWorker{activities: store, log: discardLogger()}

	if err := worker.declineUnread(t.Context(), DocumentExtractArgs{}); err != nil {
		t.Fatalf("declineUnread: %v", err)
	}
	if store.finishes != 0 {
		t.Error("a reading held by another worker was closed anyway")
	}
}

// The reading is attributed to the reader, on behalf of the human who asked —
// not to the transcript reader next door, which would tell a rep something that
// never ran had read their invoice.
func TestADocumentReadingIsAttributedToItsOwnAgent(t *testing.T) {
	requester := ids.NewV7()
	// The requester rides as the namespaced principal id the row stores, and only
	// a HUMAN namespace can be a human owner — a system one naming a uuid would
	// attribute the reading to somebody who did not ask for it.
	ctx := withDocumentReader(t.Context(), "human:"+requester.String(), ids.NewV7())

	actor, ok := principal.Actor(ctx)
	if !ok {
		t.Fatal("no acting principal was bound")
	}
	if actor.ID != documentExtractActor {
		t.Errorf("acting agent = %q, want %q", actor.ID, documentExtractActor)
	}
	if actor.OnBehalfOf != requester {
		t.Errorf("on behalf of %v, want the human who asked (%v)", actor.OnBehalfOf, requester)
	}
}
