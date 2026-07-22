// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The webhook_delivery status vocabulary lives in the table's CHECK and in
// the SQL below: 'pending' (freshly enqueued) → 'retrying' (failed, with a
// backoff deadline) → 'dead_lettered' (budget spent), or → 'delivered'.
const deliveryColumns = `id, subscription_id, event_id, event_type, status, attempts,
	last_status_code, last_error, next_retry_at, delivered_at, dead_lettered_at, created_at, updated_at`

// Delivery is the inspectable view of one attempt log (B-E10.13c). The
// signed body is not exposed — it is an internal detail of replay.
type Delivery struct {
	ID             ids.UUID
	SubscriptionID ids.UUID
	EventID        ids.UUID
	EventType      string
	Status         string
	Attempts       int
	LastStatusCode *int
	LastError      *string
	NextRetryAt    *time.Time
	DeliveredAt    *time.Time
	DeadLetteredAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func scanDelivery(r pgx.Row) (Delivery, error) {
	var d Delivery
	err := r.Scan(&d.ID, &d.SubscriptionID, &d.EventID, &d.EventType, &d.Status, &d.Attempts,
		&d.LastStatusCode, &d.LastError, &d.NextRetryAt, &d.DeliveredAt, &d.DeadLetteredAt,
		&d.CreatedAt, &d.UpdatedAt)
	return d, err
}

// ListDeliveries returns a subscription's delivery history newest-first —
// the dead-letter inspection surface (B-E10.13c). Read-gated, and the
// subscription is existence-hidden if the caller may not see it. It reports
// hasMore honestly: the dead-letter view must never look complete while
// older parked deliveries are hidden behind the page limit.
func (s *Store) ListDeliveries(ctx context.Context, subID ids.UUID, limit int) ([]Delivery, bool, error) {
	if err := auth.Require(ctx, rbacObject, principal.ActionRead); err != nil {
		return nil, false, err
	}
	if _, err := s.GetSubscription(ctx, subID); err != nil {
		return nil, false, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []Delivery
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Fetch one past the page so a full page is distinguishable from a
		// truncated one without a second count query.
		rows, err := tx.Query(ctx, "SELECT "+deliveryColumns+
			" FROM webhook_delivery WHERE subscription_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2",
			subID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDelivery(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// getDelivery reads one delivery by id in the caller's workspace.
func (s *Store) getDelivery(ctx context.Context, deliveryID ids.UUID) (Delivery, error) {
	var out Delivery
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanDelivery(tx.QueryRow(ctx,
			"SELECT "+deliveryColumns+" FROM webhook_delivery WHERE id = $1", deliveryID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, apperrors.ErrNotFound
	}
	return out, err
}

// requireReplay authorizes a replay: the caller must hold update on the
// config surface, the subscription must be visible (existence-hiding), the
// delivery must belong to it, and the action is audited to the acting
// human before the re-attempt runs.
func (s *Store) requireReplay(ctx context.Context, subID, deliveryID ids.UUID) error {
	if err := auth.Require(ctx, rbacObject, principal.ActionUpdate); err != nil {
		return err
	}
	if _, err := s.GetSubscription(ctx, subID); err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var belongs bool
		err := tx.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM webhook_delivery WHERE id = $1 AND subscription_id = $2)",
			deliveryID, subID).Scan(&belongs)
		if err != nil {
			return err
		}
		if !belongs {
			return apperrors.ErrNotFound
		}
		_, err = storekit.Audit(ctx, tx, "update", rbacObject, subID, nil,
			map[string]any{"replayed_delivery": deliveryID.String()})
		return err
	})
}

// attemptTarget is one deliverable unit: the sealed secret and body the
// signer needs, plus the identity to record the outcome against.
type attemptTarget struct {
	deliveryID    ids.UUID
	subID         ids.UUID
	targetURL     string
	sealedSecret  string
	eventType     string
	eventID       ids.UUID
	payload       []byte
	priorAttempts int
}

// subCandidate is one active subscription matching an event's type, with
// the owning principal the fan-out is bounded to (B-E10.15/BYO-EVT-4).
type subCandidate struct {
	id      ids.UUID
	ownerID ids.UUID
}

// matchingSubscriptions returns the active subscriptions in the envelope's
// workspace whose event_types include this type, each with its owner —
// the fan-out candidate set BEFORE the owner-visibility filter. Runs in
// the envelope's workspace under the tenant GUC.
func (s *Store) matchingSubscriptions(ctx context.Context, eventType string) ([]subCandidate, error) {
	var out []subCandidate
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, owner_id FROM webhook_subscription
			WHERE state = 'active' AND archived_at IS NULL
			  AND event_types @> ARRAY[$1]::text[]`, eventType)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c subCandidate
			if err := rows.Scan(&c.id, &c.ownerID); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// enqueueForSubscriptions creates a pending delivery for each named
// subscription, idempotently: the (workspace, subscription, event) unique
// key means a redelivered bus event conflicts and yields no new row — so
// it never double-POSTs. It returns only the freshly-created rows to
// attempt now. subIDs is the visibility-filtered set (BYO-EVT-4). Runs in
// the envelope's workspace.
func (s *Store) enqueueForSubscriptions(ctx context.Context, subIDs []ids.UUID, eventType string, eventID ids.UUID, body []byte) ([]attemptTarget, error) {
	if len(subIDs) == 0 {
		return nil, nil
	}
	var targets []attemptTarget
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH matched AS (
				SELECT id, target_url, signing_secret_ref
				FROM webhook_subscription
				WHERE id = ANY($4::uuid[]) AND state = 'active' AND archived_at IS NULL
			), created AS (
				INSERT INTO webhook_delivery
				  (workspace_id, subscription_id, event_id, event_type, payload, status)
				SELECT NULLIF(current_setting('app.workspace_id', true), '')::uuid,
				       m.id, $2, $1, $3::jsonb, 'pending'
				FROM matched m
				ON CONFLICT (workspace_id, subscription_id, event_id) DO NOTHING
				RETURNING id, subscription_id
			)
			SELECT c.id, c.subscription_id, m.target_url, m.signing_secret_ref
			FROM created c JOIN matched m ON m.id = c.subscription_id`,
			eventType, eventID, body, subIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t := attemptTarget{eventType: eventType, eventID: eventID, payload: body}
			if err := rows.Scan(&t.deliveryID, &t.subID, &t.targetURL, &t.sealedSecret); err != nil {
				return err
			}
			targets = append(targets, t)
		}
		return rows.Err()
	})
	return targets, err
}

// workspaceLevelEntities are the event subject types with NO per-owner row
// scope: workspace/admin-level facts (pipeline & stage config, the
// approvals ledger, the identity/access-revocation cascade, the audit
// ledger, the onboarding wizard state, the incumbent-connection lifecycle)
// whose envelope is a bare entity ref — a receiver reads any detail back
// under its own scope (events.md §0). They deliver to any live subscription
// owner. This is an ALLOW-list, not the default: every subject type that is
// not listed here, has no explicit row-scope probe below, and is not a
// ratified deferred-delivery subject is DENIED (entityVisibleTo's
// fail-closed default), so a newly-subscribable row-scoped subject can
// never silently inherit fan-out-to-everyone (BYO-EVT-4). Adding a
// subscribable event whose subject is row-scoped means adding a probe
// below; one that is genuinely ownerless means adding it here; one whose
// subject cannot yet be scope-resolved means ratifying it in a
// deferred-delivery exception — the choice is forced, never defaulted.
//
// The keys are the RUNTIME entity-type strings the emit sites stamp (the
// storekit.EmitEvent EntityType() / EmitEventForEntity caller argument),
// which are NOT the dotted event prefix: role.changed and the user.*
// lifecycle both name entity "user"; passport.revoked names "passport";
// onboarding.state_changed names "onboarding_wizard_state";
// incumbent.connected/disconnected name "incumbent_connection". The
// cold-start echoes name "approval" (already listed), so no "coldstart"
// key exists; the mirror.* events name a dynamic object_class, handled by
// deferredDeliveryEvents below, so no "mirror" key exists either.
var workspaceLevelEntities = map[string]struct{}{
	"pipeline":                {},
	"stage":                   {},
	"approval":                {},
	"audit":                   {},
	"user":                    {},
	"passport":                {},
	"onboarding_wizard_state": {},
	"incumbent_connection":    {},
}

// deferredDeliveryEvents are subscribable events whose subject cannot be
// resolved to an owner's row scope at fan-out time, keyed by EVENT TYPE
// (not entity type) because their runtime subject class collides with the
// row-scoped entity names above. The overlay mirror.* events stamp the
// diverged record's RUNTIME canonical class (rec.ObjectClass / ref.Type /
// del.ObjectClass — e.g. "person", "deal") as their entity type, but the
// id they carry is a mirror-synthetic key (externalIDToUUID) or a
// pre-materialization EntityRef — NOT a live record id the owner's grants
// can be probed against. An entity-type probe would therefore either miss
// (fail-closed by accident) or, for mirror.budget_degraded's real ref.ID,
// deliver to owners who must not see the record. Neither is acceptable, so
// delivery for these is DEFERRED pending an overlay-mirror ownership model
// (raised upstream, P3): they stay subscribable and fully catalogued, but
// entityVisibleTo returns not-visible for them — an EXPLICIT, ratified
// undelivered decision, never a silent deny and never a workspace-wide
// fan-out. Checked BEFORE the entity-type switch so the object_class
// collision can never route one of these into a row-scope probe. Each
// entry carries the rationale for the deferral, so the waiver is
// self-contained (the auditOnlyWrites precedent).
var deferredDeliveryEvents = map[string]string{
	"mirror.conflict":        "overlay mirror subject is a runtime object_class over a mirror-synthetic id — no live-record scope to probe; delivery deferred pending an overlay ownership model (upstream P3)",
	"mirror.budget_degraded": "overlay mirror subject is a runtime object_class; its ref.ID is a pre-materialization record ref, not an owner-scopable live id — delivery deferred pending an overlay ownership model (upstream P3)",
	"mirror.deleted":         "overlay mirror subject is a runtime object_class over a mirror-synthetic id — no live-record scope to probe; delivery deferred pending an overlay ownership model (upstream P3)",
	"mirror.write_rejected":  "reserved branch-2 overlay mirror event; same runtime-object_class subject shape — delivery deferred pending an overlay ownership model (upstream P3)",
}

// deferredDeliveryEntities are subscribable subjects keyed by RUNTIME
// entity type whose row scope has no probe today. retention.applied is a
// dynamic-entity event: its person/lead/deal/activity subjects DO resolve
// through the row-scope probes below, but the nightly retention sweep also
// ages out engine telemetry — ai_call (embedding traces, privacy/
// retention.go's eraseEmbedCall) and ai_call_payload (retained call
// content) — which carry no owner and no visibility probe. Delivering
// those workspace-wide would leak which telemetry rows were purged, so
// their delivery is DEFERRED pending a telemetry-ownership model (raised
// upstream, P3): EXPLICITLY undelivered, never silently denied and never
// fanned out. Unlike the mirror.* events these entity strings do NOT
// collide with a row-scoped subject, so they are safely keyed by entity
// type rather than event. Each entry carries the rationale inline.
var deferredDeliveryEntities = map[string]string{
	"ai_call":         "retention.applied over an embedding-trace ai_call row — engine telemetry with no owner and no visibility probe; delivery deferred pending a telemetry-ownership model (upstream P3)",
	"ai_call_payload": "retention.applied over a retained ai_call_payload row — engine telemetry with no owner and no visibility probe; delivery deferred pending a telemetry-ownership model (upstream P3)",
}

// entityVisibleTo reports whether the entity an event names is visible to
// ctx's principal under the row-scope gate (BYO-EVT-4: fan-out never
// escalates past what the owner may see). It classifies by EVENT TYPE
// first (a deferredDeliveryEvents subject's runtime object_class collides
// with the row-scoped entity names, so it must be caught before the
// switch), then by entity type: row-scoped subjects are probed against the
// owner's live scope; an offer inherits its parent deal's scope; genuinely
// ownerless workspace-level subjects (workspaceLevelEntities) deliver to
// any live owner; a ratified deferred-delivery subject (deferredDelivery*)
// is EXPLICITLY not delivered; ANY OTHER type is DENIED (fail-closed) so an
// unclassified subject can never leak. Out of scope reads as not-visible,
// never an error that would strand the whole fan-out.
func (s *Store) entityVisibleTo(ctx context.Context, eventType, entityType string, entityID ids.UUID) (bool, error) {
	if _, deferred := deferredDeliveryEvents[eventType]; deferred {
		// Subject class is a runtime string with no owner-scopable id —
		// ratified undelivered, and caught here so the object_class
		// collision can never fall through to a row-scope probe below.
		return false, nil
	}
	switch entityType {
	case "person", "organization", "deal", "lead", "voice_profile":
		return s.probeVisible(ctx, func(c context.Context, tx pgx.Tx) error {
			return auth.EnsureVisible(c, tx, entityType, entityID)
		})
	case "activity":
		return s.probeVisible(ctx, func(c context.Context, tx pgx.Tx) error {
			return auth.EnsureActivityVisible(c, tx, entityID)
		})
	case "signal":
		return s.probeVisible(ctx, func(c context.Context, tx pgx.Tx) error {
			return auth.EnsureSignalVisible(c, tx, entityID)
		})
	case "offer":
		// An offer has no owner of its own — it is row-scoped through its
		// parent deal, exactly as the offer read path gates (deals/offer.go).
		return s.offerVisibleTo(ctx, entityID)
	default:
		if _, ok := workspaceLevelEntities[entityType]; ok {
			return true, nil
		}
		if _, deferred := deferredDeliveryEntities[entityType]; deferred {
			// Ratified deferred-delivery subject — EXPLICITLY undelivered,
			// distinct from the accidental fail-closed default below.
			return false, nil
		}
		// Fail closed: an unclassified subject type is NOT delivered.
		return false, nil
	}
}

// probeVisible runs a single-row visibility probe in the ctx's workspace
// and maps its outcome to (visible, err): nil → visible, ErrNotFound → not
// visible (out of scope), anything else → a real error the caller surfaces.
func (s *Store) probeVisible(ctx context.Context, probe func(context.Context, pgx.Tx) error) (bool, error) {
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error { return probe(ctx, tx) })
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, apperrors.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// offerVisibleTo resolves an offer's parent deal and gates on the owner's
// visibility of THAT deal — an offer carries no owner_id, so its
// sensitivity is the deal's. An absent offer reads as not-visible.
func (s *Store) offerVisibleTo(ctx context.Context, offerID ids.UUID) (bool, error) {
	var dealID ids.UUID
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT deal_id FROM offer WHERE id = $1`, offerID).Scan(&dealID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.probeVisible(ctx, func(c context.Context, tx pgx.Tx) error {
		return auth.EnsureVisible(c, tx, "deal", dealID)
	})
}

// liveWorkspaces lists the tenants a sweep pass iterates. Like the
// retention evaluator, it reads the workspace root directly (that table is
// the tenant resolver, not RLS-scoped record data) and is bounded by fleet
// size, not tenant data volume — each workspace's due rows are then read
// under its own GUC, never cross-tenant.
func (s *Store) liveWorkspaces(ctx context.Context) ([]ids.UUID, error) {
	// rls-exempt: the retry sweeper enumerates live tenants to scan each under its own GUC (the retention-evaluator precedent); the workspace root is the tenant resolver, not RLS-scoped record data.
	rows, err := s.pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ids.UUID
	for rows.Next() {
		var id ids.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// dueRetries finds retrying deliveries in the ctx's workspace whose backoff
// has elapsed and whose subscription is still live and active (a paused
// subscription's retries wait until it resumes). Runs under the tenant GUC.
func (s *Store) dueRetries(ctx context.Context, now time.Time, limit int) ([]ids.UUID, error) {
	var out []ids.UUID
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT d.id
			FROM webhook_delivery d
			JOIN webhook_subscription s ON s.id = d.subscription_id
			WHERE d.status = 'retrying' AND d.next_retry_at <= $1
			  AND s.state = 'active' AND s.archived_at IS NULL
			ORDER BY d.next_retry_at
			LIMIT $2`, now, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id ids.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

// loadTarget rehydrates a delivery into an attemptTarget for retry/replay:
// the stored body plus the subscription's current target URL and sealed
// secret (so a rotation between attempts takes effect). Runs in-workspace.
func (s *Store) loadTarget(ctx context.Context, deliveryID ids.UUID) (attemptTarget, error) {
	var t attemptTarget
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT d.id, d.subscription_id, s.target_url, s.signing_secret_ref,
			       d.event_type, d.event_id, d.payload, d.attempts
			FROM webhook_delivery d
			JOIN webhook_subscription s
			  ON s.workspace_id = d.workspace_id AND s.id = d.subscription_id
			WHERE d.id = $1`, deliveryID).
			Scan(&t.deliveryID, &t.subID, &t.targetURL, &t.sealedSecret,
				&t.eventType, &t.eventID, &t.payload, &t.priorAttempts)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return attemptTarget{}, apperrors.ErrNotFound
	}
	return t, err
}

// outcome is the result of one HTTP attempt, translated to the next row
// state by recordOutcome.
type outcome struct {
	statusCode int    // 0 when the request never got a response (dial/timeout)
	failure    string // empty on success
}

// recordOutcome advances the delivery state machine in the target's
// workspace: success → delivered; failure with budget left → retrying
// with the next backoff deadline; budget spent → dead_lettered. Timestamps
// come from the injected clock so the schedule is testable.
func (s *Store) recordOutcome(ctx context.Context, t attemptTarget, res outcome, now time.Time) error {
	attempts := t.priorAttempts + 1
	var statusCode *int
	if res.statusCode != 0 {
		statusCode = &res.statusCode
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if res.failure == "" {
			_, err := tx.Exec(ctx, `
				UPDATE webhook_delivery
				SET status = 'delivered', attempts = $2, last_status_code = $3,
				    last_error = NULL, next_retry_at = NULL, delivered_at = $4
				WHERE id = $1`, t.deliveryID, attempts, statusCode, now)
			return err
		}
		if attempts >= maxAttempts {
			_, err := tx.Exec(ctx, `
				UPDATE webhook_delivery
				SET status = 'dead_lettered', attempts = $2, last_status_code = $3,
				    last_error = $4, next_retry_at = NULL, dead_lettered_at = $5
				WHERE id = $1`, t.deliveryID, attempts, statusCode, res.failure, now)
			return err
		}
		next := now.Add(backoff(attempts))
		_, err := tx.Exec(ctx, `
			UPDATE webhook_delivery
			SET status = 'retrying', attempts = $2, last_status_code = $3,
			    last_error = $4, next_retry_at = $5
			WHERE id = $1`, t.deliveryID, attempts, statusCode, res.failure, next)
		return err
	})
}

// resetForReplay clears a parked delivery back to pending so it can be
// re-attempted. Returns ErrNotFound if the delivery is absent in the
// caller's workspace (existence-hiding).
func (s *Store) resetForReplay(ctx context.Context, deliveryID ids.UUID) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE webhook_delivery
			SET status = 'pending', next_retry_at = NULL, dead_lettered_at = NULL, last_error = NULL
			WHERE id = $1`, deliveryID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return apperrors.ErrNotFound
		}
		return nil
	})
}
