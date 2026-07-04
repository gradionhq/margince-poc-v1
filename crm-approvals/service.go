// Package crmapprovals is the 🟡 confirm-first engine (ADR-0036,
// features/07 §8): agents STAGE an action they may not perform, humans
// DECIDE it in the inbox, and the agent REDEEMS the decision by
// re-invoking the identical call. The staged row is the authority
// object — bound to the exact proposed change (diff_hash), the staging
// passport, and the target row's version, consumed exactly once.
package crmapprovals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/fable-poc/crmctx"
	"github.com/gradionhq/fable-poc/internal/pg"
	"github.com/gradionhq/fable-poc/kernel/errs"
	"github.com/gradionhq/fable-poc/kernel/events"
	"github.com/gradionhq/fable-poc/kernel/ids"
)

// stagingTTL bounds how long an unactioned staging stays approvable; a
// week-old agent intention should be re-proposed against fresh state.
const stagingTTL = 24 * time.Hour

// redemptionTTL bounds the approve→redeem window: the human's yes is a
// judgment about the world NOW, not standing authority.
const redemptionTTL = 15 * time.Minute

type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// StageInput describes one refused 🟡 call to hold for decision.
type StageInput struct {
	Kind           string // the tool name, e.g. advance_deal
	ProposedChange json.RawMessage
	DiffHash       string
	TargetType     string
	TargetID       ids.UUID
	TargetVersion  *int64
	Summary        string
}

// Stage records a pending approval for the context's agent principal and
// emits approval.requested. It runs in the write shape every mutation
// uses: approval row + audit row + event in one transaction.
func (s *Service) Stage(ctx context.Context, in StageInput) (ids.UUID, error) {
	p, ok := crmctx.Actor(ctx)
	if !ok {
		return ids.Nil, errors.New("crmapprovals: no actor bound to context")
	}
	wsID, _ := crmctx.WorkspaceID(ctx)

	id := ids.NewV7()
	err := pg.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
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
		auditID, err := s.audit(ctx, tx, p, "create", id, map[string]any{
			"kind": in.Kind, "summary": in.Summary, "diff_hash": in.DiffHash,
		})
		if err != nil {
			return err
		}
		return s.emit(ctx, tx, p, auditID, "approval.requested", id, map[string]any{
			"kind":               in.Kind,
			"summary":            in.Summary,
			"target_entity_type": in.TargetType,
			"target_entity_id":   nullUUID(in.TargetID),
			"expires_at":         time.Now().UTC().Add(stagingTTL),
		})
	})
	return id, err
}

// row is the store shape of one approval.
type row struct {
	ID             ids.UUID
	Kind           string
	Status         string
	ProposedBy     string
	OnBehalfOf     *ids.UUID
	PassportID     *ids.UUID
	TargetType     *string
	TargetID       *ids.UUID
	TargetVersion  *int64
	Summary        *string
	ProposedChange json.RawMessage
	DiffHash       string
	ExpiresAt      time.Time
	DecidedBy      *ids.UUID
	DecidedAt      *time.Time
	ConsumedAt     *time.Time
	CreatedAt      time.Time
}

const columns = `id, kind, status, proposed_by, on_behalf_of, passport_id,
	target_entity_type, target_entity_id, target_version, summary,
	proposed_change, diff_hash, expires_at, decided_by, decided_at, consumed_at, created_at`

func scan(r pgx.Row) (row, error) {
	var a row
	err := r.Scan(&a.ID, &a.Kind, &a.Status, &a.ProposedBy, &a.OnBehalfOf, &a.PassportID,
		&a.TargetType, &a.TargetID, &a.TargetVersion, &a.Summary,
		&a.ProposedChange, &a.DiffHash, &a.ExpiresAt, &a.DecidedBy, &a.DecidedAt, &a.ConsumedAt, &a.CreatedAt)
	return a, err
}

// effectiveStatus folds lazy expiry in: a pending row past its expiry
// reads as expired everywhere without a sweeper process.
func (a row) effectiveStatus() string {
	if a.Status == "pending" && time.Now().After(a.ExpiresAt) {
		return "expired"
	}
	return a.Status
}

// inboxFetchCap bounds how many rows List scans before permission
// filtering; the display limit applies to what survives the filter.
const inboxFetchCap = 200

// List returns the inbox, newest first — but only the approvals the caller
// could themselves decide. Deciding is human work, and so is triage: an
// agent cannot browse the queue of withheld authority, and neither can a
// human who lacks the grant the staged effect needs. Without this filter
// the inbox is a workspace-wide side channel that leaks proposed_change,
// target ids, and diffs to any low-privilege user (C3/ADR-0036).
func (s *Service) List(ctx context.Context, status *string, limit int) ([]row, error) {
	if err := humanOnly(ctx); err != nil {
		return nil, err
	}
	p, _ := crmctx.Actor(ctx)
	if limit <= 0 || limit > inboxFetchCap {
		limit = 50
	}
	var out []row
	err := pg.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := `SELECT ` + columns + ` FROM approval`
		args := []any{}
		if status != nil {
			q += ` WHERE status = $1`
			args = append(args, *status)
		}
		// Fetch up to the hard cap, then filter and truncate to the display
		// limit — decidability is role/target-shaped, not expressible as a
		// single WHERE without joining every object grant.
		q += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT %d`, inboxFetchCap)
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scan(rows)
			if err != nil {
				return err
			}
			if !canDecide(p, a) {
				continue
			}
			out = append(out, a)
			if len(out) >= limit {
				break
			}
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) Get(ctx context.Context, id ids.UUID) (row, error) {
	if err := humanOnly(ctx); err != nil {
		return row{}, err
	}
	p, _ := crmctx.Actor(ctx)
	var a row
	err := pg.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		a, err = get(ctx, tx, id)
		return err
	})
	if err != nil {
		return row{}, err
	}
	// An approval the caller could not decide reads as absent — the same
	// existence-hiding the row-scope convention uses, so Get never becomes
	// a lookup oracle for out-of-scope proposed changes (C3).
	if !canDecide(p, a) {
		return row{}, errs.ErrNotFound
	}
	return a, nil
}

func get(ctx context.Context, tx pgx.Tx, id ids.UUID) (row, error) {
	a, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM approval WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return row{}, errs.ErrNotFound
	}
	return a, err
}

// AlreadyDecidedError maps to 409.
type AlreadyDecidedError struct{ Status string }

func (e *AlreadyDecidedError) Error() string { return "approval is already " + e.Status }

// EditNotSupportedError maps to 422: the edit-then-approve re-gating path
// is specified (ADR-0036) but not built yet — refusing loudly beats
// executing an un-re-gated edit.
type EditNotSupportedError struct{}

func (e *EditNotSupportedError) Error() string {
	return "edited_payload is not supported yet; reject and let the agent re-propose"
}

// Decide approves or rejects one pending approval. The approver must hold
// the RBAC the staged action itself requires — a user cannot green-light
// an effect they could not perform (ADR-0036 "who may approve").
func (s *Service) Decide(ctx context.Context, id ids.UUID, approve bool, reason *string) (row, error) {
	if err := humanOnly(ctx); err != nil {
		return row{}, err
	}
	p, _ := crmctx.Actor(ctx)

	var a row
	err := pg.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		a, err = get(ctx, tx, id)
		if err != nil {
			return err
		}
		if st := a.effectiveStatus(); st != "pending" {
			return &AlreadyDecidedError{Status: st}
		}
		if approve {
			if err := requireDecisionGrants(p, a); err != nil {
				return err
			}
		}

		status, action, verdict := "rejected", "reject", "rejected"
		if approve {
			status, action, verdict = "approved", "approve", "approved"
		}
		if _, err := tx.Exec(ctx,
			`UPDATE approval SET status = $2, decided_by = $3, decided_at = now(), decision_reason = $4
			 WHERE id = $1`,
			id, status, p.UserID, reason); err != nil {
			return err
		}
		auditID, err := s.audit(ctx, tx, p, action, id, map[string]any{
			"kind": a.Kind, "verdict": verdict, "reason": reason,
		})
		if err != nil {
			return err
		}
		if err := s.emit(ctx, tx, p, auditID, "approval.decided", id, map[string]any{
			"kind": a.Kind, "verdict": verdict, "decided_by": p.UserID,
		}); err != nil {
			return err
		}
		a, err = get(ctx, tx, id)
		return err
	})
	return a, err
}

// Redeem consumes one approved staging for exactly the call it was staged
// for: same tool, same diff_hash, same passport, and the target row still
// at the version the human saw. Single-use is enforced by the conditional
// UPDATE — two racing redemptions cannot both pass.
func (s *Service) Redeem(ctx context.Context, id ids.UUID, tool, diffHash string) error {
	p, ok := crmctx.Actor(ctx)
	if !ok {
		return errors.New("crmapprovals: no actor bound to context")
	}
	return pg.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		a, err := get(ctx, tx, id)
		if err != nil {
			// An unknown approval id reads as an invalid token, not a 404:
			// the caller is asserting authority, not browsing.
			return fmt.Errorf("no such approval: %w", errs.ErrApprovalTokenInvalid)
		}
		switch {
		case a.Status != "approved":
			return fmt.Errorf("approval is %s: %w", a.effectiveStatus(), errs.ErrApprovalTokenInvalid)
		case a.ConsumedAt != nil:
			return fmt.Errorf("approval already redeemed: %w", errs.ErrApprovalTokenInvalid)
		case a.DecidedAt != nil && time.Since(*a.DecidedAt) > redemptionTTL:
			return fmt.Errorf("approval expired %s after decision: %w", redemptionTTL, errs.ErrApprovalTokenInvalid)
		case a.Kind != tool:
			return fmt.Errorf("approval is for %s, not %s: %w", a.Kind, tool, errs.ErrApprovalTokenInvalid)
		case a.DiffHash != diffHash:
			return fmt.Errorf("call differs from the approved change: %w", errs.ErrApprovalTokenInvalid)
		case a.PassportID != nil && (p.PassportID.IsZero() || *a.PassportID != p.PassportID):
			return fmt.Errorf("approval was staged by a different passport: %w", errs.ErrApprovalTokenInvalid)
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
					*a.TargetVersion, current, errs.ErrVersionSkew)
			}
		}

		tag, err := tx.Exec(ctx,
			`UPDATE approval SET consumed_at = now() WHERE id = $1 AND consumed_at IS NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("approval already redeemed: %w", errs.ErrApprovalTokenInvalid)
		}
		_, err = s.audit(ctx, tx, p, "update", id, map[string]any{"kind": a.Kind, "redeemed": true})
		return err
	})
}

// versionTables whitelists the tables a target_version re-check may read.
var versionTables = map[string]bool{
	"person": true, "organization": true, "deal": true, "lead": true, "activity": true,
}

func targetVersion(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) (int64, error) {
	if !versionTables[table] {
		return 0, fmt.Errorf("crmapprovals: %q is not a versioned target", table)
	}
	var v int64
	err := tx.QueryRow(ctx, `SELECT version FROM `+table+` WHERE id = $1`, id).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errs.ErrNotFound
	}
	return v, err
}

// decisionGrants maps each stageable kind onto the RBAC the underlying
// effect needs; approving requires every one of them.
var decisionGrants = map[string][]struct {
	Object string
	Action crmctx.Action
}{
	"advance_deal":   {{"deal", crmctx.ActionUpdate}},
	"promote_lead":   {{"lead", crmctx.ActionUpdate}, {"person", crmctx.ActionCreate}},
	"archive_record": {}, // resolved from the target's entity type below
	"merge_records":  {}, // resolved from the target's entity type below
}

// canDecide is the visibility predicate for the inbox: true when p holds
// every grant approving a would require. It shares requireDecisionGrants
// so triage visibility and the decision gate can never drift apart — you
// see exactly what you could act on. An unknown kind (no mapping) is not
// decidable and so not visible: fail-closed.
func canDecide(p crmctx.Principal, a row) bool {
	return requireDecisionGrants(p, a) == nil
}

func requireDecisionGrants(p crmctx.Principal, a row) error {
	grants, known := decisionGrants[a.Kind]
	if !known {
		return fmt.Errorf("crmapprovals: kind %q has no decision-grant mapping", a.Kind)
	}
	if a.Kind == "archive_record" {
		if a.TargetType == nil {
			return errors.New("crmapprovals: archive_record staged without a target type")
		}
		grants = append(grants, struct {
			Object string
			Action crmctx.Action
		}{*a.TargetType, crmctx.ActionDelete})
	}
	// A merge rewrites where records point — the store maps the merge verb to
	// update, so approving needs update on the target's entity type.
	if a.Kind == "merge_records" {
		if a.TargetType == nil {
			return errors.New("crmapprovals: merge_records staged without a target type")
		}
		grants = append(grants, struct {
			Object string
			Action crmctx.Action
		}{*a.TargetType, crmctx.ActionUpdate})
	}
	for _, g := range grants {
		if !p.Permissions.Allows(g.Object, g.Action) {
			return fmt.Errorf("approving %s needs %s.%s: %w", a.Kind, g.Object, g.Action, errs.ErrPermissionDenied)
		}
	}
	return nil
}

// humanOnly guards the inbox and the decision: an agent approving its own
// staged action would collapse the whole tier model.
func humanOnly(ctx context.Context) error {
	p, ok := crmctx.Actor(ctx)
	if !ok {
		return errors.New("crmapprovals: no actor bound to context")
	}
	if p.Type != crmctx.PrincipalHuman {
		return fmt.Errorf("approvals are decided by humans: %w", errs.ErrPermissionDenied)
	}
	return nil
}

// audit appends this module's audit rows — same append-only table, this
// module's own writer (modules do not share store internals).
func (s *Service) audit(ctx context.Context, tx pgx.Tx, p crmctx.Principal, action string, entityID ids.UUID, evidence map[string]any) (ids.UUID, error) {
	wsID, _ := crmctx.WorkspaceID(ctx)
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
func (s *Service) emit(ctx context.Context, tx pgx.Tx, p crmctx.Principal, auditID ids.UUID, eventType string, entityID ids.UUID, payload map[string]any) error {
	wsID, _ := crmctx.WorkspaceID(ctx)
	correlationID, ok := crmctx.CorrelationID(ctx)
	if !ok {
		return errors.New("crmapprovals: no correlation id bound to context")
	}
	env := events.Envelope{
		EventID:     ids.NewV7(),
		Type:        eventType,
		Version:     events.VersionOf(eventType),
		WorkspaceID: wsID,
		OccurredAt:  time.Now().UTC(),
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
