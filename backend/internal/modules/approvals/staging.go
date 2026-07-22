// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// stagingTTL bounds how long an unactioned staging stays approvable; a
// week-old agent intention should be re-proposed against fresh state.
const stagingTTL = 24 * time.Hour

// StageInput describes one refused 🟡 call to hold for decision.
type StageInput struct {
	Kind           string // the tool name, e.g. advance_deal
	ProposedChange json.RawMessage
	DiffHash       string
	// TargetType + TargetID are the polymorphic reference to the staged
	// action's target (any entity kind); the id stays untyped because the
	// pair is the discriminated reference, not one entity's typed id.
	TargetType    string
	TargetID      ids.UUID
	TargetVersion *int64
	Summary       string
	// JoinPending collapses an identical live proposal under an atomic
	// transaction lock. It is for at-least-once worker paths whose retries
	// must return the existing approval instead of multiplying inbox rows.
	JoinPending bool
	// Announce is an optional kind-specific domain event (e.g.
	// coldstart.read_back_proposed) emitted in the SAME transaction as
	// approval.requested, linked to the same audit row.
	Announce []AnnouncedEvent
}

// AnnouncedEvent is one extra catalog event a staging carries. Payload
// names its own event type (events.Payload.EventType()), the same seam
// storekit.EmitEvent uses — a caller cannot pair the wrong payload with
// an announced event without failing to compile.
type AnnouncedEvent struct {
	Payload events.Payload
}

// Stage records a pending approval for the context's agent principal and
// emits approval.requested. It runs in the write shape every mutation
// uses: approval row + audit row + event in one transaction.
func (s *Service) Stage(ctx context.Context, in StageInput) (ids.ApprovalID, error) {
	var id ids.ApprovalID
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if in.JoinPending {
			id, err = s.stageOrJoinPendingInTx(ctx, tx, in)
		} else {
			id, err = s.StageInTx(ctx, tx, in)
		}
		return err
	})
	return id, err
}

// stageOrJoinPendingInTx serializes one proposal identity and returns its live
// pending approval when another worker already staged it. The transaction
// lock covers the empty-set case that a row lock cannot protect, so replicas
// cannot both observe no pending row and create duplicates.
func (s *Service) stageOrJoinPendingInTx(ctx context.Context, tx pgx.Tx, in StageInput) (ids.ApprovalID, error) {
	var id ids.ApprovalID
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return ids.ApprovalID{}, errors.New("crmapprovals: no workspace bound to context")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(
			'approval_pending:' || $1::text || ':' || $2 || ':' || $3::text || ':' || $4, 0))`,
		wsID, in.Kind, in.TargetID, in.DiffHash); err != nil {
		return ids.ApprovalID{}, fmt.Errorf("lock pending approval identity: %w", err)
	}
	err := tx.QueryRow(ctx, `SELECT id FROM approval
			WHERE workspace_id = $1 AND kind = $2 AND target_entity_id = $3 AND diff_hash = $4
			  AND status = 'pending' AND expires_at > now()
			ORDER BY created_at DESC LIMIT 1`, wsID, in.Kind, in.TargetID, in.DiffHash).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ids.ApprovalID{}, fmt.Errorf("find pending approval identity: %w", err)
	}
	return s.StageInTx(ctx, tx, in)
}

// StageInTx records a proposal through a caller-owned transaction. Compose
// uses it when another module's state transition creates the target the
// proposal refers to, so the target and its separately governed follow-up
// proposals cannot commit only halfway.
func (s *Service) StageInTx(ctx context.Context, tx pgx.Tx, in StageInput) (ids.ApprovalID, error) {
	p, ok := principal.Actor(ctx)
	if !ok {
		return ids.ApprovalID{}, errors.New("crmapprovals: no actor bound to context")
	}
	wsID, _ := principal.WorkspaceID(ctx)
	id := ids.New[ids.ApprovalKind]()
	if _, err := tx.Exec(ctx,
		`INSERT INTO approval (id, workspace_id, kind, proposed_by, on_behalf_of, passport_id,
			                       target_entity_type, target_entity_id, target_version,
			                       summary, proposed_change, diff_hash, expires_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now() + $13::interval)`,
		id, wsID, in.Kind, p.ID, nullUUID(p.OnBehalfOf), nullUUID(p.PassportID),
		nullStr(in.TargetType), nullUUID(in.TargetID), in.TargetVersion,
		nullStr(in.Summary), in.ProposedChange, in.DiffHash, stagingTTL.String()); err != nil {
		return ids.ApprovalID{}, err
	}
	auditID, err := s.audit(ctx, tx, p, "create", id.UUID, map[string]any{
		approvalKeyKind: in.Kind, "summary": in.Summary, "diff_hash": in.DiffHash,
	})
	if err != nil {
		return ids.ApprovalID{}, err
	}
	requested := crmcontracts.WebhookPayloadApprovalRequested{
		Kind:             in.Kind,
		Summary:          in.Summary,
		TargetEntityType: in.TargetType,
		TargetEntityId:   optionalTargetID(in.TargetID),
		ExpiresAt:        s.now().UTC().Add(stagingTTL),
	}
	if err := s.emit(ctx, tx, p, auditID, id.UUID, requested); err != nil {
		return ids.ApprovalID{}, err
	}
	for _, announce := range in.Announce {
		if err := s.emit(ctx, tx, p, auditID, id.UUID, announce.Payload); err != nil {
			return ids.ApprovalID{}, err
		}
	}
	return id, nil
}

// optionalTargetID converts the staging's polymorphic target id to the
// public payload's optional wire type — nil for the zero id (a staging
// with no single target row), never the zero UUID rendered as a value.
func optionalTargetID(id ids.UUID) *openapi_types.UUID {
	if id.IsZero() {
		return nil
	}
	v := openapi_types.UUID(id)
	return &v
}

// HasPendingFor reports whether a live pending staging of this kind,
// target and exact proposed change already sits in the inbox. Stagers
// fed by at-least-once triggers (connector syncs re-hitting the same
// collision) consult it so a recurring trigger cannot multiply
// identical proposals.
func (s *Service) HasPendingFor(ctx context.Context, kind string, targetID ids.UUID, diffHash string) (bool, error) {
	var exists bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM approval
			  WHERE kind = $1 AND target_entity_id = $2 AND diff_hash = $3
			    AND status = 'pending' AND expires_at > now())`,
			kind, targetID, diffHash).Scan(&exists)
	})
	return exists, err
}

// HasPendingKind reports whether a live pending staging of this kind
// sits against the target at all, whatever its proposed change. Nightly
// sweeps whose proposal moves with "today" consult it — a diff-hash
// identity check (HasPendingFor) would let every pass stack a fresh
// staging on one still awaiting decision.
func (s *Service) HasPendingKind(ctx context.Context, kind string, targetID ids.UUID) (bool, error) {
	var exists bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM approval
			  WHERE kind = $1 AND target_entity_id = $2
			    AND status = 'pending' AND expires_at > now())`,
			kind, targetID).Scan(&exists)
	})
	return exists, err
}
