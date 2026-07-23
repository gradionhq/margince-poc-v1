// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// Identity is the proposal's logical identity — a JSON object contained
	// in ProposedChange (e.g. {"from_currency":"GBP"}). Requires JoinPending:
	// staging then serializes per identity instead of per diff hash, and any
	// OTHER live pending proposal of the same kind+target carrying this
	// identity is withdrawn (forced expiry, audited) — a fresher diff for one
	// identity supersedes a stale one instead of competing with it in the
	// inbox, where approving stale-after-fresh would restore an outdated value.
	Identity json.RawMessage
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
	if len(in.Identity) > 0 {
		if !in.JoinPending {
			return ids.ApprovalID{}, errors.New("crmapprovals: Identity staging requires JoinPending")
		}
		canonical, err := canonicalIdentity(in.Identity, in.ProposedChange)
		if err != nil {
			return ids.ApprovalID{}, err
		}
		in.Identity = canonical
	}
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

// canonicalIdentity validates and canonicalizes a staging identity. It must
// be a non-empty JSON object whose values are all STRINGS — the logical key
// of a sheet row is always a string (currency code, provider/model id), and
// restricting to strings sidesteps JSON number ambiguity entirely: 1, 1.0,
// 1e0 and a 40-digit integer would each hash to a different advisory lock
// while PostgreSQL jsonb containment compares them as one numeric value, so a
// numeric identity could bypass the per-identity lock yet still supersede by
// value. Strings have no such gap — exact bytes both places. Every field must
// also equal (present, same string) the corresponding field of
// ProposedChange, since an identity the payload does not carry could never
// containment-match and would silently disable supersession. Re-marshaling
// canonicalizes key order and spacing so the lock and the containment agree
// on what "same identity" means across callers.
func canonicalIdentity(identity, proposedChange json.RawMessage) (json.RawMessage, error) {
	idFields, err := decodeJSONObject(identity)
	if err != nil || len(idFields) == 0 {
		return nil, errors.New("crmapprovals: Identity must be a non-empty JSON object")
	}
	payload, err := decodeJSONObject(proposedChange)
	if err != nil {
		return nil, errors.New("crmapprovals: Identity staging requires a JSON-object ProposedChange")
	}
	canonical := make(map[string]string, len(idFields))
	for field, want := range idFields {
		wantStr, ok := want.(string)
		if !ok {
			return nil, fmt.Errorf("crmapprovals: Identity field %q must be a string", field)
		}
		// Membership is checked separately from value: a missing key and an
		// explicit null both read back as a nil any, but jsonb containment
		// treats {"k":null} as present-and-null — an identity asserting a field
		// the payload omits would pass and then never containment-match.
		got, ok := payload[field]
		if !ok || got != want {
			return nil, fmt.Errorf("crmapprovals: Identity field %q is not carried by ProposedChange", field)
		}
		canonical[field] = wantStr
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("crmapprovals: canonicalize Identity: %w", err)
	}
	return raw, nil
}

// decodeJSONObject unmarshals exactly one JSON object with lossless numbers
// (UseNumber keeps a numeric value as its exact decimal text, not a float64).
// A non-object (array, scalar, null) is an error, and so is any trailing data
// after the object — Identity/ProposedChange is ONE object, not a stream, and
// silently reading only the first of several values would validate against a
// payload the rest of the input contradicts.
func decodeJSONObject(raw json.RawMessage) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("not a JSON object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected trailing data after JSON object")
	}
	return m, nil
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
	// The lock serializes one proposal identity: the diff hash by default, the
	// logical Identity when set — two workers proposing DIFFERENT diffs for one
	// identity must not interleave between the join-check and the supersede.
	discriminator := in.DiffHash
	if len(in.Identity) > 0 {
		discriminator = string(in.Identity)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(
			'approval_pending:' || $1::text || ':' || $2 || ':' || $3::text || ':' || $4, 0))`,
		wsID, in.Kind, in.TargetID, discriminator); err != nil {
		return ids.ApprovalID{}, fmt.Errorf("lock pending approval identity: %w", err)
	}
	err := tx.QueryRow(ctx, `SELECT id FROM approval
			WHERE workspace_id = $1 AND kind = $2 AND target_entity_id = $3 AND diff_hash = $4
			  AND status = 'pending' AND expires_at > now()
			ORDER BY created_at DESC LIMIT 1`, wsID, in.Kind, in.TargetID, in.DiffHash).Scan(&id)
	switch {
	case err == nil:
	case errors.Is(err, pgx.ErrNoRows):
		if id, err = s.StageInTx(ctx, tx, in); err != nil {
			return ids.ApprovalID{}, err
		}
	default:
		return ids.ApprovalID{}, fmt.Errorf("find pending approval identity: %w", err)
	}
	if len(in.Identity) > 0 {
		if err := s.supersedePendingInTx(ctx, tx, wsID, in, id); err != nil {
			return ids.ApprovalID{}, err
		}
	}
	return id, nil
}

// supersedePendingInTx withdraws every OTHER live pending proposal of the same
// kind+target carrying the same logical identity. Withdrawal is forced expiry,
// audited but deliberately event-free: the closed event catalog (contract-first,
// P3) defines no approval-withdrawn type, and expiry is already invisible on the
// bus — a subscriber cannot observe TTL expiry either, so folding supersession
// into expiry changes nothing a consumer could rely on, while the pull-based
// inbox reads the row as expired on every surface (effectiveStatus, decide,
// redeem). The status CHECK and the public ApprovalStatus enum stay closed; the
// audit row carries the why and the survivor.
func (s *Service) supersedePendingInTx(ctx context.Context, tx pgx.Tx, wsID ids.UUID, in StageInput, survivor ids.ApprovalID) error {
	p, ok := principal.Actor(ctx)
	if !ok {
		return errors.New("crmapprovals: no actor bound to context")
	}
	// Backdating a full day (not a second) keeps the row expired under the
	// APP clock too: effectiveStatus judges expiry with the service clock,
	// which may trail the database by ordinary NTP skew — never by a day.
	rows, err := tx.Query(ctx, `
		UPDATE approval SET expires_at = now() - interval '1 day'
		WHERE workspace_id = $1 AND kind = $2 AND target_entity_id = $3
		  AND status = 'pending' AND expires_at > now()
		  AND id <> $4 AND proposed_change @> $5
		RETURNING id`, wsID, in.Kind, in.TargetID, survivor, in.Identity)
	if err != nil {
		return fmt.Errorf("supersede pending approvals: %w", err)
	}
	superseded, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return fmt.Errorf("collect superseded approvals: %w", err)
	}
	for _, old := range superseded {
		if _, err := s.audit(ctx, tx, p, "update", old, map[string]any{
			approvalKeyKind: in.Kind, "superseded": true, "superseded_by": survivor.UUID,
		}); err != nil {
			return fmt.Errorf("audit superseded approval: %w", err)
		}
	}
	return nil
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
	// Compute ONE absolute expiry and use it for BOTH the persisted row and
	// the payload — deriving the row's expires_at from the DB now() while the
	// payload used the app clock let approval.requested.data.expires_at drift
	// from what the approval row actually stored.
	expiresAt := s.now().UTC().Add(stagingTTL)
	if _, err := tx.Exec(ctx,
		`INSERT INTO approval (id, workspace_id, kind, proposed_by, on_behalf_of, passport_id,
			                       target_entity_type, target_entity_id, target_version,
			                       summary, proposed_change, diff_hash, expires_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		id, wsID, in.Kind, p.ID, nullUUID(p.OnBehalfOf), nullUUID(p.PassportID),
		nullStr(in.TargetType), nullUUID(in.TargetID), in.TargetVersion,
		nullStr(in.Summary), in.ProposedChange, in.DiffHash, expiresAt); err != nil {
		return ids.ApprovalID{}, err
	}
	auditID, err := s.audit(ctx, tx, p, "create", id.UUID, map[string]any{
		approvalKeyKind: in.Kind, "summary": in.Summary, "diff_hash": in.DiffHash,
	})
	if err != nil {
		return ids.ApprovalID{}, err
	}
	requested := crmcontracts.PublicEventApprovalRequested{
		Kind:             in.Kind,
		Summary:          in.Summary,
		TargetEntityType: in.TargetType,
		TargetEntityId:   optionalTargetID(in.TargetID),
		ExpiresAt:        expiresAt,
	}
	if err := s.emit(ctx, tx, p, auditID, id.UUID, requested); err != nil {
		return ids.ApprovalID{}, err
	}
	for _, announce := range in.Announce {
		// emit() forces the entity type to "approval" (an announced event is
		// an approval-scoped echo). A nil payload would panic on EventType(),
		// and a non-approval payload would be mislabeled and misrouted at
		// fan-out — so refuse both rather than emit an unroutable envelope.
		if announce.Payload == nil {
			return ids.ApprovalID{}, errors.New("crmapprovals: announced event has no payload")
		}
		if entityType := announce.Payload.EntityType(); entityType != "approval" {
			return ids.ApprovalID{}, fmt.Errorf("crmapprovals: announced event payload has entity type %q, want approval", entityType)
		}
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
