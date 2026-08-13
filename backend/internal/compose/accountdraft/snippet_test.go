// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package accountdraft

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// An account whose every message shares one subject is the case that made the
// gap visible: six rows reading "Re: Welcome to Surfe!" carry no information at
// all, so a draft grounded in subjects alone has nothing to write from and the
// model fills the gap with invention.
//
// The fold reads bodies now. These pin that it does, that the envelope block
// never reaches the prompt, and that a message with no body still folds.
func TestARepeatedSubjectThreadReachesTheModelWithItsWords(t *testing.T) {
	view := viewWithActivities(t,
		activityWith(t, "Re: Welcome to Surfe!",
			"From: marine@surfe.com\nTo: rep@gradion.com\n\nWe are comparing three "+
				"providers this quarter and the deadline is the end of August."),
		activityWith(t, "Re: Welcome to Surfe!",
			"From: rep@gradion.com\nTo: marine@surfe.com\n\nHappy to walk you through "+
				"how the pricing works."),
	)

	recent := foldRecent(view)
	if len(recent) != 2 {
		t.Fatalf("expected both messages to fold, got %d", len(recent))
	}
	if !strings.Contains(recent[0].Snippet, "comparing three providers") {
		t.Fatalf("the message body did not reach the fold: %q", recent[0].Snippet)
	}
	if strings.Contains(recent[0].Snippet, "marine@surfe.com") {
		t.Fatalf("the envelope headers reached the prompt: %q", recent[0].Snippet)
	}

	payload, err := json.Marshal(Input{Recent: recent})
	if err != nil {
		t.Fatalf("encoding the account input failed: %v", err)
	}
	if !strings.Contains(string(payload), "comparing three providers") {
		t.Fatalf("the snippet did not reach the payload the prompt carries:\n%s", payload)
	}
}

// A subject-only activity is ordinary — a call or a meeting has no body — and
// must fold to an entry with an empty snippet rather than dropping out.
func TestAnActivityWithNoBodyStillFolds(t *testing.T) {
	view := viewWithActivities(t, activityWith(t, "Intro call", ""))

	recent := foldRecent(view)
	if len(recent) != 1 {
		t.Fatalf("expected the activity to fold, got %d", len(recent))
	}
	if recent[0].Snippet != "" {
		t.Fatalf("expected no snippet, got %q", recent[0].Snippet)
	}
	if recent[0].Subject != "Intro call" {
		t.Fatalf("the subject was lost: %q", recent[0].Subject)
	}
}

// A body that is only the envelope and a quoted thread carries no words of its
// own. Empty is the honest answer: the addresses must not reach the prompt as
// if they were something the account said.
func TestAQuotedOnlyBodyContributesNothing(t *testing.T) {
	view := viewWithActivities(t, activityWith(t, "Re: Welcome to Surfe!",
		"From: marine@surfe.com\nTo: rep@gradion.com\n\n> the whole of the previous message\n> quoted back"))

	recent := foldRecent(view)
	if len(recent) != 1 {
		t.Fatalf("expected the activity to fold, got %d", len(recent))
	}
	if recent[0].Snippet != "" {
		t.Fatalf("a quoted-only body produced a snippet: %q", recent[0].Snippet)
	}
}

func activityWith(t *testing.T, subject, body string) crmcontracts.Activity {
	t.Helper()
	act := crmcontracts.Activity{
		Id:         openapi_types.UUID(ids.NewV7()),
		Kind:       crmcontracts.ActivityKindEmail,
		OccurredAt: time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC),
		Subject:    &subject,
	}
	if body != "" {
		act.Body = &body
	}
	return act
}

func viewWithActivities(t *testing.T, acts ...crmcontracts.Activity) crmcontracts.Organization360 {
	t.Helper()
	return crmcontracts.Organization360{
		Activities: &crmcontracts.ActivityListResponse{Data: acts},
	}
}
