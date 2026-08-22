// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A project under legal hold freezes the activities filed under it, exactly
// as a held deal does. Both privacy engines are driven: the Art. 17 cascade
// must not destroy a subject-only note that is also filed under a held
// project, and the retention sweep must not archive an over-age note whose
// ONLY link is the held project. Each half carries an unheld twin so that
// survival is attributable to the hold and not to a selector that misses the
// class.
//
// The hold itself is set by SQL through the harness: legal_hold has no API on
// any record — an operator sets it in the database — so SQL is the real
// writer here, not a stand-in for one.

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// projectHoldFixture is a pair of projects on one account, one of them held.
type projectHoldFixture struct {
	held, free ids.UUID
}

func seedProjectHoldFixture(t *testing.T, e *Env) projectHoldFixture {
	t.Helper()
	org := e.SeedOrg(t, "Acme GmbH", nil)
	create := func(name, key string) ids.UUID {
		p, err := e.Deals.CreateProject(e.Admin(), deals.CreateProjectInput{
			Name: name, Key: &key, OrganizationID: orgIDOf(org), Source: "manual",
		})
		if err != nil {
			t.Fatalf("create project %q: %v", name, err)
		}
		return ids.UUID(p.Id)
	}
	f := projectHoldFixture{held: create("Disputed rollout", "DR-1"), free: create("Routine rollout", "RR-1")}
	e.WsExec(t, `UPDATE project SET legal_hold = true WHERE id = $1`, f.held)
	return f
}

// logNote writes a 'note' through the real writer. A note carries no
// statutory correspondence floor, so whatever survives below survives on the
// legal hold alone and not on the Handelsbrief shield.
func logNote(t *testing.T, e *Env, subject string, occurredAt time.Time, links ...activities.ActivityLinkInput) ids.UUID {
	t.Helper()
	body := "what was said"
	logged, _, err := e.Activities.LogActivity(e.Admin(), activities.LogActivityInput{
		Kind: "note", Subject: &subject, Body: &body, OccurredAt: &occurredAt, Links: links,
	})
	if err != nil {
		t.Fatalf("log %q: %v", subject, err)
	}
	return ids.UUID(logged.Id)
}

func activityBodyKept(t *testing.T, e *Env, id ids.UUID) bool {
	t.Helper()
	var kept bool
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT body = 'what was said' AND archived_at IS NULL FROM activity WHERE id = $1`, id).Scan(&kept)
	}); err != nil {
		t.Fatalf("reading activity %s: %v", id, err)
	}
	return kept
}

func TestErasureKeepsASubjectOnlyNoteFiledUnderAHeldProject(t *testing.T) {
	e := Setup(t)
	f := seedProjectHoldFixture(t, e)
	subject := e.SeedPerson(t, "Delivery Contact", nil)
	person := activities.ActivityLinkInput{EntityType: "person", EntityID: subject}
	onHeld := logNote(t, e, "Acceptance dispute", time.Now(), person,
		activities.ActivityLinkInput{EntityType: "project", EntityID: f.held})
	onFree := logNote(t, e, "Kick-off", time.Now(), person,
		activities.ActivityLinkInput{EntityType: "project", EntityID: f.free})

	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(), subject, "test"); err != nil {
		t.Fatalf("erasing an unheld subject: %v", err)
	}

	if !activityBodyKept(t, e, onHeld) {
		t.Error("the erasure destroyed a note filed under a project on legal hold")
	}
	if activityBodyKept(t, e, onFree) {
		t.Error("the twin filed under an unheld project survived — the cascade did not discriminate on the hold")
	}
}

func TestRetentionSweepSkipsAnOverAgeNoteLinkedOnlyToAHeldProject(t *testing.T) {
	e := Setup(t)
	SeedRetentionPolicies(t, e)
	f := seedProjectHoldFixture(t, e)
	// Past the 1095-day activity/ archive policy SeedRetentionPolicies authors.
	overAge := time.Now().AddDate(-4, 0, 0)
	onHeld := logNote(t, e, "Acceptance dispute", overAge,
		activities.ActivityLinkInput{EntityType: "project", EntityID: f.held})
	onFree := logNote(t, e, "Kick-off", overAge,
		activities.ActivityLinkInput{EntityType: "project", EntityID: f.free})

	svc := privacy.NewRetentionService(e.DB(), nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := svc.EvaluateInstallation(RetentionPassCtx(e.WS)); err != nil {
		t.Fatal(err)
	}

	if !activityBodyKept(t, e, onHeld) {
		t.Error("the retention sweep archived a note whose only link is a project on legal hold")
	}
	if activityBodyKept(t, e, onFree) {
		t.Error("the twin under an unheld project was not archived — the sweep did not run over this class")
	}
}
