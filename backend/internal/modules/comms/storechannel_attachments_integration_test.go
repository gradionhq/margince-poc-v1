// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package comms

// The channel half of the attachment snapshot, over a migrated Postgres. It
// rides the shared fixture in store_integration_test.go and the channel helpers
// in store_channel_integration_test.go.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// A file staged on a channel reply must be on the delivery row the dispatcher
// later reads. The snapshot is taken at staging so archiving the document later
// cannot rewrite what the timeline says a sent message carried — and a chain
// that dropped the set anywhere between the request and the row would be
// invisible until a rep's file did not arrive.
func TestStageChannelSnapshotsItsAttachments(t *testing.T) {
	e := setupStore(t)
	activity := e.telegramActivity(t)
	want := OutboundFile{
		AttachmentID: ids.NewV7(),
		Filename:     "quote.pdf",
		ContentType:  "application/pdf",
		ByteSize:     4096,
		Checksum:     "sha256:abc123",
	}
	id := e.stageChannel(t, StageChannelInput{
		ActivityID:     activity,
		Provider:       "telegram",
		Recipient:      connector.ChannelIdentity{Provider: "telegram", ChannelUserID: "778899"},
		Body:           "the quote you asked for",
		ConsentPurpose: "transactional",
		Attachments:    []OutboundFile{want},
	})

	got, err := e.store.Load(e.ctx, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("the delivery row carries %d files, want 1 — the set was dropped between staging and the row", len(got.Attachments))
	}
	// Every field, not just the id: a snapshot missing the filename or the size
	// is a delivery the carriage gate cannot size and a park reason that cannot
	// name what to fix.
	if got.Attachments[0] != want {
		t.Errorf("the snapshot is %+v, want %+v", got.Attachments[0], want)
	}
}

// A channel reply with no files loads back as carrying none — never as a row the
// shape constraint refused, and never as one file's worth of JSON null.
func TestStageChannelWithNoAttachmentsCarriesNone(t *testing.T) {
	e := setupStore(t)
	id := e.stageChannel(t, StageChannelInput{
		ActivityID:     e.telegramActivity(t),
		Provider:       "telegram",
		Recipient:      connector.ChannelIdentity{Provider: "telegram", ChannelUserID: "778899"},
		Body:           "no files here",
		ConsentPurpose: "transactional",
	})
	got, err := e.store.Load(e.ctx, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Attachments) != 0 {
		t.Errorf("a reply staged with no files loaded back carrying %d", len(got.Attachments))
	}
}
