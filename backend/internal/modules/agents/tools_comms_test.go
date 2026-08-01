// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// recordingComms captures what the tool handed the seam.
type recordingComms struct {
	anchor ids.UUID
	args   SendMessageArgs
}

func (c *recordingComms) DraftEmail(context.Context, ids.UUID, string) (string, string, error) {
	return "", "", nil
}

func (c *recordingComms) SendEmail(context.Context, ids.UUID, SendEmailArgs) (json.RawMessage, error) {
	return nil, nil
}

func (c *recordingComms) Availability(context.Context, *ids.UUID, time.Time, time.Time, int) (json.RawMessage, error) {
	return nil, nil
}

func (c *recordingComms) BookMeeting(context.Context, BookMeetingArgs) (json.RawMessage, error) {
	return nil, nil
}

func (c *recordingComms) SendMessage(_ context.Context, anchor ids.UUID, in SendMessageArgs) (json.RawMessage, error) {
	c.anchor, c.args = anchor, in
	return json.RawMessage(`{"status":"accepted"}`), nil
}

// nativeActivityProvider serves the anchor as a row this database owns —
// Freshness.Authoritative true, the only case an approval can be released for.
type nativeActivityProvider struct {
	datasource.SystemOfRecordProvider
	version int64
}

func (p nativeActivityProvider) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	return datasource.Record{
		Ref: ref, Version: p.version,
		Freshness: datasource.FreshnessInfo{Authoritative: true},
		Fields:    json.RawMessage(`{"kind":"telegram"}`),
	}, nil
}

// mirroredActivityProvider serves it as a record held elsewhere — the shape
// overlay.Provider returns, and the one refuseStagingElsewhere exists for.
type mirroredActivityProvider struct {
	datasource.SystemOfRecordProvider
}

func (mirroredActivityProvider) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	return datasource.Record{Ref: ref, Fields: json.RawMessage(`{"kind":"telegram"}`)}, nil
}

// The channel reply governs exactly as the mail send does: same scope, same
// tier, same egress declaration. A transport difference is not a governance
// difference.
func TestSendMessageToolGovernsAsSendEmailDoes(t *testing.T) {
	spec := sendMessageTool{}.Spec()

	if spec.RequiredScope != principal.ScopeSend {
		t.Errorf("RequiredScope = %q, want %q", spec.RequiredScope, principal.ScopeSend)
	}
	if spec.Tier != mcp.TierConfirmationRequired {
		t.Errorf("Tier = %v, want TierConfirmationRequired", spec.Tier)
	}
	if !spec.Egress {
		t.Error("Egress = false; the reply leaves the workspace")
	}
	if spec.OpenAPIOp != "sendMessage" {
		t.Errorf("OpenAPIOp = %q, want %q", spec.OpenAPIOp, "sendMessage")
	}
}

// The recipient is never an argument: it is resolved from the conversation.
func TestSendMessageToolNamesNoRecipient(t *testing.T) {
	var decoded struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties bool                       `json:"additionalProperties"` //nolint:tagliatelle // JSON Schema spec keyword, must be camelCase
	}
	if err := json.Unmarshal(sendMessageTool{}.Spec().InputSchema, &decoded); err != nil {
		t.Fatalf("input schema is not JSON: %v", err)
	}
	if len(decoded.Properties) == 0 {
		t.Fatal("input schema declares no properties")
	}
	for _, forbidden := range []string{"to", "cc", "chat_id", "recipient", "subject"} {
		if _, present := decoded.Properties[forbidden]; present {
			t.Errorf("input schema accepts %q; the recipient is resolved from the conversation", forbidden)
		}
	}
	if decoded.AdditionalProperties {
		t.Error("input schema does not close additionalProperties")
	}
}

// A 🟡 tool that cannot describe its own staging is advertised and unusable:
// Invoke drops a non-stageable tool with the bare refusal and creates no
// approval (registry.go:127).
func TestSendMessageToolStagesAgainstTheConversation(t *testing.T) {
	anchor := ids.NewV7()
	tool := sendMessageTool{comms: &recordingComms{}, p: nativeActivityProvider{version: 7}}

	info, err := tool.StageInfo(context.Background(),
		json.RawMessage(`{"activity_id":"`+anchor.String()+`","body":"b","consent_purpose":"support"}`))
	if err != nil {
		t.Fatalf("StageInfo: %v", err)
	}
	if info.TargetType != string(datasource.EntityActivity) || info.TargetID != anchor {
		t.Errorf("staged against %s/%v, want activity/%v", info.TargetType, info.TargetID, anchor)
	}
	if info.TargetVersion == nil || *info.TargetVersion != 7 {
		t.Errorf("TargetVersion = %v, want the anchor's row version so redemption re-checks it", info.TargetVersion)
	}
	if info.Summary == "" {
		t.Error("Summary is empty; the inbox would show an unlabelled approval")
	}
}

// An approval for a record held in an external system of record could never be
// released, so staging refuses rather than creating one.
func TestSendMessageToolRefusesToStageAMirroredConversation(t *testing.T) {
	tool := sendMessageTool{comms: &recordingComms{}, p: mirroredActivityProvider{}}

	_, err := tool.StageInfo(context.Background(),
		json.RawMessage(`{"activity_id":"`+ids.NewV7().String()+`","body":"b","consent_purpose":"support"}`))
	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Errorf("StageInfo err = %v, want ErrUnsupportedBySoR for a mirror-backed conversation", err)
	}
}

func TestSendMessageToolPassesTheAnchorAndArgsToTheSeam(t *testing.T) {
	comms := &recordingComms{}
	anchor := ids.NewV7()

	out, err := sendMessageTool{comms: comms, p: nativeActivityProvider{}}.Handle(context.Background(),
		json.RawMessage(`{"activity_id":"`+anchor.String()+`","body":"on my way","consent_purpose":"support"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if comms.anchor != anchor {
		t.Errorf("anchor = %v, want %v", comms.anchor, anchor)
	}
	if comms.args.Body != "on my way" || comms.args.ConsentPurpose != "support" {
		t.Errorf("args = %+v, want body %q purpose %q", comms.args, "on my way", "support")
	}
	if string(out) != `{"status":"accepted"}` {
		t.Errorf("out = %s, want the seam's own answer", out)
	}
}

// decodeArgs rejects unknown fields but does not require non-zero ones
// (tools.go:74). An unknown field must still be refused here, so a client
// typo is never silently dropped into a customer-visible send.
func TestSendMessageToolRefusesUnknownArguments(t *testing.T) {
	_, err := sendMessageTool{comms: &recordingComms{}, p: nativeActivityProvider{}}.Handle(
		context.Background(),
		json.RawMessage(`{"activity_id":"`+ids.NewV7().String()+`","body":"b","consent_purpose":"support","to":"@someone"}`))
	if err == nil {
		t.Error("an unknown `to` argument was accepted; the recipient is not the caller's to name")
	}
}

func TestRegisterCommsToolsRegistersTheChannelReply(t *testing.T) {
	r := NewRegistry(nil, nil)
	RegisterCommsTools(r, &recordingComms{}, nativeActivityProvider{})

	if _, ok := r.Spec("send_message"); !ok {
		t.Error("send_message is not registered; the MCP surface cannot reach the channel reply")
	}
}
