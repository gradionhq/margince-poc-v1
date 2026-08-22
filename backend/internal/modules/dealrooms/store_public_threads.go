// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// The buyer's side of the conversation: reading it, opening a thread,
// replying, and deciding on a document version. Every write needs the room
// LIVE and a capability that admits it; every statement is predicated on the
// session's room.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// BuyerThreads lists the conversation. Empty while the room serves no content.
//
// A thread on a document is shown only while the latest release NAMES that
// document: a thread the seller opened on a file they have not yet published
// would otherwise hand the buyer the file's existence and the seller's words
// about it. Room-level threads are always shown.
func (s *Store) BuyerThreads(ctx context.Context, sess Session, documentID *ids.UUID) ([]crmcontracts.DealRoomThread, error) {
	if sess.ID == ids.Nil {
		return nil, apperrors.ErrPermissionDenied
	}
	var out []crmcontracts.DealRoomThread
	err := s.tx(ctx, func(tx pgx.Tx) error {
		docs, err := publishedDocuments(ctx, tx, sess.RoomID, time.Now())
		if err != nil {
			return err
		}
		published := map[openapi_types.UUID]bool{}
		for _, d := range docs {
			published[d.ID] = true
		}
		if documentID != nil && !published[openapi_types.UUID(*documentID)] {
			out = []crmcontracts.DealRoomThread{}
			return nil
		}
		all, err := threadRows(ctx, tx, sess.RoomID, documentID)
		if err != nil {
			return err
		}
		out = make([]crmcontracts.DealRoomThread, 0, len(all))
		for _, th := range all {
			if th.DocumentId == nil || published[*th.DocumentId] {
				out = append(out, th)
			}
		}
		return nil
	})
	return out, err
}

// liveRoomForBuyerWrite settles what every buyer write needs: the room is
// live (paused refuses reversibly, the finished states as a record) and the
// capability admits writing. Returns the room as the transaction bodies want it.
func liveRoomForBuyerWrite(ctx context.Context, tx pgx.Tx, sess Session, needs string) (crmcontracts.DealRoom, error) {
	if sess.Capability == capabilityView || (needs == capabilityReviewer && sess.Capability != capabilityReviewer) {
		if needs == capabilityReviewer {
			return crmcontracts.DealRoom{}, errNotReviewer
		}
		return crmcontracts.DealRoom{}, errViewerCannotWrite
	}
	st, err := readStanding(ctx, tx, sess.RoomID)
	if err != nil {
		return crmcontracts.DealRoom{}, err
	}
	switch access := st.access(time.Now()); access {
	case accessLive:
	case accessPaused:
		return crmcontracts.DealRoom{}, pausedForBuyer()
	default:
		return crmcontracts.DealRoom{}, notContentEditable(access)
	}
	if _, err := storekit.LockRow(ctx, tx, roomObject, sess.RoomID.UUID, storekit.LiveOnly); err != nil {
		return crmcontracts.DealRoom{}, err
	}
	var room crmcontracts.DealRoom
	room.Id = openapi_types.UUID(sess.RoomID.UUID)
	if err := tx.QueryRow(ctx, `SELECT deal_id FROM deal_room WHERE id = $1`, sess.RoomID).Scan(&room.DealId); err != nil {
		return crmcontracts.DealRoom{}, fmt.Errorf("read deal room for a buyer write: %w", err)
	}
	return room, nil
}

// errNotReviewer refuses a decision from a participant the seller did not make
// a reviewer. A confirmation carries weight in a negotiation, so it is granted
// deliberately and never by default.
var errNotReviewer = &fieldError{
	field: fieldCapability,
	code:  "reviewer_required",
	msg:   "only a reviewer can decide on a document; ask your contact to make you one",
}

// publishedDocumentVersion is the attachment the latest release names for a
// document — what a buyer's thread or decision is about. A document the buyer
// cannot see is absent.
func publishedDocumentVersion(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, documentID ids.UUID) (ids.UUID, error) {
	docs, err := publishedDocuments(ctx, tx, roomID, time.Now())
	if err != nil {
		return ids.Nil, err
	}
	published, ok := findPublished(docs, ids.From[ids.DealRoomDocumentKind](documentID))
	if !ok {
		return ids.Nil, apperrors.ErrNotFound
	}
	return ids.UUID(published.AttachmentID), nil
}

// OpenBuyerThread opens a thread as the buyer. A document thread is about a
// document the latest release names, nothing else.
func (s *Store) OpenBuyerThread(ctx context.Context, sess Session, in OpenThreadInput) (crmcontracts.DealRoomThread, error) {
	if sess.ID == ids.Nil {
		return crmcontracts.DealRoomThread{}, apperrors.ErrPermissionDenied
	}
	var out crmcontracts.DealRoomThread
	err := s.tx(ctx, func(tx pgx.Tx) error {
		room, err := liveRoomForBuyerWrite(ctx, tx, sess, capabilityComment)
		if err != nil {
			return err
		}
		if in.DocumentID != nil {
			if _, err := publishedDocumentVersion(ctx, tx, sess.RoomID, *in.DocumentID); err != nil {
				return err
			}
		}
		out, err = openThreadTx(ctx, tx, room, in, threadAuthor{participantID: &sess.ParticipantID})
		return err
	})
	return out, err
}

// ReplyAsBuyer appends to an open thread of the session's room.
func (s *Store) ReplyAsBuyer(ctx context.Context, sess Session, threadID ids.UUID, body, source string) (crmcontracts.DealRoomThread, error) {
	if sess.ID == ids.Nil {
		return crmcontracts.DealRoomThread{}, apperrors.ErrPermissionDenied
	}
	var out crmcontracts.DealRoomThread
	err := s.tx(ctx, func(tx pgx.Tx) error {
		room, err := liveRoomForBuyerWrite(ctx, tx, sess, capabilityComment)
		if err != nil {
			return err
		}
		out, err = replyTx(ctx, tx, room, threadID, body, source, threadAuthor{participantID: &sess.ParticipantID})
		return err
	})
	return out, err
}

// errOpenRequiredThreads refuses a confirmation while a required-change thread
// on the document is still open. Named so the client can branch to "show me
// the threads" rather than parsing prose.
var errOpenRequiredThreads = &stateError{
	code:    "open_required_threads",
	current: threadOpen,
	wanted:  "a change you asked for is still open on this document; once your contact resolves it you can confirm",
}

// DecideAsBuyer records a decision on the published version of a document.
func (s *Store) DecideAsBuyer(ctx context.Context, sess Session, documentID ids.UUID, kind string, note *string) (crmcontracts.DealRoomDecision, error) {
	if sess.ID == ids.Nil {
		return crmcontracts.DealRoomDecision{}, apperrors.ErrPermissionDenied
	}
	if kind != decisionRequestChanges && kind != decisionConfirmVersion {
		return crmcontracts.DealRoomDecision{}, &fieldError{field: "kind", code: "unknown_kind", msg: "kind must be request_changes or confirm_version"}
	}
	var out crmcontracts.DealRoomDecision
	err := s.tx(ctx, func(tx pgx.Tx) error {
		room, err := liveRoomForBuyerWrite(ctx, tx, sess, capabilityReviewer)
		if err != nil {
			return err
		}
		attachmentID, err := publishedDocumentVersion(ctx, tx, sess.RoomID, documentID)
		if err != nil {
			return err
		}
		if kind == decisionConfirmVersion {
			blocking, err := openRequiredThreads(ctx, tx, sess.RoomID, documentID)
			if err != nil {
				return err
			}
			if blocking > 0 {
				return errOpenRequiredThreads
			}
		}
		out, err = recordDecisionTx(ctx, tx, room, sess, documentID, attachmentID, kind, note)
		return err
	})
	return out, err
}

func recordDecisionTx(ctx context.Context, tx pgx.Tx, room crmcontracts.DealRoom, sess Session, documentID, attachmentID ids.UUID, kind string, note *string) (crmcontracts.DealRoomDecision, error) {
	capturedBy, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.DealRoomDecision{}, err
	}
	id := ids.NewV7()
	if _, err := tx.Exec(ctx,
		`INSERT INTO deal_room_decision (id, room_id, document_id, attachment_id, participant_id, kind, note, source, captured_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		id, sess.RoomID, documentID, attachmentID, sess.ParticipantID, kind, note, sourceCredential, capturedBy); err != nil {
		return crmcontracts.DealRoomDecision{}, fmt.Errorf("insert deal room decision: %w", err)
	}
	auditID, err := storekit.Audit(ctx, tx, "create", decisionObject, id, nil,
		map[string]any{fieldRoomID: sess.RoomID.UUID, "document_id": documentID, fieldAttachmentID: attachmentID, "kind": kind})
	if err != nil {
		return crmcontracts.DealRoomDecision{}, fmt.Errorf("audit deal room decision: %w", err)
	}
	recorded := crmcontracts.PublicEventDealRoomDecisionRecorded{
		DealId:       room.DealId,
		DecisionId:   openapi_types.UUID(id),
		DocumentId:   openapi_types.UUID(documentID),
		AttachmentId: openapi_types.UUID(attachmentID),
		Kind:         kind,
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, sess.RoomID.UUID, recorded); err != nil {
		return crmcontracts.DealRoomDecision{}, fmt.Errorf("emit deal_room.decision_recorded: %w", err)
	}
	row := tx.QueryRow(ctx,
		`SELECT d.id, d.room_id, d.document_id, d.attachment_id, d.participant_id, p.full_name, d.kind, d.note, d.created_at
		   FROM deal_room_decision d JOIN deal_room_participant p ON p.id = d.participant_id
		  WHERE d.id = $1 AND d.room_id = $2`, id, sess.RoomID)
	return scanDecision(row)
}
