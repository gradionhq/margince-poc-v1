// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// What a buyer reads of the documents: the manifest the latest release froze,
// and the bytes of one document in it. The manifest never carries a storage
// locator; the download resolves it from the attachment row at request time,
// predicated on the session's room through the document row.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// publishedDocuments reads the room's standing and the manifest a buyer may
// see. Empty — not refused — while the room serves no content.
func publishedDocuments(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, now time.Time) ([]snapshotDocument, error) {
	st, err := readStanding(ctx, tx, roomID)
	if err != nil {
		return nil, err
	}
	if !servesContent(st.access(now)) || st.snapshot == nil {
		return []snapshotDocument{}, nil
	}
	snap, err := decodeSnapshot(st.snapshot)
	if err != nil {
		return nil, err
	}
	return snap.Documents, nil
}

func buyerDocuments(docs []snapshotDocument) []crmcontracts.BuyerRoomDocument {
	out := make([]crmcontracts.BuyerRoomDocument, 0, len(docs))
	for _, d := range docs {
		out = append(out, crmcontracts.BuyerRoomDocument{
			Id:          d.ID,
			GroupKey:    crmcontracts.DealRoomDocumentGroup(d.GroupKey),
			Title:       d.Title,
			Position:    d.Position,
			Filename:    d.Filename,
			ContentType: d.ContentType,
			ByteSize:    d.ByteSize,
		})
	}
	return out
}

// BuyerDocuments lists the published manifest.
func (s *Store) BuyerDocuments(ctx context.Context, sess Session) ([]crmcontracts.BuyerRoomDocument, error) {
	if sess.ID == ids.Nil {
		return nil, apperrors.ErrPermissionDenied
	}
	var out []crmcontracts.BuyerRoomDocument
	err := s.tx(ctx, func(tx pgx.Tx) error {
		docs, err := publishedDocuments(ctx, tx, sess.RoomID, time.Now())
		if err != nil {
			return err
		}
		out = buyerDocuments(docs)
		return nil
	})
	return out, err
}

// BuyerDocumentFile is what a download needs: the file facts the release
// froze and the locator the attachment row holds now.
type BuyerDocumentFile struct {
	Filename    string
	ContentType *string
	ByteSize    *int64
	StorageKey  string
}

// BuyerDocumentLocator resolves one published document to its object.
//
// Two predicates, both mandatory: the document must be in the latest release
// of the session's room (the manifest), and the attachment it names must still
// be reachable through a document row OF THAT ROOM. The second is what keeps
// a forged id in the manifest from reaching a file: the manifest says which
// version, the row says which room.
func (s *Store) BuyerDocumentLocator(ctx context.Context, sess Session, documentID ids.DealRoomDocumentID) (BuyerDocumentFile, error) {
	if sess.ID == ids.Nil {
		return BuyerDocumentFile{}, apperrors.ErrPermissionDenied
	}
	var out BuyerDocumentFile
	err := s.tx(ctx, func(tx pgx.Tx) error {
		docs, err := publishedDocuments(ctx, tx, sess.RoomID, time.Now())
		if err != nil {
			return err
		}
		published, ok := findPublished(docs, documentID)
		if !ok {
			return apperrors.ErrNotFound
		}
		err = tx.QueryRow(ctx,
			`SELECT a.storage_key FROM deal_room_document d
			   JOIN attachment a ON a.id = d.attachment_id
			  WHERE d.id = $1 AND d.room_id = $2 AND d.attachment_id = $3`,
			documentID, sess.RoomID, published.AttachmentID).Scan(&out.StorageKey)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("locate deal room document: %w", err)
		}
		out.Filename, out.ContentType, out.ByteSize = published.Filename, published.ContentType, published.ByteSize
		return nil
	})
	return out, err
}

func findPublished(docs []snapshotDocument, id ids.DealRoomDocumentID) (snapshotDocument, bool) {
	for _, d := range docs {
		if ids.UUID(d.ID) == id.UUID {
			return d, true
		}
	}
	return snapshotDocument{}, false
}

// Keep the contract id type in scope for the manifest's callers.
var _ openapi_types.UUID
