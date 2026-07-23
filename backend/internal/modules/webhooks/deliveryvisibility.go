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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// workspaceLevelEntities are the event subject types with NO per-owner row
// scope: workspace/admin-level facts (pipeline & stage config, the
// identity/access-revocation cascade, the audit ledger, the onboarding
// wizard state, the incumbent-connection lifecycle) whose envelope is a
// bare entity ref — a receiver reads any detail back under its own scope
// (events.md §0). They deliver to any live subscription
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
// approval.*/coldstart.* events name entity "approval" but are NOT listed
// here — an approval's envelope carries staged-change detail (summary,
// edited_change, target ids) that a bare-ref allow-list would fan out to
// owners who cannot see the target, so "approval" is instead gated by
// approvalVisibleTo in the switch below (BYO-EVT-4). The mirror.* events
// name a dynamic object_class, handled by deferredDeliveryEvents below, so
// no "mirror" key exists either.
var workspaceLevelEntities = map[string]struct{}{
	"pipeline":                {},
	"stage":                   {},
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
// retention.go's eraseEmbedCall), ai_call_payload (retained call content),
// and voice_learning_signal (aged voice-learning telemetry, privacy/
// retention.go's eraseVoiceSignalContent) — which carry no owner and no
// visibility probe. Delivering those workspace-wide would leak which
// telemetry rows were purged, so
// their delivery is DEFERRED pending a telemetry-ownership model (raised
// upstream, P3): EXPLICITLY undelivered, never silently denied and never
// fanned out. Unlike the mirror.* events these entity strings do NOT
// collide with a row-scoped subject, so they are safely keyed by entity
// type rather than event. Each entry carries the rationale inline.
var deferredDeliveryEntities = map[string]string{
	"ai_call":               "retention.applied over an embedding-trace ai_call row — engine telemetry with no owner and no visibility probe; delivery deferred pending a telemetry-ownership model (upstream P3)",
	"ai_call_payload":       "retention.applied over a retained ai_call_payload row — engine telemetry with no owner and no visibility probe; delivery deferred pending a telemetry-ownership model (upstream P3)",
	"voice_learning_signal": "retention.applied over an aged voice_learning_signal row — ownerless voice-learning telemetry, the same class as ai_call/ai_call_payload, with no owner and no visibility probe; delivery deferred pending a telemetry-ownership model (upstream P3)",
}

// entityVisibleTo reports whether the entity an event names is visible to
// ctx's principal under the READ path's FULL gate (BYO-EVT-4: fan-out never
// escalates past what the owner may see). It classifies by EVENT TYPE
// first (a deferredDeliveryEvents subject's runtime object_class collides
// with the row-scoped entity names, so it must be caught before the
// switch), then by entity type: a row-scoped subject is admitted only when
// the owner holds BOTH the object-level read capability AND the row scope —
// exactly the two halves <entity>.Get enforces (auth.Require +
// auth.EnsureVisible), so a lingering row scope with no current read grant
// can no longer leak the payload; an offer inherits its parent deal's row
// scope behind offer.read; an approval is target-visibility gated
// (approvalVisibleTo); genuinely ownerless workspace-level subjects
// (workspaceLevelEntities) deliver to any live owner (a bare ref the
// receiver re-reads under its own scope); a ratified deferred-delivery
// subject (deferredDelivery*) is EXPLICITLY not delivered; ANY OTHER type is
// DENIED (fail-closed) so an unclassified subject can never leak. Object
// denial and a row-scope miss both read as not-visible; only a real
// infrastructure error surfaces, never stranding the whole fan-out.
func (s *Store) entityVisibleTo(ctx context.Context, eventType, entityType string, entityID ids.UUID) (bool, error) {
	if _, deferred := deferredDeliveryEvents[eventType]; deferred {
		// Subject class is a runtime string with no owner-scopable id —
		// ratified undelivered, and caught here so the object_class
		// collision can never fall through to a row-scope probe below.
		return false, nil
	}
	switch entityType {
	case "person", "organization", "deal", "lead", "voice_profile":
		return s.rowScopedVisible(ctx, entityType, func(c context.Context, tx pgx.Tx) error {
			return auth.EnsureVisible(c, tx, entityType, entityID)
		})
	case "activity":
		return s.rowScopedVisible(ctx, "activity", func(c context.Context, tx pgx.Tx) error {
			return auth.EnsureActivityVisible(c, tx, entityID)
		})
	case "signal":
		return s.rowScopedVisible(ctx, "signal", func(c context.Context, tx pgx.Tx) error {
			return auth.EnsureSignalVisible(c, tx, entityID)
		})
	case "offer":
		// An offer has no owner of its own — it is row-scoped through its
		// parent deal behind offer.read, exactly as the offer read path
		// gates (deals/offer_read.go: auth.Require("offer") + deal scope).
		return s.offerVisibleTo(ctx, entityID)
	case "approval":
		// An approval (and its coldstart.* echoes) carries staged-change
		// detail — summary, edited_change, target ids — so it is gated on
		// the SAME target-visibility predicate the approvals inbox uses
		// (approvals/authority.go targetVisible, C3/ADR-0036: what you
		// cannot see you cannot decide), never fanned out workspace-wide.
		return s.approvalVisibleTo(ctx, entityID)
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

// rowScopedVisible mirrors a record read's FULL admission for a row-scoped
// subject: the object-level read capability (auth.Require — the half a bare
// EnsureVisible skips) AND the row scope must BOTH admit, exactly as
// <entity>.Get does. Object denial (ErrPermissionDenied) or a row-scope miss
// (ErrNotFound) reads as not-visible; a real error surfaces.
func (s *Store) rowScopedVisible(ctx context.Context, object string, probe func(context.Context, pgx.Tx) error) (bool, error) {
	readable, err := objectReadable(ctx, object)
	if err != nil || !readable {
		return false, err
	}
	return s.probeVisible(ctx, probe)
}

// objectReadable reports whether ctx's principal holds the object-level read
// grant on object — the auth.Require half of the read path. A denial reads as
// not-readable (false, nil); a resolution error surfaces.
func objectReadable(ctx context.Context, object string) (bool, error) {
	switch err := auth.Require(ctx, object, principal.ActionRead); {
	case err == nil:
		return true, nil
	case errors.Is(err, apperrors.ErrPermissionDenied):
		return false, nil
	default:
		return false, err
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

// offerVisibleTo mirrors GetOffer's admission: offer.read AND the parent
// deal's row scope (an offer carries no owner_id — its sensitivity is the
// deal's). Object denial or an absent/out-of-scope deal reads as not-visible.
func (s *Store) offerVisibleTo(ctx context.Context, offerID ids.UUID) (bool, error) {
	readable, err := objectReadable(ctx, "offer")
	if err != nil || !readable {
		return false, err
	}
	return s.offerDealVisible(ctx, offerID)
}

// offerDealVisible resolves an offer's parent deal and gates on the owner's
// row-scope visibility of THAT deal — the offer's row-scope anchor, shared by
// the direct offer path (behind offer.read) and the approval-target path
// (which mirrors approvals.targetVisible: deal row scope, no offer.read). An
// absent offer reads as not-visible.
func (s *Store) offerDealVisible(ctx context.Context, offerID ids.UUID) (bool, error) {
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

// approvalVisibleTo gates an approval.*/coldstart.* event on the same
// target-visibility rule the approvals inbox enforces (approvals/
// authority.go targetVisible, C3/ADR-0036): the approval's envelope leaks
// staged-change detail (summary, edited_change, target ids), so it may only
// reach an owner who can see the TARGET record. It resolves the approval's
// polymorphic target and applies the target's row scope (approvalTargetVisible)
// A target-LESS approval (some approval.requested proposals and every
// coldstart.* echo carry no target) cannot be scope-bounded, so it is
// FAIL-CLOSED (not delivered) — a ratified deferral, exactly like the
// deferredDelivery* subjects: never a workspace-wide fan-out of content the
// owner's grants could not read. A missing approval row reads as
// not-visible. The approval table is read with a raw probe under the
// existing WithWorkspaceTx boundary rather than importing the approvals
// module (a module never imports a sibling).
func (s *Store) approvalVisibleTo(ctx context.Context, approvalID ids.UUID) (bool, error) {
	var (
		targetType *string
		targetID   *ids.UUID
	)
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT target_entity_type, target_entity_id FROM approval WHERE id = $1`,
			approvalID).Scan(&targetType, &targetID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if targetType == nil || targetID == nil {
		// Target-less approval — no record whose scope could bound the
		// fan-out, so it is EXPLICITLY undelivered (fail-closed), never
		// leaked workspace-wide.
		return false, nil
	}
	return s.approvalTargetVisible(ctx, *targetType, *targetID)
}

// approvalTargetVisible applies approvals.targetVisible's TARGET-visibility
// half (approvals/authority.go): the target record's OWN row scope (person/
// organization/deal/lead/offer/signal/activity), or for the workspace-shared
// admin config the approvals surface also stages against (product,
// custom_field) the same existence floor. That target-visibility check IS the
// confidentiality boundary this gate owes: a subscriber receives an approval's
// details (summary, target ids, edited_change) only about a target it can
// already read, never one it cannot. It deliberately does NOT also apply the
// inbox's `decidable` decision-grant half — that governs who may ACT on an
// approval (an authorization concern), not who may learn a visible target's
// proposed change, so a webhook owner's fan-out set may be broader than the
// inbox's decidable set while disclosing nothing beyond what the owner could
// already read. (Diverging from entityVisibleTo's object-read capability is the
// same deliberate choice, for the same reason.) Unknown target type: fail
// closed. Self-contained duplicate of the sibling rule (a module never imports
// a sibling); the target-visibility branches must stay in step.
func (s *Store) approvalTargetVisible(ctx context.Context, targetType string, targetID ids.UUID) (bool, error) {
	switch targetType {
	case "person", "organization", "deal", "lead":
		return s.probeVisible(ctx, func(c context.Context, tx pgx.Tx) error {
			return auth.EnsureVisible(c, tx, targetType, targetID)
		})
	case "offer":
		return s.offerDealVisible(ctx, targetID)
	case "signal":
		return s.probeVisible(ctx, func(c context.Context, tx pgx.Tx) error {
			return auth.EnsureSignalVisible(c, tx, targetID)
		})
	case "activity":
		return s.probeVisible(ctx, func(c context.Context, tx pgx.Tx) error {
			return auth.EnsureActivityVisible(c, tx, targetID)
		})
	case "product":
		// Rate-card products are workspace-shared config with no row scope —
		// existence is the floor (approvals.targetVisible); an archived product
		// is not a live target.
		return s.rowExists(ctx, `SELECT EXISTS (SELECT 1 FROM product WHERE id = $1 AND archived_at IS NULL)`, targetID)
	case "custom_field":
		// The field catalog is workspace-shared admin config with no row scope;
		// no archived_at predicate — retire is a status flip that keeps the row
		// live, matching approvals.targetVisible.
		return s.rowExists(ctx, `SELECT EXISTS (SELECT 1 FROM custom_field WHERE id = $1)`, targetID)
	default:
		// Unknown target type: fail closed, exactly like approvals.targetVisible.
		return false, nil
	}
}

// rowExists runs a single-row existence probe under the ctx's workspace tx —
// the workspace-shared-config floor the approval-target gate shares.
func (s *Store) rowExists(ctx context.Context, query string, id ids.UUID) (bool, error) {
	var exists bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, query, id).Scan(&exists)
	})
	if err != nil {
		return false, err
	}
	return exists, nil
}
