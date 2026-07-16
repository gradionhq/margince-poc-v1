// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The seams ApplyActions drives each typed action through (AUTO-T05,
// AUTO-T07, features/03 §5): every one is declared with ids/json/stdlib
// types only, so this module never imports the module that actually
// backs it (ADR-0054 §9, "a module never imports a sibling"); compose
// owns every adapter that maps a seam here onto the real implementation.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Approvals is the staging dependency ApplyActions holds: a 🟡 action
// stages here and ApplyActions returns workflow.StagedApprovalError
// carrying the resulting id back to the caller, which runOne then writes
// onto the parked run row. Redemption (resuming a staged action with an
// approval token) is a later slice — runOne always calls Apply with a
// nil token today, so no Redeem method is declared here; adding one with
// no caller would be speculative.
type Approvals interface {
	Stage(ctx context.Context, in StageRequest) (ids.ApprovalID, error)
}

// StageRequest is what ApplyActions hands the approvals seam for one
// staged 🟡 action: enough for the inbox to show a human what the
// automation wants to do and for the action to be identified again once
// decided.
type StageRequest struct {
	Kind           string          // the action kind being staged, e.g. "send_email"
	ProposedChange json.RawMessage // the action's args, as the approver will see them
	DiffHash       string
	TargetType     string
	TargetID       ids.UUID
	Summary        string
}

// Lists is the add_to_list seam onto collections' static-list
// membership write (collections/members.go's Store.AddMember); compose's
// adapter drops the returned member row — an automation only needs to
// know whether the write succeeded, never the row it produced.
type Lists interface {
	AddMember(ctx context.Context, listID ids.ListID, entityType string, entityID ids.UUID) error
}

// Comms is the draft_email seam onto activities' deterministic draft
// compute (compose's commsAdapter, the same path the MCP draft_email
// tool proposes over — agents.Comms.DraftEmail structurally satisfies
// this interface, so compose reuses the one adapter rather than
// wrapping it a second time). Applying draft_email means the draft was
// computed, full stop — the send is a separate, approval-gated act
// (ActionSendEmail, already 🟡 in ApplyActions' switch), never a side
// effect of this call.
type Comms interface {
	DraftEmail(ctx context.Context, anchor ids.UUID, intent string) (subject, body string, err error)
}

// Notifier is the notify seam onto a real delivery transport. This repo
// wires none today — no notification table, no channel adapter; the
// inbox a human works from is approvals-only. ApplyActions' notify case
// checks for a nil Notifier and answers ErrNoNotificationTransport
// instead of silently discarding the firing (§3.3, UAT.md) — the day a
// transport lands, compose wires a real Notifier here and this
// interface already has a caller waiting for it.
type Notifier interface {
	Notify(ctx context.Context, recipient ids.UUID, subject, body string) error
}

// ErrNoNotificationTransport is notify's honest answer when no Notifier
// is wired: the firing matched and would have delivered, but this
// environment has nowhere to send it. runOne (workflows.go) maps it to
// a 'skipped' run with a readable reason — distinct from a Match/Plan
// condition-declined skip and distinct from 'failed' (nothing went
// wrong; delivery is simply out of scope for this run), so a rep reading
// run history sees why nothing was sent instead of a silent gap or a
// fabricated success.
var ErrNoNotificationTransport = errors.New("automation: no notification transport configured")

// Executors bundles every seam ApplyActions may drive a typed action
// through. One struct rather than a five-parameter signature: adding a
// seam is one field here, not a break at every existing call site.
type Executors struct {
	Provider  datasource.SystemOfRecordProvider
	Approvals Approvals
	Lists     Lists
	Comms     Comms
	Notifier  Notifier // nil in this repo today — see Notifier's doc
}
