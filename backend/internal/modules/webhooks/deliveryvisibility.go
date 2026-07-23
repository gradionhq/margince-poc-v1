// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

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
