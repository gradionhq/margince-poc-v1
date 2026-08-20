// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

// What reaches the connector once the carriage gate has cleared it. How a
// sender's capability is READ is the port's own question, tested beside
// connector.CarriageOf.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

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
	d := &Dispatcher{attachments: &stubAttachments{ok: true}}
	got, err := d.attachedFiles(context.Background(), Delivery{Attachments: staged})
	if err != nil {
		t.Fatalf("pairing the snapshot with its bytes failed: %v", err)
	}
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
	// The bytes travel too. A snapshot handed over without them is a part with
	// no content, which is the shape a recipient sees as an empty attachment.
	for i, file := range got {
		if len(file.Body) == 0 {
			t.Errorf("file %d reached the connector with no bytes", i)
		}
	}

	empty, err := d.attachedFiles(context.Background(), Delivery{})
	if err != nil {
		t.Fatalf("a message with no files failed: %v", err)
	}
	if empty != nil {
		t.Error("an empty staged set became a non-nil file list the adapter has to tell from 'no files'")
	}
}
