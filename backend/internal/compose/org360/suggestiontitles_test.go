// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

import (
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A suggestion's title and date are for the READER; its fingerprint is for the
// dismissal (PO-AC-N-13..14, ADR-0095/A146).
//
// The two must never meet. A dismissal is matched by fingerprint, so folding a
// title or a date into it would resurrect every suggestion every reader has
// ever dismissed — silently, and for everyone at once. That is the constraint
// that makes this change safe, and it is the one worth a test of its own.

func TestTheFingerprintIgnoresTheTitleAndTheDate(t *testing.T) {
	evidence := []crmcontracts.OrganizationBriefEvidence{{
		EntityType: crmcontracts.OrganizationBriefEvidenceEntityTypeActivity,
		EntityId:   openapi_types.UUID(ids.NewV7()),
	}}
	// The fingerprint's whole input, spelled here so a change to its signature
	// has to be reckoned with rather than absorbed.
	before := fingerprint("no_reply", "org-1", evidence)

	// Whatever a title or a date says, the situation is the same situation.
	after := fingerprint("no_reply", "org-1", evidence)
	if before != after {
		t.Fatalf("the fingerprint is not stable over identical inputs: %q vs %q", before, after)
	}

	// And the rule that builds one carries a title and a date WITHOUT them
	// reaching it: the same account, dismissed yesterday, stays dismissed.
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	orgID := ids.OrganizationID{UUID: ids.NewV7()}
	msg := lastMessage{
		ID:        ids.NewV7(),
		At:        now.Add(-30 * 24 * time.Hour),
		Direction: string(crmcontracts.ActivityDirectionOutbound),
	}

	first := staleThread(orgID, now, msg)
	if first == nil || first.Title == nil || first.DueAt == nil {
		t.Fatalf("the no-reply rule produced no title or date: %+v", first)
	}
	// A day later the SENTENCE changes (31 days, not 30) and the date is the
	// same evidence's. The fingerprint must not move: nothing about the account
	// changed, so a dismissal must still hold.
	later := staleThread(orgID, now.Add(24*time.Hour), msg)
	if later == nil {
		t.Fatal("the rule stopped firing on a thread that is quieter than before")
	}
	if *first.Title == *later.Title {
		t.Fatal("the title did not change with the wait — it is not reading the evidence")
	}
	if first.Fingerprint != later.Fingerprint {
		t.Fatalf("the fingerprint MOVED when only the title's wording did:\n  %s\n  %s\nEvery dismissal on this account would resurrect.",
			first.Fingerprint, later.Fingerprint)
	}
}

// A title says what the RULE knows. The mockups draw rows like "Prep expansion
// workshop" — a task the system has no basis for and no way to complete.
func TestATitleSaysWhatTheRuleKnows(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	orgID := ids.OrganizationID{UUID: ids.NewV7()}
	out := staleThread(orgID, now, lastMessage{
		ID:        ids.NewV7(),
		At:        now.Add(-24 * 24 * time.Hour),
		Direction: string(crmcontracts.ActivityDirectionOutbound),
	})
	if out == nil || out.Title == nil {
		t.Fatal("no suggestion or no title")
	}
	if want := "Follow up: no reply in 24 days"; *out.Title != want {
		t.Fatalf("title = %q, want %q", *out.Title, want)
	}
	// The date is the evidence's own — when the thread went quiet — never a
	// deadline the system chose for a rep.
	if out.DueAt == nil || !out.DueAt.Equal(now.Add(-24*24*time.Hour)) {
		t.Fatalf("due_at = %v, want the message's own instant", out.DueAt)
	}
}

// The no-next-step rule fires on an ABSENCE, and an absence has no date.
// Inventing one would turn a reading into an obligation nobody agreed to.
func TestARuleThatFiresOnAnAbsenceCarriesNoDate(t *testing.T) {
	orgID := ids.OrganizationID{UUID: ids.NewV7()}
	out := noNextStepSuggestion(orgID, suggestionInputs{
		open: pipeline{OpenCount: 2, OpenDigest: "digest"},
	})
	if out == nil || out.Title == nil {
		t.Fatal("no suggestion or no title")
	}
	if out.DueAt != nil {
		t.Fatalf("due_at = %v, want none — there is no task, so there is no date", out.DueAt)
	}
}
