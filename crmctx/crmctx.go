// Package crmctx carries the per-request identity every trust-boundary
// call needs: the workspace (tenant key for RLS), the acting Principal,
// and — for agent calls — the Passport. Business code reads these only
// through the typed accessors here; loose context keys are forbidden
// (interfaces.md §0, architecture/11 §4).
package crmctx

import (
	"context"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/kernel/ids"
)

// PrincipalType distinguishes the four actor classes the audit log and
// event envelope know (data-model §11, events.md §2).
type PrincipalType string

const (
	PrincipalHuman     PrincipalType = "human"
	PrincipalAgent     PrincipalType = "agent"
	PrincipalConnector PrincipalType = "connector"
	PrincipalSystem    PrincipalType = "system"
)

// Scope is a Passport-grantable verb class a tool may require
// (interfaces.md §0 scope table).
type Scope string

const (
	ScopeRead   Scope = "read"
	ScopeDraft  Scope = "draft"
	ScopeWrite  Scope = "write"
	ScopeSend   Scope = "send"
	ScopeEnrich Scope = "enrich"
)

// SeatType is the licensing capability ceiling of the human behind a
// call (data-model app_user.seat_type, A62/ADR-0047). It is a HARD ceiling
// checked before RBAC: a read seat — or an agent acting for one — may read
// but never mutate/send/approve/grant, whatever its role or passport scope
// would otherwise allow. A full seat carries no seat-level restriction.
type SeatType string

const (
	SeatFull SeatType = "full"
	SeatRead SeatType = "read"
)

// CanMutate is false only for a read seat. An unset seat is treated as a
// read seat (fail-closed): a principal whose loader forgot to resolve the
// seat must not be able to mutate on the strength of the omission.
func (s SeatType) CanMutate() bool {
	return s == SeatFull
}

// ScopeSet is the effective verb grant of a call.
type ScopeSet map[Scope]struct{}

func NewScopeSet(scopes ...Scope) ScopeSet {
	s := make(ScopeSet, len(scopes))
	for _, sc := range scopes {
		s[sc] = struct{}{}
	}
	return s
}

func (s ScopeSet) Has(sc Scope) bool {
	_, ok := s[sc]
	return ok
}

// Principal is the typed actor behind a call — it mirrors the
// audit_log.actor_* columns and the event-envelope actor. Never inferred;
// always set by the auth/Passport layer.
type Principal struct {
	Type       PrincipalType
	ID         string   // "human:<uuid>" | "agent:<id>" | "connector:<name>" | "system"
	UserID     ids.UUID // the app_user behind a human call (row-scope key); zero for system
	TeamIDs    []ids.UUID
	PassportID ids.UUID // Agent Seat Passport authorizing an agent action; zero for humans
	OnBehalfOf ids.UUID // the human authority behind an agent/connector action; zero otherwise
	Scopes     ScopeSet // effective = Passport scopes ∩ granting human's RBAC ("agent ≤ human")

	// SeatType is the licensing ceiling of the human behind the call — for
	// an agent it is the granting human's seat, since "agent ≤ human"
	// (A62/ADR-0047). Empty for the system principal (unbounded by seat).
	SeatType SeatType

	// Permissions is the merged permission policy of the principal's
	// roles (B-EP03.1). Zero value = no grants: an actor whose loader
	// forgot to resolve permissions can read and write nothing.
	Permissions Permissions
}

// Action is one CRUD verb of the object-level RBAC matrix
// (features/04 §1). Archive counts as delete (soft-delete IS the delete).
type Action string

const (
	ActionCreate Action = "create"
	ActionRead   Action = "read"
	ActionUpdate Action = "update"
	ActionDelete Action = "delete"
)

// RowScope is the row-level visibility tier (data-model §2.4): own <
// team < all. It bounds which rows of a permitted object the principal
// sees; ownerless rows are workspace-shared and visible at every tier.
type RowScope string

const (
	RowScopeOwn  RowScope = "own"
	RowScopeTeam RowScope = "team"
	RowScopeAll  RowScope = "all"
)

// rowScopeRank orders the tiers for Wider; package-level because Wider
// runs inside the per-request policy merge.
var rowScopeRank = map[RowScope]int{RowScopeOwn: 1, RowScopeTeam: 2, RowScopeAll: 3}

// Wider orders scopes for merging: a principal holding several roles
// gets the widest row scope any of them grants.
func (s RowScope) Wider(than RowScope) bool {
	return rowScopeRank[s] > rowScopeRank[than]
}

// ObjectGrant is the per-object CRUD row of a permission policy.
type ObjectGrant struct {
	Create, Read, Update, Delete bool
}

func (g ObjectGrant) allows(a Action) bool {
	switch a {
	case ActionCreate:
		return g.Create
	case ActionRead:
		return g.Read
	case ActionUpdate:
		return g.Update
	case ActionDelete:
		return g.Delete
	default:
		return false
	}
}

// Permissions is a principal's effective permission policy — the union
// of its roles' policy documents (data-model §2.4), resolved once at
// authentication. It drives query construction; it is never interpreted
// per-row on the read path (P11).
type Permissions struct {
	// RoleKeys names the roles the grants came from, for the
	// audit_log.authorization_rule attribution.
	RoleKeys []string
	Objects  map[string]ObjectGrant
	RowScope RowScope
}

// Allows answers the object-level RBAC question (B-EP03.2). Unknown
// objects and the zero value deny.
func (p Permissions) Allows(object string, a Action) bool {
	return p.Objects[object].allows(a)
}

// Rule renders the governing-rule attribution recorded in
// audit_log.authorization_rule for a permitted action.
func (p Permissions) Rule(object string, a Action) string {
	return fmt.Sprintf("role[%s] %s.%s row_scope=%s",
		strings.Join(p.RoleKeys, ","), object, a, p.RowScope)
}

type contextKey int

const (
	workspaceKey contextKey = iota
	actorKey
	correlationKey
	causationKey
)

// WithWorkspaceID binds the tenant key the RLS transaction helper will
// SET LOCAL as app.workspace_id.
func WithWorkspaceID(ctx context.Context, id ids.UUID) context.Context {
	return context.WithValue(ctx, workspaceKey, id)
}

// WorkspaceID returns the bound tenant key; ok is false when the call is
// outside any workspace (e.g. the unauthenticated bootstrap paths).
func WorkspaceID(ctx context.Context) (ids.UUID, bool) {
	id, ok := ctx.Value(workspaceKey).(ids.UUID)
	return id, ok
}

// WithActor binds the acting Principal.
func WithActor(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, actorKey, p)
}

// Actor returns the acting Principal; ok is false before authentication.
func Actor(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(actorKey).(Principal)
	return p, ok
}

// WithCorrelationID binds the operation-scoped trace key: every event a
// request / agent run / capture batch emits carries the same
// correlation_id so consumers can replay the chain as one story
// (events.md §2). The HTTP layer mints one per request; a bus consumer
// re-binds the triggering event's correlation_id before it writes.
func WithCorrelationID(ctx context.Context, id ids.UUID) context.Context {
	return context.WithValue(ctx, correlationKey, id)
}

// CorrelationID returns the bound trace key; ok is false when no
// operation scope was opened (a programming error on any write path).
func CorrelationID(ctx context.Context) (ids.UUID, bool) {
	id, ok := ctx.Value(correlationKey).(ids.UUID)
	return id, ok
}

// WithCausationEvent binds the event_id that caused the current work, so
// derived events chain causation_id → parent (capture → created →
// stage_changed, events.md §2). Unbound on direct API calls: their
// events start chains.
func WithCausationEvent(ctx context.Context, eventID ids.UUID) context.Context {
	return context.WithValue(ctx, causationKey, eventID)
}

// CausationEvent returns the parent event_id; ok is false at chain roots.
func CausationEvent(ctx context.Context) (ids.UUID, bool) {
	id, ok := ctx.Value(causationKey).(ids.UUID)
	return id, ok
}
