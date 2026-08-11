// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What the account-started send stages, and what it refuses to stage. It is
// the reply's twin in governance and its opposite in shape: no anchor, so the
// records it is filed under are the only records it has, and they carry every
// refusal the anchor carries for send_email.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// accountSendArgs is one well-formed call, so a test that varies ONE thing
// varies only that thing.
func accountSendArgs(links string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(
		`{"to":["buyer@example.test"],"subject":"Introductions","body":"b","consent_purpose":"sales","links":[%s]}`,
		links))
}

func orgLink(id ids.UUID) string {
	return fmt.Sprintf(`{"entity_type":"organization","entity_id":%q}`, id)
}

// ADR-0087 §6: the agent surface gains this operation governed identically to
// the reply, with no new authority. "Identically" is four properties, and each
// one is a way the twin could have been governed more loosely by accident.
func TestTheAccountStartedSendGovernsAsTheReplyDoes(t *testing.T) {
	spec, reply := sendAccountEmailTool{}.Spec(), sendEmailTool{}.Spec()

	if spec.RequiredScope != reply.RequiredScope {
		t.Errorf("RequiredScope = %q, want the reply's %q", spec.RequiredScope, reply.RequiredScope)
	}
	if spec.RequiredScope != principal.ScopeSend {
		t.Errorf("RequiredScope = %q, want %q", spec.RequiredScope, principal.ScopeSend)
	}
	if spec.Tier != mcp.TierConfirmationRequired {
		t.Errorf("Tier = %v, want TierConfirmationRequired — an unapproved send would leave the workspace", spec.Tier)
	}
	if !spec.Egress {
		t.Error("Egress = false; the message leaves the workspace")
	}
	if spec.OpenAPIOp != "sendAccountEmail" {
		t.Errorf("OpenAPIOp = %q, want %q", spec.OpenAPIOp, "sendAccountEmail")
	}
}

// The staged shape is a CREATE: the type the effect will write, no id, no pin.
//
// That is what makes it decidable — approvals settles an id-less shape on the
// object-read floor plus the decision grants — and it is the SAME row the REST
// door stages for this operation, whose route carries no {id} for a target to
// be taken from. One operation staged two ways is a human deciding a different
// question depending on which transport the agent used.
func TestTheAccountStartedSendStagesACreate(t *testing.T) {
	org := ids.NewV7()
	p := &multiLinkProvider{}

	info, err := sendAccountEmailTool{comms: &recordingComms{}, p: p}.
		StageInfo(context.Background(), accountSendArgs(orgLink(org)))
	if err != nil {
		t.Fatalf("StageInfo: %v", err)
	}
	if info.TargetType != string(datasource.EntityActivity) {
		t.Errorf("TargetType = %q, want %q — the effect writes an activity",
			info.TargetType, datasource.EntityActivity)
	}
	if !info.TargetID.IsZero() {
		t.Errorf("TargetID = %v, want the zero id: this send answers no message, so it targets no row", info.TargetID)
	}
	if info.TargetVersion != nil {
		t.Errorf("TargetVersion = %v, want none — pinning a linked record's version would refuse an "+
			"approved message because somebody edited that record in between", *info.TargetVersion)
	}
	if len(p.read) != 1 || p.read[0].ID != org {
		t.Errorf("read %v, want the one named link — the store refuses a link the caller cannot see, "+
			"and staging must ask that question before a human is asked anything", p.read)
	}
}

// The inbox row is the whole of what a human reads before releasing a send, so
// an addressee missing from it is a recipient nobody agreed to.
func TestTheAccountStartedSendSummaryNamesEveryArgumentItReleases(t *testing.T) {
	got := describeAccountSend(SendAccountEmailArgs{SendEmailArgs: SendEmailArgs{
		To: []string{"buyer@example.test"}, Cc: []string{"rival@example.test"}, Subject: "Q3 pricing",
	}}, []RecordLink{{EntityType: "organization", EntityID: ids.NewV7()}})

	for _, want := range []string{"buyer@example.test", "rival@example.test", `"Q3 pricing"`, "1 record(s)"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q does not name %q", got, want)
		}
	}
}

// Two refusals the send path raises anyway, raised here instead — at BOTH
// doors this verb has, because an InputSchema on the MCP surface is
// documentation rather than validation and Handle is entered with an approval
// already redeemed rather than through StageInfo.
//
// Raising them late is not a smaller bug than not raising them at all: a human
// approves a send that was never going to happen, the retry consumes the
// one-shot authority on it, and the store refuses permanently.
func TestAnUnsendableAccountStartedCallIsRefusedAtBothDoors(t *testing.T) {
	for _, tc := range []struct {
		name, args, names string
	}{
		{
			name:  "no addressee",
			args:  `{"to":[],"subject":"s","body":"b","consent_purpose":"sales","links":[{"entity_type":"organization","entity_id":"019ff000-0000-7000-8000-000000000001"}]}`,
			names: "`to`",
		},
		{
			name:  "filed under nothing",
			args:  `{"to":["buyer@example.test"],"subject":"s","body":"b","consent_purpose":"sales","links":[]}`,
			names: "`links`",
		},
	} {
		comms := &recordingComms{}
		tool := sendAccountEmailTool{comms: comms, p: &multiLinkProvider{}}
		doors := map[string]func(json.RawMessage) error{
			"staging": func(in json.RawMessage) error {
				_, err := tool.StageInfo(context.Background(), in)
				return err
			},
			"after an approval was redeemed": func(in json.RawMessage) error {
				_, err := tool.Handle(withApprovalRedeemed(sendCtx(), 0, false), in)
				return err
			},
		}
		for door, call := range doors {
			t.Run(tc.name+"/"+door, func(t *testing.T) {
				err := call(json.RawMessage(tc.args))

				var badArgs *BadArgsError
				if !errors.As(err, &badArgs) {
					t.Fatalf("err = %v, want a BadArgsError naming %s", err, tc.names)
				}
				if !strings.Contains(err.Error(), tc.names) {
					t.Errorf("err = %v, want it to name %s", err, tc.names)
				}
				if comms.accountSent != nil {
					t.Error("an unsendable call reached the comms seam")
				}
			})
		}
	}
}

// The refused call reaches the inbox rather than dead-ending: Registry.Invoke
// stages only a tool that describes its own staging, so a 🟡 verb without one
// is advertised and unusable. It is the same loop send_email's own pin covers,
// asked of the twin.
func TestARefusedAccountStartedSendReachesTheInbox(t *testing.T) {
	approvals := &recordingApprovals{}
	comms := &recordingComms{}
	registry := NewRegistry(approvals, auth.NewGate(fullSeatAuthority{}))
	RegisterCommsTools(registry, comms, &multiLinkProvider{})

	_, err := registry.Invoke(sendCtx(), "send_account_email", accountSendArgs(orgLink(ids.NewV7())))

	var staged *workflow.StagedApprovalError
	if !errors.As(err, &staged) {
		t.Fatalf("Invoke err = %v, want the refusal to have staged an approval", err)
	}
	if len(approvals.staged) != 1 {
		t.Fatalf("staged %d approvals, want exactly one", len(approvals.staged))
	}
	if comms.accountSent != nil {
		t.Error("the unapproved call reached the comms seam")
	}
}

// unreadableProvider answers every read the way a row outside the caller's
// scope does — not-found, indistinguishable from absent, which is how this
// product hides a record's existence.
type unreadableProvider struct {
	datasource.SystemOfRecordProvider
}

func (unreadableProvider) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	return datasource.Record{}, fmt.Errorf("record %s: %w", ref.ID, apperrors.ErrNotFound)
}

// A link the caller cannot see is refused at staging, with the row-scope
// answer and nothing else.
//
// The store refuses it too, but only when the approved send runs — by which
// point a human has read the proposal, said yes, and spent a one-shot
// authority on a message that was never going to leave. The refusal has to
// arrive before the question is asked.
func TestAnAccountStartedSendRefusesALinkTheCallerCannotSee(t *testing.T) {
	approvals := &recordingApprovals{}
	registry := NewRegistry(approvals, auth.NewGate(fullSeatAuthority{}))
	RegisterCommsTools(registry, &recordingComms{}, unreadableProvider{})

	_, err := registry.Invoke(sendCtx(), "send_account_email", accountSendArgs(orgLink(ids.NewV7())))

	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("Invoke err = %v, want the row-scope answer — a record the caller cannot read is not a "+
			"record they may start a conversation about", err)
	}
	if len(approvals.staged) != 0 {
		t.Errorf("staged %d approvals for an unreachable record, want none", len(approvals.staged))
	}
}

// Malformed arguments are refused before anything is staged, at both doors —
// there is no call here to put in front of a human.
func TestAnAccountStartedSendRefusesUnreadableArguments(t *testing.T) {
	tool := sendAccountEmailTool{comms: &recordingComms{}, p: &multiLinkProvider{}}
	const notAnObject = `["send","this"]`

	if _, err := tool.StageInfo(context.Background(), json.RawMessage(notAnObject)); err == nil {
		t.Error("StageInfo accepted arguments it cannot read")
	}
	if _, err := tool.Handle(withApprovalRedeemed(sendCtx(), 0, false), json.RawMessage(notAnObject)); err == nil {
		t.Error("Handle accepted arguments it cannot read")
	}
}

// Every link is read, not just the first: a send filed under one local record
// and one mirrored record is exactly what a first-link-only guard waves through
// into an approval nobody can release.
func TestTheAccountStartedSendRefusesAMirroredLinkBehindALocalOne(t *testing.T) {
	local, mirrored := ids.NewV7(), ids.NewV7()
	p := &multiLinkProvider{heldElsewhere: map[ids.UUID]bool{mirrored: true}}

	_, err := sendAccountEmailTool{comms: &recordingComms{}, p: p}.StageInfo(context.Background(),
		accountSendArgs(fmt.Sprintf(`{"entity_type":"deal","entity_id":%q},%s`, local, orgLink(mirrored))))

	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("StageInfo err = %v, want ErrUnsupportedBySoR — the second link was never validated", err)
	}
	if len(p.read) != 2 {
		t.Errorf("read %d links, want both", len(p.read))
	}
}

// The link array is chosen freely by the caller and each entry costs its own
// row-scoped read, on the refusal path, before any human has approved anything.
// The bound and the dedupe both have to precede the reads they exist to
// prevent, and this entry point is a second door onto them.
func TestAnAccountStartedSendIsBoundedAndDeduplicatedBeforeItReadsAnything(t *testing.T) {
	t.Run("refuses more links than a message could be about", func(t *testing.T) {
		links := make([]string, maxRecordLinks+1)
		for i := range links {
			links[i] = orgLink(ids.NewV7())
		}
		p := &multiLinkProvider{}

		_, err := sendAccountEmailTool{comms: &recordingComms{}, p: p}.
			StageInfo(context.Background(), accountSendArgs(strings.Join(links, ",")))

		var bad *BadArgsError
		if !errors.As(err, &bad) {
			t.Fatalf("StageInfo err = %v, want a BadArgsError refusing the oversized link array", err)
		}
		if len(p.read) != 0 {
			t.Errorf("read %d records before refusing — the cap must precede the reads it exists to prevent", len(p.read))
		}
	})

	t.Run("reads a repeated link once", func(t *testing.T) {
		org := ids.NewV7()
		repeated := make([]string, 10)
		for i := range repeated {
			repeated[i] = orgLink(org)
		}
		p := &multiLinkProvider{}

		info, err := sendAccountEmailTool{comms: &recordingComms{}, p: p}.
			StageInfo(context.Background(), accountSendArgs(strings.Join(repeated, ",")))
		if err != nil {
			t.Fatalf("StageInfo: %v", err)
		}
		if len(p.read) != 1 {
			t.Errorf("read %d records for one repeated link, want 1", len(p.read))
		}
		if !strings.Contains(info.Summary, "1 record(s)") {
			t.Errorf("summary %q counts the repeats; a human would read a reach the send does not have", info.Summary)
		}
	})
}

// The approved call sends what was staged, through the seam, with the links
// deduplicated exactly as the staged summary counted them.
func TestAnApprovedAccountStartedSendReachesTheSeam(t *testing.T) {
	org := ids.NewV7()
	comms := &recordingComms{}
	tool := sendAccountEmailTool{comms: comms, p: &multiLinkProvider{}}

	out, err := tool.Handle(withApprovalRedeemed(sendCtx(), 0, false),
		accountSendArgs(orgLink(org)+","+orgLink(org)))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(comms.accountSent) != 1 || comms.accountSent[0].EntityID != org {
		t.Fatalf("seam reached with %v, want the one deduplicated link", comms.accountSent)
	}
	var result SendEmailResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("result is not a SendEmailResult: %v", err)
	}
	if result.Status != "accepted" {
		t.Errorf("Status = %q, want the delivery path's answer", result.Status)
	}
}
