// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The communication verbs on the MCP surface (crm.yaml x-mcp-tool):
// draft_email / check_availability are 🟢 (propose, never commit);
// send_email / book_meeting are 🟡 — the registry's admission gate
// stages them for approval exactly like every other yellow tool. The
// module never touches activities' internals: compose injects the
// Comms seam, which delegates to the SAME store methods the HTTP
// transport uses.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// Comms is the seam onto the activities module's email + scheduling
// paths; compose implements it over the one store both transports use.
type Comms interface {
	DraftEmail(ctx context.Context, anchor ids.UUID, intent string) (subject, body string, err error)
	SendEmail(ctx context.Context, anchor ids.UUID, in SendEmailArgs) (json.RawMessage, error)
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

// RegisterCommsTools wires the four verbs over the injected seam.
func RegisterCommsTools(r *Registry, comms Comms) {
	if comms == nil {
		return
	}
	r.Register(draftEmailTool{comms: comms})
	r.Register(sendEmailTool{comms: comms})
	r.Register(checkAvailability{comms: comms})
	r.Register(bookMeetingTool{comms: comms})
}

// --- draft_email (🟢: proposes, never sends) ---

type draftEmailTool struct{ comms Comms }

func (t draftEmailTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "draft_email", Version: "1.0.0",
		RequiredScope: principal.ScopeDraft, Tier: mcp.TierGreen,
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
		Name: "send_email", Version: "1.0.0",
		RequiredScope: principal.ScopeSend, Tier: mcp.TierYellow, Egress: true,
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

// --- check_availability (🟢 read) ---

type checkAvailability struct{ comms Comms }

func (t checkAvailability) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "check_availability", Version: "1.0.0",
		RequiredScope: principal.ScopeRead, Tier: mcp.TierGreen,
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
		Name: "book_meeting", Version: "1.0.0",
		RequiredScope: principal.ScopeSend, Tier: mcp.TierYellow, Egress: true,
		OpenAPIOp: "bookMeeting",
		InputSchema: schema(`{"type":"object","required":["start","end"],"properties":{
			"host_user_id":{"type":"string","format":"uuid"},
			"start":{"type":"string","format":"date-time"},
			"end":{"type":"string","format":"date-time"},
			"subject":{"type":"string"},
			"links":{"type":"array","items":{"type":"object","required":["entity_type","entity_id"],"properties":{
				"entity_type":{"type":"string","enum":["person","organization","deal","lead"]},
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
