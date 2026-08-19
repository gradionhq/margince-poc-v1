// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The lead first-response SLA (formulas §18): the clock, the three first-
// response triggers, the derived state, and the at-most-once breach scan.
// The scan's SKIP LOCKED contract and the sla_state filter's SQL are only
// real against Postgres.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedLeadCreatedAt seeds a working lead whose clock started at createdAt.
func (e *promoteConsentEnv) seedLeadCreatedAt(t *testing.T, email string, createdAt time.Time) ids.LeadID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO lead (id, full_name, email, status, source, captured_by, owner_id, created_at)
		 VALUES ($1, 'Lena Lead', lower($2), 'new', 'inbound', 'human:x', $3, $4)`,
		id, email, e.user, createdAt); err != nil {
		t.Fatal(err)
	}
	return ids.From[ids.LeadKind](id)
}

func (e *promoteConsentEnv) countEvents(t *testing.T, eventType string, entity ids.UUID) int {
	t.Helper()
	var n int
	if err := e.store.tx(context.Background(), func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM event_outbox
			  WHERE envelope->>'type' = $1 AND envelope->'entity'->>'id' = $2`, eventType, entity.String()).Scan(&n)
	}); err != nil {
		t.Fatalf("count %s events: %v", eventType, err)
	}
	return n
}

// The scan marks a breached lead exactly once: a second pass finds nothing,
// and a lead answered before its deadline is never a breach.
func TestSLAScanEscalatesEachBreachOnce(t *testing.T) {
	e := setupPromoteConsent(t)
	now := time.Now().UTC()
	overdue := e.seedLeadCreatedAt(t, "overdue@example.test", now.Add(-FirstResponseTarget-time.Hour))
	fresh := e.seedLeadCreatedAt(t, "fresh@example.test", now.Add(-time.Hour))
	answered := e.seedLeadCreatedAt(t, "answered@example.test", now.Add(-FirstResponseTarget-time.Hour))
	if _, err := e.store.RecordLeadFirstResponse(e.ctx, answered, now.Add(-FirstResponseTarget-30*time.Minute)); err != nil {
		t.Fatalf("record first response: %v", err)
	}

	breaches, err := e.store.ScanLeadSLA(e.ctx, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(breaches) != 1 || breaches[0].LeadID != overdue {
		t.Fatalf("breaches = %+v, want exactly the overdue lead %s", breaches, overdue)
	}
	if breaches[0].OwnerID == nil || *breaches[0].OwnerID != ids.From[ids.UserKind](e.user) {
		t.Errorf("breach owner = %v, want the lead's owner — the escalation target", breaches[0].OwnerID)
	}
	if n := e.countEvents(t, "lead.sla_breached", overdue.UUID); n != 1 {
		t.Errorf("lead.sla_breached events = %d, want 1", n)
	}

	again, err := e.store.ScanLeadSLA(e.ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second scan re-escalated %+v; a breach is at most once per occurrence", again)
	}
	if n := e.countEvents(t, "lead.sla_breached", fresh.UUID); n != 0 {
		t.Errorf("a lead inside its target was escalated")
	}
}

// The derived state and the list filter agree: an overdue lead reads
// breached on its own row AND is what sla_state=breached lists.
func TestSLAStateReadsAndFiltersAlike(t *testing.T) {
	e := setupPromoteConsent(t)
	now := time.Now().UTC()
	overdue := e.seedLeadCreatedAt(t, "overdue@example.test", now.Add(-FirstResponseTarget-time.Hour))
	e.seedLeadCreatedAt(t, "fresh@example.test", now.Add(-time.Hour))

	lead, err := e.store.GetLead(e.ctx, overdue, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if lead.SlaState == nil || *lead.SlaState != crmcontracts.LeadSlaStateBreached || lead.SlaDeadlineAt == nil {
		t.Errorf("overdue lead sla_state=%v deadline=%v, want breached with a deadline", lead.SlaState, lead.SlaDeadlineAt)
	}
	breached := crmcontracts.ListLeadsParamsSlaState(crmcontracts.LeadSlaStateBreached)
	page, _, err := e.store.ListLeads(e.ctx, ListLeadsInput{SLAState: &breached})
	if err != nil {
		t.Fatalf("list breached: %v", err)
	}
	if len(page) != 1 || page[0].Id != lead.Id {
		t.Errorf("sla_state=breached lists %d leads, want only %s", len(page), overdue)
	}
	within := crmcontracts.ListLeadsParamsSlaState(crmcontracts.LeadSlaStateWithinTarget)
	page, _, err = e.store.ListLeads(e.ctx, ListLeadsInput{SLAState: &within})
	if err != nil {
		t.Fatalf("list within: %v", err)
	}
	if len(page) != 1 || page[0].Id == lead.Id {
		t.Errorf("sla_state=within_target lists %d leads incl. the overdue one", len(page))
	}
}

func TestDefaultLeadQueueOrdersSLAThenScoreDeterministically(t *testing.T) {
	e := setupPromoteConsent(t)
	now := time.Now().UTC().Truncate(time.Second)
	previousClock := leadSLAClock
	leadSLAClock = func() time.Time { return now }
	t.Cleanup(func() { leadSLAClock = previousClock })

	breachedLow := e.seedLeadCreatedAt(t, "breached-low@example.test", now.Add(-FirstResponseTarget-time.Hour))
	breachedHigh := e.seedLeadCreatedAt(t, "breached-high@example.test", now.Add(-FirstResponseTarget-2*time.Hour))
	atRisk := e.seedLeadCreatedAt(t, "at-risk@example.test", now.Add(-FirstResponseTarget+30*time.Minute))
	within := e.seedLeadCreatedAt(t, "within@example.test", now.Add(-time.Hour))
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE lead SET score = CASE id WHEN $1 THEN 10 WHEN $2 THEN 80 WHEN $3 THEN 100 ELSE 100 END
		  WHERE id IN ($1, $2, $3, $4)`, breachedLow, breachedHigh, atRisk, within); err != nil {
		t.Fatalf("setting queue scores: %v", err)
	}

	limit := 2
	rows, page, err := e.store.ListLeads(e.ctx, ListLeadsInput{Limit: &limit})
	if err != nil {
		t.Fatalf("list default queue: %v", err)
	}
	if !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first queue page = %+v, want a continuation cursor", page)
	}
	cursor := page.NextCursor
	// The queue is one snapshot across pages. Without the as-of time in the
	// cursor, atRisk crosses into breached here, moves ahead of the cursor and
	// disappears from the result set.
	leadSLAClock = func() time.Time { return now.Add(2 * time.Hour) }
	next, lastPage, err := e.store.ListLeads(e.ctx, ListLeadsInput{Limit: &limit, Cursor: &cursor})
	if err != nil {
		t.Fatalf("list second queue page: %v", err)
	}
	if lastPage.HasMore {
		t.Fatalf("second queue page = %+v, want the final page", lastPage)
	}
	rows = append(rows, next...)
	want := []ids.LeadID{breachedHigh, breachedLow, atRisk, within}
	if len(rows) != len(want) {
		t.Fatalf("queue rows = %d, want %d", len(rows), len(want))
	}
	for i, id := range want {
		if ids.UUID(rows[i].Id) != id.UUID {
			t.Errorf("queue[%d] = %s, want %s", i, rows[i].Id, id)
		}
	}
}

func TestLeadQuickFindIncludesExactEmailAndLinkedIn(t *testing.T) {
	e := setupPromoteConsent(t)
	now := time.Now().UTC()
	emailLead := e.seedLeadCreatedAt(t, "find-me@example.test", now)
	linkedInLead := e.seedLeadCreatedAt(t, "other@example.test", now)
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE lead SET linkedin_url = 'https://www.linkedin.com/in/find-me'
		  WHERE id = $1`, linkedInLead); err != nil {
		t.Fatalf("setting LinkedIn URL: %v", err)
	}

	for query, want := range map[string]ids.LeadID{
		"find-me@example.test":                 emailLead,
		"https://www.linkedin.com/in/find-me/": linkedInLead,
	} {
		rows, _, err := e.store.ListLeads(e.ctx, ListLeadsInput{Query: &query})
		if err != nil {
			t.Fatalf("quick-find %q: %v", query, err)
		}
		if len(rows) != 1 || ids.UUID(rows[0].Id) != want.UUID {
			t.Errorf("quick-find %q = %+v, want %s", query, rows, want)
		}
	}
}

// A human moving the lead off `new` is a first response; the stamp is set
// once and a later status change does not move it. Disqualifying an
// unanswered lead is an explicit disposition and stamps it too.
func TestFirstResponseFromHumanStatusChangeAndDisposition(t *testing.T) {
	e := setupPromoteConsent(t)
	now := time.Now().UTC()
	worked := e.seedLeadCreatedAt(t, "worked@example.test", now.Add(-time.Hour))
	status := "working"
	after, err := e.store.UpdateLead(e.ctx, worked, UpdateLeadInput{Status: &status})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if after.FirstResponseAt == nil {
		t.Fatal("a human status change off new must stamp first_response_at")
	}
	if after.SlaState != nil {
		t.Errorf("an answered lead reads sla_state=%v, want null — it owes nothing", *after.SlaState)
	}
	stamped := *after.FirstResponseAt
	back := "new"
	if _, err := e.store.UpdateLead(e.ctx, worked, UpdateLeadInput{Status: &back}); err != nil {
		t.Fatalf("update back: %v", err)
	}
	if _, err := e.store.UpdateLead(e.ctx, worked, UpdateLeadInput{Status: &status}); err != nil {
		t.Fatalf("update again: %v", err)
	}
	again, err := e.store.GetLead(e.ctx, worked, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if again.FirstResponseAt == nil || !again.FirstResponseAt.Equal(stamped) {
		t.Errorf("first_response_at moved from %v to %v; the first stamp is the one that counts", stamped, again.FirstResponseAt)
	}

	dropped := e.seedLeadCreatedAt(t, "dropped@example.test", now.Add(-time.Hour))
	closed, err := e.store.DisqualifyLead(e.ctx, dropped, DisqualifyLeadInput{})
	if err != nil {
		t.Fatalf("disqualify: %v", err)
	}
	if closed.FirstResponseAt == nil {
		t.Error("disqualifying is an explicit disposition and must stamp first_response_at")
	}
}

// The bus is unordered: a reply that happened at 09:00 may be processed
// after one from 10:00. The FIRST response is the earliest, so the later
// delivery moves the stamp back, and a later reply never moves it forward.
func TestFirstResponseKeepsTheEarliestReplyWhateverTheDeliveryOrder(t *testing.T) {
	e := setupPromoteConsent(t)
	// Truncated to what timestamptz actually stores. Postgres keeps
	// microseconds and Go carries nanoseconds, so an untruncated stamp comes
	// back a few hundred nanoseconds short and Equal fails — on roughly 999
	// runs in 1000, whenever time.Now() does not happen to land on a
	// microsecond boundary. The test is about which reply WINS, not about
	// clock resolution.
	now := time.Now().UTC().Truncate(time.Microsecond)
	lead := e.seedLeadCreatedAt(t, "ordered@example.test", now.Add(-3*time.Hour))
	late := now.Add(-time.Hour)
	early := now.Add(-2 * time.Hour)
	if set, err := e.store.RecordLeadFirstResponse(e.ctx, lead, late); err != nil || !set {
		t.Fatalf("first delivery: set=%t err=%v", set, err)
	}
	if set, err := e.store.RecordLeadFirstResponse(e.ctx, lead, early); err != nil || !set {
		t.Fatalf("earlier reply delivered second: set=%t err=%v — it must win", set, err)
	}
	if set, err := e.store.RecordLeadFirstResponse(e.ctx, lead, now); err != nil || set {
		t.Fatalf("a later reply: set=%t err=%v — it must not move the stamp", set, err)
	}
	got, err := e.store.GetLead(e.ctx, lead, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if got.FirstResponseAt == nil || !got.FirstResponseAt.Equal(early) {
		t.Errorf("first_response_at = %v, want the earliest reply %v", got.FirstResponseAt, early)
	}
}
