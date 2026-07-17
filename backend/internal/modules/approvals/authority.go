// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The decision-authority predicate: who may see and decide a staged
// approval. One predicate (decidable) backs List, Get and Decide alike
// (C3/ADR-0036) — what you cannot see you cannot decide.

package approvals

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// decisionGrants maps each stageable kind onto the RBAC the underlying
// effect needs; approving requires every one of them.
var decisionGrants = map[string][]struct {
	Object string
	Action principal.Action
}{
	"advance_deal": {{"deal", principal.ActionUpdate}},
	// progress_deal is advance_deal plus a timeline note; the gated effect
	// is the deal move, so deciding it needs the same grant.
	"progress_deal":  {{"deal", principal.ActionUpdate}},
	"promote_lead":   {{"lead", principal.ActionUpdate}, {"person", principal.ActionCreate}},
	"archive_record": {}, // resolved from the target's entity type below
	"merge_records":  {}, // resolved from the target's entity type below
	"share_record":   {}, // resolved from the target's entity type below
	"update_record":  {}, // resolved from the target's entity type below (human-edit-precedence stagings)
	"create_record":  {}, // resolved from the target's entity type below (🟡 creates staged at the transport gate, e.g. createCustomField)
	// A send is an activity write plus consent enforcement at redemption
	// time; the approver needs the write grant, the consent gate runs in
	// the handler regardless of who approved.
	"send_email":   {{"activity", principal.ActionCreate}},
	"book_meeting": {{"activity", principal.ActionCreate}},
	// Sending an offer releases the draft→sent transition (B-E03.19) —
	// an offer write; deciding it needs the same grant the send itself
	// requires.
	"send_offer": {{"offer", principal.ActionUpdate}},
	// Accepting a cold-start read-back writes enrichment fields onto an
	// organization; "enrich" is the same effect staged through the
	// transport gate by an agent caller.
	"coldstart": {{"organization", principal.ActionUpdate}},
	"enrich":    {{"organization", principal.ActionUpdate}},
	// Accepting a deep site read writes profile fields and category facts
	// onto the target organization — the same update authority enrich needs.
	"deepread": {{"organization", principal.ActionUpdate}},
	// Accepting a site_lead proposal (a published person from a deep read's
	// team page) captures them as a LEAD through the capture sink — the
	// effect is a lead create, so deciding it needs that grant.
	"site_lead": {{"lead", principal.ActionCreate}},
	// Confirming a nightly close-date correction (formulas §11 🟡 tier)
	// releases an expected_close_date write onto the deal.
	"close_date_correction": {{"deal", principal.ActionUpdate}},
	// Confirming an overnight follow-up proposal (features/07 §8a) creates
	// the drafted task activity; the target deal's visibility gates who
	// may see and decide it (targetVisible), the create grant gates the
	// write the confirm performs.
	"deal_follow_up": {{"activity", principal.ActionCreate}},
}

// kindDecidedEvents names the domain event a decision echoes for kinds
// whose lifecycle the event catalog tracks beyond approval.decided.
var kindDecidedEvents = map[string]struct{ approved, rejected string }{
	"coldstart": {approved: "coldstart.accepted", rejected: "coldstart.rejected"},
}

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
	return targetVisible(ctx, tx, a)
}

// targetVisible applies the target row's own/team/all row scope to the
// approval: holding deal.update does not entitle a rep to see — or
// decide — a staged change against another team's deal. The probe uses
// the same platform/auth clauses the owning store's reads use, so the
// approval surface can never disclose more than the record itself would.
// A staged row without a target (e.g. a cold-start proposal) is scoped
// by grants alone; a target the probe errors on stays invisible.
func targetVisible(ctx context.Context, tx pgx.Tx, a row) (bool, error) {
	if a.TargetType == nil || a.TargetID == nil {
		return true, nil
	}
	switch *a.TargetType {
	case "person", "organization", "deal", "lead":
		return auth.VisibleTo(ctx, tx, *a.TargetType, *a.TargetID)
	case "offer":
		// An offer carries no owner_id — it is visible exactly when its
		// DEAL is (the same anchoring the deals store applies), so the
		// approval surface discloses nothing the record itself would not.
		var dealID ids.UUID
		err := tx.QueryRow(ctx, `SELECT deal_id FROM offer WHERE id = $1`, *a.TargetID).Scan(&dealID)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return auth.VisibleTo(ctx, tx, "deal", dealID)
	case "product":
		// Rate-card products are workspace-shared config (no row scope) —
		// the decision-grant check above is the authority question, but a
		// staging against a product that does not exist is still not
		// decidable: existence is the floor every target type shares.
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM product WHERE id = $1 AND archived_at IS NULL)`,
			*a.TargetID).Scan(&exists); err != nil {
			return false, err
		}
		return exists, nil
	case "custom_field":
		// The field catalog is workspace-shared admin config with no row
		// scope (the product posture): the decision-grant check above is
		// the authority question, existence is the floor. No archived_at
		// predicate — retire is a status flip that keeps the row live, and
		// a staged edit against a retired field stays decidable.
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM custom_field WHERE id = $1)`,
			*a.TargetID).Scan(&exists); err != nil {
			return false, err
		}
		return exists, nil
	case "signal":
		// A signal has no owner_id — it is visible when its SUBJECT entity
		// is (the same scope the signals store applies), so a staged
		// archive discloses nothing the record itself would not.
		err := auth.EnsureSignalVisible(ctx, tx, *a.TargetID)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, apperrors.ErrNotFound):
			return false, nil
		default:
			return false, err
		}
	case "activity":
		err := auth.EnsureActivityVisible(ctx, tx, *a.TargetID)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, apperrors.ErrNotFound):
			return false, nil
		default:
			return false, err
		}
	default:
		return false, nil // unknown target type: fail closed
	}
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
