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
	pool *pgxpool.Pool
}

func NewSink(pool *pgxpool.Pool) *Sink {
	return &Sink{pool: pool}
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
			id, created, err := s.upsertActivity(ctx, tx, rec, fields)
			if err != nil {
				return err
			}
			ref = datasource.EntityRef{Type: datasource.EntityActivity, ID: id}
			if !created {
				return nil
			}
			if err := s.linkActivity(ctx, tx, id, rec.Links); err != nil {
				return err
			}
			auditID, err := storekit.Audit(ctx, tx, "create", "activity", id, nil, fields)
			if err != nil {
				return err
			}
			return storekit.Emit(ctx, tx, auditID, "activity.captured", "activity", id, map[string]any{
				"kind": fields.Kind, "source_system": rec.NaturalKey.SourceSystem,
			})
		case LeadFields:
			// The A13 resurrection guard: an erased subject's address
			// refuses re-capture — deletion sticks. The natural key, not
			// the address, names the skip (the log must not re-store PII).
			if fields.Email != "" {
				suppressed, err := storekit.EmailSuppressed(ctx, tx, fields.Email)
				if err != nil {
					return err
				}
				if suppressed {
					return fmt.Errorf("capture: %s/%s matches the erasure suppression list: %w",
						rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID, connector.ErrSkip)
				}
			}
			id, created, err := s.upsertLead(ctx, tx, rec, fields)
			if err != nil {
				return err
			}
			ref = datasource.EntityRef{Type: datasource.EntityLead, ID: id}
			if !created {
				return nil
			}
			auditID, err := storekit.Audit(ctx, tx, "create", "lead", id, nil, fields)
			if err != nil {
				return err
			}
			return storekit.Emit(ctx, tx, auditID, "lead.created", "lead", id, map[string]any{
				"source_system": rec.NaturalKey.SourceSystem,
			})
		default:
			return fmt.Errorf("capture: unmapped Fields type %T for %s", rec.Fields, rec.EntityType)
		}
	})
	if err != nil {
		return datasource.EntityRef{}, err
	}
	return ref, nil
}

func (s *Sink) upsertActivity(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields ActivityFields) (ids.UUID, bool, error) {
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return ids.Nil, false, err
	}
	occurredAt := fields.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
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
