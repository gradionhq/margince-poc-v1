// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Sink is the one connector.Sink implementation — the chokepoint every
// captured record passes on its way into the domain.
type Sink struct {
	pool   *pgxpool.Pool
	stager MergeStager
}

// MergeStager is the dedupe seam: a captured lead colliding with an
// existing record NEVER auto-merges — it stages a 🟡 merge_records
// proposal for the inbox. Compose injects the approvals engine.
type MergeStager interface {
	StageMerge(ctx context.Context, in MergeProposal) (ids.UUID, error)
}

// MergeProposal names the collision: the surviving record and the
// captured fields that would fold into it.
type MergeProposal struct {
	TargetType     string
	TargetID       ids.UUID
	ProposedChange json.RawMessage
	Summary        string
}

func NewSink(pool *pgxpool.Pool) *Sink {
	return &Sink{pool: pool}
}

// WithStager returns a copy wired to the merge-staging path.
func (s *Sink) WithStager(stager MergeStager) *Sink {
	return &Sink{pool: s.pool, stager: stager}
}

var _ connector.Sink = (*Sink)(nil)

// Upsert lands one normalized record: raw original + domain row +
// audit + captured event, one transaction, idempotent on the natural
// key. Replays return the existing row and write NOTHING new — an
// at-least-once sync loop costs no duplicate audit entries.
func (s *Sink) Upsert(ctx context.Context, rec connector.NormalizedRecord) (datasource.EntityRef, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalConnector {
		return datasource.EntityRef{}, errors.New("capture: sink requires a connector principal — the registry builds it, nothing else may")
	}
	if rec.NaturalKey.SourceSystem == "" || rec.NaturalKey.SourceID == "" {
		return datasource.EntityRef{}, errors.New("capture: a natural key is required — unkeyed capture cannot be idempotent")
	}
	if rec.CapturedBy != actor.ID {
		// Provenance comes from the authenticated principal; a connector
		// cannot claim to be another one.
		return datasource.EntityRef{}, fmt.Errorf("capture: captured_by %q does not match the acting connector %q", rec.CapturedBy, actor.ID)
	}

	var ref datasource.EntityRef
	var dedupeHit *ids.UUID
	var dedupeFields json.RawMessage
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if len(rec.Raw) > 0 {
			payload := rec.Raw
			if !json.Valid(payload) {
				// Non-JSON originals are stored as a JSON string so the
				// column type never rejects a provider's format.
				encoded, err := json.Marshal(string(rec.Raw))
				if err != nil {
					return err
				}
				payload = encoded
			}
			// Raw capture is EVIDENCE: append-once, never rewritten. A
			// replay carrying different bytes for the same natural key
			// keeps the original — silently replacing provenance would
			// gut lineage and forensic replay.
			if _, err := tx.Exec(ctx, `
				INSERT INTO raw_capture (workspace_id, source_system, source_id, payload)
				VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)
				ON CONFLICT (workspace_id, source_system, source_id) DO NOTHING`,
				rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID, payload); err != nil {
				return fmt.Errorf("capture: raw store: %w", err)
			}
		}

		switch fields := rec.Fields.(type) {
		case ActivityFields:
			var err error
			ref, err = s.captureActivity(ctx, tx, rec, fields)
			return err
		case LeadFields:
			var err error
			ref, dedupeHit, dedupeFields, err = s.captureLead(ctx, tx, rec, fields)
			return err
		default:
			return fmt.Errorf("capture: unmapped Fields type %T for %s", rec.Fields, rec.EntityType)
		}
	})
	if err != nil {
		return datasource.EntityRef{}, err
	}
	if dedupeHit != nil && s.stager != nil {
		// Staged OUTSIDE the capture transaction on purpose: the capture
		// itself wrote nothing (the collision blocked it), and the
		// proposal must survive independently for the inbox.
		if _, err := s.stager.StageMerge(ctx, MergeProposal{
			TargetType:     "lead",
			TargetID:       *dedupeHit,
			ProposedChange: dedupeFields,
			Summary:        fmt.Sprintf("Captured %s/%s duplicates an existing lead", rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID),
		}); err != nil {
			return datasource.EntityRef{}, fmt.Errorf("capture: staging the dedupe merge: %w", err)
		}
	}
	return ref, nil
}

// captureActivity lands one activity: upsert on the natural key, links,
// audit and event only when the row is new — a replay writes nothing.
func (s *Sink) captureActivity(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields ActivityFields) (datasource.EntityRef, error) {
	id, created, err := s.upsertActivity(ctx, tx, rec, fields)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	ref := datasource.EntityRef{Type: datasource.EntityActivity, ID: id}
	if !created {
		return ref, nil
	}
	if err := s.linkActivity(ctx, tx, id, rec.Links); err != nil {
		return datasource.EntityRef{}, err
	}
	auditID, err := storekit.Audit(ctx, tx, "create", "activity", id, nil, fields)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	if err := storekit.Emit(ctx, tx, auditID, "activity.captured", "activity", id, map[string]any{
		"kind": fields.Kind, "source_system": rec.NaturalKey.SourceSystem,
	}); err != nil {
		return datasource.EntityRef{}, err
	}
	return ref, nil
}

// captureLead lands one lead behind the suppression and dedupe guards.
// A collision with a live lead from another source writes nothing in
// this transaction: it returns the incumbent's ref plus the collision
// (dedupeHit, dedupeFields) for the caller to stage after commit.
func (s *Sink) captureLead(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields LeadFields) (ref datasource.EntityRef, dedupeHit *ids.UUID, dedupeFields json.RawMessage, err error) {
	// Provider payloads carry whitespace; every downstream email
	// comparison (suppression, dedupe, the DB lower()) assumes a
	// trimmed address.
	fields.Email = strings.TrimSpace(fields.Email)
	// The A13 resurrection guard: an erased subject's address
	// refuses re-capture — deletion sticks. The natural key, not
	// the address, names the skip (the log must not re-store PII).
	if fields.Email != "" {
		suppressed, err := storekit.EmailSuppressed(ctx, tx, fields.Email)
		if err != nil {
			return datasource.EntityRef{}, nil, nil, err
		}
		if suppressed {
			return datasource.EntityRef{}, nil, nil, fmt.Errorf("capture: %s/%s matches the erasure suppression list: %w",
				rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID, connector.ErrSkip)
		}
		// Dedupe: an email already on a LIVE lead from a DIFFERENT
		// source is a collision, not a second row — remember it and
		// stage the merge after this transaction commits (a replay
		// of the same natural key is the idempotent path below).
		var existing ids.UUID
		err = tx.QueryRow(ctx, `
			SELECT id FROM lead WHERE email = lower($1) AND archived_at IS NULL
			  AND (source_system IS DISTINCT FROM $2 OR source_id IS DISTINCT FROM $3)`,
			fields.Email, rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID).Scan(&existing)
		if err == nil {
			captured, err := json.Marshal(fields)
			if err != nil {
				return datasource.EntityRef{}, nil, nil, err
			}
			return datasource.EntityRef{Type: datasource.EntityLead, ID: existing}, &existing, captured, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return datasource.EntityRef{}, nil, nil, err
		}
	}
	id, created, err := s.upsertLead(ctx, tx, rec, fields)
	if err != nil {
		return datasource.EntityRef{}, nil, nil, err
	}
	ref = datasource.EntityRef{Type: datasource.EntityLead, ID: id}
	if !created {
		return ref, nil, nil, nil
	}
	auditID, err := storekit.Audit(ctx, tx, "create", "lead", id, nil, fields)
	if err != nil {
		return datasource.EntityRef{}, nil, nil, err
	}
	if err := storekit.Emit(ctx, tx, auditID, "lead.created", "lead", id, map[string]any{
		"source_system": rec.NaturalKey.SourceSystem,
	}); err != nil {
		return datasource.EntityRef{}, nil, nil, err
	}
	return ref, nil, nil, nil
}

func (s *Sink) upsertActivity(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields ActivityFields) (ids.UUID, bool, error) {
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return ids.Nil, false, err
	}
	occurredAt := defaultOccurredAt(fields.OccurredAt)
	var id ids.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO activity (workspace_id, kind, subject, body, occurred_at, direction, source_system, source_id, source, captured_by)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        $1, NULLIF($2, ''), NULLIF($3, ''), $4, NULLIF($5, ''), $6, $7, $8, $9)
		ON CONFLICT (workspace_id, source_system, source_id) WHERE source_system IS NOT NULL AND source_id IS NOT NULL
		DO NOTHING
		RETURNING id`,
		fields.Kind, fields.Subject, fields.Body, occurredAt, fields.Direction,
		rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID, captureSource(rec), rec.CapturedBy).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ids.Nil, false, fmt.Errorf("capture: activity upsert: %w", err)
	}
	// Replay: the natural key already landed — return the incumbent.
	err = tx.QueryRow(ctx,
		`SELECT id FROM activity WHERE source_system = $1 AND source_id = $2`,
		rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID).Scan(&id)
	if err != nil {
		return ids.Nil, false, fmt.Errorf("capture: activity replay lookup: %w", err)
	}
	return id, false, nil
}

func (s *Sink) upsertLead(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields LeadFields) (ids.UUID, bool, error) {
	if err := auth.Require(ctx, "lead", principal.ActionCreate); err != nil {
		return ids.Nil, false, err
	}
	var id ids.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO lead (workspace_id, full_name, email, company_name, title, source_system, source_id, source, captured_by)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        NULLIF($1, ''), NULLIF(lower($2), ''), NULLIF($3, ''), NULLIF($4, ''), $5, $6, $7, $8)
		ON CONFLICT (workspace_id, source_system, source_id) WHERE source_system IS NOT NULL AND source_id IS NOT NULL
		DO NOTHING
		RETURNING id`,
		fields.FullName, fields.Email, fields.CompanyName, fields.Title,
		rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID, captureSource(rec), rec.CapturedBy).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ids.Nil, false, fmt.Errorf("capture: lead upsert: %w", err)
	}
	err = tx.QueryRow(ctx,
		`SELECT id FROM lead WHERE source_system = $1 AND source_id = $2`,
		rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID).Scan(&id)
	if err != nil {
		return ids.Nil, false, fmt.Errorf("capture: lead replay lookup: %w", err)
	}
	return id, false, nil
}

// linkActivity resolves the normalized record's link refs. Every target
// is an FK argument naming a row-scoped record, so every one passes the
// visibility probe (H1) — a connector cannot plant a link to a row its
// granting human could not see.
func (s *Sink) linkActivity(ctx context.Context, tx pgx.Tx, activityID ids.UUID, links []datasource.EntityRef) error {
	for _, link := range links {
		column, ok := map[datasource.EntityType]string{
			datasource.EntityPerson:       "person_id",
			datasource.EntityOrganization: "organization_id",
			datasource.EntityDeal:         "deal_id",
		}[link.Type]
		if !ok {
			return fmt.Errorf("capture: activities cannot link a %s", link.Type)
		}
		if err := auth.EnsureLinkTarget(ctx, tx, string(link.Type), link.ID); err != nil {
			return fmt.Errorf("capture: link target %s %s: %w", link.Type, link.ID, err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`
			INSERT INTO activity_link (workspace_id, activity_id, entity_type, %s)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)`, column),
			activityID, string(link.Type), link.ID); err != nil {
			return fmt.Errorf("capture: linking activity: %w", err)
		}
	}
	return nil
}

// defaultOccurredAt fills a provider payload that carried no timestamp:
// capture time is the honest fallback — better a coarse "when we saw
// it" than a zero time sorting the record to the beginning of history.
func defaultOccurredAt(occurredAt time.Time) time.Time {
	if occurredAt.IsZero() {
		return time.Now().UTC()
	}
	return occurredAt
}

// captureSource is the provenance channel column value; the natural
// key's system is the honest channel name.
func captureSource(rec connector.NormalizedRecord) string {
	if rec.Source != "" {
		return rec.Source
	}
	return rec.NaturalKey.SourceSystem
}

// connectorPrincipalID renders the audit identity for a connector.
func connectorPrincipalID(name string) string {
	return "connector:" + strings.TrimPrefix(name, "connector:")
}
