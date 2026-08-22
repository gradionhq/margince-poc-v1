// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The project_gone_quiet producer (SIG-F-3, second deterministic rule): a
// project in flight that nothing has been filed against for a month gets one
// signal per quiet episode, keyed to the project's own clock, and a bounded
// reader's signal list carries it rather than dropping the project subject.

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func quietProjectPass(t *testing.T, e *Env, now time.Time) compose.GhostedPass {
	t.Helper()
	var pass compose.GhostedPass
	ctx := e.Admin()
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		var err error
		pass, err = compose.WriteProjectQuietSignals(ctx, tx, now)
		return err
	}); err != nil {
		t.Fatalf("quiet project pass: %v", err)
	}
	return pass
}

// projectSignals reads the signals whose SUBJECT is the project, through the
// real signal list as a team-scoped rep holding the signal and project grants
// — the reader the company page's signal section runs as.
func projectSignals(t *testing.T, e *Env, org ids.UUID, project ids.ProjectID) []crmcontracts.Signal {
	t.Helper()
	reader := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		Objects: map[string]principal.ObjectGrant{
			"signal": {Read: true}, "project": {Read: true}, "organization": {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	})
	store := signals.NewStore(e.DB(), nil)
	listed, _, err := store.ListSignals(reader, signals.ListSignalsInput{OrganizationID: &org})
	if err != nil {
		t.Fatalf("listing the account's signals: %v", err)
	}
	var out []crmcontracts.Signal
	for _, sig := range listed {
		if sig.EntityType != nil && *sig.EntityType == "project" && sig.EntityId != nil && ids.UUID(*sig.EntityId) == project.UUID {
			out = append(out, sig)
		}
	}
	return out
}

func TestAQuietProjectIsRaisedOncePerQuietEpisode(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()
	now := time.Now().UTC()
	org := e.SeedOrg(t, "Quiet Client", nil)
	erp := seedProject(admin, t, e, "ERP replacement", strPtr("ERP-27"), org, &e.Rep1)
	advanceProject(admin, t, e, erp.ID, deals.PhaseDelivering)
	fileActivity(admin, t, e, "meeting", now.AddDate(0, 0, -45), &erp.ID)
	// Not in flight: an initiative nobody has touched is not a finding.
	idea := seedProject(admin, t, e, "Someday", nil, org, nil)
	fileActivity(admin, t, e, "note", now.AddDate(0, 0, -45), &idea.ID)

	pass := quietProjectPass(t, e, now)
	if pass.Considered != 1 || pass.Raised != 1 {
		t.Fatalf("first pass considered %d and raised %d, want 1 and 1 (the delivering project alone)", pass.Considered, pass.Raised)
	}
	raised := projectSignals(t, e, org, erp.ID)
	if len(raised) != 1 || string(raised[0].Kind) != "project_gone_quiet" || raised[0].Status != "open" {
		t.Fatalf("the team-scoped reader's list = %+v, want one open project_gone_quiet signal on the project", raised)
	}
	if raised[0].ResolvedOrgId == nil || ids.UUID(*raised[0].ResolvedOrgId) != org {
		t.Fatalf("the signal is attributed to %v, want the project's company %s", raised[0].ResolvedOrgId, org)
	}

	// The producer runs hourly: the same silence raises nothing new.
	if again := quietProjectPass(t, e, now.Add(time.Hour)); again.Raised != 0 {
		t.Fatalf("a repeat pass raised %d, want none", again.Raised)
	}

	// Somebody files something: the project is no longer quiet. A month later
	// it is quiet AGAIN — a new episode, so a new signal beside the old one.
	fileActivity(admin, t, e, "call", now, &erp.ID)
	if woken := quietProjectPass(t, e, now.Add(2*time.Hour)); woken.Considered != 0 {
		t.Fatalf("a project touched today was considered %d times, want 0", woken.Considered)
	}
	if later := quietProjectPass(t, e, now.AddDate(0, 0, 40)); later.Raised != 1 {
		t.Fatalf("the next quiet episode raised %d, want 1", later.Raised)
	}
	if got := projectSignals(t, e, org, erp.ID); len(got) != 2 {
		t.Fatalf("signals on the project after two episodes = %d, want 2", len(got))
	}
}

// A project_gone_quiet summary names the project, so a seat holding
// signal.read without project.read is not served it — the subject arm takes
// the object grant, not only the row predicate a project admits every seat to.
func TestAProjectSignalIsWithheldFromASeatWithoutTheProjectGrant(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()
	now := time.Now().UTC()
	org := e.SeedOrg(t, "Quiet Client", nil)
	erp := seedProject(admin, t, e, "ERP replacement", nil, org, nil)
	advanceProject(admin, t, e, erp.ID, deals.PhaseDelivering)
	fileActivity(admin, t, e, "meeting", now.AddDate(0, 0, -45), &erp.ID)
	if pass := quietProjectPass(t, e, now); pass.Raised != 1 {
		t.Fatalf("raised %d, want 1", pass.Raised)
	}
	if got := projectSignals(t, e, org, erp.ID); len(got) != 1 {
		t.Fatalf("a seat WITH project.read lists %d project signals, want 1", len(got))
	}

	noProject := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		Objects:  map[string]principal.ObjectGrant{"signal": {Read: true}, "organization": {Read: true}},
		RowScope: principal.RowScopeAll,
	})
	listed, _, err := signals.NewStore(e.DB(), nil).ListSignals(noProject, signals.ListSignalsInput{OrganizationID: &org})
	if err != nil {
		t.Fatalf("listing without project.read: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("a seat without project.read was served %d signal(s) about the project: %+v", len(listed), listed)
	}
}
