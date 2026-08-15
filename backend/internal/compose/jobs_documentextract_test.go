// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The worker's own wiring, which has exactly one interesting property: it can
// reach the bytes.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
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
