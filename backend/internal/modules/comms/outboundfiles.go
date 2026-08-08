// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

// What a sent message carried, recorded as it was at the moment of sending
// (ADR-0086/A131 §4).
//
// A SNAPSHOT rather than a list of ids the reader dereferences. Archiving or
// superseding a document later changes what the account library shows and must
// change nothing about what the timeline says was attached to a message that
// already went out. A pointer would let a later edit rewrite history; this
// cannot.

import "github.com/gradionhq/margince/backend/internal/shared/kernel/ids"

// OutboundFile is one file this delivery carries, as it was when the message
// was staged. A SNAPSHOT: archiving or superseding the document later changes
// what the library shows and changes nothing about what the timeline says was
// attached to a message that already went out.
type OutboundFile struct {
	AttachmentID ids.UUID `json:"attachment_id"`
	Filename     string   `json:"filename"`
	ContentType  string   `json:"content_type,omitempty"`
	ByteSize     int64    `json:"byte_size,omitempty"`
	Checksum     string   `json:"checksum,omitempty"`
}

// orEmptyFiles renders an absent attachment set as the empty array the column's
// shape constraint expects, rather than as JSON null.
func orEmptyFiles(files []OutboundFile) []OutboundFile {
	if files == nil {
		return []OutboundFile{}
	}
	return files
}
