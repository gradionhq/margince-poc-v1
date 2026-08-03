// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The approval fan-out gate: an approval.*/coldstart.* envelope carries
// staged-change detail (summary, edited_change, target ids), so it may reach a
// subscription owner only when that owner could read the TARGET record itself.
// The gate composes what the owning store's read path composes — the
// object-level READ grant on the target's type, then that store's own row rule —
// because wider is a disclosure over a webhook the API would refuse, and
// narrower is a staged approval nobody is ever told about.
//
// The object half is applied ONCE, above every arm; each arm below is the row
// half alone. The event-subject gate every other subscribable event goes through
// is deliveryvisibility.go, whose primitives this shares.

package webhooks

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The staged target types this gate names in more than one place: once where the
// type is CLASSIFIED and again where the classified arm probes it. ONE spelling,
// because a typo in either half is silent — a type that classifies but does not
// probe fails at delivery time, and one that probes but does not classify is a
// fan-out dropped with nothing said.
const (
	targetOffer        = "offer"
	targetSignal       = "signal"
	targetActivity     = "activity"
	targetRelationship = "relationship"
)

// approvalVisibleTo gates an approval.*/coldstart.* event on the same
// target-visibility rule the approvals inbox enforces (approvals/
// targetvisibility.go targetVisible, C3/ADR-0036): the approval's envelope leaks
// staged-change detail (summary, edited_change, target ids), so it may only
// reach an owner who can see the TARGET record. It resolves the approval's
// polymorphic target and hands the shape to approvalShapeVisible. A missing
// approval row reads as not-visible. The approval table is read with a raw
// probe under the existing WithWorkspaceTx boundary rather than importing the
// approvals module (a module never imports a sibling).
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
	return s.approvalShapeVisible(ctx, targetType, targetID)
}

// approvalShapeVisible answers the fan-out question for each shape a staged
// target can carry:
//
//   - NO target type — a target-LESS approval (every coldstart.* echo, and the
//     approval.requested proposals that are about no record) or an id the
//     envelope cannot name a table for — cannot be scope-bounded at all, so it
//     is EXPLICITLY undelivered, exactly like the deferredDelivery* subjects:
//     never a workspace-wide fan-out of content the owner's grants could not
//     read.
//   - A type with NO id is a staged CREATE: there is no record yet whose scope
//     could bound the fan-out, so the floor is the object-read grant on the
//     type it will write. An owner who may read that type learns a create was
//     proposed on it; one who may not learns nothing — the same floor
//     approvalTargetVisible applies above every one of its arms.
//   - BOTH halves go to the target record's own visibility rule.
func (s *Store) approvalShapeVisible(ctx context.Context, targetType *string, targetID *ids.UUID) (bool, error) {
	if targetType == nil {
		return false, nil
	}
	if targetID == nil {
		return objectReadable(ctx, *targetType)
	}
	return s.approvalTargetVisible(ctx, *targetType, *targetID)
}

// sharedConfigExistence are the approval target types whose owning store
// applies NO row scope of its own — workspace-shared config an object grant
// governs — mapped to the existence query that is their ROW half, under the same
// object-read floor every other arm rides.
//
// Each query mirrors what its own store's read path admits, and the archive
// predicate differs across them for ONE reason, stated here rather than repeated
// per query: where archive IS the delete, an archived row is not a live target
// and the staged effect could never land. custom_field is the single exception —
// retire is a status flip that keeps the row live, so a staged edit against a
// retired field is still a live proposal.
var sharedConfigExistence = map[string]string{
	"product":              `SELECT EXISTS (SELECT 1 FROM product WHERE id = $1 AND archived_at IS NULL)`,
	"tag":                  `SELECT EXISTS (SELECT 1 FROM tag WHERE id = $1 AND archived_at IS NULL)`,
	"offer_template":       `SELECT EXISTS (SELECT 1 FROM offer_template WHERE id = $1 AND archived_at IS NULL)`,
	"webhook_subscription": `SELECT EXISTS (SELECT 1 FROM webhook_subscription WHERE id = $1 AND archived_at IS NULL)`,
	"custom_field":         `SELECT EXISTS (SELECT 1 FROM custom_field WHERE id = $1)`,
}

// approvalTargetRule names HOW a staged target type is scoped for the approval
// fan-out. It exists so "does this gate have a rule for that type at all" has ONE
// source: approvalTargetVisible dispatches on it, ApprovalTargetClassified
// reports on it, and ClassifiedApprovalTargetTypes enumerates what carries one.
//
// The approvals inbox classifies the same vocabulary for the same envelope, and
// the composition layer asserts the two agree across the UNION of both
// classifications. That gate is owed because the two drift silently: a type this
// one has no rule for is a staged row whose approval.requested fan-out is dropped
// with nothing said, while the inbox happily lists it.
type approvalTargetRule int

const (
	// targetRuleNone is the zero value on purpose: an unrecognized type falls
	// here and is not delivered.
	targetRuleNone approvalTargetRule = iota
	// targetRuleRowScoped — the target carries its own owner_id, so the read
	// path's object-read plus its own/team/all row scope answer.
	targetRuleRowScoped
	// targetRuleInheritedScope — the target owns no owner_id and inherits from
	// what it points at: an offer from its deal, a signal from its subject, an
	// activity from any linked record, an edge from ALL of its endpoints.
	targetRuleInheritedScope
	// targetRuleOwnerOnly — PERSONAL state its owning store serves to one human
	// and nobody else. Distinct from targetRuleRowScoped because own/team/all is
	// WIDER than that API: it would deliver a colleague's private row, and the
	// change staged against it, to a manager or an admin.
	targetRuleOwnerOnly
	// targetRuleSharedConfig — workspace-shared config with no row scope. There
	// is no per-owner boundary to honor and the envelope names the config row
	// itself, so the ROW half is existence alone: a staged effect against a row
	// that is gone could never land (sharedConfigExistence carries the queries).
	// The object-read floor above governs it like every other arm.
	targetRuleSharedConfig
	// targetRuleActingWorkspace — the target IS a workspace, not a record: an
	// effective-dated rate sheet has no row of its own until a proposal is
	// accepted, so a brand-new currency or model has nothing whose existence
	// could be the row half. What remains is that the named target must be THIS
	// workspace — the same rule the sibling inbox applies, since the accepted
	// effect writes to the acting workspace's sheet and not to the claimed one.
	targetRuleActingWorkspace
)

// approvalTargetRules is the classification itself. It is a TABLE rather than a
// switch because the parity gate needs the SET, not just membership, and a switch
// cannot be enumerated — a gate that cannot enumerate its subject set quietly
// shrinks to whatever source it can.
//
// The shared-config types are FOLDED IN from sharedConfigExistence rather than
// listed again: that map already names them together with the query each needs,
// and a second spelling is how one of them ends up classified here with no query
// to answer it. The fold-in wins on a shared key, so the two must stay disjoint —
// TestEveryApprovalTargetTakesItsOwningStoresRule pins each type to its own rule,
// which is what a collision breaks.
var approvalTargetRules = func() map[string]approvalTargetRule {
	rules := map[string]approvalTargetRule{
		"person":           targetRuleRowScoped,
		"organization":     targetRuleRowScoped,
		"deal":             targetRuleRowScoped,
		"lead":             targetRuleRowScoped,
		"project":          targetRuleRowScoped,
		"list":             targetRuleRowScoped,
		targetOffer:        targetRuleInheritedScope,
		targetSignal:       targetRuleInheritedScope,
		targetActivity:     targetRuleInheritedScope,
		targetRelationship: targetRuleInheritedScope,
		"saved_view":       targetRuleOwnerOnly,
		"fx_rate":          targetRuleActingWorkspace,
		"ai_model_rate":    targetRuleActingWorkspace,
	}
	for targetType := range sharedConfigExistence {
		rules[targetType] = targetRuleSharedConfig
	}
	return rules
}()

// approvalTargetRuleFor classifies one staged target type. An unlisted type
// answers targetRuleNone (the zero value) and is not delivered.
func approvalTargetRuleFor(targetType string) approvalTargetRule {
	return approvalTargetRules[targetType]
}

// ApprovalTargetClassified reports whether this staged target type has a
// visibility rule on the approval fan-out path at all. Exported for the
// composition layer's parity gate, which holds it against the approvals inbox's
// own classification of the same vocabulary: a type only one of the two
// recognizes is either a fan-out nobody receives or a row the inbox strands, and
// neither failure announces itself.
func ApprovalTargetClassified(targetType string) bool {
	return approvalTargetRuleFor(targetType) != targetRuleNone
}

// ClassifiedApprovalTargetTypes returns every staged target type this gate has a
// rule for, sorted. It is the other half of the parity gate's subject set: asking
// only what the INBOX classifies would leave the reverse drift — a type this gate
// delivers on and the inbox strands — outside what the gate ever looks at.
func ClassifiedApprovalTargetTypes() []string {
	return slices.Sorted(maps.Keys(approvalTargetRules))
}

// approvalTargetVisible gates an approval event on its TARGET record's
// visibility under the SAME read rule the target's own store applies, because the
// envelope discloses the target's details (summary, target ids, edited_change): a
// subscriber may receive an approval only about a target it could itself read,
// never one it cannot. This is the target-visibility half of
// approvals.targetVisible (approvals/targetvisibility.go), and it composes the
// same two halves in the same order — the object-READ grant on the target's type,
// then the row rule of the store that owns it.
//
// The object-read half is applied ONCE here, above the switch, so an arm added
// later inherits it: no classified target type may be answered without it,
// whatever row rule it takes. The row-scoped helpers apply it for themselves as
// well because entityVisibleTo enters them directly for the DIRECT-subject
// events, where this frame does not run; re-asking a pure capability question
// costs nothing, while dropping it from either place is a hole. This gate owes
// the read half on its own account too: unlike the inbox it has no handler-level
// approval.read entry gate, so a lingering row scope with no current read grant
// must not leak the payload through the fan-out.
//
// It deliberately omits ONLY the inbox's `decidable` decision-grant half — that
// governs who may ACT on an approval (authorization), not who may learn a visible
// target's proposed change — so a webhook owner's fan-out set may be broader than
// the inbox's decidable set while disclosing nothing beyond what the owner could
// already read. Unknown target type: fail closed. Self-contained (a module never
// imports a sibling); the row-scoped branches must stay in step with
// entityVisibleTo above.
func (s *Store) approvalTargetVisible(ctx context.Context, targetType string, targetID ids.UUID) (bool, error) {
	readable, err := objectReadable(ctx, targetType)
	if err != nil || !readable {
		return false, err
	}
	switch approvalTargetRuleFor(targetType) {
	case targetRuleRowScoped:
		return s.rowScopedVisible(ctx, targetType, func(c context.Context, tx pgx.Tx) error {
			return auth.EnsureVisible(c, tx, targetType, targetID)
		})
	case targetRuleInheritedScope:
		return s.approvalTargetVisibleThroughParent(ctx, targetType, targetID)
	case targetRuleOwnerOnly:
		// Personal state differs from its row-scoped neighbours in the row half
		// alone: ownership equality instead of own/team/all.
		return s.rowScopedVisible(ctx, targetType, func(c context.Context, tx pgx.Tx) error {
			return ensureSavedViewOwnedByActor(c, tx, targetID)
		})
	case targetRuleSharedConfig:
		// No per-owner boundary exists on workspace-shared config, so the row
		// half is that the row is still there to be acted on.
		return s.rowExists(ctx, sharedConfigExistence[targetType], targetID)
	case targetRuleActingWorkspace:
		// The row half for a target that IS a workspace rather than a record: the
		// workspace named must be the one this envelope belongs to, because the
		// accepted effect writes to the acting workspace's sheet and not to the
		// claimed one. It reaches no table (the row the proposal would create does
		// not exist yet) and asks no grant — the floor above already did, which is
		// what keeps a rep who happens to own a subscription from receiving
		// proposed FX and model pricing that no surface will show them. An unbound
		// workspace fails closed rather than comparing against the nil UUID.
		ws, bound := principal.WorkspaceID(ctx)
		return bound && targetID == ws, nil
	default:
		// Unknown target type: fail closed, exactly like approvals.targetVisible.
		return false, nil
	}
}

// approvalTargetVisibleThroughParent answers for the target kinds carrying no
// owner_id of their own, each behind the object-read grant on its own type — the
// endpoint and parent reads are the clause's own business.
func (s *Store) approvalTargetVisibleThroughParent(ctx context.Context, targetType string, targetID ids.UUID) (bool, error) {
	switch targetType {
	case targetOffer:
		return s.offerVisibleTo(ctx, targetID)
	case targetSignal:
		return s.rowScopedVisible(ctx, targetType, func(c context.Context, tx pgx.Tx) error {
			return auth.EnsureSignalVisible(c, tx, targetID)
		})
	case targetActivity:
		return s.rowScopedVisible(ctx, targetType, func(c context.Context, tx pgx.Tx) error {
			return auth.EnsureActivityVisible(c, tx, targetID)
		})
	case targetRelationship:
		// An edge inherits the CONJUNCTION of its endpoints' scope.
		return s.rowScopedVisible(ctx, targetType, func(c context.Context, tx pgx.Tx) error {
			return auth.EnsureRelationshipVisible(c, tx, targetID)
		})
	default:
		// TOTAL on purpose: a type enrolled in the inherited-scope arm with no
		// parent probe here would otherwise be answered against whichever
		// branch fell last, which is a wrong answer rather than a closed one.
		return false, fmt.Errorf("webhooks: %q is classified as inherited-scope with no parent probe", targetType)
	}
}

// ensureSavedViewOwnedByActor is the row half of the owner-only floor: the view
// must exist, be live, and belong to the human the fan-out is bounded to. Its
// store serves a saved view back on `id AND owner_id` and nothing wider, so
// anything else here would deliver a private view — and the change staged
// against it — over a webhook the API would refuse that owner the row itself in.
// Out of ownership reads as ErrNotFound, which is what the read path answers
// (existence-hiding) and what probeVisible maps to not-visible.
func ensureSavedViewOwnedByActor(ctx context.Context, tx pgx.Tx, viewID ids.UUID) error {
	who, err := owner(ctx)
	if err != nil {
		return err
	}
	var mine bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM saved_view WHERE id = $1 AND owner_id = $2 AND archived_at IS NULL)`,
		viewID, who).Scan(&mine); err != nil {
		return err
	}
	if !mine {
		return apperrors.ErrNotFound
	}
	return nil
}
