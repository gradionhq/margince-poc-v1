// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The scheduling verbs, split out of tools_comms.go when that file crossed the
// 500-line cap. The seam they ride (Comms) and its registration still live
// next door, because it serves both families — what separates them is the
// subject: these two answer about TIME and commit a slot, where the mail and
// channel verbs address a person and send them words.
//
// check_availability is 🟢 (it proposes slots and commits nothing);
// book_meeting is 🟡 — it writes a meeting and implies an invitation.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// --- check_availability (🟢 read) ---

type checkAvailability struct{ comms Comms }

func (t checkAvailability) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "check_availability", Title: "Check calendar availability", Version: toolVersionV1,
		Description:   checkAvailabilityCopy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "getAvailability",
		InputSchema: schema(`{"type":"object","required":["from","to"],"properties":{
			"host_user_id":{"type":"string","format":"uuid","description":"Defaults to the acting principal's user"},
			"from":{"type":"string","format":"date-time"` + timestampNote + `},
			"to":{"type":"string","format":"date-time"` + timestampNote + `},
			"duration_minutes":{"type":"integer","minimum":15,"maximum":480}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[AvailabilityResult](),
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
	noteDerivedContent(ctx)
	return marshalResult(t.comms.Availability(ctx, args.HostUserID, args.From, args.To, args.DurationMinutes))
}

// --- book_meeting (🟡: commits a slot + implies an invite) ---

// bookMeetingTool carries a record reader for its staging, like the two send
// verbs — but it reads the records the booking will ATTACH to rather than one
// anchor, because a booking has none.
type bookMeetingTool struct {
	comms Comms
	p     datasource.SystemOfRecordProvider
}

func (t bookMeetingTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "book_meeting", Title: "Book a meeting", Version: toolVersionV1,
		Description:   bookMeetingCopy.render(),
		RequiredScope: principal.ScopeSend, Tier: mcp.TierConfirmationRequired, Egress: true,
		OpenAPIOp: "bookMeeting",
		// `links` is REQUIRED by crm.yaml's bookMeeting body and was advertised
		// as optional, so an agent that read the schema and omitted it was
		// refused for a rule the schema never stated. Its vocabulary is spliced
		// from the contract for the same reason log_activity's is: the copy
		// here had drifted to offer a `project` link the contract refuses.
		InputSchema: schema(`{"type":"object","required":["start","end","links"],"properties":{
			"host_user_id":{"type":"string","format":"uuid"},
			"start":{"type":"string","format":"date-time"` + timestampNote + `},
			"end":{"type":"string","format":"date-time"` + timestampNote + `},
			"subject":{"type":"string"},
			"links":{"type":"array","minItems":1,"items":{"type":"object","required":["entity_type","entity_id"],"properties":{
				"entity_type":{"type":"string","enum":` + activityLinkEntityTypeEnum + `},
				"entity_id":{"type":"string","format":"uuid"}},"additionalProperties":false},"maxItems":25,
				"description":"Who and what the meeting is about; at least one. The booking is refused without it."},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved the staged call"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[PassthroughEntityResult](),
	}
}

// StageInfo puts a refused booking in the inbox instead of dead-ending it.
//
// A booking anchors on no existing row, so unlike the two send verbs it has no
// single target handed to it. What it does have is its links, and every one of
// them is read through readStageableLinks — the shared rule for a call that
// names its own records.
//
// The first link becomes the displayed target and supplies the pin, which is
// what makes a booking's staged row bind to a record rather than float free: a
// meeting is a commitment ON that record, and the human deciding it is the one
// whose scope reaches it. A booking with NO links is refused before any of
// that: crm.yaml requires them, and a staged approval with no target is a human
// asked to release a meeting attached to nothing — nothing to show them, and no
// version to pin it against.
func (t bookMeetingTool) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args BookMeetingArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	if err := requireBookingWindow(args); err != nil {
		return StageInfo{}, err
	}
	if err := requireBookingLinks(args); err != nil {
		return StageInfo{}, err
	}
	links, err := uniqueRecordLinks(args.Links)
	if err != nil {
		return StageInfo{}, err
	}
	records, err := readStageableLinks(ctx, t.p, links)
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType:    links[0].EntityType,
		TargetID:      links[0].EntityID,
		TargetVersion: &records[0].Version,
		Summary:       describeBooking(args, links),
	}, nil
}

// requireBookingWindow refuses a booking that ends before it starts.
//
// The store refuses it too (errBookingEndNotAfterStart), which is why this is
// not a correctness hole — but reaching THAT refusal costs the human's approval
// on the way past, since redemption is consumed before the handler runs. So
// both doors ask first, the same rule the link checks below follow.
func requireBookingWindow(args BookMeetingArgs) error {
	if args.End.After(args.Start) {
		return nil
	}
	return &BadArgsError{Cause: fmt.Errorf(
		"`end` (%s) does not follow `start` (%s); a booking with no duration would be refused after approval",
		args.End.Format(time.RFC3339), args.Start.Format(time.RFC3339))}
}

// requireBookingLinks enforces what the schema states: at least one link.
//
// It is a function rather than a line in StageInfo because the MCP surface does
// not validate arguments against an InputSchema — that schema is documentation —
// so the rule holds only where the code puts it, and this verb has two doors: the
// staging path and the post-approval execute. A rule enforced at one of them is
// a rule a caller meets only sometimes.
func requireBookingLinks(args BookMeetingArgs) error {
	if len(args.Links) > 0 {
		return nil
	}
	return &BadArgsError{Cause: errors.New(
		"`links` needs at least one entry: a booking names who and what it is about, " +
			"and one attached to nothing cannot be approved against a record")}
}

// describeBooking is the one line the inbox shows.
//
// It names every argument that changes what gets released — the slot, the
// subject, WHOSE calendar it lands on, and how many records it attaches to.
// A human approving from the inbox row sees only this string, so an argument
// missing from it is an effect nobody agreed to: the REST admission gate's own
// summary enumerates every body field for exactly this reason, and the two
// transports stage the same operation.
//
// The subject is the agent's own text, so it is quoted rather than run into
// the sentence; the approvals engine sanitizes every summary at the single
// staging path regardless.
func describeBooking(args BookMeetingArgs, links []RecordLink) string {
	subject := args.Subject
	if strings.TrimSpace(subject) == "" {
		subject = "(no subject)"
	}
	summary := fmt.Sprintf("Book %q from %s to %s",
		subject, args.Start.Format(time.RFC3339), args.End.Format(time.RFC3339))
	if args.HostUserID != nil {
		summary += fmt.Sprintf(" on %s's calendar", args.HostUserID)
	}
	if len(links) > 0 {
		summary += fmt.Sprintf(", attached to %d record(s)", len(links))
	}
	return summary
}

func (t bookMeetingTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args BookMeetingArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	// Both doors, not just staging. This one is reached with an approval already
	// redeemed, so a call that skipped StageInfo would otherwise execute a
	// booking the schema says is impossible — and the cap and the dedupe are
	// part of that: the human approved "attached to N record(s)" as StageInfo
	// counted them, and a booking that reaches the seam with the raw list is one
	// whose approval was read against a different reach than the one it takes.
	if err := requireBookingWindow(args); err != nil {
		return nil, err
	}
	if err := requireBookingLinks(args); err != nil {
		return nil, err
	}
	links, err := uniqueRecordLinks(args.Links)
	if err != nil {
		return nil, err
	}
	args.Links = links
	for _, link := range links {
		noteEvidence(ctx, datasource.EntityType(link.EntityType), link.EntityID)
	}
	return t.comms.BookMeeting(ctx, args)
}
