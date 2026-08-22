// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package nextaction

import (
	"context"
	"fmt"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The four answers. Plain strings on the wire, for the reason the contract
// gives; named here so the rules and the card cannot drift on a spelling.
const (
	ActionDraftEmail       = "draft_email"
	ActionCreateTask       = "create_task"
	ActionOpenMeetingBrief = "open_meeting_brief"
	ActionNone             = "none"
)

// How far ahead a booked meeting counts as "next": inside this window the
// brief is the thing to open; beyond it the deal still needs a move today.
const meetingHorizon = 72 * time.Hour

// How many timeline rows the rules read. The timeline is ordered by
// occurred_at DESC, which is when a thing happened OR is scheduled — a booked
// meeting sits at the top with a future time — so the window holds the nearest
// scheduled rows and then the most recent past ones. The rules look at the
// past rows for "last contact" and "unanswered", and an older inbound mail
// pushed out of the window by newer traffic is, by that traffic, answered.
const timelineWindow = 25

// Service assembles the recommendation from the two modules it reads.
type Service struct {
	deals      *deals.Store
	activities *activities.Store
	now        func() time.Time
}

// NewService binds the reads. Both stores carry their own gates, so a deal
// the caller cannot see is absent here exactly as it is on its own routes.
func NewService(d *deals.Store, a *activities.Store, now func() time.Time) *Service {
	return &Service{deals: d, activities: a, now: now}
}

// facts are the inputs the rules fold. Gathered once, so the rules are a pure
// function a test can drive without a database.
type facts struct {
	deal      crmcontracts.Deal
	timeline  []crmcontracts.Activity
	openTasks []activities.OpenTask
	now       time.Time
}

// Get computes the recommendation for one deal.
func (s *Service) Get(ctx context.Context, dealID ids.DealID) (crmcontracts.DealNextBestAction, error) {
	deal, err := s.deals.GetDeal(ctx, dealID, storekit.LiveOnly)
	if err != nil {
		return crmcontracts.DealNextBestAction{}, err
	}
	entityType := "deal"
	limit := timelineWindow
	timeline, _, err := s.activities.ListActivities(ctx, activities.ListActivitiesInput{
		EntityType: &entityType, EntityID: &dealID.UUID, Limit: &limit,
	})
	if err != nil {
		return crmcontracts.DealNextBestAction{}, fmt.Errorf("next action: reading the deal's timeline: %w", err)
	}
	open, _, err := s.activities.ListOpenTasks(ctx, activities.ListOpenTasksInput{
		EntityType: &entityType, EntityID: &dealID.UUID, Limit: timelineWindow,
	})
	if err != nil {
		return crmcontracts.DealNextBestAction{}, fmt.Errorf("next action: reading the deal's open tasks: %w", err)
	}
	return decide(facts{deal: deal, timeline: timeline, openTasks: open, now: s.now().UTC()}), nil
}

// decide is the rule set, in priority order. The first rule whose facts hold
// wins; each names the evidence that made it hold.
func decide(f facts) crmcontracts.DealNextBestAction {
	out := crmcontracts.DealNextBestAction{
		DealId:     f.deal.Id,
		ComputedAt: f.now,
		Evidence:   []crmcontracts.DealNextBestActionEvidence{},
	}
	if meeting, ok := upcomingMeeting(f); ok {
		return withAction(out, ActionOpenMeetingBrief,
			fmt.Sprintf("A meeting is booked %s — read the brief before it.", until(f.now, meeting.OccurredAt)),
			map[string]any{"activity_id": meeting.Id},
			evidenceOf(meeting, "Booked: "+subjectOf(meeting)))
	}
	if inbound, ok := unansweredInbound(f); ok {
		return withAction(out, ActionDraftEmail,
			fmt.Sprintf("They wrote %s and nobody has answered — draft the reply.", since(f.now, inbound.OccurredAt)),
			map[string]any{"activity_id": inbound.Id},
			evidenceOf(inbound, "Unanswered: "+subjectOf(inbound)))
	}
	if len(f.openTasks) > 0 {
		task := f.openTasks[0]
		out.Action = ActionNone
		out.Reason = fmt.Sprintf("An open task already says what is next: %q.", task.Subject)
		id := openapi_types.UUID(task.ID)
		out.Evidence = append(out.Evidence, crmcontracts.DealNextBestActionEvidence{Text: "Open task: " + task.Subject, ActivityId: &id})
		return out
	}
	return withAction(out, ActionCreateTask,
		nextStepReason(f),
		map[string]any{
			"subject": "Agree the next step on " + f.deal.Name,
			"links":   []map[string]any{{"entity_type": "deal", "entity_id": f.deal.Id}},
			"source":  "ui",
		},
		lastContactEvidence(f)...)
}

func withAction(out crmcontracts.DealNextBestAction, action, reason string, args map[string]any, evidence ...crmcontracts.DealNextBestActionEvidence) crmcontracts.DealNextBestAction {
	out.Action = action
	out.Reason = reason
	out.Arguments = &args
	out.Evidence = append(out.Evidence, evidence...)
	return out
}

// upcomingMeeting is the soonest booked meeting inside the horizon. A meeting
// with no status is booked — the predicate person360 and org360's next-meeting
// reads spell (`meeting_status IS NULL OR = 'booked'`), so the card and the
// record pages agree about which meeting is next.
func upcomingMeeting(f facts) (crmcontracts.Activity, bool) {
	var best crmcontracts.Activity
	found := false
	for _, a := range f.timeline {
		if a.Kind != crmcontracts.ActivityKindMeeting || withheld(a) {
			continue
		}
		if a.MeetingStatus != nil && *a.MeetingStatus != crmcontracts.ActivityMeetingStatusBooked {
			continue
		}
		if a.OccurredAt.Before(f.now) || a.OccurredAt.After(f.now.Add(meetingHorizon)) {
			continue
		}
		if !found || a.OccurredAt.Before(best.OccurredAt) {
			best, found = a, true
		}
	}
	return best, found
}

// unansweredInbound is the latest inbound MAIL with no outbound mail after it.
// Mail only: the verb this arm names drafts a threaded reply, which the draft
// path composes only for an inbound email — an inbound call has no thread to
// answer on, and falls through to the next rule. Among the past rows, the
// first directional mail decides.
func unansweredInbound(f facts) (crmcontracts.Activity, bool) {
	for _, a := range f.timeline {
		if a.Kind != crmcontracts.ActivityKindEmail || a.Direction == nil || a.OccurredAt.After(f.now) || withheld(a) {
			continue
		}
		if *a.Direction == crmcontracts.ActivityDirectionInbound {
			return a, true
		}
		return crmcontracts.Activity{}, false
	}
	return crmcontracts.Activity{}, false
}

// lastContact is the newest row that has already happened. The timeline's
// first rows can be scheduled meetings with future times, which are plans and
// not contact.
func lastContact(f facts) (crmcontracts.Activity, bool) {
	for _, a := range f.timeline {
		if !a.OccurredAt.After(f.now) {
			return a, true
		}
	}
	return crmcontracts.Activity{}, false
}

// withheld says the caller may know the row exists but not read it. Such a
// row is never NAMED as an operand: the verb the answer would point at gates
// on content, so the button would 404. It still counts as contact having
// happened, which is all the next-step rule needs from it.
func withheld(a crmcontracts.Activity) bool {
	return a.ContentState != nil && *a.ContentState == crmcontracts.ActivityContentStateWithheld
}

func nextStepReason(f facts) string {
	last, ok := lastContact(f)
	if !ok {
		return "Nothing has happened on this deal yet — decide the first step."
	}
	return fmt.Sprintf("Last contact was %s and nothing is planned — put the next step on the list.", since(f.now, last.OccurredAt))
}

func lastContactEvidence(f facts) []crmcontracts.DealNextBestActionEvidence {
	last, ok := lastContact(f)
	if !ok {
		return nil
	}
	return []crmcontracts.DealNextBestActionEvidence{evidenceOf(last, "Last activity: "+subjectOf(last))}
}

func evidenceOf(a crmcontracts.Activity, text string) crmcontracts.DealNextBestActionEvidence {
	id := a.Id
	at := a.OccurredAt
	return crmcontracts.DealNextBestActionEvidence{Text: text, ActivityId: &id, OccurredAt: &at}
}

func subjectOf(a crmcontracts.Activity) string {
	if a.Subject != nil && *a.Subject != "" {
		return *a.Subject
	}
	return string(a.Kind)
}

// since and until render an age the way a person says it. Days past a week
// are still days: "12 days ago" is more honest on a deal than "last week".
func since(now, at time.Time) string {
	return spanWords(now.Sub(at)) + " ago"
}

func until(now, at time.Time) string {
	return "in " + spanWords(at.Sub(now))
}

func spanWords(d time.Duration) string {
	if d < 0 {
		// A caller handed a future time to "since" or a past one to "until".
		// Rendering it as a small positive span would state a falsehood;
		// rendering nothing keeps the sentence honest about what it knows.
		d = 0
	}
	switch {
	case d < time.Hour:
		return "under an hour"
	case d < 2*time.Hour:
		return "an hour"
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d < 48*time.Hour:
		return "a day"
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}
