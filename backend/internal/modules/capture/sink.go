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
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
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
	pool       *pgxpool.Pool
	stager     MergeStager
	exclusions ExclusionRules
	ensurer    CounterpartyEnsurer
	freemail   *FreemailList
}

// fieldSourceSystem is the shared audit/event key for the originating
// system — the capture chain's provenance channel.
const fieldSourceSystem = "source_system"

// MergeStager is the dedupe seam: a captured lead colliding with an
// existing record NEVER auto-merges — it stages a 🟡 merge_records
// proposal for the inbox. Compose injects the approvals engine.
type MergeStager interface {
	// note: the returned id is the staged approval's — it stays untyped
	// because the approvals engine behind this seam is the caller's, not
	// this module's, and the value is discarded here.
	StageMerge(ctx context.Context, in MergeProposal) (ids.UUID, error)
}

// MergeProposal names the collision: the surviving record and the
// captured fields that would fold into it.
type MergeProposal struct {
	// note: TargetType + TargetID are the polymorphic pair the approvals
	// merge target carries — this is a discriminated ref, not a single
	// entity's id, so it stays untyped (kernel Ref semantics).
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
	c := *s
	c.stager = stager
	return &c
}

// WithExclusions returns a copy wired to the RC-2 personal-mail exclusion
// gate (CAP-DDL-3): before any write, a record matching the capturing
// user's rules produces zero rows and one capture.skipped event.
func (s *Sink) WithExclusions(rules ExclusionRules) *Sink {
	c := *s
	c.exclusions = rules
	return &c
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

	// The RC-2 exclusion gate runs BEFORE any write — including the raw
	// original — so an excluded (personal) message leaves ZERO rows
	// anywhere and the skip is the only trace (AC1.3, EVT-SEM-10). It lives
	// here, in the ONE writer, so every connector inherits it.
	if err := s.gateExclusion(ctx, rec); err != nil {
		return datasource.EntityRef{}, err
	}

	var ref datasource.EntityRef
	var dedupeHit *ids.LeadID
	var dedupeFields json.RawMessage
	var activityCreated bool
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
			ref, activityCreated, err = s.captureActivity(ctx, tx, rec, fields)
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
	if activityCreated {
		// Auto-create runs AFTER the activity committed, in its own
		// transaction: the timeline row is never lost to a resolver fault,
		// and a fault here is logged for the nightly reconcile, not
		// surfaced as a capture failure (the 60s p95 already delivered).
		s.ensureCounterparty(ctx, rec, ref)
	}
	if dedupeHit != nil && s.stager != nil {
		// Staged OUTSIDE the capture transaction on purpose: the capture
		// itself wrote nothing (the collision blocked it), and the
		// proposal must survive independently for the inbox.
		if _, err := s.stager.StageMerge(ctx, MergeProposal{
			TargetType:     "lead",
			TargetID:       dedupeHit.UUID,
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
func (s *Sink) captureActivity(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields ActivityFields) (datasource.EntityRef, bool, error) {
	id, created, err := s.upsertActivity(ctx, tx, rec, fields)
	if err != nil {
		return datasource.EntityRef{}, false, err
	}
	ref := datasource.EntityRef{Type: datasource.EntityActivity, ID: id.UUID}
	if !created {
		return ref, false, nil
	}
	if err := s.linkActivity(ctx, tx, id, rec.Links); err != nil {
		return datasource.EntityRef{}, false, err
	}
	auditID, err := storekit.Audit(ctx, tx, "create", "activity", id.UUID, nil, fields)
	if err != nil {
		return datasource.EntityRef{}, false, err
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, activityCaptureEventPayload(fields.Kind, rec.NaturalKey.SourceSystem)); err != nil {
		return datasource.EntityRef{}, false, err
	}
	if err := s.emitReply(ctx, tx, auditID, id, rec, fields); err != nil {
		return datasource.EntityRef{}, false, err
	}
	return ref, true, nil
}

// activityCaptureEventPayload builds the activity.captured event for the
// capture ingestion path — the one emit site (of the event's two) that
// names an originating source system; the direct-log path
// (activities/activity.go) sets no fields but kind.
func activityCaptureEventPayload(kind, sourceSystem string) crmcontracts.PublicEventActivityCaptured {
	return crmcontracts.PublicEventActivityCaptured{Kind: kind, SourceSystem: &sourceSystem}
}

// emitReply is CAP-FORMULA-1: an INBOUND message in a thread we previously
// wrote OUTBOUND in is a reply — the engagement signal scoring feeds on.
// Emitted only when the activity row is new, so the at-least-once sync loop
// cannot double-fire it; never a subject heuristic.
func (s *Sink) emitReply(ctx context.Context, tx pgx.Tx, auditID ids.UUID, id ids.ActivityID, rec connector.NormalizedRecord, fields ActivityFields) error {
	if fields.Direction != "inbound" || rec.ThreadKey == "" {
		return nil
	}
	var matched ids.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM activity
		WHERE thread_key = $1 AND direction = 'outbound' AND id <> $2
		ORDER BY occurred_at DESC LIMIT 1`,
		rec.ThreadKey, id).Scan(&matched)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("capture: reply detection: %w", err)
	}
	// contact_id resolves when the counterparty is already a person (the
	// normal reply case — the outbound leg's ensure created them); a
	// first-ever counterparty resolves in the follow-up ensure instead.
	var contactID *ids.PersonID
	if cp := strings.ToLower(strings.TrimSpace(rec.Counterparty.Email)); cp != "" {
		var personID ids.PersonID
		err := tx.QueryRow(ctx, `
			SELECT person_id FROM person_email WHERE email = $1 AND archived_at IS NULL
			ORDER BY is_primary DESC LIMIT 1`, cp).Scan(&personID)
		if err == nil {
			contactID = &personID
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("capture: reply contact lookup: %w", err)
		}
	}
	idempotencyKey := rec.NaturalKey.SourceSystem + ":" + rec.NaturalKey.SourceID
	payload := engagementReplyPayload(matched, defaultOccurredAt(fields.OccurredAt), idempotencyKey, contactID)
	return storekit.EmitEvent(ctx, tx, auditID, id.UUID, payload)
}

// engagementReplyPayload builds the engagement.reply event — the reply's
// contact_id is carried only when the counterparty already resolves to a
// known person (absent, not null, otherwise).
func engagementReplyPayload(matched ids.UUID, occurredAt time.Time, idempotencyKey string, contactID *ids.PersonID) crmcontracts.PublicEventEngagementReply {
	payload := crmcontracts.PublicEventEngagementReply{
		MatchedOutboundActivityId: openapi_types.UUID(matched),
		Channel:                   "email",
		OccurredAt:                occurredAt,
		IdempotencyKey:            idempotencyKey,
	}
	if contactID != nil {
		id := openapi_types.UUID(contactID.UUID)
		payload.ContactId = &id
	}
	return payload
}

// captureLead lands one lead behind the suppression and dedupe guards.
// A collision with a live lead from another source writes nothing in
// this transaction: it returns the incumbent's ref plus the collision
// (the incumbent's id and the captured fields) for the caller to stage
// after commit.
func (s *Sink) captureLead(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields LeadFields) (datasource.EntityRef, *ids.LeadID, json.RawMessage, error) {
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
		var existing ids.LeadID
		err = tx.QueryRow(ctx, `
			SELECT id FROM lead WHERE email = lower($1) AND archived_at IS NULL
			  AND (source_system IS DISTINCT FROM $2 OR source_id IS DISTINCT FROM $3)`,
			fields.Email, rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID).Scan(&existing)
		if err == nil {
			captured, err := json.Marshal(fields)
			if err != nil {
				return datasource.EntityRef{}, nil, nil, err
			}
			return datasource.EntityRef{Type: datasource.EntityLead, ID: existing.UUID}, &existing, captured, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return datasource.EntityRef{}, nil, nil, err
		}
	}
	id, created, err := s.upsertLead(ctx, tx, rec, fields)
	if err != nil {
		return datasource.EntityRef{}, nil, nil, err
	}
	ref := datasource.EntityRef{Type: datasource.EntityLead, ID: id.UUID}
	if !created {
		return ref, nil, nil, nil
	}
	auditID, err := storekit.Audit(ctx, tx, "create", "lead", id.UUID, nil, fields)
	if err != nil {
		return datasource.EntityRef{}, nil, nil, err
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, leadCreatedCapturePayload(rec.NaturalKey.SourceSystem)); err != nil {
		return datasource.EntityRef{}, nil, nil, err
	}
	return ref, nil, nil, nil
}

// leadCreatedCapturePayload builds the lead.created event for the
// capture auto-create path — the one emit site (of the event's two)
// that names an originating source system; the direct-create path
// (people/lead.go) sets no fields at all.
func leadCreatedCapturePayload(sourceSystem string) crmcontracts.PublicEventLeadCreated {
	return crmcontracts.PublicEventLeadCreated{SourceSystem: &sourceSystem}
}

func (s *Sink) upsertActivity(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields ActivityFields) (ids.ActivityID, bool, error) {
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return ids.ActivityID{}, false, err
	}
	occurredAt := defaultOccurredAt(fields.OccurredAt)
	var id ids.ActivityID
	err := tx.QueryRow(ctx, `
		INSERT INTO activity (workspace_id, kind, subject, body, occurred_at, direction, source_system, source_id, source, captured_by, thread_key)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        $1, NULLIF($2, ''), NULLIF($3, ''), $4, NULLIF($5, ''), $6, $7, $8, $9, NULLIF($10, ''))
		ON CONFLICT (workspace_id, source_system, source_id) WHERE source_system IS NOT NULL AND source_id IS NOT NULL
		DO NOTHING
		RETURNING id`,
		fields.Kind, fields.Subject, fields.Body, occurredAt, fields.Direction,
		rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID, captureSource(rec), rec.CapturedBy, rec.ThreadKey).Scan(&id)
	if err == nil {
		// Field-level provenance (B-E02.12) for the content fields this
		// capture set — same source/author the row itself carries.
		var stamps []storekit.FieldStamp
		for _, f := range []struct{ field, value string }{
			{"subject", fields.Subject}, {"body", fields.Body}, {"direction", fields.Direction},
		} {
			if f.value != "" {
				stamps = append(stamps, storekit.FieldStamp{Field: f.field})
			}
		}
		if err := storekit.StampFields(ctx, tx, "activity", id.UUID, captureSource(rec), rec.CapturedBy, stamps); err != nil {
			return ids.ActivityID{}, false, err
		}
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ids.ActivityID{}, false, fmt.Errorf("capture: activity upsert: %w", err)
	}
	// Replay: the natural key already landed — return the incumbent.
	err = tx.QueryRow(ctx,
		`SELECT id FROM activity WHERE source_system = $1 AND source_id = $2`,
		rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID).Scan(&id)
	if err != nil {
		return ids.ActivityID{}, false, fmt.Errorf("capture: activity replay lookup: %w", err)
	}
	return id, false, nil
}

func (s *Sink) upsertLead(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields LeadFields) (ids.LeadID, bool, error) {
	if err := auth.Require(ctx, "lead", principal.ActionCreate); err != nil {
		return ids.LeadID{}, false, err
	}
	var id ids.LeadID
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
		var stamps []storekit.FieldStamp
		for _, f := range []struct{ field, value string }{
			{"full_name", fields.FullName},
			{"email", fields.Email},
			{"company_name", fields.CompanyName},
			{"title", fields.Title},
		} {
			if f.value != "" {
				stamps = append(stamps, storekit.FieldStamp{Field: f.field})
			}
		}
		if err := storekit.StampFields(ctx, tx, "lead", id.UUID, captureSource(rec), rec.CapturedBy, stamps); err != nil {
			return ids.LeadID{}, false, err
		}
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ids.LeadID{}, false, fmt.Errorf("capture: lead upsert: %w", err)
	}
	err = tx.QueryRow(ctx,
		`SELECT id FROM lead WHERE source_system = $1 AND source_id = $2`,
		rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID).Scan(&id)
	if err != nil {
		return ids.LeadID{}, false, fmt.Errorf("capture: lead replay lookup: %w", err)
	}
	return id, false, nil
}

// linkActivity resolves the normalized record's link refs. Every target
// is an FK argument naming a row-scoped record, so every one passes the
// visibility probe (H1) — a connector cannot plant a link to a row its
// granting human could not see.
func (s *Sink) linkActivity(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, links []datasource.EntityRef) error {
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
