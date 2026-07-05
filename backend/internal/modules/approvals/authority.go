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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// decisionGrants maps each stageable kind onto the RBAC the underlying
// effect needs; approving requires every one of them.
var decisionGrants = map[string][]struct {
	Object string
	Action principal.Action
}{
	"advance_deal":   {{"deal", principal.ActionUpdate}},
	"promote_lead":   {{"lead", principal.ActionUpdate}, {"person", principal.ActionCreate}},
	"archive_record": {}, // resolved from the target's entity type below
	"merge_records":  {}, // resolved from the target's entity type below
	"share_record":   {}, // resolved from the target's entity type below
	// A send is an activity write plus consent enforcement at redemption
	// time; the approver needs the write grant, the consent gate runs in
	// the handler regardless of who approved.
	"send_email":   {{"activity", principal.ActionCreate}},
	"book_meeting": {{"activity", principal.ActionCreate}},
	// Accepting a cold-start read-back writes enrichment fields onto an
	// organization; "enrich" is the same effect staged through the
	// transport gate by an agent caller.
	"coldstart": {{"organization", principal.ActionUpdate}},
	"enrich":    {{"organization", principal.ActionUpdate}},
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
