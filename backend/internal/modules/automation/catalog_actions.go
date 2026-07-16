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

// Permission is the object+action an automation's author must hold for
// the effect the automation will carry. A firing runs as PrincipalSystem
// (platform/auth/rbac.go short-circuits object RBAC for system
// principals), so without this the author's own rights would never be
// checked at all — author-time (the permission ceiling) is the only
// moment they are. Object/Action reuse the exact spellings the rest of
// the codebase gates on (identity/internal/policy's coreObjects,
// principal.Action) — never a new vocabulary.
type Permission struct{ Object, Action string }

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
// RequiredPermission reuses the exact object+action pattern
// modules/approvals/authority.go already applies to staged effects (e.g.
// "send_email"/"book_meeting"/"deal_follow_up": {"activity", ActionCreate}):
// create_task, notify, draft_email and request_approval all land as an
// `activity` row (kind task/note/email — migrations/core/0008_activity.up.sql)
// or, for request_approval, the same "create something for a human to act
// on" shape those staged kinds already use — matching the create grant a
// human performing the same write would need. assign_owner's one shipped
// precedent in this module (Catalog()'s route_lead) reassigns a lead.
// set_field has no shipped precedent yet, so it is pinned to `deal` — the
// object most existing and forthcoming catalog templates act on
// (stage_change_create_task's trigger is deal-centric). Both are a coarse,
// single-object ceiling per action TYPE rather than per fired instance;
// Task 11's per-instance target-type resolution (the same problem the
// dynamic tier resolver already has to solve) is where a tighter,
// entity-specific ceiling would replace it.
var actionDefs = map[ActionType]ActionDef{
	ActionTypeCreateTask: {
		Type: ActionTypeCreateTask, Tier: "green", Executor: workflow.ActionCreateTask,
		RequiredPermission: Permission{Object: "activity", Action: "create"},
	},
	ActionTypeNotify: {
		Type: ActionTypeNotify, Tier: "green", Executor: workflow.ActionNotify,
		RequiredPermission: Permission{Object: "activity", Action: "create"},
	},
	ActionTypeAssignOwner: {
		Type: ActionTypeAssignOwner, Tier: "dynamic", Executor: workflow.ActionAssignOwner,
		RequiredPermission: Permission{Object: "lead", Action: "update"},
	},
	ActionTypeAddToList: {
		Type: ActionTypeAddToList, Tier: "green", Executor: workflow.ActionAddToList,
		RequiredPermission: Permission{Object: "list", Action: "update"},
	},
	ActionTypeSetField: {
		Type: ActionTypeSetField, Tier: "green", Executor: workflow.ActionUpdateRecord,
		RequiredPermission: Permission{Object: "deal", Action: "update"},
	},
	ActionTypeDraftEmail: {
		Type: ActionTypeDraftEmail, Tier: "green", Executor: workflow.ActionDraftEmail,
		RequiredPermission: Permission{Object: "activity", Action: "create"},
	},
	ActionTypeRequestApproval: {
		Type: ActionTypeRequestApproval, Tier: "yellow", Executor: workflow.ActionEmitFlowEvent,
		RequiredPermission: Permission{Object: "activity", Action: "create"},
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
