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
	"fmt"
	"strings"
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
	SendEmail(ctx context.Context, anchor ids.UUID, in SendEmailArgs) (json.RawMessage, error)
	// SendMessage replies on a captured channel conversation. It takes no
	// addressee: the recipient is the person the anchor conversation is with,
	// resolved server-side, so a reply can only reach the human who opened it.
	SendMessage(ctx context.Context, anchor ids.UUID, in SendMessageArgs) (json.RawMessage, error)
	// IsChannelKind reports whether an activity kind is a messaging-channel
	// conversation send_message may reply on. StageInfo needs the exact
	// answer activities.IsChannelKind gives — the same test the store's own
	// SendMessage refuses on — but this module may not import activities
	// directly (modules never import a sibling), so the seam carries it.
	IsChannelKind(kind string) bool
	Availability(ctx context.Context, host *ids.UUID, from, to time.Time, durationMinutes int) (json.RawMessage, error)
	BookMeeting(ctx context.Context, in BookMeetingArgs) (json.RawMessage, error)
}

type SendEmailArgs struct {
	To             []string `json:"to"`
	Cc             []string `json:"cc"`
	Subject        string   `json:"subject"`
	Body           string   `json:"body"`
	ConsentPurpose string   `json:"consent_purpose"`
}

// SendMessageArgs is one channel reply. It carries no subject and no
// addressee, and that absence is the transport's shape rather than an
// omission: a messaging channel has neither.
type SendMessageArgs struct {
	Body           string `json:"body"`
	ConsentPurpose string `json:"consent_purpose"`
}

type BookMeetingArgs struct {
	HostUserID *ids.UUID `json:"host_user_id"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Subject    string    `json:"subject"`
	Links      []struct {
		EntityType string   `json:"entity_type"`
		EntityID   ids.UUID `json:"entity_id"`
	} `json:"links"`
}

// RegisterCommsTools wires the five verbs over the injected seam. The provider
// is the record reader send_message stages against; the other four verbs do not
// stage and never read it.
func RegisterCommsTools(r *Registry, comms Comms, p datasource.SystemOfRecordProvider) {
	if comms == nil {
		return
	}
	r.Register(draftEmailTool{comms: comms})
	r.Register(sendEmailTool{comms: comms})
	r.Register(sendMessageTool{comms: comms, p: p})
	r.Register(checkAvailability{comms: comms})
	r.Register(bookMeetingTool{comms: comms})
}

// --- draft_email (🟢: proposes, never sends) ---

type draftEmailTool struct{ comms Comms }

func (t draftEmailTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "draft_email", Version: toolVersionV1,
		RequiredScope: principal.ScopeDraft, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "draftEmail",
		InputSchema: schema(`{"type":"object","required":["activity_id"],"properties":{
			"activity_id":{"type":"string","format":"uuid","description":"The thread being replied to"},
			"intent":{"type":"string","description":"What the reply should accomplish"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t draftEmailTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		ActivityID ids.UUID `json:"activity_id"`
		Intent     string   `json:"intent"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	subject, body, err := t.comms.DraftEmail(ctx, args.ActivityID, args.Intent)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"subject": subject, "body": body, "in_reply_to_activity_id": args.ActivityID,
	})
}

// --- send_email (🟡: outbound + irreversible) ---

type sendEmailTool struct{ comms Comms }

func (t sendEmailTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "send_email", Version: toolVersionV1,
		RequiredScope: principal.ScopeSend, Tier: mcp.TierConfirmationRequired, Egress: true,
		OpenAPIOp: "sendEmail",
		InputSchema: schema(`{"type":"object","required":["activity_id","to","subject","body","consent_purpose"],"properties":{
			"activity_id":{"type":"string","format":"uuid"},
			"to":{"type":"array","items":{"type":"string","format":"email"},"minItems":1},
			"cc":{"type":"array","items":{"type":"string","format":"email"}},
			"subject":{"type":"string"},
			"body":{"type":"string"},
			"consent_purpose":{"type":"string","description":"Purpose key the recipients must have granted"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t sendEmailTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		ActivityID ids.UUID `json:"activity_id"`
		SendEmailArgs
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	return t.comms.SendEmail(ctx, args.ActivityID, args.SendEmailArgs)
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
		Name: "send_message", Version: toolVersionV1,
		RequiredScope: principal.ScopeSend, Tier: mcp.TierConfirmationRequired, Egress: true,
		OpenAPIOp: "sendMessage",
		InputSchema: schema(`{"type":"object","required":["activity_id","body","consent_purpose"],"properties":{
			"activity_id":{"type":"string","format":"uuid","description":"The captured conversation being replied to"},
			"body":{"type":"string","minLength":1},
			"consent_purpose":{"type":"string","description":"Purpose key the recipient must have granted"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved the staged call"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

type sendMessageToolArgs struct {
	ActivityID ids.UUID `json:"activity_id"`
	SendMessageArgs
}

func (t sendMessageTool) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args sendMessageToolArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	rec, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityActivity, ID: args.ActivityID})
	if err != nil {
		return StageInfo{}, err
	}
	if err := refuseStagingElsewhere(rec); err != nil {
		return StageInfo{}, err
	}
	// Refuse here what Handle's eventual SendMessage call would refuse anyway
	// (errEmptyMessageBody, NotAChannelConversationError): otherwise staging
	// mints an approval a human can approve, the approved retry consumes that
	// one-shot approval on redemption, and only then does the store refuse
	// permanently — a "yes" with no path to actually happening.
	//
	// SendMessage has two more permanent refusals this does not guard:
	// ChannelRecipientError (the conversation reaches nobody, or more than
	// one person) and ChannelNotSendCapableError (the workspace has no bot
	// bound for the provider). Both are the same "yes with no path to
	// actually happening" shape as the two guarded above, but closing them
	// needs a reachability read this call does not have: the record read
	// above returns the anchor's fields, not who the conversation resolves
	// to or whether a bot is bound, and answering either question here would
	// mean a new datasource seam method plus a database read at staging
	// time — staging today only reads the anchor already fetched for
	// version-pinning.
	if strings.TrimSpace(args.Body) == "" {
		return StageInfo{}, &BadArgsError{Cause: fmt.Errorf("body is empty or whitespace-only; a channel provider rejects a text-less message")}
	}
	var anchor struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(rec.Fields, &anchor); err != nil {
		return StageInfo{}, fmt.Errorf("crmagents: activity %s read back with unreadable fields: %w", args.ActivityID, err)
	}
	if !t.comms.IsChannelKind(anchor.Kind) {
		return StageInfo{}, &BadArgsError{Cause: fmt.Errorf(
			"activity %s is a %q activity, not a messaging-channel conversation; reply on the channel the conversation was held on", args.ActivityID, anchor.Kind)}
	}
	return StageInfo{
		TargetType: string(datasource.EntityActivity), TargetID: args.ActivityID, TargetVersion: &rec.Version,
		// The inbox shows the human what they are releasing. The message text
		// is the thing being approved, so it belongs in the summary; the
		// recipient does not, because nobody named one — the conversation did.
		Summary: fmt.Sprintf("Reply on a captured conversation: %q", args.Body),
	}, nil
}

func (t sendMessageTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args sendMessageToolArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	return t.comms.SendMessage(ctx, args.ActivityID, args.SendMessageArgs)
}

// --- check_availability (🟢 read) ---

type checkAvailability struct{ comms Comms }

func (t checkAvailability) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "check_availability", Version: toolVersionV1,
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "getAvailability",
		InputSchema: schema(`{"type":"object","required":["from","to"],"properties":{
			"host_user_id":{"type":"string","format":"uuid","description":"Defaults to the acting principal's user"},
			"from":{"type":"string","format":"date-time"},
			"to":{"type":"string","format":"date-time"},
			"duration_minutes":{"type":"integer","minimum":15,"maximum":480}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t checkAvailability) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		HostUserID      *ids.UUID `json:"host_user_id"`
		From            time.Time `json:"from"`
		To              time.Time `json:"to"`
		DurationMinutes int       `json:"duration_minutes"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	return t.comms.Availability(ctx, args.HostUserID, args.From, args.To, args.DurationMinutes)
}

// --- book_meeting (🟡: commits a slot + implies an invite) ---

type bookMeetingTool struct{ comms Comms }

func (t bookMeetingTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "book_meeting", Version: toolVersionV1,
		RequiredScope: principal.ScopeSend, Tier: mcp.TierConfirmationRequired, Egress: true,
		OpenAPIOp: "bookMeeting",
		InputSchema: schema(`{"type":"object","required":["start","end"],"properties":{
			"host_user_id":{"type":"string","format":"uuid"},
			"start":{"type":"string","format":"date-time"},
			"end":{"type":"string","format":"date-time"},
			"subject":{"type":"string"},
			"links":{"type":"array","items":{"type":"object","required":["entity_type","entity_id"],"properties":{
				"entity_type":{"type":"string","enum":["person","organization","deal","lead","project"]},
				"entity_id":{"type":"string","format":"uuid"}},"additionalProperties":false}}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t bookMeetingTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args BookMeetingArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	return t.comms.BookMeeting(ctx, args)
}
