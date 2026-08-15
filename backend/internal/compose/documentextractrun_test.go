// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// One reading, from claiming the run record to closing it — the file two
// reviews found three defects in, none of which any test could see because the
// orchestration had none of its own.
//
// The store is a double rather than the real one on purpose: what is under test
// is which outcome a reading reaches and which claim it writes under, and a real
// Postgres would only add a way for those assertions to fail for another reason.
// The store's OWN behaviour (the CAS, the lease, the in-flight index) is proven
// against a real database in the integration lane.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// fakeReadStore records what a reading did to the run record.
type fakeReadStore struct {
	claimedAt time.Time
	beginErr  error
	openErr   error
	meta      crmcontracts.Attachment
	body      string

	outcome  *activities.ExtractionReadOutcome
	released bool
	finishes int
}

func (f *fakeReadStore) BeginExtractionRead(
	context.Context, ids.UUID, time.Duration,
) (activities.ExtractionRead, error) {
	if f.beginErr != nil {
		return activities.ExtractionRead{}, f.beginErr
	}
	at := f.claimedAt
	return activities.ExtractionRead{Status: activities.ExtractionReadRunning, StartedAt: &at}, nil
}

func (f *fakeReadStore) FinishExtractionRead(
	_ context.Context, _ ids.UUID, outcome activities.ExtractionReadOutcome,
) error {
	f.finishes++
	f.outcome = &outcome
	return nil
}

func (f *fakeReadStore) ReleaseExtractionRead(context.Context, ids.UUID, time.Time) error {
	f.released = true
	return nil
}

func (f *fakeReadStore) OpenAttachment(
	context.Context, ids.UUID,
) (crmcontracts.Attachment, io.ReadCloser, error) {
	if f.openErr != nil {
		return crmcontracts.Attachment{}, nil, f.openErr
	}
	return f.meta, io.NopCloser(strings.NewReader(f.body)), nil
}

func textAttachment(mime string) crmcontracts.Attachment {
	return crmcontracts.Attachment{Filename: "order-form.txt", ContentType: &mime}
}

// runReading drives one reading and answers what the store recorded.
func runReading(t *testing.T, store *fakeReadStore, brain documentCompleter) error {
	t.Helper()
	if store.claimedAt.IsZero() {
		store.claimedAt = time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	}
	d := NewDocumentExtractor(nil, brain, discardLogger())
	return d.Read(t.Context(), store, ids.NewV7(), ids.NewV7())
}

// scriptedBrain answers with one reply and declares the carriage it is given.
type scriptedBrain struct {
	*ai.FakeClient
	carriage []string
}

func (s scriptedBrain) AttachmentMIMEs() []string { return s.carriage }

func groundedDocumentReply() string {
	return allFour(
		statedField("name", "Order form", "ORDER FORM"),
		statedField(modelFieldAmount, "148500.00", "Contract value: EUR 148,500.00"),
		statedField("currency", "EUR", "Contract value: EUR 148,500.00"),
		"")
}

// The ordinary case: a text document, four fields asked for, three grounded.
func TestAReadingOfATextDocumentStoresWhatItGrounded(t *testing.T) {
	store := &fakeReadStore{meta: textAttachment("text/plain"), body: uatDocument}
	brain := scriptedBrain{ai.NewFakeClient().Script(groundedDocumentReply()), ai.DocumentMIMEs()}

	if err := runReading(t, store, brain); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if store.outcome == nil || store.outcome.Status != activities.ExtractionReadDone {
		t.Fatalf("outcome = %+v, want done", store.outcome)
	}
	if store.outcome.Detail != "" {
		t.Errorf("a reading that grounded fields explained itself anyway: %q", store.outcome.Detail)
	}
	grounded := 0
	for _, f := range store.outcome.Fields {
		if !f.Omitted {
			grounded++
		}
	}
	if grounded != 3 {
		t.Errorf("grounded %d fields, want 3", grounded)
	}
	// Every write is scoped to the claim this attempt holds.
	if !store.outcome.ClaimedAt.Equal(store.claimedAt) {
		t.Errorf("finished under claim %v, want %v", store.outcome.ClaimedAt, store.claimedAt)
	}
}

// Read it, and it states none of them. A CORRECT answer that must explain
// itself, or it cannot be told from a broken one.
func TestADocumentStatingNoneOfThemIsDoneWithAReason(t *testing.T) {
	store := &fakeReadStore{meta: textAttachment("text/plain"), body: uatDocument}
	brain := scriptedBrain{ai.NewFakeClient().Script(allFour()), ai.DocumentMIMEs()}

	if err := runReading(t, store, brain); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if store.outcome.Status != activities.ExtractionReadDone {
		t.Fatalf("status = %q, want done — a document that states none of them is not a failure", store.outcome.Status)
	}
	if store.outcome.Detail == "" {
		t.Error("an empty reading did not say why, and reads as a broken feature")
	}
}

// COULD NOT read it is a different answer, and `done` is what the panel renders
// as "it states none of them" — so an unreadable file must not reach it.
func TestADocumentThisBindingCannotCarryFailsRatherThanReadingEmpty(t *testing.T) {
	// A zip is neither text this build can read nor a type any adapter carries.
	// image/tiff would NOT do here: it matches the `image/*` declaration, so it
	// takes the bytes lane and the vendor is the one that refuses it.
	store := &fakeReadStore{meta: textAttachment("application/zip"), body: "PK\x03\x04"}
	brain := scriptedBrain{ai.NewFakeClient(), ai.DocumentMIMEs()}

	if err := runReading(t, store, brain); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if store.outcome.Status != activities.ExtractionReadFailed {
		t.Fatalf("status = %q, want failed — it was never read", store.outcome.Status)
	}
	if !strings.Contains(store.outcome.Detail, "application/zip") {
		t.Errorf("detail = %q, want it to name the type nothing can read", store.outcome.Detail)
	}
	// And no model call was made: the lane is chosen from the declaration.
	if calls := brain.Calls(); len(calls) != 0 {
		t.Errorf("made %d model call(s) for a type the binding cannot carry, want 0", len(calls))
	}
}

// A binding that carries the type gets the BYTES, not the text.
func TestACarriedDocumentGoesAsAnInputPart(t *testing.T) {
	store := &fakeReadStore{meta: textAttachment("application/pdf"), body: "%PDF-1.4 body"}
	brain := scriptedBrain{ai.NewFakeClient().Script(allFour()), ai.DocumentMIMEs()}

	if err := runReading(t, store, brain); err != nil {
		t.Fatalf("Read: %v", err)
	}
	calls := brain.Calls()
	if len(calls) == 0 {
		t.Fatal("no model call was made for a carried document")
	}
	if !strings.Contains(string(calls[0].Payload), "application/pdf") {
		t.Errorf("the document did not ride as an input part:\n%s", calls[0].Payload)
	}
}

// A reply this site may not act on fails the READING, not the job: retrying
// would ask the same question of the same document and get the same answer.
func TestAnUnusableReplyFailsTheReadingWithoutRetrying(t *testing.T) {
	store := &fakeReadStore{meta: textAttachment("text/plain"), body: uatDocument}
	brain := scriptedBrain{ai.NewFakeClient().Script(`{"fields":[]}`), ai.DocumentMIMEs()}

	if err := runReading(t, store, brain); err != nil {
		t.Fatalf("Read returned an error, so the job will retry a reading that cannot change: %v", err)
	}
	if store.outcome.Status != activities.ExtractionReadFailed {
		t.Fatalf("status = %q, want failed", store.outcome.Status)
	}
	if store.released {
		t.Error("an unusable reply released the reading, which invites an identical retry")
	}
}

// A transient fault is the JOB's to retry — and the reading goes back with it.
// Without the release the row stays running, the retry declines its own claim,
// and the reading is stranded live with nothing coming for it.
func TestARetryableFaultReleasesTheReadingBeforeHandingBackTheJob(t *testing.T) {
	boom := errors.New("object store unreachable")
	store := &fakeReadStore{meta: textAttachment("text/plain"), openErr: boom}

	err := runReading(t, store, scriptedBrain{ai.NewFakeClient(), ai.DocumentMIMEs()})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the transient fault so the job retries", err)
	}
	if !store.released {
		t.Error("the reading was not released, so the retry will find its own claim held")
	}
	if store.finishes != 0 {
		t.Error("a transient fault closed the reading, turning a blip into a permanent verdict")
	}
}

// A document that is gone is terminal, and says so in words a rep can act on
// rather than a driver string.
func TestAVanishedDocumentIsTerminalAndReadable(t *testing.T) {
	store := &fakeReadStore{meta: textAttachment("text/plain"), openErr: apperrors.ErrNotFound}

	if err := runReading(t, store, scriptedBrain{ai.NewFakeClient(), ai.DocumentMIMEs()}); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if store.outcome.Status != activities.ExtractionReadFailed {
		t.Fatalf("status = %q, want failed", store.outcome.Status)
	}
	if strings.Contains(store.outcome.Detail, "not found") {
		t.Errorf("detail = %q, want a rep-readable reason rather than the sentinel", store.outcome.Detail)
	}
}

// A claim this worker no longer holds is not its reading to work.
func TestAReadingItCouldNotClaimIsNotWorked(t *testing.T) {
	store := &fakeReadStore{beginErr: apperrors.ErrConflict}

	err := runReading(t, store, scriptedBrain{ai.NewFakeClient(), ai.DocumentMIMEs()})
	if !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("err = %v, want the claim conflict", err)
	}
	if store.finishes != 0 || store.released {
		t.Error("a reading nobody claimed was written to anyway")
	}
}

// The binding refusing a type it declared is a CONFIGURATION fault, not this
// document's — so it is refused rather than retried forever.
func TestABindingRefusingWhatItDeclaredFailsTheReading(t *testing.T) {
	store := &fakeReadStore{meta: textAttachment("application/pdf"), body: "%PDF"}
	// Declares PDF; the fake carries nothing, so the wire refuses it.
	brain := scriptedBrain{ai.NewFakeClient().CarryingNothing(), ai.DocumentMIMEs()}

	if err := runReading(t, store, brain); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if store.outcome.Status != activities.ExtractionReadFailed {
		t.Fatalf("status = %q, want failed", store.outcome.Status)
	}
}

var _ = model.ErrAttachmentUnsupported
