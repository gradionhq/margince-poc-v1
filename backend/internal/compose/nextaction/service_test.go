// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package nextaction

// The rules, as a table: which facts pick which verb, in priority order, and
// what the arguments name. Pure — no pool, no clock beyond the one injected.

import (
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

var now = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

func activity(kind crmcontracts.ActivityKind, at time.Time) crmcontracts.Activity {
	subject := "about " + string(kind)
	return crmcontracts.Activity{Id: openapi_types.UUID(ids.NewV7()), Kind: kind, OccurredAt: at, Subject: &subject}
}

func inbound(at time.Time) crmcontracts.Activity {
	a := activity(crmcontracts.ActivityKindEmail, at)
	d := crmcontracts.ActivityDirectionInbound
	a.Direction = &d
	return a
}

func outbound(at time.Time) crmcontracts.Activity {
	a := activity(crmcontracts.ActivityKindEmail, at)
	d := crmcontracts.ActivityDirectionOutbound
	a.Direction = &d
	return a
}

func withStatus(a crmcontracts.Activity, st crmcontracts.ActivityMeetingStatus) crmcontracts.Activity {
	a.MeetingStatus = &st
	return a
}

func inboundCall(at time.Time) crmcontracts.Activity {
	a := activity(crmcontracts.ActivityKindCall, at)
	d := crmcontracts.ActivityDirectionInbound
	a.Direction = &d
	return a
}

func booked(at time.Time) crmcontracts.Activity {
	a := activity(crmcontracts.ActivityKindMeeting, at)
	st := crmcontracts.ActivityMeetingStatusBooked
	a.MeetingStatus = &st
	return a
}

func TestTheRulesPickOneVerbInPriorityOrder(t *testing.T) {
	deal := crmcontracts.Deal{Id: openapi_types.UUID(ids.NewV7()), Name: "Acme rollout"}
	cases := []struct {
		name     string
		timeline []crmcontracts.Activity
		tasks    []activities.OpenTask
		want     string
		argument string
	}{
		{"a meeting inside the horizon wins over everything", []crmcontracts.Activity{inbound(now.Add(-time.Hour)), booked(now.Add(24 * time.Hour))}, []activities.OpenTask{{Subject: "x"}}, ActionOpenMeetingBrief, "activity_id"},
		{"a meeting beyond the horizon does not count", []crmcontracts.Activity{booked(now.Add(10 * 24 * time.Hour))}, nil, ActionCreateTask, "subject"},
		{"a meeting with no status is booked, as the record pages read it", []crmcontracts.Activity{activity(crmcontracts.ActivityKindMeeting, now.Add(time.Hour))}, nil, ActionOpenMeetingBrief, "activity_id"},
		{"a canceled meeting is not a next step", []crmcontracts.Activity{withStatus(activity(crmcontracts.ActivityKindMeeting, now.Add(time.Hour)), crmcontracts.ActivityMeetingStatusCanceled)}, nil, ActionCreateTask, "subject"},
		{"an inbound call is not answered by mail", []crmcontracts.Activity{inboundCall(now.Add(-time.Hour))}, nil, ActionCreateTask, "subject"},
		{"a meeting ten days out does not pass for last contact", []crmcontracts.Activity{booked(now.Add(10 * 24 * time.Hour)), outbound(now.Add(-5 * 24 * time.Hour))}, nil, ActionCreateTask, "subject"},
		{"an unanswered inbound mail asks for a reply", []crmcontracts.Activity{inbound(now.Add(-2 * time.Hour)), outbound(now.Add(-3 * 24 * time.Hour))}, nil, ActionDraftEmail, "activity_id"},
		{"an answered inbound mail does not", []crmcontracts.Activity{outbound(now.Add(-time.Hour)), inbound(now.Add(-2 * time.Hour))}, nil, ActionCreateTask, "subject"},
		{"an open task means the next step is already named", []crmcontracts.Activity{outbound(now.Add(-time.Hour))}, []activities.OpenTask{{ID: ids.NewV7(), Subject: "Send the redline"}}, ActionNone, ""},
		{"nothing at all: decide the first step", nil, nil, ActionCreateTask, "subject"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decide(facts{deal: deal, timeline: tc.timeline, openTasks: tc.tasks, now: now})
			if got.Action != tc.want {
				t.Fatalf("action = %s (%s), want %s", got.Action, got.Reason, tc.want)
			}
			if got.Reason == "" {
				t.Fatal("every answer carries a reason")
			}
			if tc.argument == "" {
				if got.Arguments != nil {
					t.Fatalf("none carries no arguments, got %v", *got.Arguments)
				}
				return
			}
			if got.Arguments == nil || (*got.Arguments)[tc.argument] == nil {
				t.Fatalf("arguments = %v, want %q", got.Arguments, tc.argument)
			}
			if len(got.Evidence) == 0 && hasPast(tc.timeline) {
				t.Fatal("an answer from a timeline with past rows names its evidence")
			}
		})
	}
}

func hasPast(timeline []crmcontracts.Activity) bool {
	for _, a := range timeline {
		if !a.OccurredAt.After(now) {
			return true
		}
	}
	return false
}

func withheldRow(a crmcontracts.Activity) crmcontracts.Activity {
	st := crmcontracts.ActivityContentStateWithheld
	a.ContentState = &st
	a.Subject = nil
	return a
}

func TestAWithheldRowIsNeverNamedAsTheOperand(t *testing.T) {
	deal := crmcontracts.Deal{Id: openapi_types.UUID(ids.NewV7()), Name: "Acme rollout"}
	got := decide(facts{deal: deal, timeline: []crmcontracts.Activity{
		withheldRow(booked(now.Add(2 * time.Hour))),
		withheldRow(inbound(now.Add(-time.Hour))),
	}, now: now})
	if got.Action != ActionCreateTask {
		t.Fatalf("a withheld meeting and mail must fall through, got %s", got.Action)
	}
	if len(got.Evidence) != 1 || got.Evidence[0].Text != "Last activity: email" {
		t.Fatalf("the withheld mail still counts as contact, by kind only: %v", got.Evidence)
	}
}

func TestACreateTaskArgumentIsAReadyTaskBody(t *testing.T) {
	deal := crmcontracts.Deal{Id: openapi_types.UUID(ids.NewV7()), Name: "Acme rollout"}
	got := decide(facts{deal: deal, now: now})
	args := *got.Arguments
	if args["subject"] != "Agree the next step on Acme rollout" || args["source"] != "ui" {
		t.Fatalf("arguments = %v", args)
	}
	links, _ := args["links"].([]map[string]any)
	if len(links) != 1 || links[0]["entity_type"] != "deal" || links[0]["entity_id"] != deal.Id {
		t.Fatalf("links = %v, want the deal", args["links"])
	}
}

func TestLastContactIgnoresWhatIsOnlyScheduled(t *testing.T) {
	deal := crmcontracts.Deal{Id: openapi_types.UUID(ids.NewV7()), Name: "Acme rollout"}
	got := decide(facts{deal: deal, timeline: []crmcontracts.Activity{
		booked(now.Add(10 * 24 * time.Hour)),
		outbound(now.Add(-5 * 24 * time.Hour)),
	}, now: now})
	if got.Reason != "Last contact was 5 days ago and nothing is planned — put the next step on the list." {
		t.Fatalf("reason = %q", got.Reason)
	}
	if spanWords(90*time.Minute) != "an hour" || spanWords(30*time.Hour) != "a day" || spanWords(-time.Hour) != "under an hour" {
		t.Fatalf("span words: %q %q %q", spanWords(90*time.Minute), spanWords(30*time.Hour), spanWords(-time.Hour))
	}
}
