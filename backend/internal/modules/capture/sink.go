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

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Sink is the one connector.Sink implementation — the chokepoint every
// captured record passes on its way into the domain.
type Sink struct {
	pool           *pgxpool.Pool
	stager         MergeStager
	ensurer        CounterpartyEnsurer
	channelEnsurer ChannelCounterpartyEnsurer
	transactional  *TransactionalList
}

// fieldSourceSystem / fieldSourceID are the shared system_log detail keys for
// the natural key of the record a capture breadcrumb is about.
const (
	fieldSourceSystem = "source_system"
	fieldSourceID     = "source_id"
	fieldReason       = "reason"
)

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
	switch shape := counterpartyShapeOf(rec.Counterparty); shape {
	case shapeAmbiguous:
		return datasource.EntityRef{}, ErrCounterpartyNamedTwice
	case shapeHalfChannel:
		return datasource.EntityRef{}, ErrChannelIdentityIncomplete
	case shapeNone, shapeMail, shapeChannel:
		// Well-formed; the channel arm is gated inside the transaction below.
	default:
		// A shape added to the enum without an arm here. Refusing it at THIS
		// edge — before the transaction opens — is what keeps the refusal cheap:
		// every downstream switch over the shape runs mid-transaction, after the
		// activity, its audit row and its captured event are written, so a
		// refusal there would fail the whole capture and hand the connector a
		// deterministic error it retries forever (sinkensure.go states what that
		// poison pill costs a mailbox). Admission is the one place a shape this
		// module cannot classify can be turned away for nothing.
		return datasource.EntityRef{}, fmt.Errorf("capture: unhandled counterparty shape %d", shape)
	}

	var ref datasource.EntityRef
	var dedupeHit *ids.LeadID
	var dedupeFields json.RawMessage
	var activityCreated bool
	var decision counterpartyDecision
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// A channel record's account id IS personal data, and THIS transaction is
		// the one that makes it durable — so the erasure is excluded here, under
		// the account's own lock, and not only at the ingress edge that admitted
		// the update. sinkchannel.go states what landing inside an erasure costs.
		if err := s.refuseErasedChannelAccount(ctx, tx, rec.Counterparty); err != nil {
			return err
		}
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
			ref, activityCreated, decision, err = s.captureActivity(ctx, tx, rec, fields)
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
		// The tier ladder already decided, and recorded its decision, inside
		// the transaction above. Creation runs AFTER that commit, in its own
		// transaction: the timeline row is never lost to a resolver fault, and
		// a fault here is logged for the nightly reconcile rather than
		// surfaced as a capture failure (the 60s p95 already delivered).
		s.ensureCounterparty(ctx, rec, ref, decision)
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
func (s *Sink) captureActivity(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields ActivityFields) (datasource.EntityRef, bool, counterpartyDecision, error) {
	id, created, err := s.upsertActivity(ctx, tx, rec, fields)
	if err != nil {
		return datasource.EntityRef{}, false, counterpartyDecision{}, err
	}
	ref := datasource.EntityRef{Type: datasource.EntityActivity, ID: id.UUID}
	if !created {
		return ref, false, counterpartyDecision{}, nil
	}
	if err := s.linkActivity(ctx, tx, id, rec.Links); err != nil {
		return datasource.EntityRef{}, false, counterpartyDecision{}, err
	}
	// Who was in it (ACT-DDL-3). Stamped here, beside the links, because the
	// connector principal bound to THIS context is the only place the mailbox
	// owner is known — every consumer downstream sees an activity whose
	// captured_by reads `connector:gmail` and cannot recover the human behind
	// it. The participant rows are the record of that fact.
	if err := stampCaptureParticipants(ctx, tx, id, actorUserID(ctx), fields.Kind, fields.Direction, rec.Counterparty.Email); err != nil {
		return datasource.EntityRef{}, false, counterpartyDecision{}, err
	}
	// Capture-audit minimization (ADR-0072/A118): the after-image is
	// metadata-only, never the subject/body (capturedActivityAuditImage).
	auditID, err := storekit.Audit(ctx, tx, "create", "activity", id.UUID, nil, capturedActivityAuditImage(rec, fields))
	if err != nil {
		return datasource.EntityRef{}, false, counterpartyDecision{}, err
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, activityCaptureEventPayload(fields.Kind, rec.NaturalKey.SourceSystem)); err != nil {
		return datasource.EntityRef{}, false, counterpartyDecision{}, err
	}
	if err := s.emitReply(ctx, tx, auditID, id, rec, fields); err != nil {
		return datasource.EntityRef{}, false, counterpartyDecision{}, err
	}
	// The tiered creation gate decides and records in THIS transaction, so a
	// SUCCESSFUL gate leaves no window between an activity landing and its
	// disposition being known. A gate FAULT is contained by the savepoint inside
	// decideCounterpartyGuarded: it costs the derivation only, the message still
	// commits, and the link-less activity plus its capture_ensure_fault
	// breadcrumb are what the reconcile pass looks for. Failing the whole capture
	// would throw away a message we had already successfully read.
	decision, err := s.decideCounterpartyGuarded(ctx, tx, rec, id.UUID)
	if err != nil {
		return datasource.EntityRef{}, false, counterpartyDecision{}, err
	}
	return ref, true, decision, nil
}

// activityCaptureEventPayload builds the activity.captured event for the
// capture ingestion path — the one emit site (of the event's two) that
// names an originating source system; the direct-log path
// (activities/activity.go) sets no fields but kind.
func activityCaptureEventPayload(kind, sourceSystem string) crmcontracts.PublicEventActivityCaptured {
	return crmcontracts.PublicEventActivityCaptured{Kind: kind, SourceSystem: &sourceSystem}
}

func (s *Sink) upsertActivity(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields ActivityFields) (ids.ActivityID, bool, error) {
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return ids.ActivityID{}, false, err
	}
	occurredAt := defaultOccurredAt(fields.OccurredAt)
	var id ids.ActivityID
	err := tx.QueryRow(ctx, `
		INSERT INTO activity (workspace_id, kind, subject, body, occurred_at, direction, source_system, source_id, source, captured_by, thread_key, counterparty_email, counterparty_outbound_attested, bulk_mail_attested)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        $1, NULLIF($2, ''), NULLIF($3, ''), $4, NULLIF($5, ''), $6, $7, $8, $9, NULLIF($10, ''), NULLIF($11, ''), $12, $13)
		ON CONFLICT (workspace_id, source_system, source_id) WHERE source_system IS NOT NULL AND source_id IS NOT NULL
		DO NOTHING
		RETURNING id`,
		fields.Kind, fields.Subject, fields.Body, occurredAt, fields.Direction,
		rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID, captureSource(rec), capturedByFor(ctx, rec), rec.ThreadKey,
		// Normalized lowercased at the write (a connector need not lowercase the
		// header case), matching the person_email normalization, so the T1
		// correspondence lookup's index-backed equality matches regardless of
		// the sender's casing without a runtime case fold.
		strings.ToLower(strings.TrimSpace(rec.Counterparty.Email)),
		// The provider's filing AND the message's authorship, never the
		// From-derived direction alone: this column is the T1
		// correspondence-positive gate's only evidence, and a forged
		// From:owner must not register as the owner's correspondence.
		rec.Counterparty.SentByOwner(),
		// This message's own RFC 2369 List-Unsubscribe header — the corroboration
		// a noise REDACTION needs before it destroys content (migration 0137).
		// Stamped per message, so a newsletter blast is destroyable while a
		// personal mail from the same address is only ever hidden.
		rec.Counterparty.ListUnsubscribe).Scan(&id)
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
	// Replay: the natural key already landed — return the incumbent. Returning
	// a record is a read, so the row scope binds on this path too; an activity
	// scopes through its links, which can move after the first capture.
	err = tx.QueryRow(ctx,
		`SELECT id FROM activity WHERE source_system = $1 AND source_id = $2`,
		rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID).Scan(&id)
	if err != nil {
		return ids.ActivityID{}, false, fmt.Errorf("capture: activity replay lookup: %w", err)
	}
	if err := auth.EnsureActivityVisible(ctx, tx, id.UUID); err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			return ids.ActivityID{}, false, skipInvisibleIncumbent(rec, "activity")
		}
		return ids.ActivityID{}, false, err
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

// skipInvisibleIncumbent refuses a record whose incumbent row — the lead an
// address collides with, the activity a replayed natural key already landed
// as — lies outside the granting human's row scope. Resolving it is not the
// connector's to do: returning the ref would disclose a row the caller cannot
// read, and writing a second row anyway would fork the record across scopes.
// The natural key names the skip, never the captured address or the
// incumbent's id — a skip must re-store neither PII nor an existence proof.
func skipInvisibleIncumbent(rec connector.NormalizedRecord, object string) error {
	return fmt.Errorf("capture: %s/%s resolves onto a %s outside the granting human's row scope: %w",
		rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID, object, connector.ErrSkip)
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

// connectorProvenance is what a captured activity's captured_by records: the
// connector AND the mailbox owner behind it, `connector:gmail:<user>`.
//
// The connector alone was not enough to say anything useful. Two colleagues
// who have both connected Gmail produce rows stamped identically, so nothing
// downstream could tell whose mailbox a message came from — the provenance
// named the software rather than the person, and any later attempt to
// attribute history had to guess or decline.
//
// It is derived from the authenticated principal, never from the record the
// connector handed us: provenance a caller can assert is provenance a caller
// can forge. A principal carrying no granting user falls back to the bare
// connector id, which is the honest answer for a connection with no human
// behind it.
func connectorProvenance(actor principal.Principal) string {
	if actor.UserID == ids.Nil {
		return actor.ID
	}
	return actor.ID + ":" + actor.UserID.String()
}

// capturedByFor is the provenance stamped on a captured activity: the acting
// connector plus the mailbox owner behind it. It falls back to the record's
// own value only when no actor is bound, which the sink has already refused
// by the time this runs — the fallback exists so a future caller cannot get a
// blank provenance out of a missing principal.
func capturedByFor(ctx context.Context, rec connector.NormalizedRecord) string {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return rec.CapturedBy
	}
	return connectorProvenance(actor)
}
