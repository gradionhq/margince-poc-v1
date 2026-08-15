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
// anchor thread/activity being replied to, Args names the intent and the
// lawful basis the resulting message would be sent under.
type draftEmailArgs struct {
	Intent string `json:"intent"`
	// ConsentPurpose is the purpose the send is gated against, declared when
	// the automation is configured (AUTO-PARAM-1's per-automation bounded
	// parameters) rather than chosen here.
	//
	// It has no default and this package supplies none. Sending is default-deny
	// PER PURPOSE (A22/ADR-0011), so a purpose picked in code would be this
	// process choosing a lawful basis on the operator's behalf — which is the
	// one thing default-deny exists to prevent. An instance configured without
	// one refuses at compose time, where the operator can see and fix it,
	// rather than staging a draft that can never be released.
	ConsentPurpose string `json:"consent_purpose"`
}

// MissingConsentPurposeError refuses to compose a draft for an automation that
// never declared what basis its send would rely on.
//
// It names the firing's own configuration, so the run's visible outcome tells
// an operator which automation to correct — a draft staged without it would
// look correct in the inbox and fail only at the moment a human released it.
type MissingConsentPurposeError struct{}

func (e *MissingConsentPurposeError) Error() string {
	return "this automation drafts an email but declares no consent purpose; " +
		"set the purpose its send is authorized under before enabling it"
}

// FieldFault names the parameter an operator has to set.
func (e *MissingConsentPurposeError) FieldFault() (field, code, message string) {
	return "consent_purpose", "missing_consent_purpose", e.Error()
}

// HeldDraftProposal is the staged payload a human decides on, and the exact
// input its release sends.
//
// It carries the WHOLE message rather than only the words, because a release
// reconstructs a send from this and nothing else: the approvals effect receives
// the proposed change, the diff hash and the approval id — never the approval's
// target — so an anchor left implicit here is an anchor the release cannot
// reach. Recipient and purpose are here for the same reason, and one more: they
// are what the human is agreeing to, not incidental plumbing.
//
// Field names are the send's own (`to`, `subject`, `body`, `consent_purpose`),
// not the run artifact's `draft_subject`/`draft_body`. The two shapes answer
// different questions — this one is a message about to be sent and is edited in
// place by the approver, that one is a record of what an automation composed —
// and giving them one set of names would let an edit to a history entry look
// like an edit to an outgoing message.
type HeldDraftProposal struct {
	// AnchorActivityID is the thread this answers. It is a UUID-shaped value in
	// the payload on purpose: the approvals edit scope pins entity references
	// found at any depth, so an edited decision can correct the words and
	// cannot re-aim the reply at a different conversation.
	AnchorActivityID ids.UUID `json:"anchor_activity_id"`
	To               string   `json:"to"`
	Subject          string   `json:"subject"`
	Body             string   `json:"body"`
	ConsentPurpose   string   `json:"consent_purpose"`
	// Intent is the automation's own instruction, carried so the approver can
	// see what the draft was ASKED to do and judge whether it did it.
	Intent string `json:"intent,omitempty"`
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
// anchor via the Comms seam and returns BOTH halves of what the firing owes —
// the artifact for run history, and the proposal a human decides on.
//
// It still never sends. Composing and staging are the entire effect; the send
// is the approval-gated completion of this action (AUTO-NOTE-1) and happens
// only when a human releases it.
//
// Two return values rather than one enriched action because the two shapes are
// not the same message twice. The artifact records what this automation
// composed and stays on the run forever; the proposal is a live message whose
// words a human may correct before releasing. Collapsing them would make an
// edit to an approval look like a rewrite of history.
func applyDraftEmail(ctx context.Context, comms Comms, action workflow.Action) (workflow.Action, HeldDraftProposal, error) {
	in, err := decodeActionArgs[draftEmailArgs](action.Args)
	if err != nil {
		return action, HeldDraftProposal{}, err
	}
	if in.ConsentPurpose == "" {
		return action, HeldDraftProposal{}, &MissingConsentPurposeError{}
	}
	// The addressee is resolved BEFORE the draft is composed. Composing can
	// cost a model call, and a thread with no counterparty on it cannot be
	// replied to however good the words are — so the refusal that is certain
	// runs before the work that is expensive.
	to, err := comms.ReplyAddress(ctx, action.Target.ID)
	if err != nil {
		return action, HeldDraftProposal{}, err
	}
	subject, body, err := comms.DraftEmail(ctx, action.Target.ID, in.Intent)
	if err != nil {
		return action, HeldDraftProposal{}, err
	}
	recorded, err := json.Marshal(draftEmailRecord{Intent: in.Intent, Subject: subject, Body: body})
	if err != nil {
		return action, HeldDraftProposal{}, fmt.Errorf("automation: recording the composed draft: %w", err)
	}
	action.Args = recorded
	return action, HeldDraftProposal{
		AnchorActivityID: action.Target.ID,
		To:               to,
		Subject:          subject,
		Body:             body,
		ConsentPurpose:   in.ConsentPurpose,
		Intent:           in.Intent,
	}, nil
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
	if resolveAssignOwnerTier(scope) == mcp.TierConfirmationRequired {
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
