// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// What a message carries, resolved at the moment it is sent.
//
// The caller names files by id; the send stores a SNAPSHOT of each one's
// metadata (ADR-0086/A131 §4). A pointer would let a later edit rewrite history
// — archive or supersede the document tomorrow and the timeline would change
// what it says a message sent today contained. A snapshot cannot.
//
// Resolution is a READ, and it carries the read's gate: GetAttachmentMeta
// refuses a file whose parent record the sender cannot see, so a rep cannot
// attach a document by guessing its id.

import (
	"context"
	"fmt"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// OutboundFile is one file this message carries, as it was when the message was
// staged. The BYTES are not here: they are read from the object store at
// transmit, so a delivery sitting on a retry ladder does not hold every
// attachment it might ever send in the database.
type OutboundFile struct {
	AttachmentID ids.UUID
	Filename     string
	ContentType  string
	ByteSize     int64
	Checksum     string
}

// attachmentIDsFrom reads the contract's optional attachment_ids into the
// module's own id type.
//
// ONE spelling for both transports: mail and a channel reply accept the same
// field with the same meaning, and two hand-rolled conversions are two places a
// nil list could become a non-nil empty one — which is the difference between
// "no files" and "a set the send has to resolve".
func attachmentIDsFrom(attachments *[]openapi_types.UUID) []ids.UUID {
	if attachments == nil {
		return nil
	}
	out := make([]ids.UUID, 0, len(*attachments))
	for _, id := range *attachments {
		out = append(out, ids.UUID(id))
	}
	return out
}

// maxAttachmentsPerSend is the most files one message may name.
//
// It mirrors `attachment_ids`' own `maxItems: 10` in the contract, and mirroring
// it in code is not belt-and-braces: NOTHING in this stack validates a request
// body against the schema, so the contract's cap is documentation until some Go
// enforces it. Unenforced, one request naming the same readable file thousands of
// times costs a transaction per element here, a jsonb array of that size on the
// delivery row, and a re-read of every element on every retry attempt — from a
// body well inside the chassis ceiling.
//
// Bound in ONE place because both transports resolve through here, and the pair
// is held to the contract by
// TestTheSendAttachmentCapMatchesTheContract.
const maxAttachmentsPerSend = 10

// TooManyAttachmentsError refuses a send naming more files than one message may
// carry. It maps to 422 with the limit in the message, because the caller can
// only fix it by sending fewer.
type TooManyAttachmentsError struct{ Named, Limit int }

func (e *TooManyAttachmentsError) Error() string {
	return fmt.Sprintf(
		"this message names %d files and a message may carry at most %d; send the rest as a second message",
		e.Named, e.Limit)
}

// FieldFault names the field the caller must shorten.
func (e *TooManyAttachmentsError) FieldFault() (field, code, message string) {
	return "attachment_ids", "too_many_attachments", e.Error()
}

// boundAttachmentIDs collapses repeats and refuses a set larger than one
// message may carry.
//
// A function of its ARGUMENT alone, apart from the resolve it guards, so both
// answers are provable without a database — and so the refusal happens before
// anything is read, which is the point of a bound on how much work a request may
// ask for.
//
// The cap applies to the DE-DUPLICATED count. Counting repeats would refuse a
// legitimate send while admitting the same total work under a shorter list, and
// a message carrying one file twice is not something a message can mean: the
// recipient would see the same document in two parts.
func boundAttachmentIDs(attachmentIDs []ids.UUID) ([]ids.UUID, error) {
	unique := make([]ids.UUID, 0, len(attachmentIDs))
	seen := make(map[ids.UUID]bool, len(attachmentIDs))
	for _, id := range attachmentIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	if len(unique) > maxAttachmentsPerSend {
		return nil, &TooManyAttachmentsError{Named: len(unique), Limit: maxAttachmentsPerSend}
	}
	return unique, nil
}

// resolveAttachments turns the caller's ids into the snapshot the delivery
// keeps.
//
// It refuses the whole send when one file cannot be resolved rather than
// dropping that file and going on. A message whose recipient sees fewer files
// than the sender attached is a message nobody is told is wrong — the same
// reasoning the dispatcher's carriage gate applies later, applied here at the
// first point where the difference is visible.
func (s *Store) resolveAttachments(ctx context.Context, attachmentIDs []ids.UUID) ([]OutboundFile, error) {
	if len(attachmentIDs) == 0 {
		return nil, nil
	}
	unique, err := boundAttachmentIDs(attachmentIDs)
	if err != nil {
		return nil, err
	}
	out := make([]OutboundFile, 0, len(unique))
	for _, id := range unique {
		meta, err := s.GetAttachmentMeta(ctx, id)
		if err != nil {
			// The sentinel travels: a file the sender cannot see is the same
			// 404 the attachment read itself gives, and the transport maps it
			// without this layer inventing a second vocabulary for it.
			return nil, fmt.Errorf("resolving an attached file: %w", err)
		}
		out = append(out, OutboundFile{
			AttachmentID: id,
			Filename:     meta.Filename,
			ContentType:  orEmpty(meta.ContentType),
			ByteSize:     orZero(meta.ByteSize),
			Checksum:     orEmpty(meta.Checksum),
		})
	}
	return out, nil
}

func orEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func orZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
