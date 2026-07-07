// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package approvals is the 🟡 confirm-first engine (ADR-0036,
// features/07 §8): agents STAGE an action they may not perform, humans
// DECIDE it in the inbox, and the agent REDEEMS the decision by
// re-invoking the identical call. The staged row is the authority
// object — bound to the exact proposed change (diff_hash), the staging
// passport, and the target row's version, consumed exactly once.
//
// Tables owned: approval. Imports shared + platform + the generated
// contract only; never a sibling module — the agent surface stages and
// redeems through an adapter injected at the composition root.
package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/diffhash"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// stagingTTL bounds how long an unactioned staging stays approvable; a
// week-old agent intention should be re-proposed against fresh state.
const stagingTTL = 24 * time.Hour

// redemptionTTL bounds the approve→redeem window: the human's yes is a
// judgment about the world NOW, not standing authority.
const redemptionTTL = 15 * time.Minute

type Service struct {
	pool *pgxpool.Pool
	// now is the service's clock: both expiry windows (staging TTL,
	// redemption TTL) are judged against it, so tests can prove the
	// pending→expired and approved→dead transitions without sleeping.
	now func() time.Time
	// effects are the per-kind follow-on executors an approval releases
	// (compose injects them — this module never imports the modules the
	// effects write into). An effect runs AFTER the decision transaction
	// commits, only on approve; exactly-once is the effect's own duty
	// (the redeem-then-execute discipline every 🟡 executor follows).
	effects map[string]ApprovedEffect
}

// ApprovedEffect executes what an approved staging of its kind proposed.
type ApprovedEffect func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, now: time.Now, effects: map[string]ApprovedEffect{}}
}

// WithEffect registers the follow-on executor for one staging kind.
func (s *Service) WithEffect(kind string, effect ApprovedEffect) *Service {
	s.effects[kind] = effect
	return s
}

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
	// Announce is an optional kind-specific domain event (e.g.
	// coldstart.read_back_proposed) emitted in the SAME transaction as
	// approval.requested, linked to the same audit row.
	Announce []AnnouncedEvent
}

// AnnouncedEvent is one extra catalog event a staging carries.
type AnnouncedEvent struct {
	Type    string
	Payload map[string]any
}

// Stage records a pending approval for the context's agent principal and
// emits approval.requested. It runs in the write shape every mutation
// uses: approval row + audit row + event in one transaction.
func (s *Service) Stage(ctx context.Context, in StageInput) (ids.ApprovalID, error) {
	p, ok := principal.Actor(ctx)
	if !ok {
		return ids.ApprovalID{}, errors.New("crmapprovals: no actor bound to context")
	}
	wsID, _ := principal.WorkspaceID(ctx)

	id := ids.New[ids.ApprovalKind]()
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO approval (id, workspace_id, kind, proposed_by, on_behalf_of, passport_id,
			                       target_entity_type, target_entity_id, target_version,
			                       summary, proposed_change, diff_hash, expires_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now() + $13::interval)`,
			id, wsID, in.Kind, p.ID, nullUUID(p.OnBehalfOf), nullUUID(p.PassportID),
			nullStr(in.TargetType), nullUUID(in.TargetID), in.TargetVersion,
			nullStr(in.Summary), in.ProposedChange, in.DiffHash, stagingTTL.String()); err != nil {
			return err
		}
		auditID, err := s.audit(ctx, tx, p, "create", id.UUID, map[string]any{
			"kind": in.Kind, "summary": in.Summary, "diff_hash": in.DiffHash,
		})
		if err != nil {
			return err
		}
		if err := s.emit(ctx, tx, p, auditID, "approval.requested", id.UUID, map[string]any{
			"kind":               in.Kind,
			"summary":            in.Summary,
			"target_entity_type": in.TargetType,
			"target_entity_id":   nullUUID(in.TargetID),
			"expires_at":         s.now().UTC().Add(stagingTTL),
		}); err != nil {
			return err
		}
		for _, announce := range in.Announce {
			if err := s.emit(ctx, tx, p, auditID, announce.Type, id.UUID, announce.Payload); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
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
		status, action, verdict = "approved", "approve", "approved"
	}
	auditEvidence := map[string]any{
		"kind": a.Kind, "verdict": verdict, "reason": reason,
	}
	decidedPayload := map[string]any{
		"kind": a.Kind, "verdict": verdict, "decided_by": p.UserID,
	}
	if edited != nil {
		if err := applyEditedPayload(ctx, tx, id, edited, a, auditEvidence, decidedPayload); err != nil {
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
	if err := s.emit(ctx, tx, p, auditID, "approval.decided", id.UUID, decidedPayload); err != nil {
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
func applyEditedPayload(ctx context.Context, tx pgx.Tx, id ids.ApprovalID, edited json.RawMessage, a row, auditEvidence, decidedPayload map[string]any) error {
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
	decidedPayload["edited"] = true
	decidedPayload["diff_hash"] = editedHash
	decidedPayload["edited_change"] = json.RawMessage(canonical)
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
	eventType := echo.rejected
	if approve {
		eventType = echo.approved
	}
	return s.emit(ctx, tx, p, auditID, eventType, id, map[string]any{
		"approval_id": id, "decided_by": p.UserID,
	})
}

// Redeem consumes one approved staging for exactly the call it was staged
// for: same tool, same diff_hash, same passport, and the target row still
// at the version the human saw. Single-use is enforced by the conditional
// UPDATE — two racing redemptions cannot both pass.
func (s *Service) Redeem(ctx context.Context, id ids.ApprovalID, tool, diffHash string) error {
	p, ok := principal.Actor(ctx)
	if !ok {
		return errors.New("crmapprovals: no actor bound to context")
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		a, err := get(ctx, tx, id)
		if err != nil {
			// An unknown approval id reads as an invalid token, not a 404:
			// the caller is asserting authority, not browsing.
			return fmt.Errorf("no such approval: %w", apperrors.ErrApprovalTokenInvalid)
		}
		switch {
		case a.Status != "approved":
			return fmt.Errorf("approval is %s: %w", a.effectiveStatus(s.now()), apperrors.ErrApprovalTokenInvalid)
		case a.ConsumedAt != nil:
			return fmt.Errorf("approval already redeemed: %w", apperrors.ErrApprovalTokenInvalid)
		case a.DecidedAt != nil && s.now().Sub(*a.DecidedAt) > redemptionTTL:
			return fmt.Errorf("approval expired %s after decision: %w", redemptionTTL, apperrors.ErrApprovalTokenInvalid)
		case a.Kind != tool:
			return fmt.Errorf("approval is for %s, not %s: %w", a.Kind, tool, apperrors.ErrApprovalTokenInvalid)
		case a.DiffHash != diffHash:
			return fmt.Errorf("call differs from the approved change: %w", apperrors.ErrApprovalTokenInvalid)
		case !p.PassportID.IsZero() && a.PassportID == nil:
			// AGENT token redemption (ADR-0055): a staging with no passport
			// binds to no agent, so no agent may consume it. A HUMAN inbox
			// decision is the other redeemer — it reached here through Decide
			// (human-only, decide-authority, pending→approved once), so an
			// unbound, human-staged approval is theirs to consume below.
			return fmt.Errorf("approval is not bound to a passport: %w", apperrors.ErrApprovalTokenInvalid)
		case !p.PassportID.IsZero() && *a.PassportID != ids.From[ids.PassportKind](p.PassportID):
			// An agent may only redeem the passport that staged the approval.
			return fmt.Errorf("approval was staged by a different passport: %w", apperrors.ErrApprovalTokenInvalid)
		}

		if a.TargetVersion != nil && a.TargetID != nil && a.TargetType != nil {
			current, err := targetVersion(ctx, tx, *a.TargetType, *a.TargetID)
			if err != nil {
				return err
			}
			if current != *a.TargetVersion {
				// The world moved since the human saw the diff — their yes
				// no longer covers it (ADR-0036); re-stage.
				return fmt.Errorf("target changed since approval (v%d → v%d): %w",
					*a.TargetVersion, current, apperrors.ErrVersionSkew)
			}
		}

		tag, err := tx.Exec(ctx,
			`UPDATE approval SET consumed_at = now() WHERE id = $1 AND consumed_at IS NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("approval already redeemed: %w", apperrors.ErrApprovalTokenInvalid)
		}
		_, err = s.audit(ctx, tx, p, "update", id.UUID, map[string]any{"kind": a.Kind, "redeemed": true})
		return err
	})
}

// versionTables whitelists the tables a target_version re-check may read:
// every versioned entity type a staging can target under its own table
// name. A type outside this set (e.g. the partner extension, which
// audits on its organization row) cannot be version-pinned — stagers
// must leave TargetVersion nil for it rather than mint a pin redemption
// could never verify.
var versionTables = map[string]bool{
	"person": true, "organization": true, "deal": true, "lead": true, "activity": true,
	"offer": true, "product": true, "list": true, "tag": true, "relationship": true,
}

// TargetVersionCheckable reports whether a staged approval against this
// entity type can carry a target_version pin that Redeem is able to
// re-verify (ADR-0036 §2).
func TargetVersionCheckable(entityType string) bool {
	return versionTables[entityType]
}

func targetVersion(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) (int64, error) {
	if !versionTables[table] {
		return 0, fmt.Errorf("crmapprovals: %q is not a versioned target", table)
	}
	var v int64
	err := tx.QueryRow(ctx, `SELECT version FROM `+table+` WHERE id = $1`, id).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, apperrors.ErrNotFound
	}
	return v, err
}

// audit appends this module's audit rows — same append-only table, this
// module's own writer (modules do not share store internals).
func (s *Service) audit(ctx context.Context, tx pgx.Tx, p principal.Principal, action string, entityID ids.UUID, evidence map[string]any) (ids.UUID, error) {
	wsID, _ := principal.WorkspaceID(ctx)
	raw, err := json.Marshal(evidence)
	if err != nil {
		return ids.Nil, err
	}
	id := ids.NewV7()
	_, err = tx.Exec(ctx,
		`INSERT INTO audit_log (id, workspace_id, actor_type, actor_id, passport_id, on_behalf_of, action, entity_type, entity_id, evidence)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'approval', $8, $9)`,
		id, wsID, string(p.Type), p.ID, nullUUID(p.PassportID), nullUUID(p.OnBehalfOf),
		action, entityID, raw)
	return id, err
}

// emit stages one approval.* event in the transactional outbox, complete
// envelope, exactly like every other module's writes.
func (s *Service) emit(ctx context.Context, tx pgx.Tx, p principal.Principal, auditID ids.UUID, eventType string, entityID ids.UUID, payload map[string]any) error {
	wsID, _ := principal.WorkspaceID(ctx)
	correlationID, ok := principal.CorrelationID(ctx)
	if !ok {
		return errors.New("crmapprovals: no correlation id bound to context")
	}
	env := events.Envelope{
		EventID:     ids.NewV7(),
		Type:        eventType,
		Version:     events.VersionOf(eventType),
		WorkspaceID: wsID,
		OccurredAt:  s.now().UTC(),
		Actor: events.Actor{
			Type: string(p.Type), ID: p.ID,
			PassportID: nullUUID(p.PassportID), OnBehalfOf: nullUUID(p.OnBehalfOf),
		},
		Entity: events.EntityRef{Type: "approval", ID: entityID},
		Trace:  events.Trace{CorrelationID: correlationID, AuditLogID: auditID},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env.Payload = raw
	stream, err := events.StreamFor(eventType)
	if err != nil {
		return err
	}
	if err := env.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO event_outbox (stream, envelope) VALUES ($1, $2)`, stream, body)
	return err
}

func nullUUID(id ids.UUID) *ids.UUID {
	if id.IsZero() {
		return nil
	}
	return &id
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
