// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

import "github.com/gradionhq/margince/backend/internal/shared/ports/workflow"

// ActionType is the closed, user-facing action vocabulary (RC-11,
// features/10 §1): the seven effects a catalog or agent-authored
// automation may carry. Distinct from workflow.ActionKind, the executor
// vocabulary one layer down — this type names what a workspace author
// picks from the catalog; ActionDef.Executor is the registry's ruling on
// which typed executor actually carries it out.
type ActionType string

// The seven user-facing action types (RC-11), in declaration order.
const (
	ActionTypeCreateTask      ActionType = "create_task"
	ActionTypeNotify          ActionType = "notify"
	ActionTypeAssignOwner     ActionType = "assign_owner"
	ActionTypeAddToList       ActionType = "add_to_list"
	ActionTypeSetField        ActionType = "set_field"
	ActionTypeDraftEmail      ActionType = "draft_email"
	ActionTypeRequestApproval ActionType = "request_approval"
)

// Tier vocabulary for ActionDef.Tier, named so the registry reads in one
// spelling and a mis-tier is a build error rather than a silent typo.
const (
	tierGreen   = "green"
	tierYellow  = "yellow"
	tierDynamic = "dynamic"
)

// The RBAC object and verb spellings the author-time ceiling gates on —
// the same vocabulary platform/auth enforces (identity/internal/policy's
// coreObjects, principal.Action). Named here so the registry reads
// uniformly; coreObjects is a sibling internal package and not importable.
const (
	rbacObjActivity = "activity"
	rbacObjList     = "list"
	rbacVerbCreate  = "create"
	rbacVerbUpdate  = "update"
)

// PermissionShape says whether an action's RequiredPermission.Object is a
// fixed value or must be resolved from the automation's fired target at
// match time — never left to the convention that an empty Object means
// "resolved," which is indistinguishable from "nobody filled this in."
// Two members, and only two: an action is one or the other, never
// neither.
type PermissionShape string

const (
	// PermissionPinned marks an action that always acts on the same entity
	// type regardless of what the trigger fired on, so Object below IS that
	// fixed value (e.g. add_to_list always mutates list membership).
	PermissionPinned PermissionShape = "pinned"
	// PermissionTargetScoped marks an action whose real object is whatever
	// entity type the automation's trigger fired on — workflows.go's
	// ApplyActions routes both assign_owner and set_field to
	// provider.Update{Ref: action.Target}, and Target's type comes from
	// the event, not from this registry. Object is deliberately left
	// unset here — Permission's doc covers why an author-time check is
	// enough for now and what the eventual match-time gate must resolve —
	// the same problem modules/approvals/authority.go already solves for
	// its own entity-agnostic kinds (update_record et al.: "resolved from
	// the target's entity type below", fail-closed on an unknown type).
	PermissionTargetScoped PermissionShape = "target_scoped"
)

// Permission is the verb+object an automation's author must hold for the
// effect the automation will carry. A firing currently runs as
// PrincipalSystem, and platform/auth's object RBAC short-circuits system
// principals — so this author-time check is presently the only moment an
// automation's rights are checked at all. It is a fast-fail convenience,
// not the security boundary: the boundary automations need is an
// owner-bound match-time gate — resolving automation.owner_id's live seat
// + RBAC against the real, now-known entity type, the way platform/auth's
// on-behalf-of admission path already does for agent principals — and
// that gate does not exist yet. Action is the verb, genuinely static per
// action type; Object is set only when Shape is PermissionPinned. Both
// reuse the exact spellings the rest of the codebase gates on
// (identity/internal/policy's coreObjects, principal.Action) — never a
// new vocabulary.
type Permission struct {
	Shape  PermissionShape
	Object string
	Action string
}

// ActionDef is the registry's ruling on one user-facing action: which
// executor runs it, what tier it carries, and which permission its author
// must already hold. Tier is read from here and never from the caller.
type ActionDef struct {
	Type               ActionType
	Tier               string // "green" | "yellow" | "dynamic"
	Executor           workflow.ActionKind
	RequiredPermission Permission
}

// actionDefs is the registry body: one row per closed ActionType. Tiers
// are pinned per features/10 §1 / B-E15.1: task/notify/draft/set-field
// are 🟢, send/mass-reassign/close/archive are 🟡. request_approval is
// itself the confirm-first act, so it is 🟡 by its own nature, never
// user-set. assign_owner is "dynamic" (not a placeholder): ADR-0026 §3
// already runs this shape for advance_deal (🟢 between open stages, 🟡 to
// Won/Lost), and AUTO-T07 states the same split for reassignment —
// owner-scoped is 🟢, reassign-at-scale is the held 🟡 form. The tier
// resolves from the automation's own filter/scope at fire time; this
// registry only declares that the tier IS dynamic, never which of the
// two a given firing lands on — the resolver that reads the automation's
// filter/scope to decide is not wired yet.
//
// RequiredPermission's Shape is proven for three actions by workflows.go's
// ApplyActions switch: create_task's executor case forces entity =
// datasource.EntityActivity no matter what fired it, so it is
// PermissionPinned; assign_owner and set_field share an executor case
// that routes to provider.Update{Ref: action.Target}, whose entity type
// comes from the firing event rather than this registry, so both are
// PermissionTargetScoped (verb: update) — pinning either to a single
// object would gate the wrong entity whenever the trigger fires on
// something else.
//
// The other four — notify, add_to_list, draft_email, request_approval —
// have no entity-naming executor case in that switch yet: notify,
// add_to_list, and draft_email currently fall through to ApplyActions'
// default ("unknown action kind") case, and request_approval's executor
// (ActionEmitFlowEvent) hits a case that names no entity either. Their
// PermissionPinned classification is reasoned ahead of wiring, not
// proven by the switch: draft_email is expected to always create an
// `activity` row (kind email, migrations/core/0008_activity.up.sql), the
// same as create_task; add_to_list is expected to always mutate list
// membership; notify and request_approval are expected to land as the
// same "create something for a human to act on" shape
// modules/approvals/authority.go already grants on
// send_email/book_meeting/deal_follow_up. Each pin is confirmed when its
// executor gains an entity-naming case in that switch.
var actionDefs = map[ActionType]ActionDef{
	ActionTypeCreateTask: {
		Type: ActionTypeCreateTask, Tier: tierGreen, Executor: workflow.ActionCreateTask,
		RequiredPermission: Permission{Shape: PermissionPinned, Object: rbacObjActivity, Action: rbacVerbCreate},
	},
	ActionTypeNotify: {
		Type: ActionTypeNotify, Tier: tierGreen, Executor: workflow.ActionNotify,
		RequiredPermission: Permission{Shape: PermissionPinned, Object: rbacObjActivity, Action: rbacVerbCreate},
	},
	ActionTypeAssignOwner: {
		Type: ActionTypeAssignOwner, Tier: tierDynamic, Executor: workflow.ActionAssignOwner,
		RequiredPermission: Permission{Shape: PermissionTargetScoped, Action: rbacVerbUpdate},
	},
	ActionTypeAddToList: {
		Type: ActionTypeAddToList, Tier: tierGreen, Executor: workflow.ActionAddToList,
		RequiredPermission: Permission{Shape: PermissionPinned, Object: rbacObjList, Action: rbacVerbUpdate},
	},
	ActionTypeSetField: {
		Type: ActionTypeSetField, Tier: tierGreen, Executor: workflow.ActionUpdateRecord,
		RequiredPermission: Permission{Shape: PermissionTargetScoped, Action: rbacVerbUpdate},
	},
	ActionTypeDraftEmail: {
		Type: ActionTypeDraftEmail, Tier: tierGreen, Executor: workflow.ActionDraftEmail,
		RequiredPermission: Permission{Shape: PermissionPinned, Object: rbacObjActivity, Action: rbacVerbCreate},
	},
	ActionTypeRequestApproval: {
		Type: ActionTypeRequestApproval, Tier: tierYellow, Executor: workflow.ActionEmitFlowEvent,
		RequiredPermission: Permission{Shape: PermissionPinned, Object: rbacObjActivity, Action: rbacVerbCreate},
	},
}

// AllActionTypes is the closed set, in declaration order. The closure
// test asserts this exactly matches the pinned RC-11 list, in both
// directions.
func AllActionTypes() []ActionType {
	return []ActionType{
		ActionTypeCreateTask, ActionTypeNotify, ActionTypeAssignOwner, ActionTypeAddToList,
		ActionTypeSetField, ActionTypeDraftEmail, ActionTypeRequestApproval,
	}
}

// ActionDefFor resolves one action's registry entry; ok=false for
// anything outside the closed set.
func ActionDefFor(a ActionType) (ActionDef, bool) {
	def, ok := actionDefs[a]
	return def, ok
}
