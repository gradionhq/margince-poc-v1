// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/diffhash"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// AlreadyDecidedError maps to 409.
type AlreadyDecidedError struct{ Status string }

func (e *AlreadyDecidedError) Error() string { return "approval is already " + e.Status }

// InvalidEditError maps to 422: an edited payload that is not a JSON
// object cannot be canonicalized, so it cannot become an authority.
type InvalidEditError struct{ Cause error }

func (e *InvalidEditError) Error() string { return "edited_payload: " + e.Cause.Error() }
func (e *InvalidEditError) Unwrap() error { return e.Cause }

// Decide approves or rejects one pending approval. Both verdicts demand
// the same authority the inbox demands for visibility: the RBAC the
// staged action itself requires plus row-scope visibility of the target —
// a user cannot green-light an effect they could not perform, and a
// rejection is a decision too, not a free action anyone holding a leaked
// UUID may take. An undecidable approval reads as absent, exactly like
// Get, so Decide never becomes the lookup oracle the inbox filter closed.
func (s *Service) Decide(ctx context.Context, id ids.ApprovalID, approve bool, reason *string) (row, error) {
	return s.decide(ctx, id, approve, reason, nil)
}

// DecideEdited is the ADR-0036 §4 modify-then-approve arm: the human's
// edited payload replaces the staged change under a freshly computed
// diff_hash, and the decision's audit row carries BOTH the original
// agent proposal and the human's version. The edited effect re-enters
// admission from scratch by construction: a kind effect executes under
// the APPROVER's principal against the stores' own RBAC gates, and an
// agent redemption only fits the new hash if it re-presents the edited
// call — which the gate re-tiers and re-admits like any other call. The
// old hash, and any token bound to it, no longer opens anything.
func (s *Service) DecideEdited(ctx context.Context, id ids.ApprovalID, edited json.RawMessage) (row, error) {
	if len(edited) == 0 {
		return row{}, &InvalidEditError{Cause: errors.New("empty payload")}
	}
	return s.decide(ctx, id, true, nil, edited)
}

func (s *Service) decide(ctx context.Context, id ids.ApprovalID, approve bool, reason *string, edited json.RawMessage) (row, error) {
	if err := humanOnly(ctx); err != nil {
		return row{}, err
	}
	p, _ := principal.Actor(ctx)

	var a row
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		a, err = s.decideInTx(ctx, tx, p, id, approve, reason, edited)
		return err
	})
	if err != nil {
		return a, err
	}
	// The kind's follow-on effect runs after the decision committed: the
	// approval IS decided either way; an effect failure surfaces to the
	// deciding human (the approved-unredeemed row and its audit trail
	// say exactly how far it got) rather than un-deciding anything.
	if effect, ok := s.effects[a.Kind]; ok && approve {
		if err := effect(ctx, id, a.ProposedChange, a.DiffHash); err != nil {
			return a, fmt.Errorf("approved, but executing the %s effect failed: %w", a.Kind, err)
		}
	}
	return a, err
}

// decideInTx runs the decision inside the caller's transaction: the
// decide-authority + row-scope gate, the pending guard, the optional
// modify-then-approve edit, the status write, and the write shape. It
// returns the re-read row so the follow-on effect runs against committed
// state.
func (s *Service) decideInTx(ctx context.Context, tx pgx.Tx, p principal.Principal, id ids.ApprovalID, approve bool, reason *string, edited json.RawMessage) (row, error) {
	// The row lock makes the pending pre-read and the status write below
	// one race-free unit: two concurrent decisions cannot both pass the
	// pending guard. Taken raw — the approval table has no archived_at,
	// so storekit.LockRow's live filter does not apply here.
	var locked ids.ApprovalID
	if err := tx.QueryRow(ctx, `SELECT id FROM approval WHERE id = $1 FOR UPDATE`, id).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return row{}, apperrors.ErrNotFound
		}
		return row{}, err
	}
	a, err := get(ctx, tx, id)
	if err != nil {
		return row{}, err
	}
	visible, err := decidable(ctx, tx, p, a)
	if err != nil {
		return row{}, err
	}
	if !visible {
		return row{}, apperrors.ErrNotFound
	}
	if st := a.effectiveStatus(s.now()); st != "pending" {
		return row{}, &AlreadyDecidedError{Status: st}
	}

	status, action, verdict := "rejected", "reject", "rejected"
	if approve {
		status, action, verdict = approvalStatusApproved, "approve", approvalStatusApproved
	}
	auditEvidence := map[string]any{
		approvalKeyKind: a.Kind, "verdict": verdict, "reason": reason,
	}
	decidedPayload := crmcontracts.WebhookPayloadApprovalDecided{
		Kind: a.Kind, Verdict: verdict, DecidedBy: openapi_types.UUID(p.UserID),
	}
	if edited != nil {
		if err := applyEditedPayload(ctx, tx, id, edited, a, auditEvidence, &decidedPayload); err != nil {
			return row{}, err
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE approval SET status = $2, decided_by = $3, decided_at = now(), decision_reason = $4
		 WHERE id = $1`,
		id, status, p.UserID, reason); err != nil {
		return row{}, err
	}
	auditID, err := s.audit(ctx, tx, p, action, id.UUID, auditEvidence)
	if err != nil {
		return row{}, err
	}
	if err := s.emit(ctx, tx, p, auditID, id.UUID, decidedPayload); err != nil {
		return row{}, err
	}
	if err := s.emitKindDecided(ctx, tx, p, auditID, id.UUID, a.Kind, approve); err != nil {
		return row{}, err
	}
	return get(ctx, tx, id)
}

// applyEditedPayload is the modify-then-approve write (ADR-0036 §4): the
// human's edited payload replaces the staged change under a freshly
// computed diff_hash, and both sides of the human delta go on the record
// — what the agent proposed, and what the human actually released. The
// decided event carries the human's version, so a suspended agent run
// resumes with THIS call; the original hash no longer opens anything.
func applyEditedPayload(ctx context.Context, tx pgx.Tx, id ids.ApprovalID, edited json.RawMessage, a row, auditEvidence map[string]any, decidedPayload *crmcontracts.WebhookPayloadApprovalDecided) error {
	canonical, editedHash, hashErr := diffhash.Canonical(edited)
	if hashErr != nil {
		return &InvalidEditError{Cause: hashErr}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE approval SET proposed_change = $2, diff_hash = $3 WHERE id = $1`,
		id, canonical, editedHash); err != nil {
		return err
	}
	auditEvidence["edited"] = true
	auditEvidence["original_change"] = json.RawMessage(a.ProposedChange)
	auditEvidence["original_diff_hash"] = a.DiffHash
	auditEvidence["edited_change"] = json.RawMessage(canonical)
	auditEvidence["edited_diff_hash"] = editedHash

	// edited_change stays an OPEN object on the wire (A9): the staged
	// kind's proposed_change shape varies by kind, so the payload carries
	// it as a raw map rather than a narrowly typed struct that would drop
	// a future kind's fields.
	var editedChange map[string]any
	if err := json.Unmarshal(canonical, &editedChange); err != nil {
		return fmt.Errorf("approvals: canonicalized edited change did not decode as a JSON object: %w", err)
	}
	wasEdited := true
	decidedPayload.Edited = &wasEdited
	decidedPayload.DiffHash = &editedHash
	decidedPayload.EditedChange = &editedChange
	return nil
}

// emitKindDecided fires the kind-specific echo of the verdict (e.g. a
// coldstart read-back's approved/rejected event) on the same audit row,
// when the staging's kind registers one.
func (s *Service) emitKindDecided(ctx context.Context, tx pgx.Tx, p principal.Principal, auditID, id ids.UUID, kind string, approve bool) error {
	echo, ok := kindDecidedEvents[kind]
	if !ok {
		return nil
	}
	build := echo.rejected
	if approve {
		build = echo.approved
	}
	return s.emit(ctx, tx, p, auditID, id, build(openapi_types.UUID(id), openapi_types.UUID(p.UserID)))
}
