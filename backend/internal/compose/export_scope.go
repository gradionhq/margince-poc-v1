// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The export bundle's row-visibility clauses (B-E11.10a): each member
// applies the very same predicate its list endpoint uses, so the export
// can never hand a caller a row their lists would hide. Every clause here
// composes the platform auth predicates (auth.ScopeClauseFor /
// ActivityScopeClause / VisiblePredicate) — scope policy has exactly one
// spelling (ADR-0054 §8); the writer only routes each member to the
// matching one.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// memberScope renders the row-visibility predicate for one member,
// dispatching to the same clause its list endpoint uses. An empty clause
// means unbounded (row_scope=all, or the system actor) or workspace-wide
// reference data.
func memberScope(ctx context.Context, m exportMember, alias string, arg func(any) int) (string, error) {
	switch m.scope {
	case scopeShareable:
		return auth.ScopeClauseFor(ctx, m.table, alias, arg)
	case scopeActivity:
		return auth.ActivityScopeClause(ctx, alias, arg)
	case scopeRelationship:
		return relationshipExportScope(ctx, alias, arg)
	case scopeAttachment:
		return polymorphicVisible(ctx, alias+".entity_type", alias+".entity_id", arg)
	case scopeAudit:
		return auditExportScope(ctx, alias, arg)
	case scopeWorkspace:
		return "", nil
	default:
		return "", fmt.Errorf("export: unknown scope mode %d for %q", m.scope, m.table)
	}
}

// relationshipExportScope mirrors the relationship list's
// endpoint-visibility rule: every non-null endpoint must be visible, so
// an edge never discloses a record on the far side the caller cannot see.
func relationshipExportScope(ctx context.Context, alias string, arg func(any) int) (string, error) {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return "", errors.New("compose: no actor bound to export context")
	}
	if auth.Unbounded(actor) {
		return "", nil
	}
	var clauses []string
	for _, endpoint := range []struct{ column, table string }{
		{"person_id", "person"},
		{"organization_id", "organization"},
		{"counterparty_org_id", "organization"},
		{"deal_id", "deal"},
	} {
		predicate := auth.VisiblePredicate(actor, endpoint.table, arg)
		clauses = append(clauses, fmt.Sprintf(
			`(%[1]s.%[2]s IS NULL OR EXISTS (
			   SELECT 1 FROM %[3]s ep WHERE ep.id = %[1]s.%[2]s AND ep.archived_at IS NULL AND %[4]s))`,
			alias, endpoint.column, endpoint.table, predicate("ep")))
	}
	return "(" + strings.Join(clauses, " AND ") + ")", nil
}

// polymorphicVisible renders "the referenced record is visible" for a
// polymorphic (entity_type, entity_id) pair — the attachment manifest and
// the audit-log member both ride it so neither leaks a row pointing at a
// record outside the caller's scope. Unbounded actors carry no clause.
func polymorphicVisible(ctx context.Context, typeCol, idCol string, arg func(any) int) (string, error) {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return "", errors.New("compose: no actor bound to export context")
	}
	if auth.Unbounded(actor) {
		return "", nil
	}
	var parts []string
	for _, e := range []struct{ kind, table string }{
		{"person", "person"},
		{"organization", "organization"},
		{"deal", "deal"},
		{"lead", "lead"},
	} {
		predicate := auth.VisiblePredicate(actor, e.table, arg)
		parts = append(parts, fmt.Sprintf(
			`(%s = '%s' AND EXISTS (SELECT 1 FROM %s ep WHERE ep.id = %s AND %s))`,
			typeCol, e.kind, e.table, idCol, predicate("ep")))
	}
	// Activities have no owner; they inherit visibility from their links.
	activityClause, err := auth.ActivityScopeClause(ctx, "av", arg)
	if err != nil {
		return "", err
	}
	parts = append(parts, fmt.Sprintf(
		`(%s = 'activity' AND EXISTS (SELECT 1 FROM activity av WHERE av.id = %s AND %s))`,
		typeCol, idCol, activityClause))
	return "(" + strings.Join(parts, " OR ") + ")", nil
}

// auditExportScope scopes the audit_log to the caller's view: a row is
// visible when it targets a record the caller can see, or when the caller
// is themselves the actor (their own trail is always theirs). Rows about
// object types outside the row-scoped core (login, config) stay with the
// unbounded admin. Full row-count reconciliation with the actor's RBAC
// view is B-E11.9; this keeps the writer from leaking before that lands.
func auditExportScope(ctx context.Context, alias string, arg func(any) int) (string, error) {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return "", errors.New("compose: no actor bound to export context")
	}
	if auth.Unbounded(actor) {
		return "", nil
	}
	entity, err := polymorphicVisible(ctx, alias+".entity_type", alias+".entity_id", arg)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("(%s OR %s.actor_id = $%d)", entity, alias, arg(actor.ID)), nil
}
