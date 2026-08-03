// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The target half of the decision-authority predicate: whether the record a
// staged row points at is one the asking human may see. It composes what the
// owning store's read path composes — the object-level READ grant on the
// target's type, and then that store's own row rule — because a probe wider than
// that store discloses a record through the inbox, and a probe narrower than it
// strands the staged row where nobody can release or reject it.
//
// The object half is applied ONCE, above every arm; each arm below is the row
// half alone.

package approvals

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// targetProbe names HOW the ROW half of a target type's visibility is decided —
// the object-read grant on the type is composed above every one of them, in
// targetVisible. It exists so the answer "is a staged row against this target
// decidable at all" has ONE source: targetVisible dispatches on it,
// TargetShapeDecidable reports on it, and ClassifiedTargetTypes enumerates what
// carries one.
//
// That mattered the moment the tool surface started minting staged rows for types
// nobody had checked. A type with no rule is not decidable, which means its
// staged row is invisible in the inbox AND undecidable at the decision — an
// authority object a human can neither release nor reject, and the fan-out that
// would have told them about it is dropped for the same reason. The composition
// layer derives the obligation over the generated policy table
// (TestEveryConfirmFirstTargetTypeIsDecidable), so a confirm-first verb whose
// staged shape has no rule here fails a gate instead of shipping a zombie.
type targetProbe int

const (
	// probeNoRule is the zero value on purpose: an unrecognized type falls here
	// and fails closed.
	probeNoRule targetProbe = iota
	// probeOwnScope — the row carries owner_id, so its own row scope answers.
	probeOwnScope
	// probeInheritedScope — the row owns no owner_id and inherits from what it
	// points at: an offer from its deal, a signal from its subject, an activity
	// from any linked record, an edge from ALL of its endpoints.
	probeInheritedScope
	// probeExistence — workspace-shared config with no row scope of its own, so
	// the row half is existence alone: a staging against a row that is gone is
	// not decidable. The object-read floor and the decision grants are the
	// authority.
	probeExistence
	// probeOwnerOnly — the row is PERSONAL state, which its owning store serves
	// to one human and nobody else, so the probe is that same ownership. It is
	// distinct from probeOwnScope because own/team/all is WIDER than the API on
	// such a table: it would show a manager or an admin a colleague's private
	// row, and the change staged against it, in an inbox the read path refuses
	// them the row itself in.
	probeOwnerOnly
	// probeActingWorkspace — the target IS a workspace (an effective-dated price
	// sheet with no row of its own yet), so the floor is that it is THIS one.
	probeActingWorkspace
)

// targetProbes is the classification itself: which probe answers for each staged
// target type. It is a TABLE rather than a switch because two readers need it —
// targetVisible dispatches on it, and ClassifiedTargetTypes hands the composition
// layer's parity gate the set it has to cover, which a switch cannot enumerate.
// A gate that cannot enumerate its subject set silently shrinks to whatever
// source it can enumerate, which is quieter than being wrong.
//
// The existence-probed types are FOLDED IN from existenceProbes rather than
// listed again: that map already names them together with the query each needs,
// and a second spelling is how one of them ends up classified with no query to
// answer it. The fold-in wins on a key it shares with the literal above, so the
// two must stay disjoint — TestEveryTargetProbeMirrorsItsOwningStoresReadRule
// pins each type to its own probe, which is what a collision breaks.
var targetProbes = func() map[string]targetProbe {
	probes := map[string]targetProbe{
		tablePerson:        probeOwnScope,
		tableOrganization:  probeOwnScope,
		tableDeal:          probeOwnScope,
		tableLead:          probeOwnScope,
		tableProject:       probeOwnScope,
		tableList:          probeOwnScope,
		targetOffer:        probeInheritedScope,
		targetSignal:       probeInheritedScope,
		objectActivity:     probeInheritedScope,
		targetRelationship: probeInheritedScope,
		targetSavedView:    probeOwnerOnly,
		targetFxRate:       probeActingWorkspace,
		targetAIModelRate:  probeActingWorkspace,
	}
	for targetType := range existenceProbes {
		probes[targetType] = probeExistence
	}
	return probes
}()

// probeFor classifies one staged target type. An unlisted type answers
// probeNoRule (the zero value) and fails closed.
func probeFor(targetType string) targetProbe { return targetProbes[targetType] }

// ClassifiedTargetTypes returns every staged target type this package has a
// visibility rule for, sorted.
//
// Exported for the composition layer's parity gate against the webhook fan-out's
// own classification of the same vocabulary. That gate cannot take its subject
// set from the contract's agent-policy table alone: a target type staged by a
// server-side proposal flow rather than by an agent's call — an effective-dated
// rate sheet is one — appears in no agent policy at all, so a gate reading only
// that table reads green over exactly the drift it exists to catch.
func ClassifiedTargetTypes() []string {
	return slices.Sorted(maps.Keys(targetProbes))
}

// targetShape is a staged target reduced to which halves it carries, which is
// all the shape rule below needs — and naming the two at every call site is what
// keeps a caller from transposing them.
type targetShape struct{ hasType, hasID bool }

// settledByShape answers the staged shapes whose decidability the target PAIR
// settles on its own, before any row is probed:
//
//   - NO target id — whether the row names a target type (a staged CREATE,
//     whose record does not exist yet) or nothing at all (a cold-start
//     proposal, which is about no record yet) — carries no ROW half at all.
//     There is nothing whose scope could bound it, so what remains is the
//     object-read floor on the type it names (targetVisible applies it) and the
//     grants requireDecisionGrants demands of the caller.
//   - An id with NO type is not decidable. It names a concrete record the
//     probe cannot resolve, and treating it as unbounded would put that
//     record's summary and proposed change in the inbox of everyone holding
//     the object grant, and let any of them decide a write against a row their
//     own scope hides.
//
// A pair carrying BOTH halves is not settled here: it goes to the target type's
// own probe. ONE spelling of the rule, because targetVisible runs it for the
// inbox and the decision while TargetShapeDecidable reports it to the
// composition layer's gate — a second copy would let the gate read green over
// the predicate a human's inbox actually runs.
func settledByShape(shape targetShape) (settled, visible bool) {
	if !shape.hasID {
		return true, true
	}
	if !shape.hasType {
		return true, false
	}
	return false, false
}

// TargetShapeDecidable reports whether a staged row carrying this target SHAPE
// — the target type, plus whether the staging names a concrete target id — can
// be seen and decided at all. Exported for the composition layer's gate: a
// confirm-first verb whose staged shape answers false mints authority objects
// no human can ever release or reject.
//
// The type alone is not the question, and asking it that way reads green over
// half the class: a staging with no target id is decidable whatever its type
// is, and a type with a probe below is still undecidable when the id that probe
// needs is absent.
//
// It answers the shape and the row half's classification, which is all a SHAPE
// can answer. The object-read floor targetVisible composes above every arm is a
// question about the asking human, so the gate proves that half its own way:
// read on the staged target's own type has to be a grant some role document may
// name, or no principal that can exist passes the floor.
func TargetShapeDecidable(targetType string, hasTargetID bool) bool {
	if settled, visible := settledByShape(targetShape{hasType: true, hasID: hasTargetID}); settled {
		return visible
	}
	return probeFor(targetType) != probeNoRule
}

// targetVisible applies the target record's OWN read rule to the approval —
// BOTH halves of it, in the order the owning store applies them. The object-READ
// grant on the target's type comes first: holding tag.delete does not entitle a
// human to the tags they may not read, nor to the change staged against one. The
// arm then applies that store's row rule: holding deal.update does not entitle a
// rep to see — or decide — a staged change against another team's deal, and
// holding saved_view.delete does not entitle anyone to a colleague's private
// view. With the decision grants requireDecisionGrants demands (authority.go),
// those are the whole predicate — read the target's type, see its row, hold what
// the effect needs — so the inbox can never disclose more than the record would.
//
// A pair missing either half is answered by settledByShape, which states that
// rule — still under the floor whenever it names a type at all; a pair carrying
// both goes to the type's probe below, and a target the probe errors on stays
// invisible.
//
// It takes the pair rather than a row because a target-FILTERED read asks the
// same question about a target the client named, before any row is in hand.
// That read is entered only with BOTH halves in hand (ListInput.targeted), so
// the id-less shape can never turn a type filter into a page of rows whose own
// targets the caller cannot see. An unrecognized type must fail closed there
// too: auth.VisibleTo errors on a table it does not row-scope, so the switch
// below — not the caller — is what keeps a made-up target_entity_type from
// reaching it.
func targetVisible(ctx context.Context, tx pgx.Tx, targetType *string, targetID *ids.UUID) (bool, error) {
	settled, visible := settledByShape(targetShape{hasType: targetType != nil, hasID: targetID != nil})
	if targetType == nil {
		// No type is no object to require a read grant ON, and settledByShape
		// settles both such shapes: a target-LESS proposal, which is about no
		// record at all, and an id whose table the staging never named.
		return visible, nil
	}
	// The object-read floor, ONE rule above every arm rather than a line inside
	// each: it is the half the owning store applies before its own row rule, so
	// an arm added later inherits it without anyone remembering. A denial reads
	// as not-visible — the caller turns that into the same existence-hiding
	// answer the record's own read gives, never a 403 that would confirm the row.
	readable, err := objectReadable(ctx, *targetType)
	if err != nil || !readable {
		return false, err
	}
	if settled {
		// A staged CREATE: no row exists yet whose scope could bound it, so read
		// on the type its row will land in is the whole rule the floor above just
		// applied, and the decision grants carry the rest of the authority.
		return visible, nil
	}
	switch probeFor(*targetType) {
	case probeOwnScope:
		return auth.VisibleTo(ctx, tx, *targetType, *targetID)
	case probeInheritedScope:
		return targetVisibleThroughParent(ctx, tx, *targetType, *targetID)
	case probeExistence:
		return targetExists(ctx, tx, *targetType, *targetID)
	case probeOwnerOnly:
		return targetOwnedByActor(ctx, tx, *targetType, *targetID)
	case probeActingWorkspace:
		// Effective-dated price sheets are workspace-shared admin config
		// with no row scope. A refresh proposal targets the workspace (a
		// brand-new currency/model has no row yet), so existence is not the
		// row half here — the object-read floor above and the decision-grant
		// check (admin/ops Create) carry the authority. The row half that
		// remains is that the shown target IS the acting workspace: a proposal
		// whose target_id is some other workspace is not decidable here (its
		// effect would write to this context's sheet, not the claimed one).
		wsID, ok := principal.WorkspaceID(ctx)
		return ok && *targetID == wsID, nil
	default:
		return false, nil // no rule for this target type: fail closed
	}
}

// objectReadable reports whether the asking human holds the object-level READ
// grant on a staged target's type — the auth.Require half every owning store
// applies before it looks at a row at all.
//
// A denial is an ANSWER, not an error: the inbox hides an approval it may not
// disclose exactly as the record's own read hides the record, so a permission
// denial must reach the caller as not-visible and become a 404, never a 403 that
// would confirm the staged row exists. Any other resolution failure surfaces.
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

// targetVisibleThroughParent answers for the target kinds that carry no
// owner_id of their own and are visible exactly when the record they hang off
// is — the same anchoring each one's own store applies, so a staged action
// discloses nothing the record itself would not.
func targetVisibleThroughParent(ctx context.Context, tx pgx.Tx, targetType string, targetID ids.UUID) (bool, error) {
	if targetType == targetOffer {
		var dealID ids.UUID
		err := tx.QueryRow(ctx, `SELECT deal_id FROM offer WHERE id = $1`, targetID).Scan(&dealID)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return auth.VisibleTo(ctx, tx, tableDeal, dealID)
	}
	var ensure func(context.Context, pgx.Tx, ids.UUID) error
	switch targetType {
	case targetSignal:
		ensure = auth.EnsureSignalVisible
	case objectActivity:
		ensure = auth.EnsureActivityVisible
	case targetRelationship:
		// An edge inherits the CONJUNCTION of its endpoints' scope, which is one
		// spelling in platform/auth because people's own reads and this probe are
		// two readers of the same rule.
		ensure = auth.EnsureRelationshipVisible
	default:
		// TOTAL on purpose. A signal default read as "whatever is left", so a type
		// added to probeFor's inherited-scope arm — which looks like the whole act
		// of enrolling one — would have been probed against the SIGNAL table: a
		// wrong-scope answer rather than a closed one, from the branch that exists
		// to be closed. probeFor is the one source only if this cannot silently
		// disagree with it.
		return false, fmt.Errorf(
			"crmapprovals: %q is classified as inherited-scope with no parent probe", targetType)
	}
	switch err := ensure(ctx, tx, targetID); {
	case err == nil:
		return true, nil
	case errors.Is(err, apperrors.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// existenceProbes are the target types whose owning store applies NO row scope
// of its own — workspace-shared config an object grant governs — mapped to the
// existence query that is their whole visibility floor. The decision-grant check
// is the authority question; a staging against a record that does not exist is
// still not decidable.
//
// Each query mirrors what its own store's read path admits, and the archive
// predicate differs across them for ONE reason, stated here rather than repeated
// per query: where archive IS the delete, an archived row is not a live target
// and the effect staged against it could never land, so the probe requires a
// live row. custom_field is the single exception — retire is a status flip that
// keeps the row live, so a staged edit against a retired field stays decidable.
var existenceProbes = map[string]string{
	targetProduct:             `SELECT EXISTS (SELECT 1 FROM product WHERE id = $1 AND archived_at IS NULL)`,
	targetTag:                 `SELECT EXISTS (SELECT 1 FROM tag WHERE id = $1 AND archived_at IS NULL)`,
	targetOfferTemplate:       `SELECT EXISTS (SELECT 1 FROM offer_template WHERE id = $1 AND archived_at IS NULL)`,
	targetWebhookSubscription: `SELECT EXISTS (SELECT 1 FROM webhook_subscription WHERE id = $1 AND archived_at IS NULL)`,
	"custom_field":            `SELECT EXISTS (SELECT 1 FROM custom_field WHERE id = $1)`,
}

func targetExists(ctx context.Context, tx pgx.Tx, targetType string, targetID ids.UUID) (bool, error) {
	query, ok := existenceProbes[targetType]
	if !ok {
		// Unreachable while probeFor derives its existence arm from this map,
		// and total so it stays that way: a type answered against some other
		// type's table is a wrong answer, which is worse than a loud one.
		return false, fmt.Errorf("crmapprovals: %q is classified as existence-probed with no existence query", targetType)
	}
	var exists bool
	if err := tx.QueryRow(ctx, query, targetID).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// targetOwnedByActor is the floor for a target that is PERSONAL state: its
// owning store reads the row back for exactly one human, so the probe is that
// same ownership equality against a live row.
//
// It is deliberately NOT auth.VisibleTo. That clause admits own/team/all, so on
// a table the API serves owner-only it would put a colleague's private row — and
// the change staged against it — into a manager's or an admin's inbox, and let
// them decide a write the read path refuses them the row itself for. The probe
// may never be wider than the store it mirrors.
func targetOwnedByActor(ctx context.Context, tx pgx.Tx, targetType string, targetID ids.UUID) (bool, error) {
	if targetType != targetSavedView {
		// TOTAL on purpose, like the inherited-scope probe: a type enrolled in
		// probeOwnerOnly with no ownership query here must fail loudly rather
		// than be answered against somebody else's table.
		return false, fmt.Errorf("crmapprovals: %q is classified as owner-only with no ownership query", targetType)
	}
	p, ok := principal.Actor(ctx)
	if !ok {
		return false, errors.New("crmapprovals: no actor bound to context")
	}
	if p.UserID == ids.Nil {
		// A principal with no human identity owns no personal state, and
		// comparing owner_id against the nil UUID would answer for a row
		// nobody owns rather than for nothing.
		return false, nil
	}
	var visible bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM saved_view WHERE id = $1 AND owner_id = $2 AND archived_at IS NULL)`,
		targetID, p.UserID).Scan(&visible); err != nil {
		return false, err
	}
	return visible, nil
}
