// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The three composing action executors ApplyActions (workflows.go)
// delegates to: notify, add_to_list, and draft_email. Split out of
// workflows.go so that file's own switch stays readable as the executor
// count grows (the same reasoning gen-workflow's docs give for splitting
// a package once a named trigger fires).

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// decodeActionArgs unmarshals one action's Args into a typed shape,
// treating an absent/empty payload as the type's zero value — a Plan
// that names everything it needs via Target alone may leave Args empty.
func decodeActionArgs[T any](args json.RawMessage) (T, error) {
	var out T
	if len(args) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(args, &out); err != nil {
		return out, fmt.Errorf("automation: decoding action args: %w", err)
	}
	return out, nil
}

// notifyArgs is what a notify action's Args carries once a real
// transport is wired: who to notify and the message. No shipped Plan
// emits ActionNotify yet (ErrNoNotificationTransport, seams.go), so this
// shape is exercised by this executor's own unit tests today, not by a
// live handler.
type notifyArgs struct {
	Recipient ids.UUID `json:"recipient"`
	Subject   string   `json:"subject"`
	Body      string   `json:"body"`
}

// applyNotify is notify's executor: a nil Notifier (this repo wires
// none) answers ErrNoNotificationTransport so the firing lands as a
// visible, honestly-reasoned run instead of a silent no-op or a
// fabricated success. A wired Notifier delivers for real — the args
// decode only has to succeed on that path, since it is the only path
// that ever reads them.
func applyNotify(ctx context.Context, notifier Notifier, action workflow.Action) error {
	if notifier == nil {
		return ErrNoNotificationTransport
	}
	in, err := decodeActionArgs[notifyArgs](action.Args)
	if err != nil {
		return err
	}
	return notifier.Notify(ctx, in.Recipient, in.Subject, in.Body)
}

// addToListArgs names the static list an add_to_list action writes
// into; the member being added is the action's own Target.
type addToListArgs struct {
	ListID ids.ListID `json:"list_id"`
}

// applyAddToList is add_to_list's executor: Target names the entity
// being added, Args names the list.
func applyAddToList(ctx context.Context, lists Lists, action workflow.Action) error {
	in, err := decodeActionArgs[addToListArgs](action.Args)
	if err != nil {
		return err
	}
	return lists.AddMember(ctx, in.ListID, string(action.Target.Type), action.Target.ID)
}

// draftEmailArgs names what draft_email hands to Comms: Target is the
// anchor thread/activity being replied to, Args names the intent.
type draftEmailArgs struct {
	Intent string `json:"intent"`
}

// applyDraftEmail is draft_email's executor: it computes a draft and
// stops there — the send is ActionSendEmail, already 🟡 in ApplyActions'
// switch. The computed subject/body are discarded once returned
// error-free, the same way ApplyActions already discards
// provider.Create/Update's non-error results; applying draft_email
// means a draft was successfully computed, full stop.
func applyDraftEmail(ctx context.Context, comms Comms, action workflow.Action) error {
	in, err := decodeActionArgs[draftEmailArgs](action.Args)
	if err != nil {
		return err
	}
	_, _, err = comms.DraftEmail(ctx, action.Target.ID, in.Intent)
	return err
}

// applyAssignOwner is ActionAssignOwner's executor: AUTO-T07's dynamic
// tier decides whether this firing writes straight through provider.Update
// (🟢, single-entity) or stages for a human decision instead of ever
// reaching it (🟡, at-scale) — the same fork advance_deal already runs
// for won/lost (ADR-0026 §3). A staged 🟡 comes back as a
// *workflow.StagedApprovalError, the same sentinel-as-error shape
// ApplyActions' own 🟡 kinds already return, so the caller's ordinary
// `if err != nil` handles both without a second return value to check.
// scope is the caller's own resolved input (ApplyActions, workflows.go);
// taking it as a parameter here — rather than resolving it inline — is
// what lets this function's own tests prove the 🟡 branch against a
// synthetic scaled scope, never a caller-set override.
func applyAssignOwner(ctx context.Context, ex Executors, action workflow.Action, scope AssignOwnerScope) error {
	if resolveAssignOwnerTier(scope) == mcp.TierYellow {
		id, err := stageForApproval(ctx, ex.Approvals, action)
		if err != nil {
			return err
		}
		return &workflow.StagedApprovalError{ApprovalID: id}
	}
	_, err := ex.Provider.Update(ctx, datasource.UpdateInput{
		Ref:    action.Target,
		Patch:  action.Args,
		Source: systemSource,
	})
	return err
}
