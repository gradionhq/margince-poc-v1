// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// redemptionTTL bounds the approve→redeem window: the human's yes is a
// judgment about the world NOW, not standing authority.
const redemptionTTL = 15 * time.Minute

// RedemptionWindow is redemptionTTL for callers that must not outlive it —
// a durable handle to a decision has to stay answerable for exactly as long as
// the decision can still be acted on, and one that expired sooner would strand
// an approval a human had already granted.
const RedemptionWindow = redemptionTTL

// Redeem consumes one approved staging for exactly the call it was staged
// for: same tool, same diff_hash, same passport, and the target row still
// at the version the human saw. Single-use is enforced by the conditional
// UPDATE — two racing redemptions cannot both pass.
//
// It answers the version the approval was pinned to (nil when it carried no
// pin). This matters for the callers that redeem in one transaction and
// apply the effect in ANOTHER: the skew check here proves the row was at
// version N when the redemption committed, not that it still is when the
// effect writes. A caller that forwards the authorized call onward binds
// its own write to the returned pin, so the store re-checks it inside the
// transaction that actually mutates. Callers that can hold one transaction
// should use RedeemAndApply instead and have no window at all.
func (s *Service) Redeem(ctx context.Context, id ids.ApprovalID, tool, diffHash string) (version int64, pinned bool, err error) {
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var rerr error
		version, pinned, rerr = s.RedeemInTx(ctx, tx, id, tool, diffHash)
		return rerr
	})
	return version, pinned, err
}

// RedeemAndApply consumes the authority object and applies its effect in the
// same transaction. Effects that can expose a half-applied state use this
// path: a failed domain write leaves the approval unconsumed and retryable.
func (s *Service) RedeemAndApply(ctx context.Context, id ids.ApprovalID, tool, diffHash string, apply func(pgx.Tx) error) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, _, err := s.RedeemInTx(ctx, tx, id, tool, diffHash); err != nil {
			return err
		}
		return apply(tx)
	})
}

// RedeemInTx validates and consumes one approval through a caller-owned
// transaction, answering the version it was pinned to. pinned is false
// for an approval that carried none — a create, or a target type with no
// version column.
func (s *Service) RedeemInTx(ctx context.Context, tx pgx.Tx, id ids.ApprovalID, tool, diffHash string) (version int64, pinned bool, err error) {
	p, ok := principal.Actor(ctx)
	if !ok {
		return 0, false, errors.New("crmapprovals: no actor bound to context")
	}
	a, err := get(ctx, tx, id)
	if err != nil {
		// An unknown approval id reads as an invalid token, not a 404:
		// the caller is asserting authority, not browsing.
		return 0, false, fmt.Errorf("no such approval: %w", apperrors.ErrApprovalTokenInvalid)
	}
	if err := validateRedemption(a, p, tool, diffHash, s.now()); err != nil {
		return 0, false, err
	}

	if err := validateRedemptionTarget(ctx, tx, a); err != nil {
		return 0, false, err
	}

	tag, err := tx.Exec(ctx,
		`UPDATE approval SET consumed_at = now() WHERE id = $1 AND consumed_at IS NULL`, id)
	if err != nil {
		return 0, false, err
	}
	if tag.RowsAffected() != 1 {
		return 0, false, fmt.Errorf("approval already redeemed: %w", apperrors.ErrApprovalTokenInvalid)
	}
	if _, err := s.audit(ctx, tx, p, "update", id.UUID, map[string]any{approvalKeyKind: a.Kind, "redeemed": true}); err != nil {
		return 0, false, err
	}
	if a.TargetVersion == nil {
		return 0, false, nil
	}
	return *a.TargetVersion, true, nil
}

func validateRedemption(a row, p principal.Principal, tool, diffHash string, now time.Time) error {
	switch {
	case a.Status != approvalStatusApproved:
		return fmt.Errorf("approval is %s: %w", a.effectiveStatus(now), apperrors.ErrApprovalTokenInvalid)
	case a.ConsumedAt != nil:
		return fmt.Errorf("approval already redeemed: %w", apperrors.ErrApprovalTokenInvalid)
	case a.DecidedAt != nil && now.Sub(*a.DecidedAt) > redemptionTTL:
		return fmt.Errorf("approval expired %s after decision: %w", redemptionTTL, apperrors.ErrApprovalTokenInvalid)
	case a.Kind != tool:
		return fmt.Errorf("approval is for %s, not %s: %w", a.Kind, tool, apperrors.ErrApprovalTokenInvalid)
	case a.DiffHash != diffHash:
		return fmt.Errorf("call differs from the approved change: %w", apperrors.ErrApprovalTokenInvalid)
	case !p.PassportID.IsZero() && a.PassportID == nil:
		return fmt.Errorf("approval is not bound to a passport: %w", apperrors.ErrApprovalTokenInvalid)
	case !p.PassportID.IsZero() && *a.PassportID != ids.From[ids.PassportKind](p.PassportID):
		return fmt.Errorf("approval was staged by a different passport: %w", apperrors.ErrApprovalTokenInvalid)
	default:
		return nil
	}
}

func validateRedemptionTarget(ctx context.Context, tx pgx.Tx, a row) error {
	if a.TargetVersion == nil || a.TargetID == nil || a.TargetType == nil {
		return nil
	}
	current, err := targetVersion(ctx, tx, *a.TargetType, *a.TargetID)
	if err != nil {
		return err
	}
	if current != *a.TargetVersion {
		return fmt.Errorf("target changed since approval (v%d → v%d): %w",
			*a.TargetVersion, current, apperrors.ErrVersionSkew)
	}
	return nil
}

// versionTables whitelists the tables a target_version re-check may read:
// every entity type a staging can target whose own table both carries a
// version column and BUMPS it on every write (storekit's guarded patch),
// under its own table name. A type outside this set (e.g. the partner
// extension, which audits on its organization row) cannot be
// version-pinned — stagers must leave TargetVersion nil for it rather
// than mint a pin redemption could never verify.
//
// Membership is what makes the whole binding real for a type, in both
// directions: resolveTargetVersion reads the row's version into the staged
// approval, validateRedemptionTarget re-checks it at redemption, and the
// gate forwards it as the released call's own If-Match. A type missing
// here stages with a NULL pin, and every one of those three steps
// short-circuits silently — the human approves a row that anyone may then
// change before the authorized call lands.
var versionTables = map[string]bool{
	tablePerson: true, tableOrganization: true, tableDeal: true, tableLead: true, objectActivity: true,
	targetOffer: true, targetProduct: true, tableList: true, targetTag: true, targetRelationship: true,
	tableProject: true, targetSavedView: true, targetOfferTemplate: true, targetWebhookSubscription: true,
}

// contextTargetKinds are the staging kinds whose target_entity names what the
// proposal is ABOUT rather than the row its effect writes to. Their stagings
// carry no version pin, and the value is the reason why.
//
// The pin binds an approval to the exact content state of the row it
// authorizes an operation against — what stops a confirmed "send this offer"
// executing over an offer that changed underneath it. A proposal that CREATES
// something has no such row, and pinning it anyway binds the approval to a row
// that unrelated writes bump.
//
// This is keyed on the KIND rather than declared per service instance,
// because staging and deciding do not always run through the same instance:
// a declaration attached to the wiring was silently absent wherever a second
// service was constructed, which is the failure it exists to prevent.
// TestEveryContextTargetKindIsExplained holds each entry to its rationale.
var contextTargetKinds = map[string]string{
	"site_lead": "A lead read off a company's website is FILED under that company so the " +
		"inbox can group and filter it, but creating the lead reads none of the " +
		"company's own fields. Pinning it bound the approval to a row that unrelated " +
		"writes bump — and the same enrichment run that discovers the leads writes " +
		"the company's profile fields, so the pin went stale before any human saw " +
		"the lead and every accept failed for the row's lifetime.",
}

// unpinnedKinds are the staging kinds whose target IS the row their effect
// writes — so contextTargetKinds does not describe them — but for which the pin
// protects nothing, and therefore only ever cancels the approval when the row
// moves for an unrelated reason. The value is the reason why.
//
// Two waivers rather than one because they make different claims, and a reader
// has to be able to tell which is being made. contextTargetKinds says the target
// is context the proposal is merely ABOUT; this one says the target is the
// operand and the pin still binds nothing. Filing a kind under the wrong one
// leaves a label that reads true and is not, which is worse than the pin it
// removes. TestEveryUnpinnedKindIsExplained holds each entry to its rationale,
// and TestNoKindIsBothContextOnlyAndUnpinned holds the two maps apart.
var unpinnedKinds = map[string]string{
	kindLinkedInMatch: "The proposal's claim is \"this imported connection is this contact\", and no " +
		"field edit on the contact can make that claim false — the founder decision is " +
		"explicitly that editing a contact must not cancel a LinkedIn match waiting to be " +
		"decided. The write it authorizes is an additive, idempotent person_social insert " +
		"rather than a patch of any field a human could have seen, so there is no content " +
		"state for a pin to protect. Pinning also broke the second of two matches onto one " +
		"contact, because applying the first bumps that person's version.",
}

// TargetIsContextOnly reports whether this kind's target names context rather
// than the row the effect operates on.
func TargetIsContextOnly(kind string) bool {
	_, ok := contextTargetKinds[kind]
	return ok
}

// TargetVersionUnpinned reports whether this kind operates on its target but
// declines the version pin, per unpinnedKinds.
func TargetVersionUnpinned(kind string) bool {
	_, ok := unpinnedKinds[kind]
	return ok
}

// ContextTargetKinds reports the declared kinds and their rationales, so a
// fitness test can hold each one to an explanation.
func ContextTargetKinds() map[string]string {
	return copyRationales(contextTargetKinds)
}

// UnpinnedKinds reports the declared kinds and their rationales, for the same
// reason ContextTargetKinds does.
func UnpinnedKinds() map[string]string {
	return copyRationales(unpinnedKinds)
}

func copyRationales(declared map[string]string) map[string]string {
	out := make(map[string]string, len(declared))
	for kind, why := range declared {
		out[kind] = why
	}
	return out
}

// TargetVersionCheckable reports whether a staged approval against this
// entity type can carry a target_version pin that Redeem is able to
// re-verify (ADR-0036 §2).
func TargetVersionCheckable(entityType string) bool {
	return versionTables[entityType]
}

func targetVersion(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) (int64, error) {
	if !versionTables[table] {
		return 0, fmt.Errorf("crmapprovals: %q is not a versioned target", table)
	}
	var v int64
	err := tx.QueryRow(ctx, `SELECT version FROM `+table+` WHERE id = $1`, id).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, apperrors.ErrNotFound
	}
	return v, err
}
