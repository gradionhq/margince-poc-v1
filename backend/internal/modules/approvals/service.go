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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

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
