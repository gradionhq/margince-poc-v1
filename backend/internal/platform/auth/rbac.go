// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

// Object-level RBAC + row-level scoping (B-EP03.2/.3a, features/04 §1),
// entity-agnostic and table-parameterized — the per-module stores call
// these at every entry point so every caller — HTTP, the MCP tool
// surface — rides the same enforcement path (architecture/06: no agent
// bypass; ADR-0054 §8: authorization is platform policy, not a domain
// module). Object denial answers ErrPermissionDenied (403); a row outside
// the caller's scope answers ErrNotFound, exactly like a row in another
// tenant (existence is not disclosed).

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

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

// Unbounded reports whether the actor sees every row of a permitted
// object: the system principal, or row_scope=all.
func Unbounded(p principal.Principal) bool {
	return p.Type == principal.PrincipalSystem || p.Permissions.RowScope == principal.RowScopeAll
}

// OwnerPredicate renders the own/team visibility test over one table's
// owner_id (qualified by alias when non-empty). It returns a FUNCTION so
// callers embedding the predicate for several tables (the activity link
// walk) register $me/$teams once and reuse the positions.
//
// The predicate is TOTAL: an actor who sees every row renders TRUE rather
// than nothing. Callers still skip the clause entirely via UnboundedFor
// where they can, but one that composes the predicate without asking
// first gets a widening arm instead of an accidental narrowing to `own` —
// row_scope=all matches no branch below.
func OwnerPredicate(p principal.Principal, arg func(any) int) func(alias string) string {
	if Unbounded(p) {
		return func(string) string { return "TRUE" }
	}
	me := arg(p.UserID)
	col := func(alias string) string {
		if alias == "" {
			return "owner_id"
		}
		return alias + ".owner_id"
	}
	if p.Permissions.RowScope == principal.RowScopeTeam {
		teams := arg(p.TeamIDs)
		return func(alias string) string {
			return fmt.Sprintf(`(%[1]s IS NULL OR %[1]s = $%[2]d OR %[1]s IN (
			   SELECT tm.user_id FROM team_membership tm WHERE tm.team_id = ANY($%[3]d)))`,
				col(alias), me, teams)
		}
	}
	// own — and the zero value: an unresolved scope never widens.
	return func(alias string) string {
		return fmt.Sprintf(`(%[1]s IS NULL OR %[1]s = $%[2]d)`, col(alias), me)
	}
}

// shareableTables are the record types manual per-record grants can
// widen (A52/ADR-0039); grants on anything else cannot exist (the
// record_grant CHECK is the schema-side twin of this set).
var shareableTables = map[string]bool{"person": true, "organization": true, "deal": true, "lead": true, "project": true}

// ownerPrivateTables carry a `visibility` column (migration 0095): a row
// is either 'workspace' — everyone in the workspace, the default — or
// 'owner', the capturing user's alone until a human edit or approval
// promotes it. Connector auto-create writes 'owner' (ADR-0063 §7), so
// this is the trust boundary around an unpromoted inbox: the contacts a
// mailbox sync invented are not yet the workspace's contacts.
//
// Capture privacy is a property of the ROW, not a scope tier, so it does
// NOT yield to row_scope=all. An admin reading a colleague's unpromoted
// captured contacts is precisely the disclosure the boundary exists to
// prevent (founder decision, 2026-07-31: the importing user only, not
// even Admin). Only the system principal — provisioning, the relay, the
// privacy engines — reads these tables unfiltered.
var ownerPrivateTables = map[string]bool{"person": true, "organization": true}

// UnboundedFor reports whether the actor reads the named tables with NO
// predicate at all: Unbounded narrowed by capture privacy. Every read
// path that skips its row-scope clause asks THIS, not Unbounded, so that
// adding a visibility column to a table tightens every such path at once.
// Unbounded itself stays what it is — an admission test ("is this an
// all-scope human?") that several engines gate on.
func UnboundedFor(p principal.Principal, tables ...string) bool {
	if p.Type == principal.PrincipalSystem {
		return true
	}
	if !Unbounded(p) {
		return false
	}
	for _, table := range tables {
		if ownerPrivateTables[table] {
			return false
		}
	}
	return true
}

// ownerScopedTables is the closed set of table names the row-scope
// primitives interpolate into SQL — exactly the tables carrying an
// owner_id column. Several callers pass a table name derived from
// client input (link entity types, grant record types, search anchors);
// they allowlist at their own seam, but the primitive rejects unknown
// names itself so a new caller that forwards an unvalidated string is
// an error, never an injection.
var ownerScopedTables = map[string]bool{
	"person": true, "organization": true, "deal": true, "lead": true, "project": true,
	"list": true, "saved_view": true, "automation": true, "voice_profile": true,
}

// VisiblePredicate is the FULL row-visibility test for one table, in
// three arms: capture privacy (an owner-private row answers to its owner
// alone), the own/team owner predicate, and a live manual grant to the
// caller or one of their teams (write satisfies read). This — not
// OwnerPredicate — is what every read path over a shareable table
// composes; both the visibility column and the grant evaluate LIVE, so
// promoting a captured record or revoking a share binds on the next query.
//
// The arms compose as (capture-private ? owner-only : scope) OR grant.
// An explicit share therefore still widens an owner-private row — sharing
// is a deliberate human disclosure by someone who could already read it,
// which is the same act that promotion is. Scope alone never widens one.
func VisiblePredicate(p principal.Principal, table string, arg func(any) int) func(alias string) string {
	return predicateFor(p, table, arg, withCapturePrivacy)
}

// capturePrivacy selects whether a rendered predicate enforces the
// visibility column. Every interactive read enforces it; only the
// subject-rights probe above lifts it, and it says why.
type capturePrivacy bool

const (
	withCapturePrivacy    capturePrivacy = true
	withoutCapturePrivacy capturePrivacy = false
)

func predicateFor(p principal.Principal, table string, arg func(any) int, capture capturePrivacy) func(alias string) string {
	scope := OwnerPredicate(p, arg)
	// The system principal is trusted by construction and reads both
	// arms away; an unbounded human still faces capture privacy.
	private := bool(capture) && ownerPrivateTables[table] && p.Type != principal.PrincipalSystem
	// An unbounded actor needs no grant arm to see a shareable row —
	// unless capture privacy just took it away from them again.
	shareable := shareableTables[table] && (!Unbounded(p) || private)
	if !private && !shareable {
		return scope
	}
	me := arg(p.UserID)
	// The correlated subqueries below reference the OUTER row's columns;
	// an unqualified name would capture record_grant's own, so the table
	// name qualifies whenever no alias does.
	col := func(alias, name string) string {
		if alias != "" {
			return alias + "." + name
		}
		return table + "." + name
	}

	visible := scope
	if private {
		inner := visible
		visible = func(alias string) string {
			return fmt.Sprintf(`((%s <> 'owner' AND %s) OR %s = $%d)`,
				col(alias, "visibility"), inner(alias), col(alias, "owner_id"), me)
		}
	}
	if !shareable {
		return visible
	}
	teams := arg(p.TeamIDs)
	inner := visible
	return func(alias string) string {
		return fmt.Sprintf(`(%s OR EXISTS (
		   SELECT 1 FROM record_grant rg
		   WHERE rg.record_type = '%s' AND rg.record_id = %s
		     AND (rg.expires_at IS NULL OR rg.expires_at > now())
		     AND ((rg.subject_type = 'user' AND rg.subject_id = $%d)
		       OR (rg.subject_type = 'team' AND rg.subject_id = ANY($%d)))))`,
			inner(alias), table, col(alias, "id"), me, teams)
	}
}

// ScopeClause renders the own/team/all row-visibility predicate over an
// owner_id column (B-EP03.3a). arg registers a query argument and
// returns its 1-based position, matching the list builders' convention.
// An empty clause means unbounded (row_scope=all, or the system actor).
// Ownerless rows (owner_id IS NULL) are workspace-shared and visible at
// every tier.
func ScopeClause(ctx context.Context, arg func(any) int) (string, error) {
	p, err := rbacActor(ctx)
	if err != nil {
		return "", err
	}
	if Unbounded(p) {
		return "", nil
	}
	return OwnerPredicate(p, arg)(""), nil
}

// ScopeClauseFor renders the full visibility predicate (owner scope OR
// live record grant) for one named table with an alias — the spelling
// every list/search/report path over a shareable table uses.
func ScopeClauseFor(ctx context.Context, table, alias string, arg func(any) int) (string, error) {
	if !ownerScopedTables[table] {
		return "", fmt.Errorf("auth: %q is not a row-scoped table", table)
	}
	p, err := rbacActor(ctx)
	if err != nil {
		return "", err
	}
	if UnboundedFor(p, table) {
		return "", nil
	}
	return VisiblePredicate(p, table, arg)(alias), nil
}

// EnsureVisibleLive is the strict row probe: the row must EXIST, be LIVE
// (archived_at IS NULL) and pass the caller's row scope. It differs from
// EnsureVisible in both halves that matter to a caller handing data back —
// an unbounded actor does not skip the existence check, and a soft-deleted
// row never passes.
//
// Both differences are load-bearing where a record is served or referenced
// outside the store that owns it. Art. 17 erasure anonymizes a person in
// place and stamps archived_at while LEAVING owner_id alone, so the
// tombstone still satisfies the original owner's predicate: a probe without
// the live filter answers "yes, still yours" for a record every live read
// path now refuses.
func EnsureVisibleLive(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) error {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)

	clause, err := ScopeClauseFor(ctx, table, "", arg)
	if err != nil {
		return err
	}
	q := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $%d AND archived_at IS NULL`, table, idPos)
	if clause != "" {
		q += " AND " + clause
	}
	q += ")"

	var visible bool
	if err := tx.QueryRow(ctx, q, args...).Scan(&visible); err != nil {
		return err
	}
	if !visible {
		return apperrors.ErrNotFound
	}
	return nil
}

// EnsureVisibleForSubjectRights is EnsureVisible for the GDPR engines:
// it applies the caller's own/team row scope exactly like EnsureVisible,
// but does NOT apply capture privacy. Articles 15 and 17 owe the data
// subject everything the controller holds about them, and an unpromoted
// captured record is still held — a SAR that silently omitted it, or an
// erasure that silently spared it, would be the defect. The crossing is
// authorized by the stronger object gate every caller here passes first
// (person.delete, the same trust level erasure needs) plus, on the SAR
// path, an explicit unbounded-scope check.
//
// It deliberately does not widen the OWNER scope: a rep with person.delete
// still cannot erase a colleague's person. Only the capture-privacy arm
// is lifted, so the caller sees exactly what their scope tier holds.
func EnsureVisibleForSubjectRights(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) error {
	if !ownerScopedTables[table] {
		return fmt.Errorf("auth: %q is not a row-scoped table", table)
	}
	p, err := rbacActor(ctx)
	if err != nil {
		return err
	}
	if Unbounded(p) {
		return nil
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)
	clause := predicateFor(p, table, arg, withoutCapturePrivacy)("")

	var visible bool
	err = tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $%d AND %s)`, table, idPos, clause),
		args...).Scan(&visible)
	if err != nil {
		return err
	}
	if !visible {
		return apperrors.ErrNotFound
	}
	return nil
}

// EnsureLinkTarget verifies an activity link's target row exists AND is
// visible to the caller — an explicit RLS-scoped probe, because the FK
// that would otherwise catch a bad id is checked as the table owner and
// so bypasses RLS: without this, a guessed foreign UUID would persist a
// cross-tenant link. A link to an archived record is equally refused: the
// link would outlive the row it names.
func EnsureLinkTarget(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) error {
	return EnsureVisibleLive(ctx, tx, table, id)
}

// VisibleTo probes whether one row passes the caller's row scope WITHOUT
// erroring — for the dedupe pre-checks, which must answer 409 either way
// but may only disclose the existing row's id when the caller could read
// it (existence-hiding must survive the conflict path).
func VisibleTo(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) (bool, error) {
	err := EnsureVisible(ctx, tx, table, id)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, apperrors.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// EnsureVisible applies the row scope to a single-row operation: get,
// update, archive, advance. Out of scope reads as ErrNotFound — the
// caller cannot distinguish "not yours" from "not there", by design.
// Activities scope through their links (the activities module's
// link-walk clause); pipelines have no owner and are governed by object
// grants only.
func EnsureVisible(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) error {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)

	clause, err := ScopeClauseFor(ctx, table, "", arg)
	if err != nil {
		return err
	}
	if clause == "" {
		return nil
	}

	var visible bool
	err = tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $%d AND %s)`, table, idPos, clause),
		args...).Scan(&visible)
	if err != nil {
		return err
	}
	if !visible {
		return apperrors.ErrNotFound
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
