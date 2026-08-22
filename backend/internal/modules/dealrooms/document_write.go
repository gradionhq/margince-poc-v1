// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// The seller curating a room's documents. Every change here is EDITORIAL: it
// reaches the buyer through the release that publishes it, which is why none
// of these writes announces itself — deal_room.published does, for all of them
// at once, exactly as for a task definition.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// AddDocumentInput is the validated shape both transports add a document from.
type AddDocumentInput struct {
	AttachmentID ids.UUID
	GroupKey     string
	// Title is the buyer-facing name; empty means "the attachment's filename".
	Title    string
	Position int
	Source   string
}

// AddDocument puts an attachment of the deal in front of the buyer.
func (s *Store) AddDocument(ctx context.Context, roomID ids.DealRoomID, in AddDocumentInput) (crmcontracts.DealRoomDocument, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionCreate); err != nil {
		return crmcontracts.DealRoomDocument{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.DealRoomDocument{}, err
	}
	var out crmcontracts.DealRoomDocument
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = addDocumentTx(ctx, tx, roomID, in, by)
		return err
	})
	return out, err
}

func addDocumentTx(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, in AddDocumentInput, by string) (crmcontracts.DealRoomDocument, error) {
	room, err := openRoomForContent(ctx, tx, roomID)
	if err != nil {
		return crmcontracts.DealRoomDocument{}, err
	}
	filename, err := attachmentOfDeal(ctx, tx, in.AttachmentID, ids.From[ids.DealRoomKind](ids.UUID(room.Id)))
	if err != nil {
		return crmcontracts.DealRoomDocument{}, err
	}
	title := in.Title
	if title == "" {
		title = filename
	}
	id := ids.New[ids.DealRoomDocumentKind]()
	if _, err := tx.Exec(ctx,
		`INSERT INTO deal_room_document (id, room_id, attachment_id, group_key, title, position, source, captured_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, roomID, in.AttachmentID, in.GroupKey, title, in.Position, in.Source, by); err != nil {
		if storekit.IsUniqueViolation(err) {
			return crmcontracts.DealRoomDocument{}, errDocumentAlreadyInRoom
		}
		return crmcontracts.DealRoomDocument{}, fmt.Errorf("insert deal room document: %w", err)
	}
	if _, err := storekit.Audit(ctx, tx, "create", documentObject, id.UUID, nil,
		map[string]any{fieldRoomID: roomID.UUID, fieldAttachmentID: in.AttachmentID, "group_key": in.GroupKey, columnTitle: title}); err != nil {
		return crmcontracts.DealRoomDocument{}, fmt.Errorf("audit deal room document add: %w", err)
	}
	return readDocument(ctx, tx, roomID, id)
}

// attachmentOfDeal admits a file into the room: it must be in the room's
// deal's Files area (inTheDealsFilesArea — the same predicate publish and the
// buyer download re-apply), and, when it arrived with a message, that message
// must be one THIS caller may read. The second half is the grant the first
// cannot carry: a captured file follows its message's audience, and a rep with
// write on the deal must not be able to hand a teammate's limited-audience
// mail to an outsider. Any other id is absent, so a caller cannot use this
// path to learn that some other record's file exists.
func attachmentOfDeal(ctx context.Context, tx pgx.Tx, attachmentID ids.UUID, roomID ids.DealRoomID) (string, error) {
	var (
		filename   string
		entityType string
		entityID   ids.UUID
	)
	err := tx.QueryRow(ctx,
		`SELECT a.filename, a.entity_type, a.entity_id
		   FROM attachment a JOIN deal_room r ON r.id = $2
		  WHERE a.id = $1 AND `+inTheDealsFilesArea,
		attachmentID, roomID).Scan(&filename, &entityType, &entityID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apperrors.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read attachment for deal room document: %w", err)
	}
	if entityType == "activity" {
		// The object grant first, then the row: a seat that lost activity.read
		// must not be able to replay an id it once saw. Denial reads as absent,
		// as every attachment read answers.
		if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
			if errors.Is(err, apperrors.ErrPermissionDenied) {
				return "", apperrors.ErrNotFound
			}
			return "", err
		}
		if err := auth.EnsureActivityContentVisible(ctx, tx, entityID); err != nil {
			return "", err
		}
	}
	return filename, nil
}

// errDocumentAlreadyInRoom refuses showing the same file version twice.
var errDocumentAlreadyInRoom = &stateError{
	code:    "deal_room_document_already_present",
	current: "live",
	wanted:  "this file is already in the room; rename or regroup the existing entry instead",
}

// UpdateDocumentInput is the validated patch. Every field is optional.
type UpdateDocumentInput struct {
	GroupKey  *string
	Title     *string
	Position  *int
	IfVersion *int64
}

// UpdateDocument renames, regroups or reorders a document.
func (s *Store) UpdateDocument(ctx context.Context, roomID ids.DealRoomID, id ids.DealRoomDocumentID, in UpdateDocumentInput) (crmcontracts.DealRoomDocument, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionUpdate); err != nil {
		return crmcontracts.DealRoomDocument{}, err
	}
	var out crmcontracts.DealRoomDocument
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := openRoomForContent(ctx, tx, roomID); err != nil {
			return err
		}
		if _, err := storekit.LockRow(ctx, tx, documentObject, id.UUID, storekit.LiveOnly); err != nil {
			return err
		}
		current, err := readDocument(ctx, tx, roomID, id)
		if err != nil {
			return err
		}
		p := storekit.NewPatch()
		if in.GroupKey != nil {
			p.Set("group_key", string(current.GroupKey), *in.GroupKey)
		}
		if in.Title != nil {
			p.Set(columnTitle, current.Title, *in.Title)
		}
		if in.Position != nil {
			p.Set("position", current.Position, *in.Position)
		}
		if !p.Empty() {
			if err := p.ApplyGuarded(ctx, tx, documentObject, id.UUID, in.IfVersion); err != nil {
				return err
			}
			if _, err := storekit.Audit(ctx, tx, "update", documentObject, id.UUID, p.Before(), p.After()); err != nil {
				return fmt.Errorf("audit deal room document update: %w", err)
			}
		}
		out, err = readDocument(ctx, tx, roomID, id)
		return err
	})
	return out, err
}

// RemoveDocument takes a document out of the room. The attachment stays; a
// release already published keeps naming this version, because that is what
// the buyer was shown.
func (s *Store) RemoveDocument(ctx context.Context, roomID ids.DealRoomID, id ids.DealRoomDocumentID, ifVersion *int64) (crmcontracts.DealRoomDocument, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionDelete); err != nil {
		return crmcontracts.DealRoomDocument{}, err
	}
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.DealRoomDocument{}, err
	}
	var out crmcontracts.DealRoomDocument
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := openRoomForContent(ctx, tx, roomID); err != nil {
			return err
		}
		if _, err := storekit.LockRow(ctx, tx, documentObject, id.UUID, storekit.LiveOnly); err != nil {
			return err
		}
		current, err := readDocument(ctx, tx, roomID, id)
		if err != nil {
			return err
		}
		p := storekit.NewPatch()
		p.Set("archived_at", nil, time.Now().UTC())
		if err := p.ApplyGuarded(ctx, tx, documentObject, id.UUID, ifVersion); err != nil {
			return fmt.Errorf("remove deal room document: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "archive", documentObject, id.UUID,
			map[string]any{columnTitle: current.Title, fieldAttachmentID: current.AttachmentId}, p.After()); err != nil {
			return fmt.Errorf("audit deal room document remove: %w", err)
		}
		out, err = readArchivedDocument(ctx, tx, roomID, id)
		return err
	})
	return out, err
}
