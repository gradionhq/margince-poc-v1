// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The visit baseline and the three counts it answers.
//
// The mark is per user and moves only through the explicit acknowledgment,
// monotonically. The counts it drives run against it read-only: a GET that
// advanced the mark would destroy the answer the caller opened the page for.
//
// Every fixture here sets its timestamps explicitly. The read's clock is
// pinned to org360Clock while the database's now() is not, so a fixture on
// now() would land on one side of the baseline by accident.

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/org360"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestOrganizationViewAckIsMonotonicAndPerUser(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep1 := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)

	first, err := svc.Acknowledge(rep1, org)
	if err != nil {
		t.Fatalf("first acknowledgment: %v", err)
	}
	if !first.LastViewedAt.Equal(org360Clock) {
		t.Errorf("last_viewed_at = %v, want the pinned instant %v", first.LastViewedAt, org360Clock)
	}

	// A second tab whose clock lags must not rewind the mark: the upsert
	// keeps the later of the two.
	lagging := org360.NewService(e.Pool, people.NewStore(e.Pool), approvals.NewService(e.Pool),
		func() time.Time { return org360Clock.Add(-time.Hour) })
	second, err := lagging.Acknowledge(rep1, org)
	if err != nil {
		t.Fatalf("lagging acknowledgment: %v", err)
	}
	if !second.LastViewedAt.Equal(org360Clock) {
		t.Errorf("last_viewed_at = %v after a lagging ack, want the earlier %v to be kept",
			second.LastViewedAt, org360Clock)
	}

	// Rep2 shares Rep1's team and can read the same account, but has never
	// visited it: the baseline is per user, not per record.
	rep2 := e.As(e.Rep2, []ids.UUID{e.Team1}, org360RepPerms)
	view, err := svc.Assemble(rep2, org)
	if err != nil {
		t.Fatalf("assemble as the second rep: %v", err)
	}
	if view.SinceLastVisit == nil {
		t.Fatal("since_last_visit absent for a rep holding the activity grant")
	}
	if view.SinceLastVisit.BaselineAt != nil {
		t.Errorf("baseline_at = %v for a rep who never acknowledged this account, want null",
			view.SinceLastVisit.BaselineAt)
	}
}

// The 360 is a read: it must never advance the mark it reports against,
// or the "what changed" answer destroys itself on first sight.
func TestOrganization360DoesNotAdvanceTheVisitBaseline(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)

	if _, err := svc.Assemble(rep, org); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if marks := e.WsCount(t, `SELECT count(*) FROM user_record_view WHERE entity_id = $1`, org.UUID); marks != 0 {
		t.Errorf("user_record_view rows after a GET = %d, want 0 — only the ack writes the baseline", marks)
	}
	if _, err := svc.Acknowledge(rep, org); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	if marks := e.WsCount(t, `SELECT count(*) FROM user_record_view WHERE entity_id = $1`, org.UUID); marks != 1 {
		t.Errorf("user_record_view rows after an ack = %d, want 1", marks)
	}
}

// An agent acting through a passport is not a visitor: it must not consume
// the human's unread marker, and it cannot triage approvals either.
func TestOrganization360RefusesTheVisitBaselineToAnAgent(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))

	if _, err := svc.Acknowledge(agentWithOrgRead(e), org); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("agent acknowledgment → %v, want ErrPermissionDenied", err)
	}
	view, err := svc.Assemble(agentWithOrgRead(e), org)
	if err != nil {
		t.Fatalf("assemble as an agent: %v", err)
	}
	if view.PendingApprovals != nil {
		t.Error("pending_approvals present for an agent — triage is human work, so the section must be omitted")
	}
	if !slices.Contains(view.SectionsOmitted, "pending_approvals") {
		t.Errorf("sections_omitted = %v, want it to name pending_approvals for an agent", view.SectionsOmitted)
	}
}

// The three deltas the header line is built from. dealStageMoves is the
// most easily-wrong query in the read — it has to count moves without
// counting the deal's creation — so it is seeded both ways.
func TestOrganization360CountsWhatChangedSinceTheAcknowledgedVisit(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := org360Service(e)
	pipeline, stage, won := DealFixture(t, e)
	// A REAL seeded user, not e.Admin()'s synthetic one: the baseline row
	// carries a composite foreign key to app_user, so only a user that
	// exists can acknowledge a visit.
	admin := e.As(e.Rep1, nil, AdminPerms)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))

	// A deal created and an activity logged BEFORE the visit: neither is news.
	// created_at is set explicitly on both sides of the baseline, because the
	// read's clock is pinned to org360Clock while the database's now() is not
	// — a fixture that used now() would sit on one side of the baseline by
	// accident rather than by design.
	before := e.SeedDeal(t, "Old deal", pipeline, stage, &e.Rep1)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, before, org.UUID)
	old := SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, created_at, source, captured_by)
		VALUES ($1, $2, 'note', 'before the visit', '2026-05-01T09:00:00Z', '2026-05-01T09:00:00Z', 'manual', 'human:x')`, e.WS)
	e.WsExec(t, `INSERT INTO activity_link (workspace_id, activity_id, entity_type, organization_id)
		VALUES ($1, $2, 'organization', $3)`, e.WS, old, org.UUID)

	if _, err := svc.Acknowledge(admin, org); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	// Now: one new activity, one real stage move, and one NEW deal — whose
	// creation writes a first-stage history row that must not count as a move.
	fresh := SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, created_at, source, captured_by)
		VALUES ($1, $2, 'note', 'after the visit', '2026-06-15T09:00:00Z', '2026-06-15T09:00:00Z', 'manual', 'human:x')`, e.WS)
	e.WsExec(t, `INSERT INTO activity_link (workspace_id, activity_id, entity_type, organization_id)
		VALUES ($1, $2, 'organization', $3)`, e.WS, fresh, org.UUID)
	created := e.SeedDeal(t, "Brand new deal", pipeline, stage, &e.Rep1)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, created, org.UUID)
	if _, err := e.Deals.AdvanceDeal(admin, ids.From[ids.DealKind](before), deals.AdvanceDealInput{ToStageID: won}); err != nil {
		t.Fatalf("advancing the old deal: %v", err)
	}

	view, err := svc.Assemble(admin, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	delta := view.SinceLastVisit
	if delta == nil {
		t.Fatal("since_last_visit absent for an admin")
	}
	if delta.BaselineAt == nil {
		t.Fatal("baseline_at is null after an acknowledgment")
	}
	if delta.NewActivities != 1 {
		t.Errorf("new_activities = %d, want 1 — only the activity logged after the visit is news",
			delta.NewActivities)
	}
	if delta.DealStageMoves == nil {
		t.Fatal("deal_stage_moves is null for a caller holding the deal grant")
	}
	if *delta.DealStageMoves != 1 {
		t.Errorf("deal_stage_moves = %d, want 1 — a deal CREATED since the visit is a new deal, not a move",
			*delta.DealStageMoves)
	}
}

// The first visit is not "nothing happened": a caller who has never opened
// the account gets the whole history counted, against a null baseline.
func TestOrganization360CountsTheWholeHistoryOnAFirstVisit(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	logged := SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'note', 'first contact', now(), 'manual', 'human:x')`, e.WS)
	e.WsExec(t, `INSERT INTO activity_link (workspace_id, activity_id, entity_type, organization_id)
		VALUES ($1, $2, 'organization', $3)`, e.WS, logged, org.UUID)

	view, err := svc.Assemble(e.Admin(), org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.SinceLastVisit == nil || view.SinceLastVisit.BaselineAt != nil {
		t.Fatalf("since_last_visit = %+v, want a null baseline on a first visit", view.SinceLastVisit)
	}
	if view.SinceLastVisit.NewActivities != 1 {
		t.Errorf("new_activities = %d on a first visit, want the account's whole history counted",
			view.SinceLastVisit.NewActivities)
	}
}
