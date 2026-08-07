// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

// The transmit-time recheck of a delivery's files (ADR-0086/A131 §3).
//
// A delivery is not sent when it is staged. Every case here is about that
// window: what the staging check knew has had time to stop being true.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// deliveryWithFiles is liveDelivery carrying one attachment, which is what
// makes the integrity gate ask anything at all.
func deliveryWithFiles() Delivery {
	del := liveDelivery()
	del.Attachments = []OutboundFile{{
		AttachmentID: ids.NewV7(), Filename: "offer.pdf",
		ContentType: "application/pdf", ByteSize: 1024,
	}}
	return del
}

func TestDispatchParksWhenAnAttachedFileNoLongerPassesTheScan(t *testing.T) {
	sender := &carryingSender{}
	store := &fakeStore{delivery: deliveryWithFiles()}
	files := &stubAttachments{
		reason: `"offer.pdf" did not pass the malware scan in time to be sent: attachment blocked`,
	}
	d := newAttachmentDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, files)

	got, err := dispatch(context.Background(), d, store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomeParked || sender.calls != 0 {
		t.Fatalf("outcome=%v calls=%d, want OutcomeParked/0 — a blocked file must not leave the building", got, sender.calls)
	}
	// The reason names the file. A park record reading "an attachment cannot be
	// sent" leaves the sender guessing which of several to fix.
	if !strings.Contains(store.parked, "offer.pdf") {
		t.Errorf("parked reason = %q, want it to name the file", store.parked)
	}
}

// The sender losing access is not the same event as the scanner objecting, and
// both have to reach this gate — the file is unchanged, the authority is not.
func TestDispatchParksWhenTheSenderCanNoLongerSeeAnAttachedFile(t *testing.T) {
	sender := &carryingSender{}
	store := &fakeStore{delivery: deliveryWithFiles()}
	files := &stubAttachments{
		reason: "a file attached to this message is no longer available to the sender; it was archived, or their access to the record holding it was withdrawn",
	}
	d := newAttachmentDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, files)

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeParked || sender.calls != 0 {
		t.Fatalf("outcome=%v calls=%d, want OutcomeParked/0", got, sender.calls)
	}
	if !strings.Contains(store.parked, "no longer available to the sender") {
		t.Errorf("parked reason = %q, want the withdrawn-access reason", store.parked)
	}
}

// It PARKS rather than stripping the file and sending the rest: a recipient who
// sees fewer files than the timeline records is a permanently wrong record that
// nobody is told about.
func TestDispatchNeverTransmitsTheTextWithoutTheRefusedFile(t *testing.T) {
	sender := &carryingSender{}
	store := &fakeStore{delivery: deliveryWithFiles()}
	d := newAttachmentDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}},
		&stubAttachments{reason: "the file was quarantined"})

	if _, err := dispatch(context.Background(), d, store.delivery.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if sender.calls != 0 {
		t.Fatalf("the provider was called %d times for a message whose file was refused", sender.calls)
	}
}

// An outage is not a verdict. Parking on a failure to LEARN whether the file is
// clean would destroy every legitimate send in flight during a database blip.
func TestDispatchRetriesWhenTheAttachmentCheckFailsTransiently(t *testing.T) {
	sender := &carryingSender{}
	store := &fakeStore{delivery: deliveryWithFiles()}
	d := newAttachmentDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}},
		&stubAttachments{err: errors.New("attachment store timeout")})

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeRetry {
		t.Errorf("outcome = %v, want OutcomeRetry — an outage is not a quarantine", got)
	}
	if store.parked != "" {
		t.Errorf("parked on a transient attachment fault: %q", store.parked)
	}
	if sender.calls != 0 {
		t.Errorf("transmitted %d times without an answer about the files", sender.calls)
	}
}

// This lane reaches a real external mailbox, so an unwired gate is a deployment
// defect that must fail closed rather than wave every attachment through.
func TestDispatchParksWhenNoAttachmentAuthorityIsWired(t *testing.T) {
	sender := &carryingSender{}
	store := &fakeStore{delivery: deliveryWithFiles()}
	d := newAttachmentDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, nil)

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeParked || sender.calls != 0 {
		t.Errorf("outcome=%v calls=%d, want OutcomeParked/0 — an unwired gate must not pass files", got, sender.calls)
	}
}

// A message with no files must not pay for the gate: an unwired authority is
// inert here, and a wired one is never asked.
func TestDispatchDoesNotAskAboutFilesWhenThereAreNone(t *testing.T) {
	sender := &carryingSender{}
	store := &fakeStore{delivery: liveDelivery()}
	files := &stubAttachments{ok: true}
	d := newAttachmentDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, files)

	got, err := dispatch(context.Background(), d, store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomeSent {
		t.Fatalf("outcome = %v, want OutcomeSent for a message with no attachments", got)
	}
	if len(files.asked) != 0 {
		t.Errorf("the attachment authority was asked about %v for a message carrying no files", files.asked)
	}
}

// The gate must ask about the files the delivery actually carries, or it would
// pass every message by asking about nothing.
func TestDispatchAsksAboutEveryFileTheDeliveryCarries(t *testing.T) {
	sender := &carryingSender{}
	del := deliveryWithFiles()
	second := OutboundFile{AttachmentID: ids.NewV7(), Filename: "terms.pdf"}
	del.Attachments = append(del.Attachments, second)
	store := &fakeStore{delivery: del}
	files := &stubAttachments{ok: true}
	d := newAttachmentDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, files)

	if _, err := dispatch(context.Background(), d, store.delivery.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(files.asked) != 2 {
		t.Fatalf("the gate asked about %d files, want both the delivery carries", len(files.asked))
	}
	if files.asked[0] != del.Attachments[0].AttachmentID || files.asked[1] != second.AttachmentID {
		t.Errorf("the gate asked about %v, want the delivery's own attachment ids", files.asked)
	}
}
