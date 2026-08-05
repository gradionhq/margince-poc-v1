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

// Require is the object-level admission gate: the actor's merged role
// policy must grant the action on the object type. The system principal
// (workspace provisioning) is trusted by construction and has no role.
func Require(ctx context.Context, object string, action principal.Action) error {
	p, err := rbacActor(ctx)
	if err != nil {
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
// see it. The connector and system principals are not agents and pass.
func RequireHuman(ctx context.Context) error {
	p, err := rbacActor(ctx)
	if err != nil {
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
	"record_unshare":   principal.ActionUpdate,
}

// AuthzRule renders the audit_log.authorization_rule attribution for a
// permitted mutation: which merged role policy allowed which action.
func AuthzRule(p principal.Principal, entityType string, auditAction string) string {
	if p.Type == principal.PrincipalSystem {
		return "system"
	}
	action, ok := auditActionGrant[auditAction]
	if !ok {
		return ""
	}
	return p.Permissions.Rule(entityType, action)
}

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
	if p.Type == principal.PrincipalSystem {
		return nil
	}
	if slices.Contains(p.Permissions.RoleKeys, roleAdmin) {
		return nil
	}
	return fmt.Errorf("admin-only operation: %w", apperrors.ErrPermissionDenied)
}
