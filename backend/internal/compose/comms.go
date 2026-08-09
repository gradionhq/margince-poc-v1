// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The Comms seam: the MCP communication verbs delegate to the SAME
// activities store methods the HTTP transport uses (drafting included)
// — two transports, one send path, one consent gate.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// sendAccepted is what a send answers with: the delivery path took it. It is
// not a claim about arrival, and it is one spelling so the two send tools
// cannot report the same outcome differently.
const sendAccepted = "accepted"

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
}

var _ agents.Comms = commsAdapter{}

// commsAdapter also structurally satisfies automation.Comms (the same
// DraftEmail signature, seams.go) — the deterministic draft_email
// workflow action reuses this ONE adapter rather than wrapping it a
// second time.
var _ automation.Comms = commsAdapter{}

func (c commsAdapter) DraftEmail(ctx context.Context, anchor ids.UUID, intent string) (string, string, error) {
	if c.draft != nil {
		return c.draft.DraftEmail(ctx, anchor, intent)
	}
	activity, err := c.store.GetActivity(ctx, ids.From[ids.ActivityKind](anchor), storekit.LiveOnly)
	if err != nil {
		return "", "", err
	}
	topic := ""
	if activity.Subject != nil {
		topic = *activity.Subject
	}
	subject, body := activities.DeterministicEmailDraft(topic, intent)
	return subject, body, nil
}

// SendEmail carries no DraftRef, and that absence is a statement: a voice
// outcome is the OWNER's judgment of the machine's draft (ADR-0066 §4), so an
// agent's send resolves none — the recorder refuses a non-human principal
// anyway, and naming a reference here would only make that refusal look like
// an accident of wiring.
func (c commsAdapter) SendEmail(ctx context.Context, anchor ids.UUID, in agents.SendEmailArgs) (agents.SendEmailResult, error) {
	sent, err := c.store.SendEmail(ctx, activities.FromActivity(ids.From[ids.ActivityKind](anchor)), activities.SendEmailInput{
		Recipients:     append(append([]string{}, in.To...), in.Cc...),
		Cc:             append([]string{}, in.Cc...),
		Subject:        in.Subject,
		Body:           in.Body,
		ConsentPurpose: in.ConsentPurpose,
	}, c.gate, c.stager)
	if err != nil {
		return agents.SendEmailResult{}, err
	}
	return agents.SendEmailResult{ActivityID: ids.UUID(sent.Id), Status: sendAccepted}, nil
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
// store's own SendMessage refuses on — so StageInfo's pre-check and Handle's
// eventual refusal can never drift onto two different answers for the same kind.
func (c commsAdapter) IsChannelKind(kind string) bool { return activities.IsChannelKind(kind) }

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
