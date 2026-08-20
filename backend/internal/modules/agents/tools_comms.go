// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The communication verbs on the MCP surface (crm.yaml x-mcp-tool):
// draft_email / check_availability are 🟢 (propose, never commit);
// send_email / send_message / book_meeting are 🟡 — the registry's admission gate
// stages them for approval exactly like every other confirmation_required tool. The
// module never touches activities' internals: compose injects the
// Comms seam, which delegates to the SAME store methods the HTTP
// transport uses.

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// Comms is the seam onto the activities module's email + scheduling
// paths; compose implements it over the one store both transports use.
type Comms interface {
	DraftEmail(ctx context.Context, anchor ids.UUID, intent string) (subject, body string, err error)
	// DraftAccountEmail composes the FIRST message to a record, where
	// DraftEmail can only continue a thread that already exists. It takes the
	// records the conversation would be filed under instead of an anchor —
	// the same shape SendAccountEmail takes, for the same reason: there is no
	// prior message, and the product refuses to fabricate a placeholder
	// activity to obtain one (ADR-0087).
	//
	// Without it there is no way to draft the follow-up after a first meeting,
	// which is the case the web app's own "Draft a follow-up" button serves;
	// an assistant asked for one had to fall back on a note nobody can send.
	DraftAccountEmail(ctx context.Context, links []RecordLink, intent string) (subject, body string, err error)
	SendEmail(ctx context.Context, anchor ids.UUID, in SendEmailArgs) (SendEmailResult, error)
	// SendAccountEmail starts a NEW conversation instead of continuing one
	// (ADR-0087). It takes no anchor — there is no prior message, and the
	// product refuses to fabricate a placeholder activity to obtain one — so
	// the records the message is filed under are named instead of inherited.
	SendAccountEmail(ctx context.Context, links []RecordLink, in SendEmailArgs) (SendEmailResult, error)
	// SendMessage replies on a captured channel conversation. It takes no
	// addressee: the recipient is the person the anchor conversation is with,
	// resolved server-side, so a reply can only reach the human who opened it.
	SendMessage(ctx context.Context, anchor ids.UUID, in SendMessageArgs) (SendMessageResult, error)
	// ChannelKinds reports whether an activity kind is a messaging-channel
	// conversation send_message may reply on. The send_message resolver needs
	// the exact answer activities.IsChannelKind gives — the same test the
	// store's own SendMessage refuses on — but this module may not import
	// activities directly (modules never import a sibling), so the seam
	// carries it. Embedded rather than declared inline because the REST door
	// reaches that resolver holding the question alone (commandcomms.go).
	ChannelKinds
	Availability(ctx context.Context, host *ids.UUID, from, to time.Time, durationMinutes int) (AvailabilityResult, error)
	BookMeeting(ctx context.Context, in BookMeetingArgs) (json.RawMessage, error)
}

type SendEmailArgs struct {
	To             []string `json:"to"`
	Cc             []string `json:"cc"`
	Subject        string   `json:"subject"`
	Body           string   `json:"body"`
	ConsentPurpose string   `json:"consent_purpose"`
	// ScheduledAt defers the send to an instant instead of now (ADR-0104).
	// Empty sends immediately, which is what every caller meant before this
	// field existed.
	//
	// The approval a scheduled send needs is the one it already needs: this is
	// still 🟡, still staged, and the token is redeemed when the message is
	// SCHEDULED — inside the minutes-scale window ADR-0036 pins, rather than
	// stretched across the deferral. What protects the fire is that every live
	// gate runs again there.
	ScheduledAt string `json:"scheduled_at,omitempty"`
	// ScheduledTZ is the IANA zone the moment was chosen in, required with it.
	ScheduledTZ string `json:"scheduled_tz,omitempty"`
}

// SendMessageArgs is one channel reply. It carries no subject and no
// addressee, and that absence is the transport's shape rather than an
// omission: a messaging channel has neither.
type SendMessageArgs struct {
	Body           string `json:"body"`
	ConsentPurpose string `json:"consent_purpose"`
}

type BookMeetingArgs struct {
	HostUserID *ids.UUID    `json:"host_user_id"`
	Start      time.Time    `json:"start"`
	End        time.Time    `json:"end"`
	Subject    string       `json:"subject"`
	Links      []RecordLink `json:"links"`
}

// RegisterCommsTools wires the six verbs over the injected seam. The provider
// is the record reader the four 🟡 verbs stage against; draft_email and
// check_availability propose nothing durable and never read it.
//
// A nil comms seam is a legal composition and registers nothing — the verbs
// simply are not offered. A nil provider is NOT: the four 🟡 verbs would
// register, advertise themselves on tools/list, and then dereference it the
// first time a human-approvable call was staged. Failing at wiring time is the
// difference between a boot that does not start and a surface that offers
// four sends it panics on, so this asserts rather than silently dropping them
// — a comms surface missing exactly its outbound verbs is the confusing
// middle, not a safe default.
func RegisterCommsTools(r *Registry, comms Comms, p datasource.SystemOfRecordProvider) {
	if comms == nil {
		return
	}
	if p == nil {
		//craft:ignore panic-in-domain composition-time wiring assertion — fires only while cmd wiring runs, never on a request path
		panic("crmagents: RegisterCommsTools needs a record provider — the confirm-first sends, the account-started send and book_meeting all read the records they stage against")
	}
	r.Register(draftEmailTool{comms: comms})
	r.Register(sendEmailTool{comms: comms, p: p})
	r.Register(sendAccountEmailTool{comms: comms, p: p})
	r.Register(sendMessageTool{comms: comms, p: p})
	r.Register(checkAvailability{comms: comms})
	r.Register(bookMeetingTool{comms: comms, p: p})
}

// --- draft_email (🟢: proposes, never sends) ---

type draftEmailTool struct{ comms Comms }

func (t draftEmailTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "draft_email", Title: "Draft an email reply", Version: toolVersionV1,
		Description:   draftEmailCopy.render(),
		RequiredScope: principal.ScopeDraft, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "draftEmail",
		InputSchema: schema(`{"type":"object","properties":{
			"activity_id":{"type":"string","format":"uuid","description":"The thread replied to; omit and give links for a first message"},
			"links":{"type":"array","minItems":1,"maxItems":25,"items":{"type":"object","required":["entity_type","entity_id"],"properties":{
				"entity_type":{"type":"string","enum":` + activityLinkEntityTypeEnum + `},
				"entity_id":{"type":"string","format":"uuid"}},"additionalProperties":false}},
			"intent":{"type":"string"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[DraftEmailResult](),
	}
}

func (t draftEmailTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		ActivityID ids.UUID     `json:"activity_id"`
		Links      []RecordLink `json:"links"`
		Intent     string       `json:"intent"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	// Two shapes, one verb: continue a thread, or open one. A first message
	// used to be impossible here — the anchor was required, and after a first
	// meeting with no inbound mail there was no thread to name, so the
	// follow-up the web app offers a button for could not be drafted at all.
	//
	// Kept on this tool rather than split into a second: the caller's question
	// is the same ("draft an email"), and the tool listing rides in every
	// prompt — a near-duplicate schema is paid for on every call by every
	// caller, including the ones that never draft anything.
	if args.ActivityID == (ids.UUID{}) {
		if len(args.Links) == 0 {
			return nil, &BadArgsError{Cause: errors.New(
				"give activity_id to reply to a thread, or links to open a new conversation")}
		}
		subject, body, err := t.comms.DraftAccountEmail(ctx, args.Links, args.Intent)
		if err != nil {
			return nil, err
		}
		// No noteDerivedContent: this composes from the caller's own intent,
		// not from captured thread content, so it carries no external tier.
		for _, l := range args.Links {
			noteEvidence(ctx, datasource.EntityType(l.EntityType), l.EntityID)
		}
		return json.Marshal(DraftEmailResult{Subject: subject, Body: body, Links: args.Links})
	}
	if len(args.Links) > 0 {
		return nil, &BadArgsError{Cause: errors.New(
			"give activity_id or links, not both: a reply is filed where its thread already is")}
	}
	subject, body, err := t.comms.DraftEmail(ctx, args.ActivityID, args.Intent)
	if err != nil {
		return nil, err
	}
	// The draft is composed from a captured thread, so its text carries that
	// thread's content and its tier.
	noteDerivedContent(ctx)
	noteEvidence(ctx, datasource.EntityActivity, args.ActivityID)
	return json.Marshal(DraftEmailResult{
		Subject: subject, Body: body, InReplyToActivityID: args.ActivityID,
	})
}

// --- send_email (🟡: outbound + irreversible) ---

// sendEmailTool carries a record reader for the same reason its channel twin
// does: a 🟡 tool stages through StageInfo, and staging has to pin the version
// of the row the effect anchors on and refuse one whose authority lives in
// another system.
type sendEmailTool struct {
	comms Comms
	p     datasource.SystemOfRecordProvider
}

func (t sendEmailTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "send_email", Title: "Send an email", Version: toolVersionV1,
		Description:   sendEmailCopy.render(),
		RequiredScope: principal.ScopeSend, Tier: mcp.TierConfirmationRequired, Egress: true,
		OpenAPIOp: "sendEmail",
		InputSchema: schema(`{"type":"object","required":["activity_id","to","subject","body","consent_purpose"],"properties":{
			"activity_id":{"type":"string","format":"uuid"},
			"to":{"type":"array","items":{"type":"string","format":"email"},"minItems":1},
			"cc":{"type":"array","items":{"type":"string","format":"email"}},
			"subject":{"type":"string"},
			"body":{"type":"string"},
			"consent_purpose":{"type":"string","description":"Purpose key the recipients must have granted"},
			"scheduled_at":{"type":"string","format":"date-time"` + timestampNote + `},
			"scheduled_tz":{"type":"string","description":"IANA zone name the moment was chosen in (e.g. Europe/Berlin), required with scheduled_at. The send is deferred to that instant: no activity exists until it fires, and every gate re-runs then."},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved the staged call"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[SendEmailResult](),
	}
}

type sendEmailToolArgs struct {
	ActivityID ids.UUID `json:"activity_id"`
	SendEmailArgs
}

// StageInfo decodes this door's arguments into the mail-reply command and
// delegates: the refusals and the staged subject live in the resolver
// (commandcomms.go), where the REST door reaches the same ones for the same
// operation.
//
// The recipients are NOT resolved, and that is the difference from
// send_message rather than an omission. A mail send names its own addressees
// in `to`/`cc`, so they travel inside the staged arguments and are covered by
// the diff_hash — the approved retry can only reach the addresses the human
// read. A channel reply names none, which is why its recipient has to be
// resolved server-side, and why binding an approval to a recipient is an open
// question there and a settled one here.
func (t sendEmailTool) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args sendEmailToolArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	return StageSubject(ctx, NewSendEmailCall(t.p, SendEmailCommand{
		ActivityID: args.ActivityID,
		To:         args.To,
		Cc:         args.Cc,
		Subject:    args.Subject,
	}))
}

func (t sendEmailTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args sendEmailToolArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	noteEvidence(ctx, datasource.EntityActivity, args.ActivityID)
	return marshalResult(t.comms.SendEmail(ctx, args.ActivityID, args.SendEmailArgs))
}

// --- send_message (🟡: outbound + irreversible, the channel twin of send_email) ---

// sendMessageTool carries a record reader its mail twin does not: a 🟡 tool
// stages through StageInfo, and staging has to know the version of the row the
// effect targets and refuse one whose authority lives in another system.
type sendMessageTool struct {
	comms Comms
	p     datasource.SystemOfRecordProvider
}

func (t sendMessageTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "send_message", Title: "Reply on a channel conversation", Version: toolVersionV1,
		Description:   sendMessageCopy.render(),
		RequiredScope: principal.ScopeSend, Tier: mcp.TierConfirmationRequired, Egress: true,
		OpenAPIOp: "sendMessage",
		InputSchema: schema(`{"type":"object","required":["activity_id","body","consent_purpose"],"properties":{
			"activity_id":{"type":"string","format":"uuid","description":"The captured conversation being replied to"},
			"body":{"type":"string","minLength":1},
			"consent_purpose":{"type":"string","description":"Purpose key the recipient must have granted"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved the staged call"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[SendMessageResult](),
	}
}

type sendMessageToolArgs struct {
	ActivityID ids.UUID `json:"activity_id"`
	SendMessageArgs
}

// StageInfo decodes this door's arguments into the channel-reply command and
// delegates. The kind test travels with the call rather than being asked here:
// the resolver refuses a non-channel anchor for BOTH doors, and this tool's
// own seam is what supplies the answer either way.
func (t sendMessageTool) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args sendMessageToolArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	return StageSubject(ctx, NewSendMessageCall(t.p, t.comms, SendMessageCommand{
		ActivityID: args.ActivityID,
		Body:       args.Body,
	}))
}

func (t sendMessageTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args sendMessageToolArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	noteEvidence(ctx, datasource.EntityActivity, args.ActivityID)
	return marshalResult(t.comms.SendMessage(ctx, args.ActivityID, args.SendMessageArgs))
}
