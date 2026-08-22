// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// What a publish would change: the draft diffed against the latest release.
// The seller reads this before pressing Publish, and the deal page's card
// reads it to say "unpublished changes" — one read, computed here from the
// frozen snapshot, rather than two browser-side diffs that would disagree.

import (
	"context"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The change kinds, spelled once. They are a closed set the client renders
// as sentences; a kind not in this list is a client bug, never a server one.
const (
	changeTitle              = "title_changed"
	changeWelcome            = "welcome_changed"
	changeDocumentAdded      = "document_added"
	changeDocumentRemoved    = "document_removed"
	changeDocumentRetitled   = "document_retitled"
	changeDocumentRegrouped  = "document_regrouped"
	changeDocumentReordered  = "document_reordered"
	changeDocumentIneligible = "document_ineligible"
)

// Changes returns what the next release would show differently from the
// latest one. A room never published reports every eligible document as
// added.
func (s *Store) Changes(ctx context.Context, roomID ids.DealRoomID) (crmcontracts.DealRoomChanges, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionRead); err != nil {
		return crmcontracts.DealRoomChanges{}, err
	}
	var out crmcontracts.DealRoomChanges
	err := s.tx(ctx, func(tx pgx.Tx) error {
		room, err := readRoom(ctx, tx, roomID)
		if err != nil {
			return err
		}
		st, err := readStanding(ctx, tx, roomID)
		if err != nil {
			return err
		}
		eligible, err := publishableDocumentRows(ctx, tx, roomID)
		if err != nil {
			return err
		}
		all, err := documentRows(ctx, tx, roomID)
		if err != nil {
			return err
		}
		var published *releaseSnapshot
		if st.releaseNo != nil {
			snap, err := decodeSnapshot(st.snapshot)
			if err != nil {
				return err
			}
			published = &snap
			no := *st.releaseNo
			out.ReleaseNo = &no
		}
		out.Changes = diffRelease(room, published, eligible, all)
		out.HasChanges = len(out.Changes) > 0
		return nil
	})
	return out, err
}

// diffRelease lists the differences between the draft and the release. Order
// is stable — room text first, then documents in their draft order, then the
// removed ones — so the list reads the same on every refresh.
func diffRelease(room crmcontracts.DealRoom, published *releaseSnapshot, eligible, all []crmcontracts.DealRoomDocument) []crmcontracts.DealRoomChange {
	changes := []crmcontracts.DealRoomChange{}
	was := map[openapi_types.UUID]snapshotDocument{}
	if published == nil {
		// Never published: the title is what the first release would show,
		// so a room with nothing else in it can still go out.
		changes = append(changes, crmcontracts.DealRoomChange{Kind: changeTitle})
		if room.WelcomeMessage != nil && *room.WelcomeMessage != "" {
			changes = append(changes, crmcontracts.DealRoomChange{Kind: changeWelcome})
		}
	}
	if published != nil {
		if published.Title != room.Title {
			changes = append(changes, crmcontracts.DealRoomChange{Kind: changeTitle})
		}
		if !sameText(published.WelcomeMessage, room.WelcomeMessage) {
			changes = append(changes, crmcontracts.DealRoomChange{Kind: changeWelcome})
		}
		for _, d := range published.Documents {
			was[d.ID] = d
		}
	}
	eligibleIDs := map[openapi_types.UUID]bool{}
	for _, d := range eligible {
		eligibleIDs[d.Id] = true
		changes = append(changes, documentChanges(d, was)...)
	}
	for _, d := range all {
		// Still in the draft, no longer eligible: it would drop out of the next
		// release whether or not a release ever carried it.
		if !eligibleIDs[d.Id] {
			changes = append(changes, change(changeDocumentIneligible, d.Id, d.Title))
		}
	}
	for _, d := range published.documentsOrNone() {
		if _, live := eligibleIDs[d.ID]; !live && !inDraft(all, d.ID) {
			changes = append(changes, change(changeDocumentRemoved, d.ID, d.Title))
		}
	}
	return changes
}

func documentChanges(d crmcontracts.DealRoomDocument, was map[openapi_types.UUID]snapshotDocument) []crmcontracts.DealRoomChange {
	before, ok := was[d.Id]
	if !ok {
		return []crmcontracts.DealRoomChange{change(changeDocumentAdded, d.Id, d.Title)}
	}
	var out []crmcontracts.DealRoomChange
	if before.Title != d.Title {
		out = append(out, change(changeDocumentRetitled, d.Id, d.Title))
	}
	if before.GroupKey != string(d.GroupKey) {
		out = append(out, change(changeDocumentRegrouped, d.Id, d.Title))
	}
	if before.Position != d.Position {
		out = append(out, change(changeDocumentReordered, d.Id, d.Title))
	}
	return out
}

func change(kind string, id openapi_types.UUID, title string) crmcontracts.DealRoomChange {
	docID := id
	t := title
	return crmcontracts.DealRoomChange{Kind: kind, DocumentId: &docID, Title: &t}
}

func inDraft(all []crmcontracts.DealRoomDocument, id openapi_types.UUID) bool {
	for _, d := range all {
		if d.Id == id {
			return true
		}
	}
	return false
}

func sameText(a, b *string) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	}
	return *a == *b
}

// documentsOrNone reads the published manifest of a release that may not exist.
func (snap *releaseSnapshot) documentsOrNone() []snapshotDocument {
	if snap == nil {
		return nil
	}
	return snap.Documents
}
