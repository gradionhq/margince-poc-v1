// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
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

// IsChannelKind mirrors activities.IsChannelKind's answer for this
// installation's one wired channel provider, without importing that module
// (agents may not import a sibling): "telegram" is the only kind
// send_message can ever reply on, so it is the only one this double admits.
func (c *recordingComms) IsChannelKind(kind string) bool { return kind == "telegram" }

// nativeActivityProvider serves the anchor as a row this database owns —
// Freshness.Authoritative true, the only case an approval can be released for.
type nativeActivityProvider struct {
	datasource.SystemOfRecordProvider
	version int64
	// kind is the anchor activity's kind; empty defaults to "telegram" so
	// existing call sites that don't care about the kind stay a channel.
	kind string
}

func (p nativeActivityProvider) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	kind := p.kind
	if kind == "" {
		kind = "telegram"
	}
	fields, err := json.Marshal(map[string]string{"kind": kind})
	if err != nil {
		return datasource.Record{}, err
	}
	return datasource.Record{
		Ref: ref, Version: p.version,
		Freshness: datasource.FreshnessInfo{Authoritative: true},
		Fields:    fields,
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
		// This is the tool's own contribution to staging, not the pin of
		// record: the approvals engine resolves its own version server-side
		// inside the staging transaction and the adapter that forwards this
		// StageRequest drops TargetVersion rather than passing it through.
		t.Errorf("TargetVersion = %v, want the anchor's row version", info.TargetVersion)
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

// An empty (or whitespace-only) body must never mint an approval: Handle's
// eventual SendMessage call refuses it with errEmptyMessageBody, so a human
// who approved it would have released a "yes" that can only fail on
// redemption — the shape StageInfo exists to close off.
func TestSendMessageToolRefusesToStageAnEmptyBody(t *testing.T) {
	for name, body := range map[string]string{"empty": "", "whitespace-only": "   "} {
		t.Run(name, func(t *testing.T) {
			tool := sendMessageTool{comms: &recordingComms{}, p: nativeActivityProvider{}}

			_, err := tool.StageInfo(context.Background(),
				json.RawMessage(`{"activity_id":"`+ids.NewV7().String()+`","body":"`+body+`","consent_purpose":"support"}`))
			var bad *BadArgsError
			if !errors.As(err, &bad) {
				t.Errorf("StageInfo err = %v, want *BadArgsError for a %s body", err, name)
			}
		})
	}
}

// An anchor that is not a messaging-channel conversation (a note, a mail
// thread, ...) must never mint an approval either: Handle's eventual
// SendMessage call refuses it with NotAChannelConversationError, the same
// "yes with no path to happening" shape the empty-body guard closes.
func TestSendMessageToolRefusesToStageANonChannelAnchor(t *testing.T) {
	tool := sendMessageTool{comms: &recordingComms{}, p: nativeActivityProvider{kind: "note"}}

	_, err := tool.StageInfo(context.Background(),
		json.RawMessage(`{"activity_id":"`+ids.NewV7().String()+`","body":"b","consent_purpose":"support"}`))
	var bad *BadArgsError
	if !errors.As(err, &bad) {
		t.Errorf("StageInfo err = %v, want *BadArgsError for a non-channel anchor kind", err)
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

// multiLinkProvider serves each linked record with its own authority, so a
// booking that mixes a local record with a mirrored one can be exercised.
type multiLinkProvider struct {
	datasource.SystemOfRecordProvider
	// heldElsewhere names the ids whose authority lives in another system;
	// everything else reads back as a row this database owns.
	heldElsewhere map[ids.UUID]bool
	read          []datasource.EntityRef
}

func (p *multiLinkProvider) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	p.read = append(p.read, ref)
	return datasource.Record{
		Ref: ref, Version: 3, Fields: json.RawMessage(`{}`),
		Freshness: datasource.FreshnessInfo{Authoritative: !p.heldElsewhere[ref.ID]},
	}, nil
}

// sendCtx is one authenticated agent holding the send scope — enough to pass
// the scope arm of admission and be refused on the tier, which is the refusal
// staging exists to catch.
func sendCtx() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(),
		Scopes:     principal.NewScopeSet(principal.ScopeSend),
	})
}

// Both outbound verbs were advertised as confirm-first and could not be
// confirmed: Registry.Invoke stages only a tool that implements StageInfo, and
// neither did, so the refusal came back bare and no approval was ever minted.
// A 🟡 tool a human can never say yes to is a tool that does not work.
func TestRefusedOutboundCallsReachTheInbox(t *testing.T) {
	anchor, deal := ids.NewV7(), ids.NewV7()
	for _, tc := range []struct {
		tool, args  string
		wantTarget  string
		wantSummary string
	}{
		{
			tool:        "send_email",
			args:        fmt.Sprintf(`{"activity_id":%q,"to":["buyer@example.test"],"subject":"Next steps","body":"b","consent_purpose":"support"}`, anchor),
			wantTarget:  string(datasource.EntityActivity),
			wantSummary: `Send an email to buyer@example.test, subject "Next steps"`,
		},
		{
			tool: "book_meeting",
			args: fmt.Sprintf(`{"start":"2026-08-03T09:00:00Z","end":"2026-08-03T09:30:00Z","subject":"Review","links":[{"entity_type":"deal","entity_id":%q}]}`, deal),
			// A booking anchors on no row, so the first link is what the
			// human sees and what the pin is taken from.
			wantTarget:  "deal",
			wantSummary: `Book "Review" from 2026-08-03T09:00:00Z to 2026-08-03T09:30:00Z, attached to 1 record(s)`,
		},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			approvals := &recordingApprovals{}
			registry := NewRegistry(approvals, auth.NewGate(fullSeatAuthority{}))
			RegisterCommsTools(registry, &recordingComms{}, &multiLinkProvider{})

			_, err := registry.Invoke(sendCtx(), tc.tool, json.RawMessage(tc.args))

			var staged *workflow.StagedApprovalError
			if !errors.As(err, &staged) {
				t.Fatalf("Invoke err = %v, want a StagedApprovalError — the refusal must mint an approval, not dead-end", err)
			}
			if len(approvals.staged) != 1 {
				t.Fatalf("staged %d approvals, want 1", len(approvals.staged))
			}
			got := approvals.staged[0]
			if got.Tool != tc.tool {
				t.Errorf("staged under tool %q, want %q", got.Tool, tc.tool)
			}
			if got.TargetType != tc.wantTarget {
				t.Errorf("TargetType = %q, want %q", got.TargetType, tc.wantTarget)
			}
			if got.TargetVersion == nil {
				t.Error("no version pinned — redemption could not detect the target changing under the approval")
			}
			if got.Summary != tc.wantSummary {
				t.Errorf("Summary = %q, want %q", got.Summary, tc.wantSummary)
			}
			if got.DiffHash == "" {
				t.Error("no diff hash — the approval would not be bound to this exact call")
			}
		})
	}
}

// Staging refuses what execution would refuse anyway. Otherwise the approval
// is a trap: a human reads it, says yes, the approved retry consumes the
// one-shot authority, and only then does the store refuse — the yes is spent
// on something that was never going to happen. Both cases below were found by
// driving a real session against the surface, which staged all of them.
func TestStagingRefusesASendOrBookingExecutionWouldRefuse(t *testing.T) {
	anchor := ids.NewV7()
	for _, tc := range []struct {
		name, tool, args, wantNamed string
	}{
		{
			name:      "a mail with no addressee reaches nobody",
			tool:      "send_email",
			args:      fmt.Sprintf(`{"activity_id":%q,"to":[],"subject":"s","body":"b","consent_purpose":"support"}`, anchor),
			wantNamed: "`to` is empty",
		},
		{
			name:      "a meeting that ends before it starts is not bookable",
			tool:      "book_meeting",
			args:      `{"start":"2026-08-10T15:00:00Z","end":"2026-08-10T14:00:00Z","subject":"s"}`,
			wantNamed: "does not follow `start`",
		},
		{
			name:      "a meeting of zero length is not bookable either",
			tool:      "book_meeting",
			args:      `{"start":"2026-08-10T15:00:00Z","end":"2026-08-10T15:00:00Z","subject":"s"}`,
			wantNamed: "does not follow `start`",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			approvals := &recordingApprovals{}
			registry := NewRegistry(approvals, auth.NewGate(fullSeatAuthority{}))
			RegisterCommsTools(registry, &recordingComms{}, &multiLinkProvider{})

			_, err := registry.Invoke(sendCtx(), tc.tool, json.RawMessage(tc.args))

			var bad *BadArgsError
			if !errors.As(err, &bad) {
				t.Fatalf("Invoke err = %v, want a BadArgsError refusing it before staging", err)
			}
			if !strings.Contains(err.Error(), tc.wantNamed) {
				t.Errorf("err = %q, want it to name %q — the agent wrote the argument and is the one who can fix it", err, tc.wantNamed)
			}
			if len(approvals.staged) != 0 {
				t.Errorf("staged %d approvals for a call that can never execute: %+v", len(approvals.staged), approvals.staged)
			}
		})
	}
}

// A nil comms seam and a nil provider are not the same kind of absence. Without
// comms there is nothing to offer and registering nothing is right. Without a
// provider the three 🟡 verbs would still register, advertise themselves on
// tools/list, and dereference it the first time a call was staged — so wiring
// fails instead, rather than shipping a surface that panics on its own
// outbound verbs.
func TestRegisterCommsToolsDistinguishesTheTwoAbsences(t *testing.T) {
	noComms := NewRegistry(nil, nil)
	RegisterCommsTools(noComms, nil, nil)
	if got := len(noComms.Specs()); got != 0 {
		t.Errorf("registered %d tools with no comms seam, want 0", got)
	}

	mustPanic(t, "a provider-less comms surface advertises three sends it cannot stage", func() {
		RegisterCommsTools(NewRegistry(nil, nil), &recordingComms{}, nil)
	})
}

// A human approving from the inbox row reads ONE line. Any argument that
// changes what gets released and is missing from it is an effect nobody agreed
// to — the diff_hash binds it faithfully either way, which is exactly what
// makes an omission from the DISPLAY the problem rather than a harmless
// abbreviation. The REST admission gate enumerates every body field for this
// reason; both transports stage the same operation.
func TestAStagedSummaryNamesEveryArgumentItReleases(t *testing.T) {
	host, deal, org := ids.NewV7(), ids.NewV7(), ids.NewV7()

	t.Run("a send names its cc, not only its to", func(t *testing.T) {
		got := describeSend(sendEmailToolArgs{SendEmailArgs: SendEmailArgs{
			To: []string{"buyer@example.test"}, Cc: []string{"rival@example.test"}, Subject: "Q3 pricing",
		}})
		for _, want := range []string{"buyer@example.test", "rival@example.test", `"Q3 pricing"`} {
			if !strings.Contains(got, want) {
				t.Errorf("summary %q does not name %q", got, want)
			}
		}
	})

	t.Run("a booking names whose calendar and how many records", func(t *testing.T) {
		args := BookMeetingArgs{
			HostUserID: &host, Subject: "Review",
			Start: time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 8, 10, 9, 30, 0, 0, time.UTC),
		}
		got := describeBooking(args, []bookingLink{
			{EntityType: "deal", EntityID: deal}, {EntityType: "organization", EntityID: org},
		})
		for _, want := range []string{host.String(), "2 record(s)", `"Review"`} {
			if !strings.Contains(got, want) {
				t.Errorf("summary %q does not name %q", got, want)
			}
		}
	})
}
