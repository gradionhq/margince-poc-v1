// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// send_account_email — the account-started twin of send_email (ADR-0087 §6).
// Its own file because the ORIGIN is what separates it from the mail verbs next
// door: this one starts a conversation and names the records it belongs to,
// where they answer one that already exists.
//
// 🟡, scope `send`, governed identically to the reply and with no new
// authority: an agent stages, a human's own action IS the approval (ADR-0055).
// Everything after the origin — the consent gate, deliverability, identity
// minting, the single-transaction staging of activity + delivery + job — is the
// reply path's, reached through the same store method the HTTP transport calls.

import (
	"context"
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// sendAccountEmailTool carries a record reader like its reply twin, but reads
// something else with it: the twin reads its ANCHOR, and this has none.
type sendAccountEmailTool struct {
	comms Comms
	p     datasource.SystemOfRecordProvider
}

// SendAccountEmailArgs is one account-started send: the reply's arguments,
// minus the anchor, plus the records the new conversation is filed under.
type SendAccountEmailArgs struct {
	SendEmailArgs
	Links []RecordLink `json:"links"`
}

func (t sendAccountEmailTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "send_account_email", Title: "Start an email conversation from a record", Version: toolVersionV1,
		Description:   sendAccountEmailCopy.render(),
		RequiredScope: principal.ScopeSend, Tier: mcp.TierConfirmationRequired, Egress: true,
		OpenAPIOp: "sendAccountEmail",
		InputSchema: schema(`{"type":"object","required":["to","subject","body","consent_purpose","links"],"properties":{
			"to":{"type":"array","items":{"type":"string","format":"email"},"minItems":1},
			"cc":{"type":"array","items":{"type":"string","format":"email"}},
			"subject":{"type":"string"},
			"body":{"type":"string"},
			"consent_purpose":{"type":"string","description":"Purpose key the recipients must have granted"},
			"links":{"type":"array","minItems":1,"items":{"type":"object","required":["entity_type","entity_id"],"properties":{
				"entity_type":{"type":"string","enum":` + activityLinkEntityTypeEnum + `},
				"entity_id":{"type":"string","format":"uuid"}},"additionalProperties":false},"maxItems":25,
				"description":"The records this conversation is filed under; at least one. The send is refused without it."},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved the staged call"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[SendEmailResult](),
	}
}

// StageInfo decodes this door's arguments into the account-started send
// command and delegates: the refusals and the staged subject live in the
// resolver (commandlinked.go), where the REST door reaches the same ones for
// the same operation — including the CREATE shape this stages, target type
// `activity` with no id and no pin, which the REST door used to reach by a
// different route entirely (its route carries no `{id}` for the walk to read).
func (t sendAccountEmailTool) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args SendAccountEmailArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	return StageSubject(ctx, NewSendAccountEmailCall(t.p, SendAccountEmailCommand{
		To:      args.To,
		Cc:      args.Cc,
		Subject: args.Subject,
		Links:   args.Links,
	}))
}

// readAccountSendArgs decodes one call and applies the refusals the send path
// would otherwise raise only after a human had approved it.
//
// Handle goes through it, and that is the point: the MCP surface does not
// validate arguments against an InputSchema — that schema is documentation —
// so a rule enforced only at staging is one an approved retry never meets. It
// calls the SAME two functions the resolver's Guards calls rather than
// restating either, so the approved retry cannot be admitted by a rule the
// staging never applied.
func readAccountSendArgs(in json.RawMessage) (SendAccountEmailArgs, []RecordLink, error) {
	var args SendAccountEmailArgs
	if err := decodeArgs(in, &args); err != nil {
		return SendAccountEmailArgs{}, nil, err
	}
	if err := requireAddressee(args.To); err != nil {
		return SendAccountEmailArgs{}, nil, err
	}
	if err := requireAccountSendLinks(args.Links); err != nil {
		return SendAccountEmailArgs{}, nil, err
	}
	links, err := uniqueRecordLinks(args.Links)
	if err != nil {
		return SendAccountEmailArgs{}, nil, err
	}
	return args, links, nil
}

func (t sendAccountEmailTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	args, links, err := readAccountSendArgs(in)
	if err != nil {
		return nil, err
	}
	for _, link := range links {
		noteEvidence(ctx, datasource.EntityType(link.EntityType), link.EntityID)
	}
	return marshalResult(t.comms.SendAccountEmail(ctx, links, args.SendEmailArgs))
}
