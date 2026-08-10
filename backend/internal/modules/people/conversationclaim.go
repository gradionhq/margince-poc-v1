// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// What was promised, asked and decided in captured conversations (ADR-0097 D1).
//
// ONE store behind four cards. Commitments, open questions, decisions and the
// what-matters rows share a lifecycle — extracted → cited → correctable →
// dismissible — and differ only by kind, so three stores would be three copies
// of the same correction machinery.
//
// GROUNDED OR ABSENT. A claim carries the activity it was read from and the
// verbatim snippet, and this writer refuses one that carries neither: an
// ungrounded candidate is dropped rather than stored, because a claim nobody
// can check against what was actually written is the thing the whole mechanism
// exists to prevent.
//
// The extraction task that will call this is still to come (issue #849). Until
// then the demo seed is its only caller, which is exactly why the seed goes
// through THIS writer rather than SQL: a card filled by rows the real writer
// never produces proves nothing about the real writer.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ClaimInput is one claim to record.
type ClaimInput struct {
	PersonID   ids.PersonID
	Kind       string
	Body       string
	ActivityID ids.UUID
	Quote      string
	DueAt      *time.Time
	Source     string
}

// RecordConversationClaim writes one claim through the audited write shape.
//
// It gates the PERSON and the ACTIVITY separately, because the claim names
// both: citing a message the caller cannot open would disclose that the
// message exists, which is what the activity read protects.
func (s *Store) RecordConversationClaim(ctx context.Context, in ClaimInput) (crmcontracts.ConversationClaim, error) {
	if err := auth.Require(ctx, "person", principal.ActionUpdate); err != nil {
		return crmcontracts.ConversationClaim{}, err
	}
	if in.Body == "" {
		return crmcontracts.ConversationClaim{}, httperr.Validation("body", "required",
			"a claim says something; an empty one is not a claim")
	}
	if in.Quote == "" || in.ActivityID == (ids.UUID{}) {
		// Grounded or absent. Both halves, because a snippet with no source is
		// unverifiable and a source with no snippet cannot be checked against.
		return crmcontracts.ConversationClaim{}, httperr.Validation("source_quote", "required",
			"a claim carries the activity it was read from and the words it was read from — an ungrounded claim is dropped, never stored")
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.ConversationClaim{}, err
	}

	var out crmcontracts.ConversationClaim
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisibleLive(ctx, tx, "person", in.PersonID.UUID); err != nil {
			return err
		}
		// Activities are reachability-scoped rather than row-scoped, so they
		// have their own probe. Live, not merely visible: a claim must not
		// quote a message that has since been archived.
		if err := auth.EnsureActivityVisibleLive(ctx, tx, in.ActivityID); err != nil {
			return err
		}
		var id ids.UUID
		err := tx.QueryRow(ctx, `
			INSERT INTO conversation_claim
				(workspace_id, person_id, kind, body, source_activity_id, source_quote,
				 due_at, evidence_fingerprint, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			RETURNING id`,
			storekit.MustWorkspace(ctx), in.PersonID, in.Kind, in.Body,
			in.ActivityID, in.Quote, in.DueAt,
			claimFingerprint(in), in.Source, by).Scan(&id)
		if err != nil {
			return fmt.Errorf("write the conversation claim: %w", err)
		}
		auditID, err := storekit.Audit(ctx, tx, "create", "person", in.PersonID.UUID, nil,
			map[string]any{"claim_kind": in.Kind, "claim_id": id.String()})
		if err != nil {
			return fmt.Errorf("audit the conversation claim: %w", err)
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, in.PersonID.UUID,
			crmcontracts.PublicEventConversationClaimCaptured{
				ClaimId: openapi_types.UUID(id),
				Kind:    in.Kind,
			}); err != nil {
			return fmt.Errorf("emit conversation_claim.captured: %w", err)
		}
		out = crmcontracts.ConversationClaim{
			Id:               openapi_types.UUID(id),
			Kind:             crmcontracts.ConversationClaimKind(in.Kind),
			Body:             in.Body,
			SourceActivityId: openapi_types.UUID(in.ActivityID),
			SourceQuote:      in.Quote,
			Status:           crmcontracts.ConversationClaimStatusOpen,
			DueAt:            in.DueAt,
			NeedsReview:      false,
		}
		return nil
	})
	return out, err
}

// claimFingerprint pins the evidence a correction is made against. A later
// extraction run that reads the same words from the same message produces the
// same digest, which is what lets a human's correction hold against it.
func claimFingerprint(in ClaimInput) string {
	sum := sha256.Sum256([]byte(in.ActivityID.String() + "\x00" + in.Kind + "\x00" + in.Quote))
	return hex.EncodeToString(sum[:])
}

// ClaimsForPerson reads this person's live claims, newest first.
//
// The activity join is what keeps a claim from outliving its evidence: a claim
// whose source the caller may not read is not returned, because the claim
// would otherwise quote a message the reader has no grant for.
func (s *Store) ClaimsForPerson(ctx context.Context, tx pgx.Tx, personID ids.PersonID, limit int) ([]crmcontracts.ConversationClaim, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	personPos := arg(personID)
	scope, err := auth.ActivityScopeClause(ctx, "a", arg)
	if err != nil {
		return nil, err
	}
	if scope == "" {
		scope = "true"
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT c.id, c.kind, c.body, c.source_activity_id, c.source_quote,
		       a.subject, a.occurred_at, c.status, c.due_at, c.needs_review,
		       c.corrected_at, c.task_activity_id
		FROM conversation_claim c
		JOIN activity a ON a.id = c.source_activity_id AND a.archived_at IS NULL
		WHERE c.person_id = $%d AND c.archived_at IS NULL AND (%s)
		ORDER BY c.created_at DESC
		LIMIT %d`, personPos, scope, limit), args...)
	if err != nil {
		return nil, fmt.Errorf("read the conversation claims: %w", err)
	}
	defer rows.Close()

	out := make([]crmcontracts.ConversationClaim, 0, limit)
	for rows.Next() {
		var claim crmcontracts.ConversationClaim
		var id, activityID ids.UUID
		var taskID *ids.UUID
		var subject *string
		var occurredAt time.Time
		if err := rows.Scan(&id, &claim.Kind, &claim.Body, &activityID, &claim.SourceQuote,
			&subject, &occurredAt, &claim.Status, &claim.DueAt, &claim.NeedsReview,
			&claim.CorrectedAt, &taskID); err != nil {
			return nil, fmt.Errorf("scan a conversation claim: %w", err)
		}
		claim.Id = openapi_types.UUID(id)
		claim.SourceActivityId = openapi_types.UUID(activityID)
		claim.SourceLabel = subject
		claim.OccurredAt = &occurredAt
		if taskID != nil {
			task := openapi_types.UUID(*taskID)
			claim.TaskActivityId = &task
		}
		out = append(out, claim)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the conversation claims: %w", err)
	}
	return out, nil
}

// RecordConversationClaim implements POST /people/{id}/claims.
func (h Handlers) RecordConversationClaim(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req crmcontracts.RecordConversationClaimRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	claim, err := h.store.RecordConversationClaim(r.Context(), ClaimInput{
		PersonID:   ids.From[ids.PersonKind](ids.UUID(id)),
		Kind:       string(req.Kind),
		Body:       req.Body,
		ActivityID: ids.UUID(req.SourceActivityId),
		Quote:      req.SourceQuote,
		DueAt:      req.DueAt,
		Source:     "extraction",
	})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, claim)
}
