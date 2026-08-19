// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The controller's two decisions about what a statutory obligation holds
// (A165/ADR-0114 §4). The classification is a proxy for a legal judgement that
// belongs to the controller under Art. 5(2), and a product that makes that
// judgement unappealable makes its customer unable to comply — so an
// administrator holding the retention authority may RELEASE a record the
// derivation held wrongly, or PIN one it missed.
//
// Neither is a toggle. Both require a stated reason, both are audited, and
// both are attributed to the person who decided: DEPACK-AC-5a forbids a
// SILENT override, which a logged decision by a named accountable person is
// the opposite of.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Audit verbs of the controller's two decisions (0287, A167/ADR-0114 §6).
const (
	actionRelease = "release"
	actionPin     = "pin"
)

// maxOverrideReason bounds what the contract accepts, so a reason that would
// be truncated on its way to the audit row is refused before it is recorded.
const maxOverrideReason = 2000

// ReleaseRestriction ends a restriction by ERASING the record, with a stated
// reason, and records who decided.
//
// Releasing does not return the record to ordinary use, and that is the whole
// shape of the operation: the Art. 17 request the restriction suspended is
// still outstanding, so lifting the obligation completes the erasure rather
// than undoing it. The data layer enforces exactly this — 0290's guard admits
// a lift only in the same statement that clears the content — so a release
// that tried to merely unhide would be refused by the database, not only by
// this code.
func (e *Eraser) ReleaseRestriction(ctx context.Context, activityID ids.UUID, reason StatedReason) error {
	return e.db.Tx(ctx, func(tx pgx.Tx) error {
		decision, err := admitRestrictionDecision(ctx, tx, reason)
		if err != nil {
			return err
		}
		// The same lift the expiry sweep performs, under the same legal-hold
		// exclusion: a litigation hold says somebody must keep this record,
		// and it outranks BOTH the clock and a controller's decision until it
		// is lifted. A release that destroyed held evidence would spoliate
		// exactly what the nightly pass refuses to touch.
		class, err := liftAndEraseHeldRecord(ctx, tx, activityID, notHeldThroughAnyLink("a.id"))
		if errors.Is(err, pgx.ErrNoRows) {
			return releaseFoundNothing(ctx, tx, activityID)
		}
		if err != nil {
			return err
		}
		if err := e.purgeHeldRecordTraces(ctx, tx, activityID); err != nil {
			return err
		}
		auditID, err := storekit.AuditWithEvidence(ctx, tx, actionRelease, "activity", activityID, nil, nil, map[string]any{
			evidenceKeyCause: "controller_release", evidenceKeyClass: class,
			evidenceKeyReason: decision.reason, "decided_by_name": decision.name,
		})
		if err != nil {
			return err
		}
		return storekit.EmitEvent(ctx, tx, auditID, activityID, crmcontracts.PublicEventRetentionRestricted{
			Action:     crmcontracts.Release,
			ActivityId: openapi_types.UUID(activityID),
		})
	})
}

// releaseFoundNothing tells the three ways a release matches no row apart,
// because an administrator acting on a list needs to know which happened. A
// record that never existed — or that this workspace does not hold — is a 404.
// One under a legal hold is a 409 naming the hold: the decision was refused,
// not lost. One that exists and is no longer restricted is also a 409, because
// the window elapsed or another administrator released it first and there is
// nothing left to release. Reporting any of them as not-found would tell an
// administrator their target does not exist when it does.
func releaseFoundNothing(ctx context.Context, tx pgx.Tx, activityID ids.UUID) error {
	var exists, held bool
	err := tx.QueryRow(ctx, `
		SELECT true, NOT EXISTS (SELECT 1 FROM activity a WHERE a.id = $1 `+notHeldThroughAnyLink("a.id")+`)
		  FROM activity WHERE id = $1`, activityID).Scan(&exists, &held)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperrors.ErrNotFound
	}
	if err != nil {
		return err
	}
	if held {
		return fmt.Errorf("a legal hold on a record linked to this one outranks the erasure until it is lifted: %w", apperrors.ErrConflict)
	}
	return fmt.Errorf("the record is no longer under a retention obligation: %w", apperrors.ErrConflict)
}

// PinToFloor places a record under the statutory floor that the derivation
// missed, restricting it for the window in force, with a stated reason.
//
// The case this exists for is named in the spec rather than hypothetical:
// §257 HGB covers supplier and purchasing correspondence, which qualifies in
// law and has no deal in this product to hang off (DEPACK-AC-5h). No automatic
// rule available here would find it, so the accountable controller says so.
//
// A pin is not free-setting a class. What DEPACK-PARAM-5 forbids is editing a
// class's period or treatment; what a pin sets is the claim that THIS record
// is correspondence of that class — a finding of fact about a document, made
// by a named person, recorded with a reason.
//
// `restricted_reason` on the row carries the CLASS, exactly as a derived
// restriction writes it (erasure_restrict.go), NOT the controller's words:
// the row says which obligation holds it, and who decided and why lives in
// the evidence row and the audit tombstone, where an accountability question
// is actually answered.
func (e *Eraser) PinToFloor(ctx context.Context, activityID ids.UUID, reason StatedReason) error {
	interval, anchor := statutoryFloorArgs()
	return e.db.Tx(ctx, func(tx pgx.Tx) error {
		decision, err := admitRestrictionDecision(ctx, tx, reason)
		if err != nil {
			return err
		}
		if err := auth.EnsureActivityVisible(ctx, tx, activityID); err != nil {
			// A held record reads as gone to every reader (A165 §2), so a
			// not-found here is ambiguous on its own. pinRefusalFor says which
			// it was: a record that is not there, or one already held.
			return pinRefusalFor(ctx, tx, activityID, err)
		}
		if err := recordPinEvidence(ctx, tx, activityID, decision); err != nil {
			return err
		}
		var until time.Time
		err = tx.QueryRow(ctx, `
			UPDATE activity a
			   SET retention_class = coalesce(a.retention_class, $2),
			       retention_class_at = coalesce(a.retention_class_at, now()),
			       restricted_at = now(), restricted_reason = $2,
			       restricted_until = `+floorWindowEnd(3, 4)+`,
			       raw = NULL, counterparty_email = NULL,
			       redacted_fields = a.redacted_fields || ARRAY(SELECT c FROM unnest(ARRAY[
			           CASE WHEN a.raw IS NOT NULL THEN 'raw' END,
			           CASE WHEN a.counterparty_email IS NOT NULL THEN 'counterparty_email' END]) AS c
			         WHERE c IS NOT NULL),
			       archived_at = coalesce(a.archived_at, now())
			 WHERE a.id = $1 AND a.restricted_at IS NULL
			 RETURNING a.restricted_until`, activityID, retentionClassCorrespondence, interval, anchor).Scan(&until)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("the record is already under a retention obligation: %w", apperrors.ErrConflict)
		}
		if err != nil {
			return err
		}
		if err := pinnedRecordLeavesDerivedCopies(ctx, tx, activityID); err != nil {
			return err
		}
		class := retentionClassCorrespondence
		auditID, err := storekit.AuditWithEvidence(ctx, tx, actionPin, "activity", activityID, nil, nil, map[string]any{
			evidenceKeyCause: "controller_pin", evidenceKeyClass: class, evidenceKeyBasis: statutoryBasisCorrespondence,
			"restricted_until": until, evidenceKeyReason: decision.reason, "decided_by_name": decision.name,
		})
		if err != nil {
			return err
		}
		return storekit.EmitEvent(ctx, tx, auditID, activityID, crmcontracts.PublicEventRetentionRestricted{
			Action:          crmcontracts.Pin,
			ActivityId:      openapi_types.UUID(activityID),
			RestrictedUntil: &until,
			RetentionClass:  &class,
		})
	})
}

// pinRefusalFor reads a visibility refusal for what it actually was. The probe
// answers not-found for a record that is not there AND for one already held,
// because holding a record is what makes it unreadable — so telling the second
// administrator their target does not exist, when it exists and is already
// doing what they asked for, would be a lie the probe cannot help making.
// Any other refusal (a row scope that genuinely excludes the caller) stands.
func pinRefusalFor(ctx context.Context, tx pgx.Tx, activityID ids.UUID, refusal error) error {
	if !errors.Is(refusal, apperrors.ErrNotFound) {
		return refusal
	}
	var held bool
	err := tx.QueryRow(ctx,
		`SELECT restricted_at IS NOT NULL FROM activity WHERE id = $1`, activityID).Scan(&held)
	if errors.Is(err, pgx.ErrNoRows) {
		return refusal
	}
	if err != nil {
		return err
	}
	if held {
		return fmt.Errorf("the record is already under a retention obligation: %w", apperrors.ErrConflict)
	}
	return refusal
}

// pinnedRecordLeavesDerivedCopies drops what a similarity probe or a proposal
// could still reach a pinned record's body through. The body itself stays —
// that is what the obligation keeps — but a restricted record must not survive
// in a projection (A165 §2), and a vector is the body in another shape.
func pinnedRecordLeavesDerivedCopies(ctx context.Context, tx pgx.Tx, activityID ids.UUID) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM embedding WHERE entity_type = 'activity' AND entity_id = $1`, activityID); err != nil {
		return err
	}
	return purgeTranscriptReadings(ctx, tx, []ids.UUID{activityID})
}

// recordPinEvidence writes the controller's finding of fact BEFORE the
// restriction, because the guard refuses a restriction with no evidence behind
// it, and because the evidence is what a supervisory authority is shown. A pin
// names no deal — the case it exists for has none — so the attribution is what
// substantiates it: the deciding user's id and their display name frozen
// beside it, so a deactivated account does not turn an attributed decision
// into an anonymous one.
func recordPinEvidence(ctx context.Context, tx pgx.Tx, activityID ids.UUID, decision restrictionDecision) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO activity_retention_evidence
		  (activity_id, basis, qualified_at, decided_by, decided_by_name, reason)
		VALUES ($1, 'controller_pin', now(), $2, $3, $4)`,
		activityID, storekit.UUIDOrNil(decision.userID), decision.name, decision.reason); err != nil {
		return fmt.Errorf("recording the controller's pin decision: %w", err)
	}
	return nil
}

// restrictionDecision is who decided and why — the two things that make an
// override a decision rather than a toggle.
type restrictionDecision struct {
	userID ids.UUID
	name   string
	reason string
}

// StatedReason is the reason a release or a pin carries, checked at the
// transport before either operation runs.
//
// It is a type rather than a string so the check cannot be skipped by a
// caller: the two store methods take one, and the only way to obtain one is
// ParseStatedReason. Whitespace is not a reason — it passes a required-field
// check and says nothing, which is exactly the silent override DEPACK-AC-5a
// forbids.
type StatedReason struct{ text string }

// ParseStatedReason admits a reason a controller actually stated. The bound
// matches the contract's, so a reason that would be truncated on its way to
// the audit row is refused before it is recorded rather than after.
func ParseStatedReason(reason string) (StatedReason, error) {
	stated := strings.TrimSpace(reason)
	if stated == "" || len([]rune(stated)) > maxOverrideReason {
		return StatedReason{}, httperr.Validation("reason", "required", fmt.Sprintf(
			"a release or a pin records a controller's decision, so it must state why in 1–%d characters",
			maxOverrideReason))
	}
	return StatedReason{text: stated}, nil
}

// admitRestrictionDecision is the gate both operations share: a human session
// (an agent never decides what the installation keeps, even carrying an
// admin's passport) and the retention authority's UPDATE — the same object
// that governs the ladder, held admin-only by the seeded roles.
//
// The deciding user's DISPLAY NAME is read here and frozen into the evidence,
// because a deactivated or deleted account must not turn an attributed
// decision into an anonymous one (A167/ADR-0114 §3). Reading it needs the
// transaction, so the caller passes one.
func admitRestrictionDecision(ctx context.Context, tx pgx.Tx, reason StatedReason) (restrictionDecision, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return restrictionDecision{}, fmt.Errorf("only a named human decides what a statutory obligation holds: %w", apperrors.ErrPermissionDenied)
	}
	if err := auth.Require(ctx, retentionPolicyObject, principal.ActionUpdate); err != nil {
		return restrictionDecision{}, err
	}
	var name string
	err := tx.QueryRow(ctx, `SELECT display_name FROM app_user WHERE id = $1`, actor.UserID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && strings.TrimSpace(name) == "") {
		// The evidence CHECK refuses an unattributed pin, so a decision this
		// installation cannot name is refused HERE, with an explanation, rather
		// than as a constraint violation from underneath.
		return restrictionDecision{}, fmt.Errorf(
			"the deciding account carries no display name, and an unattributed decision cannot be accounted for: %w",
			apperrors.ErrPermissionDenied)
	}
	if err != nil {
		return restrictionDecision{}, err
	}
	return restrictionDecision{userID: actor.UserID, name: name, reason: reason.text}, nil
}
