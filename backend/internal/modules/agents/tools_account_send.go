// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// send_account_email — the account-started twin of send_email (ADR-0087 §6),
// in its own file because tools_comms.go is at its length ceiling and this is
// one origin's worth of code, not a second send.
//
// 🟡, scope `send`, governed identically to the reply and with no new
// authority: an agent stages, a human's own action IS the approval (ADR-0055).
// Everything after the origin — the consent gate, deliverability, identity
// minting, the single-transaction staging of activity + delivery + job — is the
// reply path's, reached through the same store method the HTTP transport calls.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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

// StageInfo puts a refused account-started send in the inbox instead of
// dead-ending it.
//
// WHAT IT STAGES IS A CREATE, and the shape says so: the target type is
// `activity` with no id and no version pin. This send answers no message, so
// there is no row its effect depends on — approvals.settledByShape reads that
// shape as decidable on the object-read floor plus the decision grants, which
// is the same row the REST door stages when the identical call arrives as a
// bearer request (compose.stageRefusal reads a target id out of the route's
// {id} parameter and this route has none). One operation, one staged shape,
// whichever door it came through.
//
// It deliberately does NOT pin one of the links the way book_meeting pins its
// first: a booking is a commitment ON the record it names, so its staleness is
// the record's; a send's text is not, and pinning a link would refuse an
// approved message because somebody edited that company's address in between.
//
// The links are still read, because that is a question about the STAGER's
// reach rather than the approver's: the store refuses a link the caller cannot
// see, at execution — so without the same probe here, an agent naming a company
// it cannot read mints an approval a human reads, approves, and watches fail
// with the one-shot authority already spent.
//
// What this does not pre-empt, so neither reads as covered: the consent gate's
// per-purpose verdict, the workspace's mailbox send capability, and whether an
// address belongs to a person on file. All are refusals a human's yes cannot
// fix, and all need reads staging does not have.
func (t sendAccountEmailTool) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	args, links, err := readAccountSendArgs(in)
	if err != nil {
		return StageInfo{}, err
	}
	if _, err := readStageableLinks(ctx, t.p, links); err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType: string(datasource.EntityActivity),
		Summary:    describeAccountSend(args, links),
	}, nil
}

// readAccountSendArgs decodes one call and applies the refusals the send path
// would otherwise raise only after a human had approved it.
//
// Both doors go through it, and that is the point: the MCP surface does not
// validate arguments against an InputSchema — that schema is documentation —
// so a rule enforced only at staging is one an approved retry never meets.
func readAccountSendArgs(in json.RawMessage) (SendAccountEmailArgs, []RecordLink, error) {
	var args SendAccountEmailArgs
	if err := decodeArgs(in, &args); err != nil {
		return SendAccountEmailArgs{}, nil, err
	}
	if len(args.To) == 0 {
		return SendAccountEmailArgs{}, nil, &BadArgsError{Cause: errors.New(
			"`to` is empty; a send with no addressee reaches nobody and would be refused after approval")}
	}
	if len(args.Links) == 0 {
		return SendAccountEmailArgs{}, nil, &BadArgsError{
			Cause: errors.New("`links` needs at least one entry: a message filed under no record " +
				"is one nobody finds again, and the store refuses it"),
			Guidance: "name the company, person or deal this conversation is about",
		}
	}
	links, err := uniqueRecordLinks(args.Links)
	if err != nil {
		return SendAccountEmailArgs{}, nil, err
	}
	return args, links, nil
}

// describeAccountSend is the one line the inbox shows for an account-started
// send: who it reaches, cc included, what it says it is about, and how many
// records it will land on.
//
// Every addressee, for the reason the reply twin's summary states — an unnamed
// recipient is a recipient nobody agreed to. The links are counted rather than
// listed: their ids mean nothing to a human reading one line, and the staged
// row is decidable on the activity floor rather than on those records, so
// naming them would disclose more than the decision rests on.
func describeAccountSend(args SendAccountEmailArgs, links []RecordLink) string {
	summary := fmt.Sprintf("Start an email conversation with %s", strings.Join(args.To, ", "))
	if len(args.Cc) > 0 {
		summary += fmt.Sprintf(", cc %s", strings.Join(args.Cc, ", "))
	}
	return summary + fmt.Sprintf(", subject %q, filed under %d record(s)", args.Subject, len(links))
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
