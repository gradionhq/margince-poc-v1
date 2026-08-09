// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The decision-authority predicate: who may see and decide a staged
// approval. One predicate (decidable) backs List, Get and Decide alike
// (C3/ADR-0036) — what you cannot see you cannot decide. Its target half —
// whether the record a staged row points at is one this human may see — is
// targetvisibility.go.

package approvals

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// objectActivity is the RBAC object every timeline write is governed by, spelled
// once: three staged kinds and the target-visibility switch below all name it,
// and a typo in any of them would silently ask for a grant nobody holds.
const objectActivity = "activity"

// grantRequirement is one RBAC pair approving a staged kind requires. Named
// rather than anonymous because the derivation below appends to the set and the
// composition layer's satisfiability gate reads the objects back out of it.
type grantRequirement struct {
	Object string
	Action principal.Action
}

// decisionGrants maps each stageable kind onto the RBAC its effect needs given
// the KIND ALONE; approving requires every one of them. A kind whose grant also
// depends on what the staging points at carries that half in
// targetResolvedGrants, and approving then requires the UNION — no kind needs
// both today, and decisionGrantsFor combines them so one that grows a second half
// gains authority rather than silently losing the first.
var decisionGrants = map[string][]grantRequirement{
	// A step-up requires NO object grant, and the empty slice is the decision
	// rather than an omission. Releasing a volume window touches no record: it
	// does not widen what the agent may read, only how much of what it may
	// already read it may be handed. There is no object to name, and naming one
	// would be a fiction that decided the wrong question — a human holding
	// deal.update is not thereby the person who lent this passport.
	//
	// What bounds it instead is selfOnlyKinds below: the lender, and nobody
	// else. Without that entry this empty set would make a step-up decidable by
	// everyone, which is the failure decisionGrantsFor's own comment names — so
	// the two entries are one decision and TestAStepUpIsDecidedByTheLenderAlone
	// holds them together.
	KindQuotaRelease: {},

	"advance_deal": {{tableDeal, principal.ActionUpdate}},
	// progress_deal is advance_deal plus a timeline note; the gated effect
	// is the deal move, so deciding it needs the same grant.
	"progress_deal": {{tableDeal, principal.ActionUpdate}},
	"promote_lead":  {{tableLead, principal.ActionUpdate}, {tablePerson, principal.ActionCreate}},
	// Disqualifying retires the lead in place, and the store gates it on
	// `lead:delete` (people/lead.go DisqualifyLead) as the REST twin's DELETE
	// implies. Deciding takes the grant PERFORMING it takes: anything less and
	// the confirm-first control point sits with someone who could not do the
	// thing they are releasing.
	"disqualify_lead": {{tableLead, principal.ActionDelete}},
	// A phase transition writes the project row and one phase-history row in
	// one transaction (deals/project_advance.go); the gated effect is the
	// project move, so the approver needs the project's update grant.
	"advance_project_phase": {{tableProject, principal.ActionUpdate}},
	// A send is an activity write plus consent enforcement at redemption
	// time; the approver needs the write grant, the consent gate runs in
	// the handler regardless of who approved.
	"send_email": {{objectActivity, principal.ActionCreate}},
	// send_message is the same effect on a messaging channel: an activity
	// write, with the consent gate running in the handler whoever approved it.
	"send_message": {{objectActivity, principal.ActionCreate}},
	"book_meeting": {{objectActivity, principal.ActionCreate}},
	// Accepting a cold-start read-back writes enrichment fields onto an
	// organization; "enrich" is the same effect staged through the
	// transport gate by an agent caller.
	"coldstart": {{tableOrganization, principal.ActionUpdate}},
	"enrich":    {{tableOrganization, principal.ActionUpdate}},
	// A rate refresh proposes an effective-dated row on a workspace-shared price
	// sheet, and deciding it requires BOTH write verbs on that sheet.
	//
	// The release is an upsert: it inserts a new (currency, day) or replaces an
	// existing rate, and which one it will be is not knowable when the decision
	// is made — the sheet can change between the decision and the apply. The
	// apply also runs as the system principal, so the store's in-transaction
	// check on the specific verb never fires here; this is the only grant
	// standing between an approver and the row the release replaces.
	//
	// Either verb alone would authorize the operation it does not name: a
	// create-only approver could release an overwrite, precisely the
	// substitution the store's second check exists to refuse. Requiring both is
	// the conservative reading — approve an upsert only if you could have
	// performed either half yourself. Every seeded role holding one holds the
	// other (writeNoDelete for admin and ops, the zero grant for everyone
	// else), so this constrains edited roles only, and constrains them right.
	"fx_rate_proposal": {
		{targetFxRate, principal.ActionCreate},
		{targetFxRate, principal.ActionUpdate},
	},
	"ai_model_rate_proposal": {
		{targetAIModelRate, principal.ActionCreate},
		{targetAIModelRate, principal.ActionUpdate},
	},
	// Accepting a deep site read writes profile fields and category facts
	// onto the target organization — the same update authority enrich needs.
	"deepread": {{tableOrganization, principal.ActionUpdate}},
	// Accepting a site_lead proposal (a published person from a deep read's
	// team page) captures them as a LEAD through the capture sink — the
	// effect is a lead create, so deciding it needs that grant.
	"site_lead": {{tableLead, principal.ActionCreate}},
	// Approving a LinkedIn match links an imported connection to a contact and
	// writes that contact's LinkedIn address — a person write, so deciding it
	// needs the grant the write itself takes.
	"linkedin_match": {{tablePerson, principal.ActionUpdate}},
	// Accepting a capture_counterparty proposal (ADR-0072/A118: a first-time
	// sender the verdict engine could not judge) creates the person and, unless
	// the domain is free-mail, the organization behind them — so deciding it
	// needs both create grants, exactly as if the approver had typed them in.
	"capture_counterparty": {{tablePerson, principal.ActionCreate}, {tableOrganization, principal.ActionCreate}},
	// Accepting an org_name_promotion proposal (PO-F-2a: one employee's
	// signature naming their company, with nothing corroborating it) renames
	// the organization — the same update authority the name editor needs.
	"org_name_promotion": {{tableOrganization, principal.ActionUpdate}},
	// Accepting a lifecycle_change proposal (the account-intelligence arc: the
	// correspondence says the contract ended while the record still reads as
	// live) moves the account's stage — the same update authority the header's
	// own stage picker needs.
	//
	// It also carries the signal's own summary in its payload and settles that
	// signal on accept, so it needs the signal grant too. Without it an
	// organization editor could read model-derived correspondence and close a
	// signal they have no standing to see — the ordinary triage path takes
	// signal:update and EnsureSignalVisible for exactly that reason.
	"lifecycle_change": {{tableOrganization, principal.ActionUpdate}, {targetSignal, principal.ActionUpdate}},
	// Confirming a nightly close-date correction (formulas §11 🟡 tier)
	// releases an expected_close_date write onto the deal.
	"close_date_correction": {{tableDeal, principal.ActionUpdate}},
	// Confirming an overnight follow-up proposal (features/07 §8a) creates
	// the drafted task activity; the target deal's visibility gates who
	// may see and decide it (targetVisible), the create grant gates the
	// write the confirm performs.
	"deal_follow_up": {{objectActivity, principal.ActionCreate}},
}

// targetResolvedGrants are the kinds whose decision grant is not fixed by the
// kind but read off the staged target's own entity type, mapped to the action
// the release performs on it:
//
//   - archive_record deletes the target.
//   - merge_records rewrites where records point, and the store maps the merge
//     verb to update.
//   - update_record releases a field patch (human-edit precedence,
//     interfaces.md §2.1) — the grant the patch itself would need.
//   - create_record releases a new record of the type (a 🟡 create staged at the
//     transport gate, e.g. createCustomField).
//
// ONE table, because two readers derive from it: requireDecisionGrants enforces
// the pair against a principal, and DecisionGrantObjects reports the objects to
// the composition layer's satisfiability gate. A second copy would let that gate
// certify a route whose real decision demands a different object.
var targetResolvedGrants = map[string]principal.Action{
	"archive_record": principal.ActionDelete,
	"merge_records":  principal.ActionUpdate,
	"update_record":  principal.ActionUpdate,
	"create_record":  principal.ActionCreate,
}

// decidedEcho builds the approved/rejected payload a kind's decision
// echoes, given the decided approval's own id and the deciding human's
// user id — the fixed shape every decided-echo carries today.
type decidedEcho struct {
	approved, rejected func(approvalID, decidedBy openapi_types.UUID) events.Payload
}

// kindDecidedEvents names the domain event a decision echoes for kinds
// whose lifecycle the event catalog tracks beyond approval.decided.
var kindDecidedEvents = map[string]decidedEcho{
	"coldstart": {
		approved: func(approvalID, decidedBy openapi_types.UUID) events.Payload {
			return crmcontracts.PublicEventColdstartAccepted{ApprovalId: approvalID, DecidedBy: decidedBy}
		},
		rejected: func(approvalID, decidedBy openapi_types.UUID) events.Payload {
			return crmcontracts.PublicEventColdstartRejected{ApprovalId: approvalID, DecidedBy: decidedBy}
		},
	},
}

// The target types this package names in more than one place. They are the
// `target_entity_type` vocabulary the staged rows carry, and this package spells
// each in several places — the visibility probe's classification, the
// decision-grant map, and the version-table whitelist. One spelling, because a
// typo makes a target undecidable in the first and unpinnable in the last, and
// neither failure announces itself.
const (
	targetOffer        = "offer"
	targetProduct      = "product"
	targetRelationship = "relationship"
	targetSavedView    = "saved_view"
	targetSignal       = "signal"
	targetTag          = "tag"
	// The workspace-shared config rows an object grant governs, each named by
	// both the existence probe and the version-table whitelist.
	targetOfferTemplate       = "offer_template"
	targetWebhookSubscription = "webhook_subscription"
	// The effective-dated rate sheets. Both are named twice — the probe
	// classification and the decision-grant map — and both are workspace-scoped
	// config with no row of their own until a proposal is accepted.
	targetFxRate      = "fx_rate"
	targetAIModelRate = "ai_model_rate"
	// The row-scoped record tables. Named as a SET rather than one at a time: this
	// package spells them in the probe classification, the decision-grant map and
	// the version-table whitelist, and a typo makes a target undecidable in the
	// first and unpinnable in the last without announcing either.
	tablePerson       = "person"
	tableOrganization = "organization"
	tableDeal         = "deal"
	tableLead         = "lead"
	tableProject      = "project"
	tableList         = "list"
)

// selfOnlyKinds are the staging kinds whose proposal is nobody's business but
// the member it was staged for.
//
// The inbox is a SHARED surface by design — a manager triages what a rep
// staged — and for almost every kind that is the point. It is wrong for one:
// a LinkedIn match names a third party out of one member's imported address
// book, people who never agreed to be in this CRM at all. The endpoints this
// kind replaced were owner-only and said so; routing the same question through
// a shared inbox would have handed every admin a readable copy of a
// colleague's contact list, which is a bigger disclosure than the feature it
// enables.
//
// So a self-only kind adds one predicate to the two below: the deciding human
// must BE the member it was staged for. It is the inbox's mirror of the
// webhooks module's selfOnlyEvents, which keeps the same three LinkedIn facts
// off the workspace fan-out for the same reason.
//
// A step-up is the other: "may this agent keep reading" is a question about ONE
// connection, and the only person who can answer it is the human whose authority
// that connection borrows.
var selfOnlyKinds = map[string]bool{"linkedin_match": true, KindQuotaRelease: true}

// decidable is the ONE visibility-and-authority predicate for the inbox
// and the decision: true when p holds every grant approving a would
// require AND could read the staged target itself — the object-read grant
// on its type, then that record's own row rule (targetvisibility.go). It
// backs List, Get and Decide alike, so triage visibility and the decision
// gate can never drift apart — you see exactly what you could act on, and
// what you cannot see you cannot decide (in either direction). Two shapes
// narrow that to ONE seat, the member the row was staged for: a self-only
// kind, and a staged create against a table whose rows belong to one human
// each. An unknown kind (no mapping) or unknown target type is not
// decidable: fail-closed.
func decidable(ctx context.Context, tx pgx.Tx, p principal.Principal, a row) (bool, error) {
	if requireDecisionGrants(p, a) != nil {
		return false, nil
	}
	if selfOnlyKinds[a.Kind] || stagedForStagerOnly(a.TargetType, a.TargetID != nil) {
		// Two routes to the same predicate: a kind whose subject is one member's
		// own business, and a staged create against a table whose rows belong to
		// one human each — where no row exists yet for an ownership probe to ask.
		// Fail-closed on a missing stager: a proposal nobody is recorded for is
		// one nobody may read, not one everybody may.
		if a.OnBehalfOf == nil || p.UserID == ids.Nil || a.OnBehalfOf.UUID != p.UserID {
			return false, nil
		}
	}
	return targetVisible(ctx, tx, a.TargetType, a.TargetID)
}

func requireDecisionGrants(p principal.Principal, a row) error {
	grants, err := decisionGrantsFor(a.Kind, a.TargetType)
	if err != nil {
		return err
	}
	for _, g := range grants {
		if !p.Permissions.Allows(g.Object, g.Action) {
			return fmt.Errorf("approving %s needs %s.%s: %w", a.Kind, g.Object, g.Action, apperrors.ErrPermissionDenied)
		}
	}
	return nil
}

// decisionGrantsFor derives every grant approving this staging requires: the
// kind's own, plus — for the kinds whose authority is the target's entity type —
// that type's. An unmapped kind, and a target-resolved kind staged with no
// target type, both error: neither can be decided by anyone, and answering an
// empty grant set for either would make them decidable by everyone.
func decisionGrantsFor(kind string, targetType *string) ([]grantRequirement, error) {
	fixed, hasFixed := decisionGrants[kind]
	action, resolvedFromTarget := targetResolvedGrants[kind]
	if !hasFixed && !resolvedFromTarget {
		return nil, fmt.Errorf("crmapprovals: kind %q has no decision-grant mapping", kind)
	}
	if !resolvedFromTarget {
		return fixed, nil
	}
	if targetType == nil {
		return nil, fmt.Errorf("crmapprovals: %s staged without a target type", kind)
	}
	// Cloned, not appended in place: fixed is the map's own slice, and appending
	// into its spare capacity would rewrite what the next caller reads.
	return append(slices.Clone(fixed), grantRequirement{*targetType, action}), nil
}

// DecisionGrantObjects reports the RBAC objects approving a staging of this kind
// against this target type requires.
//
// Exported for the composition layer's decidability gate, which has to prove
// more than that the staged target has a visibility rule: the derived grants must
// also be SATISFIABLE. An object outside the vocabulary a role document may name
// is allowed by no principal that can exist, so a staged row demanding it is
// permanently undecidable — the same dead row as a missing visibility rule,
// reached from the authority side. This package cannot ask identity for that
// vocabulary itself (a module never imports a sibling), so it answers what it
// demands and the composition layer, which imports both, checks it.
func DecisionGrantObjects(kind, targetType string) ([]string, error) {
	grants, err := decisionGrantsFor(kind, &targetType)
	if err != nil {
		return nil, err
	}
	objects := make([]string, 0, len(grants))
	for _, g := range grants {
		objects = append(objects, g.Object)
	}
	return objects, nil
}

// humanOnly guards the inbox and the decision: an agent approving its own
// staged action would collapse the whole tier model.
func humanOnly(ctx context.Context) error {
	p, ok := principal.Actor(ctx)
	if !ok {
		return errors.New("crmapprovals: no actor bound to context")
	}
	if p.Type != principal.PrincipalHuman {
		return fmt.Errorf("approvals are decided by humans: %w", apperrors.ErrPermissionDenied)
	}
	return nil
}

// KindHasDecisionGrants reports whether a stageable kind carries a
// decision-grant mapping — its own, or one resolved from the staged target's
// entity type. The composition layer's fitness test calls it for every
// 🟡/dynamic tool in the registry: a tool that can stage an approval nobody is
// mapped to decide would strand its stagings in a queue no inbox shows
// (decidable fails closed on unknown kinds).
func KindHasDecisionGrants(kind string) bool {
	if _, ok := decisionGrants[kind]; ok {
		return true
	}
	_, resolvedFromTarget := targetResolvedGrants[kind]
	return resolvedFromTarget
}
