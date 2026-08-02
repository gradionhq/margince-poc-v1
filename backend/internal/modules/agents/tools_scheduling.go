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
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "getAvailability",
		InputSchema: schema(`{"type":"object","required":["from","to"],"properties":{
			"host_user_id":{"type":"string","format":"uuid","description":"Defaults to the acting principal's user"},
			"from":{"type":"string","format":"date-time"` + timestampNote + `},
			"to":{"type":"string","format":"date-time"` + timestampNote + `},
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
		RequiredScope: principal.ScopeSend, Tier: mcp.TierConfirmationRequired, Egress: true,
		OpenAPIOp: "bookMeeting",
		InputSchema: schema(`{"type":"object","required":["start","end"],"properties":{
			"host_user_id":{"type":"string","format":"uuid"},
			"start":{"type":"string","format":"date-time"` + timestampNote + `},
			"end":{"type":"string","format":"date-time"` + timestampNote + `},
			"subject":{"type":"string"},
			"links":{"type":"array","items":{"type":"object","required":["entity_type","entity_id"],"properties":{
				"entity_type":{"type":"string","enum":["person","organization","deal","lead","project"]},
				"entity_id":{"type":"string","format":"uuid"}},"additionalProperties":false},"maxItems":25},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved the staged call"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

// StageInfo puts a refused booking in the inbox instead of dead-ending it.
//
// A booking anchors on no existing row, so unlike the two send verbs it has no
// single target handed to it. What it does have is its links, and those are
// records: EVERY one is read and refused if its authority lives in another
// system, because redemption's version pin reads our own tables and a
// mirror-held link has no row there — the same un-releasable approval
// refuseStagingElsewhere exists to prevent, reached through a different door.
// Checking every link rather than the one displayed is deliberate: a booking
// with a local deal and a mirrored organization is exactly the case a
// first-link-only check would wave through.
//
// The first link becomes the displayed target and supplies the pin. A booking
// with no links stages with no target at all, which the approvals engine
// serves (the pin is simply absent) — a slot on a calendar is a real thing to
// approve even when it names no record.
func (t bookMeetingTool) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args BookMeetingArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	// Refuse here what the store refuses at execution, for the same reason the
	// mail send does: a human should not be asked to approve a meeting that
	// ends before it starts, spend the one-shot approval on it, and only then
	// be told it was never bookable.
	if !args.End.After(args.Start) {
		return StageInfo{}, &BadArgsError{Cause: fmt.Errorf(
			"`end` (%s) does not follow `start` (%s); a booking with no duration would be refused after approval",
			args.End.Format(time.RFC3339), args.Start.Format(time.RFC3339))}
	}
	links, err := bookingLinks(args)
	if err != nil {
		return StageInfo{}, err
	}
	info := StageInfo{Summary: describeBooking(args, links)}
	for i, link := range links {
		rec, readErr := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityType(link.EntityType), ID: link.EntityID})
		if readErr != nil {
			return StageInfo{}, readErr
		}
		if err := refuseStagingElsewhere(rec); err != nil {
			return StageInfo{}, err
		}
		if i == 0 {
			info.TargetType, info.TargetID, info.TargetVersion = link.EntityType, link.EntityID, &rec.Version
		}
	}
	return info, nil
}

// maxBookingLinks bounds how many records one booking may attach to.
//
// This is a REQUEST BOUND, not a modelling opinion. Each link costs its own
// row-scoped provider read in its own transaction, and the array is chosen
// freely by the caller: at the 1 MiB body limit a single tools/call could
// carry ~15,000 of them, spending tens of thousands of queries against a
// 16-connection pool inside one request — before any human has approved
// anything, since staging runs on the refusal path. A meeting that genuinely
// touches more records than this is not a meeting.
const maxBookingLinks = 25

// bookingLinks validates and de-duplicates the links a booking attaches to.
// Deduplicating first matters as much as the cap: the same id repeated is the
// cheapest way to turn one call into N reads, and it is also just a caller
// mistake worth not charging for twice.
func bookingLinks(args BookMeetingArgs) ([]bookingLink, error) {
	if len(args.Links) > maxBookingLinks {
		return nil, &BadArgsError{Cause: fmt.Errorf(
			"a booking may attach to at most %d records; this call names %d", maxBookingLinks, len(args.Links))}
	}
	seen := make(map[bookingLink]struct{}, len(args.Links))
	unique := make([]bookingLink, 0, len(args.Links))
	for _, l := range args.Links {
		link := bookingLink{EntityType: l.EntityType, EntityID: l.EntityID}
		if _, dup := seen[link]; dup {
			continue
		}
		seen[link] = struct{}{}
		unique = append(unique, link)
	}
	return unique, nil
}

// bookingLink is one (type, id) pair, comparable so it can key the dedupe set.
type bookingLink struct {
	EntityType string
	EntityID   ids.UUID
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
func describeBooking(args BookMeetingArgs, links []bookingLink) string {
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
	return t.comms.BookMeeting(ctx, args)
}
