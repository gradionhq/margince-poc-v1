// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The morning digest's projects section, built by the nightly pass and read
// back over GET /digest: a phase move recorded in the window, a task filed
// under a project overnight, and a project the quiet rule fires on — all
// seeded through the real writers, and a project outside the window or out of
// flight absent from each list.

import (
	"net/http"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (b *backfillWireEnv) seedDigestProject(t *testing.T, name string, org ids.UUID, owner *ids.UUID) ids.ProjectID {
	t.Helper()
	in := deals.CreateProjectInput{Name: name, OrganizationID: ids.From[ids.OrganizationKind](org), Source: "manual"}
	if owner != nil {
		id := ids.From[ids.UserKind](*owner)
		in.OwnerID = &id
	}
	p, err := b.env.Deals.CreateProject(b.env.Admin(), in)
	if err != nil {
		t.Fatalf("create project %q: %v", name, err)
	}
	return ids.From[ids.ProjectKind](ids.UUID(p.Id))
}

func (b *backfillWireEnv) fileOnProject(t *testing.T, kind string, project ids.ProjectID, when time.Time, due *time.Time) {
	t.Helper()
	subject := kind + " on the project"
	if _, _, err := b.env.Activities.LogActivity(b.env.Admin(), activities.LogActivityInput{
		Kind: kind, Subject: &subject, OccurredAt: &when, DueAt: due, Source: "manual",
		Links: []activities.ActivityLinkInput{{EntityType: "project", EntityID: project.UUID}},
	}); err != nil {
		t.Fatalf("logging a %s on the project: %v", kind, err)
	}
}

func TestMorningDigestCarriesTheProjectsSection(t *testing.T) {
	b := setupBackfillWire(t)
	e := b.env
	admin := e.Admin()
	now := time.Now().UTC()
	org := e.SeedOrg(t, "Digest Client", nil)

	moved := b.seedDigestProject(t, "Moved overnight", org, nil)
	if _, err := e.Deals.AdvanceProjectPhase(admin, moved, deals.AdvanceProjectPhaseInput{ToPhase: deals.PhasePursuing}); err != nil {
		t.Fatalf("advance the project: %v", err)
	}
	due := now.Add(72 * time.Hour)
	promised := b.seedDigestProject(t, "Promised overnight", org, nil)
	b.fileOnProject(t, "task", promised, now, &due)
	b.fileOnProject(t, "task", promised, now, &due)
	quiet := b.seedDigestProject(t, "Gone quiet", org, &e.Rep1)
	if _, err := e.Deals.AdvanceProjectPhase(admin, quiet, deals.AdvanceProjectPhaseInput{ToPhase: deals.PhaseDelivering}); err != nil {
		t.Fatalf("advance the quiet project: %v", err)
	}
	b.fileOnProject(t, "meeting", quiet, now.AddDate(0, 0, -40), nil)
	// Touched last week: in flight, recently active, in no list.
	busy := b.seedDigestProject(t, "Busy", org, nil)
	if _, err := e.Deals.AdvanceProjectPhase(admin, busy, deals.AdvanceProjectPhaseInput{ToPhase: deals.PhaseDelivering}); err != nil {
		t.Fatalf("advance the busy project: %v", err)
	}
	b.fileOnProject(t, "call", busy, now.AddDate(0, 0, -7), nil)

	if err := b.registry.BuildDigests(b.human, now); err != nil {
		t.Fatalf("BuildDigests: %v", err)
	}
	status, digest := b.readDigest(t, nil)
	if status != http.StatusOK {
		t.Fatalf("digest after build → %d, want 200", status)
	}
	if digest.Projects == nil {
		t.Fatal("the digest carries no projects section")
	}
	projects := *digest.Projects

	// Every project's birth row and the four advances all fall in the window;
	// the advances are what a reader acts on, and they lead.
	moves := map[string]string{}
	for _, change := range projects.PhaseChanges {
		if change.FromPhase != nil {
			moves[change.Name] = *change.FromPhase + "→" + change.ToPhase
		}
	}
	if moves["Moved overnight"] != "initiative→pursuing" || moves["Gone quiet"] != "initiative→delivering" || moves["Busy"] != "initiative→delivering" {
		t.Fatalf("phase changes = %v, want the three advances recorded in the window", moves)
	}
	if len(projects.NewCommitments) != 1 || projects.NewCommitments[0].Name != "Promised overnight" ||
		projects.NewCommitments[0].NewOpenCommitments != 2 {
		t.Fatalf("new commitments = %+v, want Promised overnight with 2", projects.NewCommitments)
	}
	if len(projects.GoneQuiet) != 1 || projects.GoneQuiet[0].Name != "Gone quiet" ||
		ids.UUID(projects.GoneQuiet[0].ProjectId) != quiet.UUID {
		t.Fatalf("gone quiet = %+v, want Gone quiet alone (Busy was touched last week)", projects.GoneQuiet)
	}
	silent := projects.GoneQuiet[0]
	if silent.DaysQuiet < 39 || silent.DaysQuiet > 41 || silent.OwnerId == nil || ids.UUID(*silent.OwnerId) != e.Rep1 {
		t.Fatalf("the quiet row = %+v, want about 40 days quiet and Rep1 as owner", silent)
	}
}
