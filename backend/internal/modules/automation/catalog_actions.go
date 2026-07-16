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

const (
	ActionTypeCreateTask      ActionType = "create_task"
	ActionTypeNotify          ActionType = "notify"
	ActionTypeAssignOwner     ActionType = "assign_owner"
	ActionTypeAddToList       ActionType = "add_to_list"
	ActionTypeSetField        ActionType = "set_field"
	ActionTypeDraftEmail      ActionType = "draft_email"
	ActionTypeRequestApproval ActionType = "request_approval"
)

// PermissionShape says whether an action's RequiredPermission.Object is a
// fixed value or must be resolved from the automation's fired target at
// match time — never left to the convention that an empty Object means
// "resolved," which is indistinguishable from "nobody filled this in."
// Two members, and only two: an action is one or the other, never
// neither.
type PermissionShape string

const (
	// PermissionPinned: the action always acts on the same entity type
	// regardless of what the trigger fired on, so Object below IS that
	// fixed value (e.g. add_to_list always mutates list membership).
	PermissionPinned PermissionShape = "pinned"
	// PermissionTargetScoped: the action's real object is whatever entity
	// type the automation's trigger fired on — workflows.go's
	// ApplyActions routes both assign_owner and set_field to
	// provider.Update{Ref: action.Target}, and Target's type comes from
	// the event, not from this registry. Object is deliberately left
	// unset here. This registry's Permission is an author-time fast-fail
	// check, not the security boundary: an automation fires on behalf of
	// its owner (automation.owner_id), and Task 13's match-time resolver
	// is what checks the real, now-known entity type against the owner's
	// live seat + RBAC (the ratified pattern at
	// platform/auth/admit.go:70-95) — the same problem
	// modules/approvals/authority.go already solves for its own
	// entity-agnostic kinds (update_record et al.: "resolved from the
	// target's entity type below", fail-closed on an unknown type).
	PermissionTargetScoped PermissionShape = "target_scoped"
)

// Permission is the verb+object an automation's author must hold for the
// effect the automation will carry. A firing runs as PrincipalSystem
// (platform/auth/rbac.go short-circuits object RBAC for system
// principals), so without this the author's own rights would never be
// checked at all — author-time (the permission ceiling) is the only
// moment they are. Action is the verb, genuinely static per action type;
// Object is set only when Shape is PermissionPinned. Both reuse the exact
// spellings the rest of the codebase gates on (identity/internal/policy's
// coreObjects, principal.Action) — never a new vocabulary.
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
// resolves from the automation's own filter/scope at fire time (Task
// 11's job); this registry only declares that the tier IS dynamic, never
// which of the two a given firing lands on.
//
// RequiredPermission's Shape follows workflows.go's ApplyActions switch,
// the ground truth for what each executor actually touches:
// assign_owner and set_field both route to
// provider.Update{Ref: action.Target}, and Target's type is read off the
// firing event, not chosen here — pinning either to a single object would
// gate the wrong entity whenever the trigger fires on something else, so
// both are PermissionTargetScoped (verb: update). Every other action's
// executor targets a fixed entity type regardless of what fired it
// (create_task and draft_email always create an `activity` row — kind
// task/email, migrations/core/0008_activity.up.sql; add_to_list always
// mutates list membership; notify and request_approval both land as the
// same "create something for a human to act on" shape
// modules/approvals/authority.go already grants on
// send_email/book_meeting/deal_follow_up) — so those five are
// PermissionPinned.
var actionDefs = map[ActionType]ActionDef{
	ActionTypeCreateTask: {
		Type: ActionTypeCreateTask, Tier: "green", Executor: workflow.ActionCreateTask,
		RequiredPermission: Permission{Shape: PermissionPinned, Object: "activity", Action: "create"},
	},
	ActionTypeNotify: {
		Type: ActionTypeNotify, Tier: "green", Executor: workflow.ActionNotify,
		RequiredPermission: Permission{Shape: PermissionPinned, Object: "activity", Action: "create"},
	},
	ActionTypeAssignOwner: {
		Type: ActionTypeAssignOwner, Tier: "dynamic", Executor: workflow.ActionAssignOwner,
		RequiredPermission: Permission{Shape: PermissionTargetScoped, Action: "update"},
	},
	ActionTypeAddToList: {
		Type: ActionTypeAddToList, Tier: "green", Executor: workflow.ActionAddToList,
		RequiredPermission: Permission{Shape: PermissionPinned, Object: "list", Action: "update"},
	},
	ActionTypeSetField: {
		Type: ActionTypeSetField, Tier: "green", Executor: workflow.ActionUpdateRecord,
		RequiredPermission: Permission{Shape: PermissionTargetScoped, Action: "update"},
	},
	ActionTypeDraftEmail: {
		Type: ActionTypeDraftEmail, Tier: "green", Executor: workflow.ActionDraftEmail,
		RequiredPermission: Permission{Shape: PermissionPinned, Object: "activity", Action: "create"},
	},
	ActionTypeRequestApproval: {
		Type: ActionTypeRequestApproval, Tier: "yellow", Executor: workflow.ActionEmitFlowEvent,
		RequiredPermission: Permission{Shape: PermissionPinned, Object: "activity", Action: "create"},
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
