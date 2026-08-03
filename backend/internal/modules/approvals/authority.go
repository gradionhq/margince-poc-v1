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

// decisionGrants maps each stageable kind onto the RBAC the underlying
// effect needs; approving requires every one of them.
var decisionGrants = map[string][]struct {
	Object string
	Action principal.Action
}{
	"advance_deal": {{tableDeal, principal.ActionUpdate}},
	// progress_deal is advance_deal plus a timeline note; the gated effect
	// is the deal move, so deciding it needs the same grant.
	"progress_deal":  {{tableDeal, principal.ActionUpdate}},
	"promote_lead":   {{tableLead, principal.ActionUpdate}, {tablePerson, principal.ActionCreate}},
	"archive_record": {}, // resolved from the target's entity type below
	"merge_records":  {}, // resolved from the target's entity type below
	"share_record":   {}, // resolved from the target's entity type below
	"update_record":  {}, // resolved from the target's entity type below (human-edit-precedence stagings)
	"create_record":  {}, // resolved from the target's entity type below (🟡 creates staged at the transport gate, e.g. createCustomField)
	// A send is an activity write plus consent enforcement at redemption
	// time; the approver needs the write grant, the consent gate runs in
	// the handler regardless of who approved.
	"send_email": {{objectActivity, principal.ActionCreate}},
	// send_message is the same effect on a messaging channel: an activity
	// write, with the consent gate running in the handler whoever approved it.
	"send_message": {{objectActivity, principal.ActionCreate}},
	"book_meeting": {{objectActivity, principal.ActionCreate}},
	// Sending an offer releases the draft→sent transition (B-E03.19) —
	// an offer write; deciding it needs the same grant the send itself
	// requires.
	"send_offer": {{targetOffer, principal.ActionUpdate}},
	// Accepting a cold-start read-back writes enrichment fields onto an
	// organization; "enrich" is the same effect staged through the
	// transport gate by an agent caller.
	"coldstart": {{tableOrganization, principal.ActionUpdate}},
	"enrich":    {{tableOrganization, principal.ActionUpdate}},
	// A rate refresh proposes an effective-dated row on a workspace-shared
	// price sheet; deciding it needs the same admin/ops Create grant the
	// editor's write path requires.
	"fx_rate_proposal":       {{targetFxRate, principal.ActionCreate}},
	"ai_model_rate_proposal": {{targetAIModelRate, principal.ActionCreate}},
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
	// Confirming a nightly close-date correction (formulas §11 🟡 tier)
	// releases an expected_close_date write onto the deal.
	"close_date_correction": {{tableDeal, principal.ActionUpdate}},
	// Confirming an overnight follow-up proposal (features/07 §8a) creates
	// the drafted task activity; the target deal's visibility gates who
	// may see and decide it (targetVisible), the create grant gates the
	// write the confirm performs.
	"deal_follow_up": {{objectActivity, principal.ActionCreate}},
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
var selfOnlyKinds = map[string]bool{"linkedin_match": true}

// decidable is the ONE visibility-and-authority predicate for the inbox
// and the decision: true when p holds every grant approving a would
// require AND can see the target row under their own/team/all scope. It
// backs List, Get and Decide alike, so triage visibility and the decision
// gate can never drift apart — you see exactly what you could act on, and
// what you cannot see you cannot decide (in either direction). An unknown
// kind (no mapping) or unknown target type is not decidable: fail-closed.
func decidable(ctx context.Context, tx pgx.Tx, p principal.Principal, a row) (bool, error) {
	if requireDecisionGrants(p, a) != nil {
		return false, nil
	}
	if selfOnlyKinds[a.Kind] {
		// Fail-closed on a missing stager: a self-only proposal nobody is
		// recorded for is one nobody may read, not one everybody may.
		if a.OnBehalfOf == nil || p.UserID == ids.Nil || a.OnBehalfOf.UUID != p.UserID {
			return false, nil
		}
	}
	return targetVisible(ctx, tx, a.TargetType, a.TargetID)
}

func requireDecisionGrants(p principal.Principal, a row) error {
	grants, known := decisionGrants[a.Kind]
	if !known {
		return fmt.Errorf("crmapprovals: kind %q has no decision-grant mapping", a.Kind)
	}
	if a.Kind == "archive_record" {
		if a.TargetType == nil {
			return errors.New("crmapprovals: archive_record staged without a target type")
		}
		grants = append(grants, struct {
			Object string
			Action principal.Action
		}{*a.TargetType, principal.ActionDelete})
	}
	// Sharing widens who sees the target — approving needs the target
	// type's update grant, exactly like a direct share would.
	if a.Kind == "share_record" {
		if a.TargetType == nil {
			return errors.New("crmapprovals: share_record staged without a target type")
		}
		grants = append(grants, struct {
			Object string
			Action principal.Action
		}{*a.TargetType, principal.ActionUpdate})
	}
	// A merge rewrites where records point — the store maps the merge verb to
	// update, so approving needs update on the target's entity type.
	if a.Kind == "merge_records" {
		if a.TargetType == nil {
			return errors.New("crmapprovals: merge_records staged without a target type")
		}
		grants = append(grants, struct {
			Object string
			Action principal.Action
		}{*a.TargetType, principal.ActionUpdate})
	}
	// A human-edit-precedence staging (interfaces.md §2.1) releases a
	// field patch — approving needs the update grant the patch itself
	// would need on the target's entity type.
	if a.Kind == "update_record" {
		if a.TargetType == nil {
			return errors.New("crmapprovals: update_record staged without a target type")
		}
		grants = append(grants, struct {
			Object string
			Action principal.Action
		}{*a.TargetType, principal.ActionUpdate})
	}
	// A staged 🟡 create (a schema change like createCustomField) releases
	// a new record of the target type — approving needs the create grant
	// the write itself would need.
	if a.Kind == "create_record" {
		if a.TargetType == nil {
			return errors.New("crmapprovals: create_record staged without a target type")
		}
		grants = append(grants, struct {
			Object string
			Action principal.Action
		}{*a.TargetType, principal.ActionCreate})
	}
	for _, g := range grants {
		if !p.Permissions.Allows(g.Object, g.Action) {
			return fmt.Errorf("approving %s needs %s.%s: %w", a.Kind, g.Object, g.Action, apperrors.ErrPermissionDenied)
		}
	}
	return nil
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
// decision-grant mapping. The composition layer's fitness test calls it
// for every 🟡/dynamic tool in the registry: a tool that can stage an
// approval nobody is mapped to decide would strand its stagings in a
// queue no inbox shows (decidable fails closed on unknown kinds).
func KindHasDecisionGrants(kind string) bool {
	_, ok := decisionGrants[kind]
	return ok
}
