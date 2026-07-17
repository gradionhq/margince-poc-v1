// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The match-time owner-permission gate (AUTO-T06): the real security
// boundary for a human-authored automation. requireAuthorCeiling
// (ceiling.go) is a fast-fail UX check at authoring time; a firing runs
// long after that, and the author's authority can be revoked in between.
// Before a human-authored automation fires, this re-resolves the OWNER's
// live seat AND RBAC through the shared/ports/authz seam — ADR-0055's "the
// granting human's live seat/RBAC", the same two-check order admit.go runs
// for an agent principal — and refuses the firing if the owner can no
// longer perform the planned effect — never the author's stamped-at-
// authoring copy, and never a cached one.
//
// A system-seeded automation (SeedStarterAutomationsTx, automations.go)
// stamps no owner_id — there is no human authority behind it to re-check,
// so the gate skips entirely for a zero ev.OwnerID. A human-authored
// automation always carries an owner (automations.go's Create stamps
// storekit.UUIDOrNil(actor.UserID)), so the only way to reach this gate
// with a NULL owner is the trusted system-seed path.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// gateDecision is the pure, DB-free verdict checkOwnerPermission renders.
// The zero value means "proceed" — runOne turns a blocked verdict into a
// durable run row (recordBlocked) and lets a non-nil error (a transient
// resolver failure) propagate for the caller to retry.
type gateDecision struct {
	blocked bool
	reason  string
}

// checkOwnerPermission re-derives ev.OwnerID's live RBAC and checks it
// against every action the effect plans, fail-closed throughout: a
// missing/archived/suspended owner and a denied permission both land as a
// blocked decision (never a silent pass), while an infrastructure error
// resolving that authority is returned as-is so the firing is retried
// rather than answered from a guess.
func checkOwnerPermission(ctx context.Context, resolver authz.Resolver, ev workflow.Event, effect workflow.Effect) (gateDecision, error) {
	if ev.OwnerID == ids.Nil {
		// System-seeded: no human authored this firing, so there is no
		// owner authority to re-check — run as today.
		return gateDecision{}, nil
	}
	if resolver == nil {
		// A composed engine always carries a resolver (compose/workflows.go);
		// reaching here is a wiring bug, not a permission question — it
		// must not be swallowed into a false "allow".
		return gateDecision{}, errors.New("automation: match-time gate composed with no authz.Resolver")
	}

	// The seat ceiling is a HARD cap, checked ahead of RBAC — the identical
	// order admit.go's Admit runs for an agent principal (A62/ADR-0047): a
	// read seat may never mutate, whatever its role grants still say.
	// Every action this engine ever plans mutates a record (the closed
	// RC-11 catalog, catalog_actions.go: create_task, notify, assign_owner,
	// add_to_list, set_field, draft_email, request_approval all write), so
	// there is no read-only branch to spare here.
	seat, err := resolver.SeatType(ctx, ev.WorkspaceID, ev.OwnerID)
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			// Same honest-hard-case as EffectiveRBAC below: a gone/archived/
			// suspended owner is a real denial, never an outage.
			return gateDecision{blocked: true, reason: "the automation's owner no longer has access"}, nil
		}
		return gateDecision{}, fmt.Errorf("automation: resolving the owner's live seat: %w", err)
	}
	if !seat.CanMutate() {
		return gateDecision{blocked: true, reason: "the automation's owner no longer has a seat that can make changes"}, nil
	}

	rbac, err := resolver.EffectiveRBAC(ctx, ev.WorkspaceID, ev.OwnerID)
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			// The owner's live RBAC resolves to ErrNotFound for gone,
			// archived, or suspended — never to an empty-but-valid
			// authority (shared/ports/authz's Resolver doc). That is a
			// real denial, not an outage.
			return gateDecision{blocked: true, reason: "the automation's owner no longer has access"}, nil
		}
		return gateDecision{}, fmt.Errorf("automation: resolving the owner's live authority: %w", err)
	}

	for _, action := range effect.Actions {
		if decision, blocked := checkActionPermission(rbac, action); blocked {
			return decision, nil
		}
	}
	return gateDecision{}, nil
}

// checkActionPermission answers one planned action against the owner's
// resolved RBAC. Split out of checkOwnerPermission so the per-action
// object resolution (pinned vs target-scoped) reads as its own step.
func checkActionPermission(rbac authz.RBAC, action workflow.Action) (gateDecision, bool) {
	perm, ok := RequiredPermissionForKind(action.Kind)
	if !ok {
		// Every catalog action's executor is proven to resolve here by
		// TestRequiredPermissionForKindReverseMapIsUnambiguous
		// (catalog_closure_test.go); reaching an unregistered kind at
		// fire time means an ungoverned action would otherwise run
		// unchecked — fail closed.
		return gateDecision{blocked: true, reason: fmt.Sprintf("action %q carries no registered permission to gate", action.Kind)}, true
	}

	object := perm.Object
	if perm.Shape == PermissionTargetScoped {
		// Gate the action's OWN write target, never the trigger's entity:
		// every shipped handler's Plan sets Target to the entity it fired
		// on (people/leadrouting.go's ActionAssignOwner: Target: ev.Entity),
		// so the two coincide today — but a future handler whose Plan
		// routes a target-scoped action at a DIFFERENT entity than its
		// trigger must still be gated against what it actually writes,
		// never against what merely triggered it. EntityType's spellings
		// already match the RBAC object vocabulary (identity/internal/
		// policy's coreObjects), so no translation table is needed.
		if action.Target.Type == "" {
			return gateDecision{blocked: true, reason: fmt.Sprintf("action %q has no resolvable target entity to gate against", action.Kind)}, true
		}
		object = string(action.Target.Type)
	}

	if !rbac.Permissions.Allows(object, principal.Action(perm.Action)) {
		return gateDecision{blocked: true, reason: fmt.Sprintf("owner's authority no longer permits %s on %s", perm.Action, object)}, true
	}
	return gateDecision{}, false
}

// recordBlocked lands the match-time gate's refusal as a durable
// 'blocked' run (migration 0061 added 'blocked' to the status CHECK): the
// SAME direct-claim shape recordSkip uses (engine_run.go), not
// MarkRunBlocked's parked→blocked transition (engine_blocked.go) —
// this firing never reached Apply, so it never staged an approval for
// that path to reverse. planned carries what Plan actually computed, so
// the run history shows exactly what was refused, not an empty plan.
func (e *WorkflowEngine) recordBlocked(ctx context.Context, h workflow.Handler, ev workflow.Event, planned json.RawMessage, reason string) error {
	detail, err := reasonDetail(reason)
	if err != nil {
		return fmt.Errorf("automation: encoding the match-time gate's blocked reason: %w", err)
	}
	_, err = e.claimRun(ctx, h, ev, planned, "blocked", detail)
	return err
}
