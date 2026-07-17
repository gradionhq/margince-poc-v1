// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The three composing action executors ApplyActions (engine.go)
// delegates to: notify, add_to_list, and draft_email. Split out of
// engine.go so that file's own switch stays readable as the executor
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

// draftEmailRecord is the durable artifact a draft_email firing leaves on
// workflow_run.applied: the intent that was requested plus the composed
// draft. Comms.DraftEmail is pure compute — it returns an in-memory
// subject/body and persists nothing — and the async automation path has
// no agent to receive that text the way the MCP surface does. Carrying it
// here is what makes the run's 'applied' status honest: a real, findable
// draft exists in run history, never sent (the send is ActionSendEmail,
// already 🟡). This is the whole effect of draft_email, so it is recorded,
// not discarded.
type draftEmailRecord struct {
	Intent  string `json:"intent,omitempty"`
	Subject string `json:"draft_subject"`
	Body    string `json:"draft_body"`
}

// applyDraftEmail is draft_email's executor: it composes a draft over the
// anchor via the Comms seam and returns the action ENRICHED with that
// draft, so ApplyActions records the draft onto workflow_run.applied. It
// never sends — composing is the entire effect, and a lost draft under a
// reported 'applied' would be a fake success.
func applyDraftEmail(ctx context.Context, comms Comms, action workflow.Action) (workflow.Action, error) {
	in, err := decodeActionArgs[draftEmailArgs](action.Args)
	if err != nil {
		return action, err
	}
	subject, body, err := comms.DraftEmail(ctx, action.Target.ID, in.Intent)
	if err != nil {
		return action, err
	}
	recorded, err := json.Marshal(draftEmailRecord{Intent: in.Intent, Subject: subject, Body: body})
	if err != nil {
		return action, fmt.Errorf("automation: recording the composed draft: %w", err)
	}
	action.Args = recorded
	return action, nil
}

// applyAssignOwner is ActionAssignOwner's executor: AUTO-T07's dynamic
// tier decides whether this firing writes straight through provider.Update
// (🟢, single-entity) or stages for a human decision instead of ever
// reaching it (🟡, at-scale) — the same fork advance_deal already runs
// for won/lost (ADR-0026 §3). A staged 🟡 comes back as a
// *workflow.StagedApprovalError, the same sentinel-as-error shape
// ApplyActions' own 🟡 kinds already return, so the caller's ordinary
// `if err != nil` handles both without a second return value to check.
// scope is the caller's own resolved input (ApplyActions, engine.go);
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
