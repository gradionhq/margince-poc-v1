// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// The release snapshot, typed once so the writer (publish) and the only reader
// that interprets it (the buyer edge) cannot drift on a key. The seller's
// release read hands the snapshot over as an opaque object and does not care.

import (
	"encoding/json"
	"fmt"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// releaseSnapshot is what a release freezes: every editorial value a buyer
// reads.
type releaseSnapshot struct {
	Title          string              `json:"title"`
	DealID         openapi_types.UUID  `json:"deal_id"`
	ReleasedAt     time.Time           `json:"released_at"`
	WelcomeMessage *string             `json:"welcome_message,omitempty"`
	StewardUserID  *openapi_types.UUID `json:"steward_user_id,omitempty"`
	Documents      []snapshotDocument  `json:"documents"`
}

// snapshotDocument is a document as published: the buyer-facing title, its
// group and order, and the exact attachment (version) it points at, with the
// file facts a download needs. The storage key is deliberately NOT here — the
// public read resolves it through the attachment row at download time, so a
// release never carries a locator.
type snapshotDocument struct {
	ID           openapi_types.UUID `json:"id"`
	AttachmentID openapi_types.UUID `json:"attachment_id"`
	GroupKey     string             `json:"group_key"`
	Title        string             `json:"title"`
	Position     int                `json:"position"`
	Filename     string             `json:"filename"`
	ContentType  *string            `json:"content_type,omitempty"`
	ByteSize     *int64             `json:"byte_size,omitempty"`
}

// snapshotOf copies every buyer-visible editorial value into the frozen
// projection. What is NOT here matters as much as what is: no live CRM read
// reaches the buyer through a release, so a deal renamed after publication does
// not silently rewrite what the buyer was shown.
func snapshotOf(room crmcontracts.DealRoom, docs []crmcontracts.DealRoomDocument) releaseSnapshot {
	snap := releaseSnapshot{
		Title:          room.Title,
		DealID:         room.DealId,
		ReleasedAt:     time.Now().UTC(),
		WelcomeMessage: room.WelcomeMessage,
		StewardUserID:  room.StewardUserId,
		Documents:      make([]snapshotDocument, 0, len(docs)),
	}
	for _, d := range docs {
		snap.Documents = append(snap.Documents, snapshotDocument{
			ID: d.Id, AttachmentID: d.AttachmentId, GroupKey: string(d.GroupKey), Title: d.Title,
			Position: d.Position, Filename: filenameOf(d), ContentType: d.ContentType, ByteSize: d.ByteSize,
		})
	}
	return snap
}

// decodeSnapshot reads a stored release back. Keys this struct no longer
// carries (releases once froze a shared to-do list) are ignored: an old release
// still decodes, and what it showed in those keys is not served again.
func decodeSnapshot(raw []byte) (releaseSnapshot, error) {
	var snap releaseSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return releaseSnapshot{}, fmt.Errorf("decode release snapshot: %w", err)
	}
	return snap, nil
}

// filenameOf reads the attachment's stored filename off a document row. The
// contract marks it readOnly, which the generator renders as optional; every
// row this module reads joins the attachment, so it is always present here.
func filenameOf(d crmcontracts.DealRoomDocument) string {
	if d.Filename == nil {
		return ""
	}
	return *d.Filename
}
