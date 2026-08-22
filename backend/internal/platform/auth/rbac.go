// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

// Object-level RBAC (B-EP03.2, features/04 §1), entity-agnostic — the
// per-module stores call these at every entry point so every caller — HTTP,
// the MCP tool surface — rides the same enforcement path (architecture/06:
// no agent bypass; ADR-0054 §8: authorization is platform policy, not a
// domain module). This file is the admission question "may this principal
// touch this KIND of record", answered with ErrPermissionDenied (403);
// rowscope.go answers "which ROWS", with ErrNotFound so existence is never
// disclosed. Together they are the ONE admission point every store rides.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// rbacActor resolves the acting principal; a missing actor is a
// programming error (the middleware always binds one).
func rbacActor(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.Actor(ctx)
	if !ok {
		return principal.Principal{}, errors.New("auth: no actor bound to context")
	}
	return p, nil
}

// refuseBuyer turns away the one principal kind that holds no CRM authority at
// all. It is applied at every gate in this package rather than left to the
// permission check below each one, because those gates answer from
// `Permissions` — so a buyer would be admitted by exactly the accident that it
// is minted carrying none. That is not a guarantee; it is a coincidence one
// careless constructor would end, and the caller admitted by it would be an
// external person with a room link.
//
// A buyer's authority is its Deal Room session, and the Deal Room's own store
// methods carry the room predicate that grants it. Nothing in platform/auth
// admits a buyer, and the refusal is stated once here so a gate added later
// inherits it by calling the same helper.
func refuseBuyer(p principal.Principal, what string) error {
	if p.Type != principal.PrincipalBuyer {
		return nil
	}
	return fmt.Errorf("%s: a Deal Room participant holds no CRM authority: %w",
		what, apperrors.ErrPermissionDenied)
}

// Require is the object-level admission gate: the actor's merged role
// policy must grant the action on the object type. The system principal
// (workspace provisioning) is trusted by construction and has no role.
func Require(ctx context.Context, object string, action principal.Action) error {
	p, err := rbacActor(ctx)
	if err != nil {
		return err
	}
	if err := refuseBuyer(p, object+"."+string(action)); err != nil {
		return err
	}
	if p.Type == principal.PrincipalSystem {
		return nil
	}
	if !p.Permissions.Allows(object, action) {
		return fmt.Errorf("%s.%s: %w", object, action, apperrors.ErrPermissionDenied)
	}
	return nil
}

// RequireAny admits when the actor holds ANY of the listed actions on the
// object. It exists for a write whose exact action is not yet knowable —
// an upsert learns insert-vs-overwrite only from the table — so the caller
// can still refuse a principal holding NONE of them without taking a pool
// connection. It is the upfront half of a pair, never a substitute for
// requiring the specific action once the write knows it (see UpsertAction).
func RequireAny(ctx context.Context, object string, actions ...principal.Action) error {
	p, err := rbacActor(ctx)
	if err != nil {
		return err
	}
	if err := refuseBuyer(p, object); err != nil {
		return err
	}
	if p.Type == principal.PrincipalSystem {
		return nil
	}
	verbs := make([]string, len(actions))
	for i, a := range actions {
		if p.Permissions.Allows(object, a) {
			return nil
		}
		verbs[i] = string(a)
	}
	return fmt.Errorf("%s.%s: %w", object, strings.Join(verbs, "|"), apperrors.ErrPermissionDenied)
}

// UpsertAction names the grant an upsert actually demands once it knows
// which half it is: create for a row it inserts, update for a row it
// replaces. Keeping the mapping here means the two upsert sites that admit
// on RequireAny(create, update) cannot disagree about what the second check
// asks for — or about the audit verb, which is this same word.
func UpsertAction(replacing bool) principal.Action {
	if replacing {
		return principal.ActionUpdate
	}
	return principal.ActionCreate
}

// RequireHuman refuses an AGENT (Passport) principal outright, whatever its
// scope or the granting human's RBAC. It is the runtime twin of the
// contract's `x-agent-access: human-only` for the operations the agent gate
// cannot cover: reads. The gate only inspects mutating methods, so a
// human-only GET (an admin-only sheet) must reject an agent principal here,
// or an admin-minted read-scoped passport would satisfy the object grant and
// see it. A buyer is refused too, by the shared helper: "human" here means a
// seated member, and an external Deal Room participant is not one. The
// connector and system principals are, and pass.
func RequireHuman(ctx context.Context) error {
	p, err := rbacActor(ctx)
	if err != nil {
		return err
	}
	if err := refuseBuyer(p, "human-only operation"); err != nil {
		return err
	}
	if p.Type == principal.PrincipalAgent {
		return fmt.Errorf("human-only operation: %w", apperrors.ErrPermissionDenied)
	}
	return nil
}

// auditActionGrant maps each audit_log.action verb onto the CRUD grant
// that authorizes it. Package-level: AuthzRule sits on every write path.
//
// Each entry names the grant the verb's write path actually demands, so
// the attribution is the rule that admitted the call rather than a
// plausible-looking one: export is person.delete because SAR assembly is
// gated on it, and erase is voice_profile.update because clearing a
// corpus is gated as an update. A verb missing here renders a BLANK
// authorization_rule, which reads as "no rule applied" years later —
// TestEveryAuditVerbRendersItsAuthorizationRule keeps the set closed.
var auditActionGrant = map[string]principal.Action{
	"create":           principal.ActionCreate,
	"update":           principal.ActionUpdate,
	"assign":           principal.ActionUpdate,
	"advance_stage":    principal.ActionUpdate,
	"advance_phase":    principal.ActionUpdate,
	"restore":          principal.ActionUpdate,
	"archive":          principal.ActionDelete,
	"merge":            principal.ActionUpdate,
	"promote":          principal.ActionUpdate,
	"consent_grant":    principal.ActionUpdate,
	"consent_withdraw": principal.ActionUpdate,
	"activity_relink":  principal.ActionUpdate,
	"resolve":          principal.ActionUpdate,
	"erase":            principal.ActionUpdate,
	"export":           principal.ActionDelete,
	"record_share":     principal.ActionUpdate,
	// The commission ledger. Accruing MAKES a row, so it is the create grant
	// commissions.Store.Accrue actually requires; paying moves an existing
	// entry's state, which is the update grant Decide requires. The rule
	// recorded on the row has to be the grant the write really took.
	// Deal Room ACCESS. Admitting an outside person and taking that access back
	// are both writes against the room, gated on deal_room.update at the store —
	// a participant carries no object grant of its own, so update is the rule
	// that actually admitted the call.
	"invite": principal.ActionUpdate,
	"revoke": principal.ActionUpdate,
	// The Deal Room lifecycle. All four move an existing room's state and all
	// four take deal_room.update at the store, so update is the grant that
	// actually admitted the call — not delete for close, which ends no row, and
	// not create for publish, which makes a release but is gated on the room.
	"publish":        principal.ActionUpdate,
	"pause":          principal.ActionUpdate,
	"resume":         principal.ActionUpdate,
	"close":          principal.ActionUpdate,
	"accrue":         principal.ActionCreate,
	"pay":            principal.ActionUpdate,
	"record_unshare": principal.ActionUpdate,
	// Binding a data provider's credential is a create and cutting it is a
	// delete, matching what integrations.Store.Connect and .Disconnect
	// actually require — the rule recorded on the row has to be the grant the
	// write path checked, or the ledger attributes the act to a permission
	// nobody exercised.
	"connect":    principal.ActionCreate,
	"disconnect": principal.ActionDelete,
	// The scheduled-send verbs govern on the activity the message will become:
	// scheduling one is the right to create that activity, and moving or
	// withdrawing a pending message is the right to change it. Release and hold
	// are written by the timer under the SENDER's re-derived grants, so they
	// record the same rule the immediate send would have.
	"schedule":   principal.ActionCreate,
	"reschedule": principal.ActionUpdate,
	"cancel":     principal.ActionUpdate,
	"release":    principal.ActionCreate,
	"hold":       principal.ActionUpdate,
}

// AuthzRule renders the audit_log.authorization_rule attribution for a
// permitted mutation: which merged role policy allowed which action.
//
// A principal with NO HUMAN BEHIND IT renders "system" instead, and a bare
// connector is one — the scheduled sweeps run on paths ratified as ungated in
// `ungatedEntryPoints`, where nothing was checked because there was no grant to
// check and nobody to hold one. Rendering the merged policy for one writes
// `role[] finance_invoice.create row_scope=`, which reads years later as a
// principal that HELD a role and a row scope and had neither. audit_log is
// append-only, so that reading cannot be corrected once written.
//
// "No human behind it" is the same test `storekit.OwnerOrActor` already applies
// when it leaves such a row ownerless, and it is what `OnBehalfOf` records: a
// connector acting for somebody — the capture path's, which carries the
// granting human's own RBAC — falls through to the policy below and renders it,
// because there a rule really did admit the call.
func AuthzRule(p principal.Principal, entityType string, auditAction string) string {
	if p.Type == principal.PrincipalSystem ||
		(p.Type == principal.PrincipalConnector && p.OnBehalfOf == ids.Nil) {
		return "system"
	}
	// A buyer's authority is the room session, and rendering the policy for one
	// would write `role[] deal.update row_scope=` — the very string this
	// function exists to avoid, claiming a role and a row scope the actor never
	// held. Name what actually admitted the call instead.
	if p.Type == principal.PrincipalBuyer {
		return ruleDealRoomSession
	}
	action, ok := auditActionGrant[auditAction]
	if !ok {
		return ""
	}
	return p.Permissions.Rule(entityType, action)
}

// ruleDealRoomSession is the authorization_rule a buyer's write records: the
// room session admitted it, and no role policy was consulted.
const ruleDealRoomSession = "deal_room_session"

const roleAdmin = "admin"

// RequireAdmin admits only a principal carrying the workspace "admin" role.
// Object grants can't express installation-wide administration, so admin
// endpoints gate on the role directly. A system principal (internal callers)
// passes; every other non-admin is denied with the existence-preserving
// ErrPermissionDenied (403).
func RequireAdmin(ctx context.Context) error {
	p, ok := principal.Actor(ctx)
	if !ok {
		return fmt.Errorf("no principal in context: %w", apperrors.ErrPermissionDenied)
	}
	if err := refuseBuyer(p, "admin-only operation"); err != nil {
		return err
	}
	if p.Type == principal.PrincipalSystem {
		return nil
	}
	if slices.Contains(p.Permissions.RoleKeys, roleAdmin) {
		return nil
	}
	return fmt.Errorf("admin-only operation: %w", apperrors.ErrPermissionDenied)
}
