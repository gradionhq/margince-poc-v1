// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The Comms seam: the MCP communication verbs delegate to the SAME
// activities store methods the HTTP transport uses (drafting included)
// — two transports, one send path, one consent gate.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// sendAccepted is what a send answers with: the delivery path took it. It is
// not a claim about arrival, and it is one spelling so the two send tools
// cannot report the same outcome differently.
const sendAccepted = "accepted"

// sendScheduled is the other thing a send can answer: the message will leave at
// the moment the caller named, and every gate will be asked again then.
const sendScheduled = "scheduled"

type commsAdapter struct {
	store *activities.Store
	gate  activities.ConsentGate
	draft activities.EmailDrafter
	// stager records an accepted send for transmission. It is the same seam
	// the HTTP transport passes, so the tool surface cannot accept a message
	// nothing will carry.
	stager activities.DeliveryStager
	// channelStager is the same machinery in its channel shape. Two fields
	// rather than one because the delivery store keeps two staging shapes: one
	// struct carrying both an RFC822 subject and a channel recipient could
	// describe a message that is half of each.
	channelStager activities.ChannelDeliveryStager
	// timer wakes a message the caller chose to send later. Nil refuses to
	// defer, exactly as it does on the HTTP transport: an agent must not be
	// able to promise a moment nothing will wake at.
	timer activities.ScheduleTimer
}

var _ agents.Comms = commsAdapter{}

// commsAdapter also satisfies automation.Comms (seams.go) — the
// deterministic draft_email workflow action reuses this ONE adapter
// rather than wrapping it a second time. DraftEmail is shared with
// agents.Comms; ReplyAddress below is automation's alone, because only
// the automation surface composes a message with no human present to
// name who it goes to.
var _ automation.Comms = commsAdapter{}

// ReplyAddress forwards automation's addressee question to the activities
// store, which owns the participants a thread's counterparty is read from.
//
// It asks the store rather than the drafter even when a model drafting lane is
// wired: who a reply is TO is a fact about the record, and a routed drafter
// must not be able to change it.
func (c commsAdapter) ReplyAddress(ctx context.Context, anchor ids.UUID) (string, error) {
	return c.store.ReplyAddressFor(ctx, ids.From[ids.ActivityKind](anchor))
}

func (c commsAdapter) DraftEmail(ctx context.Context, anchor ids.UUID, intent string) (string, string, error) {
	if c.draft != nil {
		return c.draft.DraftEmail(ctx, anchor, intent)
	}
	activity, err := c.store.GetActivityContent(ctx, ids.From[ids.ActivityKind](anchor), storekit.LiveOnly)
	if err != nil {
		return "", "", err
	}
	answering := activities.DraftContext{
		Band:     convstate.BandFresh,
		Threaded: activities.IsMailThread(activity.Kind, activity.Direction),
	}
	if activity.Subject != nil {
		answering.Topic = *activity.Subject
	}
	if activity.Body != nil {
		answering.Body = *activity.Body
	}
	subject, body := activities.DeterministicEmailDraft(answering, intent)
	return subject, body, nil
}

// DraftAccountEmail composes the first message to a record.
//
// There is no thread to read, so the DraftContext is built from what a first
// message actually is rather than from prior correspondence: BandFresh (this
// IS the opening), Threaded false (nothing is being answered), and the intent
// as the topic — the caller's own words for what the message should do, which
// is the only subject material in existence before a conversation starts.
//
// It deliberately does NOT resolve a recipient. DraftEmail can ask the store
// who a thread is with; here the caller names the addressee at send time
// (send_account_email takes `to`), and inventing one from a link would put an
// address in a draft nobody chose.
//
// The links are not read either, and that is a judgment worth stating: the
// drafter takes text, not records, and reading a company's fields into the
// opening line would make the draft's content depend on data the approving
// human is not looking at. They are carried for the SEND to file under.
func (c commsAdapter) DraftAccountEmail(
	ctx context.Context, links []agents.RecordLink, intent string,
) (string, string, error) {
	if len(links) == 0 {
		return "", "", &agents.BadArgsError{Cause: errors.New(
			"links must name at least one record this conversation is filed under; " +
				"a first message has no thread to inherit them from")}
	}
	// Deterministic only, for now. activities.EmailDrafter (the routed model
	// lane) takes an anchor by signature — DraftEmail(ctx, anchor, intent) —
	// so a first message cannot reach it without widening that interface and
	// the AI task behind it. Deliberately left for its own change: the tool's
	// value is that a first message can be drafted AT ALL, and the copy says
	// plainly that the fallback is a short deterministic note.
	subject, body := activities.DeterministicEmailDraft(activities.DraftContext{
		Band:     convstate.BandFresh,
		Threaded: false,
		Topic:    intent,
	}, intent)
	return subject, body, nil
}

// SendEmail carries no DraftRef, and that absence is a statement: a voice
// outcome is the OWNER's judgment of the machine's draft (ADR-0066 §4), so an
// agent's send resolves none — the recorder refuses a non-human principal
// anyway, and naming a reference here would only make that refusal look like
// an accident of wiring.
func (c commsAdapter) SendEmail(ctx context.Context, anchor ids.UUID, in agents.SendEmailArgs) (agents.SendEmailResult, error) {
	return c.send(ctx, activities.FromActivity(ids.From[ids.ActivityKind](anchor)), in)
}

// SendAccountEmail starts a NEW conversation instead of continuing one
// (ADR-0087). It differs from the reply above in the origin and in nothing
// else: the records the message is filed under are named by the caller because
// there is no anchor to inherit them from, and each is row-scope probed by the
// store before the send runs.
func (c commsAdapter) SendAccountEmail(
	ctx context.Context, links []agents.RecordLink, in agents.SendEmailArgs,
) (agents.SendEmailResult, error) {
	filed := make([]activities.ActivityLinkInput, 0, len(links))
	for _, l := range links {
		filed = append(filed, activities.ActivityLinkInput{EntityType: l.EntityType, EntityID: l.EntityID})
	}
	return c.send(ctx, activities.FromAccount(filed), in)
}

// send is the mail send both origins share, spelled once so the two tools
// cannot drift onto different consent gates, different delivery stagers or
// different readings of who the recipients are. Cc is merged into Recipients
// because consent is owed to every addressee — the same rule the HTTP
// transport's sendInputFrom states, and the reason neither is hand-rolled per
// call site.
func (c commsAdapter) send(
	ctx context.Context, origin activities.SendOrigin, in agents.SendEmailArgs,
) (agents.SendEmailResult, error) {
	sched, err := agentSchedule(in)
	if err != nil {
		return agents.SendEmailResult{}, err
	}
	out, err := c.store.SendOrSchedule(ctx, origin, activities.SendEmailInput{
		Recipients:     append(append([]string{}, in.To...), in.Cc...),
		Cc:             append([]string{}, in.Cc...),
		Subject:        in.Subject,
		Body:           in.Body,
		ConsentPurpose: in.ConsentPurpose,
	}, sched, c.gate, c.stager, c.timer)
	if err != nil {
		return agents.SendEmailResult{}, err
	}
	if out.Scheduled != nil {
		return agents.SendEmailResult{
			ScheduledSendID: out.Scheduled.ID,
			ScheduledAt:     out.Scheduled.ScheduledAt.Format(time.RFC3339),
			Status:          sendScheduled,
		}, nil
	}
	return agents.SendEmailResult{ActivityID: ids.UUID(out.Activity.Id), Status: sendAccepted}, nil
}

// agentSchedule reads a tool call's optional scheduling fields.
//
// The instant is parsed HERE rather than passed as a string, so a malformed one
// is refused before anything is staged for a human to approve — an approver
// should never be shown a message whose moment the server cannot read.
func agentSchedule(in agents.SendEmailArgs) (*activities.SendSchedule, error) {
	if in.ScheduledAt == "" && in.ScheduledTZ == "" {
		return nil, nil //nolint:nilnil // "send now" IS the answer for an optional schedule, not a missing value.
	}
	if in.ScheduledAt == "" {
		return nil, &activities.InvalidScheduleError{Field: activities.FieldScheduledAt, Reason: "is required when a zone is given"}
	}
	if in.ScheduledTZ == "" {
		return nil, &activities.InvalidScheduleError{Field: activities.FieldScheduledTZ, Reason: "is required when a moment is given"}
	}
	at, err := time.Parse(time.RFC3339, in.ScheduledAt)
	if err != nil {
		return nil, &activities.InvalidScheduleError{Field: activities.FieldScheduledAt, Reason: "is not an RFC3339 instant"}
	}
	return &activities.SendSchedule{At: at, TZ: in.ScheduledTZ}, nil
}

// SendMessage replies on a captured channel conversation through the SAME
// store method the HTTP transport calls, so the consent gate, the recipient
// resolution and the RBAC check cannot differ by transport. The recipient is
// absent from the arguments by design: the store resolves it from the anchor.
func (c commsAdapter) SendMessage(ctx context.Context, anchor ids.UUID, in agents.SendMessageArgs) (agents.SendMessageResult, error) {
	sent, err := c.store.SendMessage(ctx, ids.From[ids.ActivityKind](anchor), activities.SendMessageInput{
		Body:           in.Body,
		ConsentPurpose: in.ConsentPurpose,
	}, c.gate, c.channelStager)
	if err != nil {
		return agents.SendMessageResult{}, err
	}
	return agents.SendMessageResult{ActivityID: ids.UUID(sent.Id), Status: sendAccepted}, nil
}

// IsChannelKind delegates to activities.IsChannelKind — the same test the
// store's own SendMessage refuses on — so the staging pre-check and Handle's
// eventual refusal can never drift onto two different answers for the same kind.
func (c commsAdapter) IsChannelKind(kind string) bool { return activities.IsChannelKind(kind) }

// CanSendOnProvider delegates to activities.CanSendOnProvider for the same
// reason: the pre-staging refusal and the store's own must answer alike.
func (c commsAdapter) CanSendOnProvider(provider string) bool {
	return activities.CanSendOnProvider(provider)
}

// channelKinds is that same question WITHOUT the send machinery around it, for
// the REST admission gate: its send_message resolver must refuse a non-channel
// anchor before a human is asked about the reply, and it has no reason to hold
// a seam that can also send mail, book meetings and read calendars in order to
// ask (restCommandDeps, agentcommand.go).
//
// It reaches activities.IsChannelKind exactly as the adapter above does, so the
// two are one answer arrived at from two places rather than two answers — there
// is no state here for a second opinion to accumulate in.
type channelKinds struct{}

func (channelKinds) IsChannelKind(kind string) bool { return activities.IsChannelKind(kind) }

func (channelKinds) CanSendOnProvider(provider string) bool {
	return activities.CanSendOnProvider(provider)
}

var _ agents.ChannelKinds = channelKinds{}

func (c commsAdapter) Availability(ctx context.Context, host *ids.UUID, from, to time.Time, durationMinutes int) (agents.AvailabilityResult, error) {
	hostID, err := defaultHost(ctx, host)
	if err != nil {
		return agents.AvailabilityResult{}, err
	}
	// The store applies its default slot duration when none is named.
	slots, truncated, err := c.store.Availability(ctx, ids.From[ids.UserKind](hostID), from, to, time.Duration(durationMinutes)*time.Minute)
	if err != nil {
		return agents.AvailabilityResult{}, err
	}
	// truncated is not decoration on this surface. The walk stops at a cap, and
	// a model handed a capped list with nothing marking it will tell a rep there
	// is no later opening — the same failure AtRiskReport.Truncated and
	// intro_path_to's candidates_truncated exist to prevent.
	// An empty LIST, not a null: a fully booked window is a real answer, and a
	// model handed null reads it as "unknown" and hedges about a calendar the
	// server read successfully. The declared schema requires the member, so a
	// null would also cost the answer its structured half.
	free := make([]agents.FreeSlot, 0, len(slots))
	for _, s := range slots {
		free = append(free, agents.FreeSlot{Start: s.Start, End: s.End})
	}
	return agents.AvailabilityResult{Slots: free, Truncated: truncated}, nil
}

func (c commsAdapter) BookMeeting(ctx context.Context, in agents.BookMeetingArgs) (json.RawMessage, error) {
	hostID, err := defaultHost(ctx, in.HostUserID)
	if err != nil {
		return nil, err
	}
	booked := activities.BookMeetingInput{
		Host: ids.From[ids.UserKind](hostID), Start: in.Start, End: in.End, Subject: in.Subject,
	}
	for _, l := range in.Links {
		booked.Links = append(booked.Links, activities.ActivityLinkInput{
			EntityType: l.EntityType, EntityID: l.EntityID,
		})
	}
	meeting, err := c.store.BookMeeting(ctx, booked)
	if err != nil {
		return nil, err
	}
	return json.Marshal(meeting)
}

// defaultHost resolves the calendar owner: the explicit host, else the
// acting principal's user. An agent principal has no own calendar —
// it must name one (and the store's delegation gate answers).
func defaultHost(ctx context.Context, host *ids.UUID) (ids.UUID, error) {
	if host != nil {
		return *host, nil
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID.IsZero() {
		return ids.Nil, fmt.Errorf("comms: no host named and the principal has no user calendar")
	}
	return actor.UserID, nil
}
