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
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type commsAdapter struct {
	store *activities.Store
	gate  activities.ConsentGate
}

var _ agents.Comms = commsAdapter{}

func (c commsAdapter) DraftEmail(ctx context.Context, anchor ids.UUID, intent string) (string, string, error) {
	// The deterministic draft over the anchor's own context — the same
	// rule the HTTP handler applies; the model-backed voice draft rides
	// the router once the drafting lane is wired.
	activity, err := c.store.GetActivity(ctx, ids.From[ids.ActivityKind](anchor), storekit.LiveOnly)
	if err != nil {
		return "", "", err
	}
	topic := ""
	if activity.Subject != nil {
		topic = *activity.Subject
	}
	subject, body := deterministicDraft(topic, intent)
	return subject, body, nil
}

// deterministicDraft is the ONE spelling of the deterministic follow-up
// voice: draft_email (reply-anchored) and draft_follow_ups_for
// (deal-anchored) both compose over it, so the two draft paths cannot
// drift. The model-backed Voice-DNA draft replaces the body here once
// the drafting lane rides the model router.
func deterministicDraft(topic, intent string) (subject, body string) {
	subject = "Re: follow-up"
	if topic != "" {
		subject = "Re: " + topic
	}
	var b strings.Builder
	b.WriteString("Hi,\n\nfollowing up on ")
	if topic != "" {
		fmt.Fprintf(&b, "%q", topic)
	} else {
		b.WriteString("our last conversation")
	}
	b.WriteString(".")
	if strings.TrimSpace(intent) != "" {
		b.WriteString("\n\n" + strings.TrimSpace(intent))
	}
	b.WriteString("\n\nBest regards")
	return subject, b.String()
}

func (c commsAdapter) SendEmail(ctx context.Context, anchor ids.UUID, in agents.SendEmailArgs) (json.RawMessage, error) {
	sent, err := c.store.SendEmail(ctx, ids.From[ids.ActivityKind](anchor), activities.SendEmailInput{
		Recipients:     append(append([]string{}, in.To...), in.Cc...),
		Subject:        in.Subject,
		Body:           in.Body,
		ConsentPurpose: in.ConsentPurpose,
	}, c.gate)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"activity_id": sent.Id, "status": "accepted"})
}

func (c commsAdapter) Availability(ctx context.Context, host *ids.UUID, from, to time.Time, durationMinutes int) (json.RawMessage, error) {
	hostID, err := defaultHost(ctx, host)
	if err != nil {
		return nil, err
	}
	// The store applies its default slot duration when none is named.
	slots, err := c.store.Availability(ctx, ids.From[ids.UserKind](hostID), from, to, time.Duration(durationMinutes)*time.Minute)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"slots": slots})
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
