// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// The seller's reads of a room's documents. Every row joins its attachment,
// because the seller needs the stored filename beside the buyer-facing title
// to know which file a row is.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// documentObject is the table storekit patches, locks and audits. The RBAC
// gate is the ROOM's, as for tasks and the roster: curating what a buyer reads
// is part of running the room.
const documentObject = "deal_room_document"

// The four groups, as machine keys. Labels are the client's i18n.
var documentGroups = map[string]bool{
	"commercial":          true,
	"legal":               true,
	"security_privacy":    true,
	"delivery_operations": true,
}

// documentColumns is the projection every document read returns, in the order
// scanDocument consumes it. The attachment's file facts ride along so the
// seller can tell rows apart and the manifest can be frozen from one read.
const documentColumns = `d.id, d.room_id, d.attachment_id, d.group_key, d.title, d.position,
	a.filename, a.content_type, a.byte_size,
	d.source, d.captured_by, d.version, d.created_at, d.updated_at, d.archived_at`

const documentFrom = `deal_room_document d JOIN attachment a ON a.id = d.attachment_id`

// ListDocuments returns a room's documents in group-then-position order.
func (s *Store) ListDocuments(ctx context.Context, roomID ids.DealRoomID) ([]crmcontracts.DealRoomDocument, storekit.Page, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	var out []crmcontracts.DealRoomDocument
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// Reading the room first IS the scope gate, exactly as for tasks.
		if _, err := readRoom(ctx, tx, roomID); err != nil {
			return err
		}
		var err error
		out, err = documentRows(ctx, tx, roomID)
		return err
	})
	// Bounded by what one deal hands a buyer; answered whole, page object kept
	// because every list response in this contract carries one.
	return out, storekit.Page{}, err
}

func documentRows(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID) ([]crmcontracts.DealRoomDocument, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	rows, err := tx.Query(ctx, storekit.SQLf(
		`SELECT %s FROM %s
		  WHERE d.room_id = $%d AND d.archived_at IS NULL
		  ORDER BY d.group_key, d.position, d.created_at, d.id`,
		documentColumns, documentFrom, arg(roomID)), args...)
	if err != nil {
		return nil, fmt.Errorf("list deal room documents: %w", err)
	}
	defer rows.Close()
	var out []crmcontracts.DealRoomDocument
	for rows.Next() {
		doc, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read deal room documents: %w", err)
	}
	return out, nil
}

// readDocument returns one live document in a room; a document of another
// room is absent rather than refused.
func readDocument(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, id ids.DealRoomDocumentID) (crmcontracts.DealRoomDocument, error) {
	return readDocumentIn(ctx, tx, roomID, id, " AND d.archived_at IS NULL")
}

// readArchivedDocument is the response to a removal, read from the row because
// the trigger wrote the version.
func readArchivedDocument(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, id ids.DealRoomDocumentID) (crmcontracts.DealRoomDocument, error) {
	return readDocumentIn(ctx, tx, roomID, id, "")
}

func readDocumentIn(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, id ids.DealRoomDocumentID, liveOnly string) (crmcontracts.DealRoomDocument, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	roomPos, docPos := arg(roomID), arg(id)
	row := tx.QueryRow(ctx, storekit.SQLf(
		`SELECT %s FROM %s WHERE d.room_id = $%d AND d.id = $%d`+liveOnly,
		documentColumns, documentFrom, roomPos, docPos), args...)
	doc, err := scanDocument(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.DealRoomDocument{}, apperrors.ErrNotFound
	}
	return doc, err
}

func scanDocument(row rowScanner) (crmcontracts.DealRoomDocument, error) {
	var (
		out        crmcontracts.DealRoomDocument
		group      string
		filename   string
		archivedAt *time.Time
		capturedBy string
	)
	if err := row.Scan(&out.Id, &out.RoomId, &out.AttachmentId, &group, &out.Title, &out.Position,
		&filename, &out.ContentType, &out.ByteSize,
		&out.Source, &capturedBy, &out.Version, &out.CreatedAt, &out.UpdatedAt, &archivedAt); err != nil {
		return crmcontracts.DealRoomDocument{}, fmt.Errorf("scan deal room document: %w", err)
	}
	out.GroupKey = crmcontracts.DealRoomDocumentGroup(group)
	out.Filename = &filename
	out.CapturedBy = &capturedBy
	out.ArchivedAt = archivedAt
	return out, nil
}

// documentIDOf reads the UUID out of a contract id, named once for the three writers.
func documentIDOf(id openapi_types.UUID) ids.DealRoomDocumentID {
	return ids.From[ids.DealRoomDocumentKind](ids.UUID(id))
}
