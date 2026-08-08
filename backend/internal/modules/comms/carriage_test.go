// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

// The two halves the carriage gate rests on: how a sender's capability is read,
// and what reaches the connector once it has been cleared.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type declaredCarrier struct{ carries bool }

func (c declaredCarrier) CarriesAttachments() bool { return c.carries }

// A sender written before attachments existed compiles unchanged and must read
// as CANNOT CARRY, never as unknown. That is the no-default rule, and getting it
// wrong is exactly how a message goes out stripped.
func TestASenderThatNeverDeclaredCarriageCarriesNothing(t *testing.T) {
	if carriesAttachments(struct{}{}) {
		t.Error("a sender not implementing AttachmentCarrier was read as carrying files")
	}
	if carriesAttachments(declaredCarrier{carries: false}) {
		t.Error("a sender declaring it carries nothing was read as carrying files")
	}
	if !carriesAttachments(declaredCarrier{carries: true}) {
		t.Error("a sender declaring carriage was read as carrying nothing")
	}
}

// Every staged file reaches the connector, carrying its own identity. A subset
// here would be the strip the gate forbids, arriving one layer lower; a bare id
// would let archiving the document later rewrite what the timeline says was sent.
func TestOutboundFilesTravelWholeAndCarryTheirIdentity(t *testing.T) {
	staged := []OutboundFile{
		{
			AttachmentID: ids.NewV7(), Filename: "contract.pdf",
			ContentType: "application/pdf", ByteSize: 4096, Checksum: "sha256:x",
		},
		{AttachmentID: ids.NewV7(), Filename: "annex.pdf"},
	}
	got := outboundFiles(staged)
	if len(got) != len(staged) {
		t.Fatalf("handed the connector %d files, staged %d — an adapter may never transmit a set that differs from the one it was handed",
			len(got), len(staged))
	}
	for name, value := range map[string]string{
		"filename":     got[0].Filename,
		"content type": got[0].ContentType,
		"checksum":     got[0].Checksum,
	} {
		if value == "" {
			t.Errorf("the snapshot dropped the %s, so a later change to the document would rewrite what the timeline says was sent", name)
		}
	}
	if outboundFiles(nil) != nil {
		t.Error("an empty staged set became a non-nil file list the adapter has to tell from 'no files'")
	}
}
