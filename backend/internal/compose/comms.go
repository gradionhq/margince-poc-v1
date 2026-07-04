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
	activity, err := c.store.GetActivity(ctx, anchor, false)
	if err != nil {
		return "", "", err
	}
	subject := "Re: follow-up"
	if activity.Subject != nil && *activity.Subject != "" {
		subject = "Re: " + *activity.Subject
	}
	var body strings.Builder
	body.WriteString("Hi,\n\nfollowing up on ")
	if activity.Subject != nil && *activity.Subject != "" {
		fmt.Fprintf(&body, "%q", *activity.Subject)
	} else {
		body.WriteString("our last conversation")
	}
	body.WriteString(".")
	if strings.TrimSpace(intent) != "" {
		body.WriteString("\n\n" + strings.TrimSpace(intent))
	}
	body.WriteString("\n\nBest regards")
	return subject, body.String(), nil
}

func (c commsAdapter) SendEmail(ctx context.Context, anchor ids.UUID, in agents.SendEmailArgs) (json.RawMessage, error) {
	sent, err := c.store.SendEmail(ctx, anchor, activities.SendEmailInput{
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
	duration := 30 * time.Minute
	if durationMinutes > 0 {
		duration = time.Duration(durationMinutes) * time.Minute
	}
	slots, err := c.store.Availability(ctx, hostID, from, to, duration)
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
		Host: hostID, Start: in.Start, End: in.End, Subject: in.Subject,
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
