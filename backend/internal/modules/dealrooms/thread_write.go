// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// Writing to the conversation. The transaction bodies are shared by both
// sides — an author is either a participant or a user, and the SQL does not
// care which — while the entry points differ: the seller's take the seat
// gates, the buyer's (store_public_threads.go) take the session.

import (
	"context"
	"fmt"
	"strings"
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

// threadAuthor is who is writing: exactly one of the two is set, which the
// schema's one-author CHECK also holds.
type threadAuthor struct {
	participantID *ids.DealRoomParticipantID
	userID        *ids.UUID
}

func (a threadAuthor) side() string {
	if a.participantID != nil {
		return sideBuyer
	}
	return sideSeller
}

// OpenThreadInput is the validated shape both sides open a thread from.
type OpenThreadInput struct {
	DocumentID     *ids.UUID
	Body           string
	RequiredChange bool
	Source         string
}

// cleanBody trims a comment and refuses an empty one.
func cleanBody(body string) (string, error) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return "", &fieldError{field: "body", code: codeRequired, msg: "a comment needs some words"}
	}
	if len(trimmed) > commentLimit {
		return "", &fieldError{field: "body", code: codeTooLong, msg: fmt.Sprintf("a comment is at most %d characters", commentLimit)}
	}
	return trimmed, nil
}

// commentLimit bounds one comment. Long enough for a real question, short
// enough that the public edge is not a place to store documents.
const commentLimit = 8000

// OpenThread is the seller's side opening a thread.
func (s *Store) OpenThread(ctx context.Context, roomID ids.DealRoomID, in OpenThreadInput) (crmcontracts.DealRoomThread, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionUpdate); err != nil {
		return crmcontracts.DealRoomThread{}, err
	}
	userID, err := writingUser(ctx)
	if err != nil {
		return crmcontracts.DealRoomThread{}, err
	}
	var out crmcontracts.DealRoomThread
	err = s.tx(ctx, func(tx pgx.Tx) error {
		room, err := openRoomForContent(ctx, tx, roomID)
		if err != nil {
			return err
		}
		out, err = openThreadTx(ctx, tx, room, in, threadAuthor{userID: &userID})
		return err
	})
	return out, err
}

// writingUser names the seat behind a seller-side write. A principal with no
// user (an agent acting for nobody, the system) cannot be an author.
func writingUser(ctx context.Context) (ids.UUID, error) {
	p, ok := principal.Actor(ctx)
	if !ok || p.UserID == ids.Nil {
		return ids.Nil, apperrors.ErrPermissionDenied
	}
	return p.UserID, nil
}

// openThreadTx inserts the thread and its first comment, and announces the
// comment. A document thread is pinned to the version the room's manifest
// currently names; a room-level thread has no document and no version.
func openThreadTx(ctx context.Context, tx pgx.Tx, room crmcontracts.DealRoom, in OpenThreadInput, by threadAuthor) (crmcontracts.DealRoomThread, error) {
	capturedBy, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.DealRoomThread{}, err
	}
	roomID := ids.From[ids.DealRoomKind](ids.UUID(room.Id))
	var attachmentID *ids.UUID
	if in.DocumentID != nil {
		id, err := currentVersionOf(ctx, tx, roomID, *in.DocumentID)
		if err != nil {
			return crmcontracts.DealRoomThread{}, err
		}
		attachmentID = &id
	} else if in.RequiredChange {
		return crmcontracts.DealRoomThread{}, &fieldError{field: "required_change", code: "needs_document", msg: "only a thread on a document can require a change"}
	}
	threadID := ids.NewV7()
	if _, err := tx.Exec(ctx,
		`INSERT INTO deal_room_thread (id, room_id, document_id, attachment_id, required_change, author_participant_id, author_user_id, source, captured_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		threadID, roomID, in.DocumentID, attachmentID, in.RequiredChange, by.participantID, by.userID, in.Source, capturedBy); err != nil {
		return crmcontracts.DealRoomThread{}, fmt.Errorf("insert deal room thread: %w", err)
	}
	if _, err := storekit.Audit(ctx, tx, "create", threadObject, threadID, nil,
		map[string]any{fieldRoomID: roomID.UUID, "document_id": in.DocumentID, "required_change": in.RequiredChange}); err != nil {
		return crmcontracts.DealRoomThread{}, fmt.Errorf("audit deal room thread: %w", err)
	}
	first := commentPlacement{threadID: threadID, documentID: in.DocumentID, opensThread: true, requiredChange: in.RequiredChange}
	if err := postCommentTx(ctx, tx, room, first, in.Body, in.Source, by); err != nil {
		return crmcontracts.DealRoomThread{}, err
	}
	return readThread(ctx, tx, roomID, threadID)
}

// currentVersionOf is the attachment the room currently shows for a document.
// Absent for a document of another room, or one removed.
func currentVersionOf(ctx context.Context, tx pgx.Tx, roomID ids.DealRoomID, documentID ids.UUID) (ids.UUID, error) {
	var attachmentID ids.UUID
	err := tx.QueryRow(ctx,
		`SELECT attachment_id FROM deal_room_document WHERE id = $1 AND room_id = $2 AND archived_at IS NULL`,
		documentID, roomID).Scan(&attachmentID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ids.Nil, apperrors.ErrNotFound
		}
		return ids.Nil, fmt.Errorf("read deal room document version: %w", err)
	}
	return attachmentID, nil
}

// commentPlacement says where a comment lands: which thread, on which
// document, and whether it is the thread's first word (which carries the
// thread's required-change flag into the event).
type commentPlacement struct {
	threadID       ids.UUID
	documentID     *ids.UUID
	opensThread    bool
	requiredChange bool
}

// postCommentTx inserts one comment and announces it. The caller has settled
// that the thread is open and the room live.
func postCommentTx(ctx context.Context, tx pgx.Tx, room crmcontracts.DealRoom, at commentPlacement, body, source string, by threadAuthor) error {
	capturedBy, err := storekit.CapturedBy(ctx)
	if err != nil {
		return err
	}
	commentID := ids.NewV7()
	if _, err := tx.Exec(ctx,
		`INSERT INTO deal_room_comment (id, room_id, thread_id, body, author_participant_id, author_user_id, source, captured_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		commentID, ids.UUID(room.Id), at.threadID, body, by.participantID, by.userID, source, capturedBy); err != nil {
		return fmt.Errorf("insert deal room comment: %w", err)
	}
	auditID, err := storekit.Audit(ctx, tx, "create", commentObject, commentID, nil,
		map[string]any{fieldRoomID: ids.UUID(room.Id), "thread_id": at.threadID, fieldSide: by.side()})
	if err != nil {
		return fmt.Errorf("audit deal room comment: %w", err)
	}
	var docID *openapi_types.UUID
	if at.documentID != nil {
		u := openapi_types.UUID(*at.documentID)
		docID = &u
	}
	posted := crmcontracts.PublicEventDealRoomCommentPosted{
		DealId:         room.DealId,
		ThreadId:       openapi_types.UUID(at.threadID),
		CommentId:      openapi_types.UUID(commentID),
		DocumentId:     docID,
		Side:           by.side(),
		OpensThread:    at.opensThread,
		RequiredChange: &at.requiredChange,
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, ids.UUID(room.Id), posted); err != nil {
		return fmt.Errorf("emit deal_room.comment_posted: %w", err)
	}
	return nil
}

// errThreadResolved refuses a reply to a settled thread: reopening is a new
// thread, so the record of what was settled when stays readable.
var errThreadResolved = &stateError{
	code:    "deal_room_thread_resolved",
	current: threadResolved,
	wanted:  "this thread is resolved; open a new one to continue",
}

// replyTx appends to an open thread of the room.
func replyTx(ctx context.Context, tx pgx.Tx, room crmcontracts.DealRoom, threadID ids.UUID, body, source string, by threadAuthor) (crmcontracts.DealRoomThread, error) {
	roomID := ids.From[ids.DealRoomKind](ids.UUID(room.Id))
	if _, err := storekit.LockRow(ctx, tx, threadObject, threadID, storekit.NoArchiveColumn); err != nil {
		return crmcontracts.DealRoomThread{}, err
	}
	current, err := readThread(ctx, tx, roomID, threadID)
	if err != nil {
		return crmcontracts.DealRoomThread{}, err
	}
	if current.State != threadOpen {
		return crmcontracts.DealRoomThread{}, errThreadResolved
	}
	var documentID *ids.UUID
	if current.DocumentId != nil {
		u := ids.UUID(*current.DocumentId)
		documentID = &u
	}
	if err := postCommentTx(ctx, tx, room, commentPlacement{threadID: threadID, documentID: documentID}, body, source, by); err != nil {
		return crmcontracts.DealRoomThread{}, err
	}
	return readThread(ctx, tx, roomID, threadID)
}

// Reply is the seller's side answering in a thread.
func (s *Store) Reply(ctx context.Context, roomID ids.DealRoomID, threadID ids.UUID, body, source string) (crmcontracts.DealRoomThread, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionUpdate); err != nil {
		return crmcontracts.DealRoomThread{}, err
	}
	userID, err := writingUser(ctx)
	if err != nil {
		return crmcontracts.DealRoomThread{}, err
	}
	var out crmcontracts.DealRoomThread
	err = s.tx(ctx, func(tx pgx.Tx) error {
		room, err := openRoomForContent(ctx, tx, roomID)
		if err != nil {
			return err
		}
		out, err = replyTx(ctx, tx, room, threadID, body, source, threadAuthor{userID: &userID})
		return err
	})
	return out, err
}

// ResolveThread closes a thread. Human-only: resolving a required-change
// thread is what unblocks the buyer's confirmation, so a person stands behind
// it. Already resolved answers the thread unchanged.
func (s *Store) ResolveThread(ctx context.Context, roomID ids.DealRoomID, threadID ids.UUID) (crmcontracts.DealRoomThread, error) {
	if err := auth.Require(ctx, roomObject, principal.ActionUpdate); err != nil {
		return crmcontracts.DealRoomThread{}, err
	}
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.DealRoomThread{}, err
	}
	userID, err := writingUser(ctx)
	if err != nil {
		return crmcontracts.DealRoomThread{}, err
	}
	var out crmcontracts.DealRoomThread
	err = s.tx(ctx, func(tx pgx.Tx) error {
		room, err := openRoomForContent(ctx, tx, roomID)
		if err != nil {
			return err
		}
		if _, err := storekit.LockRow(ctx, tx, threadObject, threadID, storekit.NoArchiveColumn); err != nil {
			return err
		}
		current, err := readThread(ctx, tx, roomID, threadID)
		if err != nil {
			return err
		}
		if current.State == threadResolved {
			out = current
			return nil
		}
		p := storekit.NewPatch()
		p.Set("state", threadOpen, threadResolved)
		p.Set("resolved_at", nil, time.Now().UTC())
		p.Set("resolved_by_user_id", nil, userID)
		if err := p.ApplyGuardedIn(ctx, tx, threadObject, threadID, nil, storekit.NoArchiveColumn); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "resolve", threadObject, threadID, p.Before(), p.After())
		if err != nil {
			return fmt.Errorf("audit deal room thread resolve: %w", err)
		}
		resolved := crmcontracts.PublicEventDealRoomThreadResolved{
			DealId:     room.DealId,
			ThreadId:   openapi_types.UUID(threadID),
			DocumentId: current.DocumentId,
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, ids.UUID(room.Id), resolved); err != nil {
			return fmt.Errorf("emit deal_room.thread_resolved: %w", err)
		}
		out, err = readThread(ctx, tx, roomID, threadID)
		return err
	})
	return out, err
}
