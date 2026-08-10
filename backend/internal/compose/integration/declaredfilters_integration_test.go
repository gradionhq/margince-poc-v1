// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// What the declared list filters answer, against real SQL.
//
// Each of these parameters was declared by the contract and dropped by its
// handler, so `?tag=vip` — or `?domain=`, or `?assignee_id=` — returned the
// UNFILTERED page with 200 OK. A unit test cannot tell that apart from a
// working filter, because the wrong answer is well-formed and the right one is
// a property of the query the database runs. So each filter is put to a set
// seeded to have a right answer and a wrong one: the row that matches, and the
// row a dropped filter would hand back with it.

import (
	"context"
	"slices"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// tagCurator is a principal that may author the tag vocabulary and apply it.
// The harness's admin fixture mirrors the real seed, which carries no tag
// grant, so the fixture asks for exactly the object it needs rather than
// widening the shared one.
func tagCurator(e *Env) context.Context {
	return e.As(ids.NewV7(), nil, principal.Permissions{
		Objects: map[string]principal.ObjectGrant{
			"tag": {Create: true, Read: true, Update: true},
		},
		RowScope: principal.RowScopeAll,
	})
}

func TestThePersonListNarrowsByTagName(t *testing.T) {
	e := Setup(t)
	tagged := e.SeedPerson(t, "Tagged Person", nil)
	e.SeedPerson(t, "Untagged Person", nil)

	tags := collections.NewStore(e.Pool)
	curator := tagCurator(e)
	vip, err := tags.CreateTag(curator, "VIP", nil)
	if err != nil {
		t.Fatalf("creating the tag: %v", err)
	}
	if _, err := tags.ApplyTag(curator, vip.ID, "person", tagged); err != nil {
		t.Fatalf("applying the tag: %v", err)
	}

	// Folded on both sides: the vocabulary is unique under lower(name), so
	// the caller's capitalization decides nothing about which tag they named.
	for _, asked := range []string{"VIP", "vip", " Vip "} {
		page, _, err := e.People.ListPeople(e.Admin(), people.ListPeopleInput{Tag: &asked})
		if err != nil {
			t.Fatalf("listing people by tag %q: %v", asked, err)
		}
		if len(page) != 1 || ids.UUID(page[0].Id) != tagged {
			t.Fatalf("tag=%q returned %d people, want only the tagged one", asked, len(page))
		}
	}

	unknown := "no-such-tag"
	page, _, err := e.People.ListPeople(e.Admin(), people.ListPeopleInput{Tag: &unknown})
	if err != nil {
		t.Fatalf("listing people by an unused tag: %v", err)
	}
	if len(page) != 0 {
		t.Fatalf("a tag nobody carries returned %d people — a dropped filter answers the whole list", len(page))
	}
}

func TestTheOrganizationListNarrowsByDomain(t *testing.T) {
	e := Setup(t)
	held, err := e.People.CreateOrganization(e.Admin(), people.CreateOrganizationInput{
		DisplayName: "Acme", Domains: []people.OrgDomainInput{{Domain: "acme.example", IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("seeding the account that holds the domain: %v", err)
	}
	if _, err := e.People.CreateOrganization(e.Admin(), people.CreateOrganizationInput{
		DisplayName: "Other", Domains: []people.OrgDomainInput{{Domain: "other.example", IsPrimary: true}},
	}); err != nil {
		t.Fatalf("seeding the account that does not: %v", err)
	}

	// Folded the way the column stores it, so the lookup answers the same for
	// a caller who typed the domain out of an email signature.
	for _, asked := range []string{"acme.example", "ACME.example"} {
		page, _, err := e.People.ListOrganizations(e.Admin(), people.ListOrganizationsInput{Domain: &asked})
		if err != nil {
			t.Fatalf("listing organizations by domain %q: %v", asked, err)
		}
		if len(page) != 1 || page[0].Id != held.Id {
			t.Fatalf("domain=%q returned %d accounts, want only the one that lists it", asked, len(page))
		}
	}

	unheld := "nobody.example"
	page, _, err := e.People.ListOrganizations(e.Admin(), people.ListOrganizationsInput{Domain: &unheld})
	if err != nil {
		t.Fatalf("listing organizations by an unheld domain: %v", err)
	}
	if len(page) != 0 {
		t.Fatalf("a domain no account lists returned %d accounts — a dropped filter answers the whole list", len(page))
	}
}

func TestTheActivityListNarrowsToTheOpenTasksOneAssigneeHolds(t *testing.T) {
	e := Setup(t)
	due := time.Now().Add(24 * time.Hour)
	mine := e.logTask(t, "Call the buyer", e.Rep1, due)
	closed := e.logTask(t, "Already handled", e.Rep1, due)
	e.logTask(t, "Someone else's", e.Rep3, due)
	if _, err := e.Activities.UpdateActivity(e.Admin(), ids.From[ids.ActivityKind](closed),
		activities.UpdateActivityInput{IsDone: boolPtr(true)}); err != nil {
		t.Fatalf("completing the task: %v", err)
	}

	assignee := ids.From[ids.UserKind](e.Rep1)
	page, _, err := e.Activities.ListActivities(e.Admin(), activities.ListActivitiesInput{AssigneeID: &assignee})
	if err != nil {
		t.Fatalf("listing activities by assignee: %v", err)
	}
	if len(page) != 1 || ids.UUID(page[0].Id) != mine {
		t.Fatalf("assignee_id returned %d activities, want the one open task that person holds "+
			"(a dropped filter answers every task in the workspace, a done one included)", len(page))
	}
}

// logTask writes one open task for an assignee through the store the wire
// path uses, so the row carries whatever a real task carries.
func (e *Env) logTask(t *testing.T, subject string, assignee ids.UUID, due time.Time) ids.UUID {
	t.Helper()
	assigneeID := ids.From[ids.UserKind](assignee)
	activity, _, err := e.Activities.LogActivity(e.Admin(), activities.LogActivityInput{
		Kind: "task", Subject: &subject, DueAt: &due, AssigneeID: &assigneeID, Source: "manual",
	})
	if err != nil {
		t.Fatalf("logging task %q: %v", subject, err)
	}
	return ids.UUID(activity.Id)
}

func TestThePipelineListAnswersIncludeArchived(t *testing.T) {
	e := Setup(t)
	if _, err := e.Deals.CreatePipeline(e.Admin(), deals.CreatePipelineInput{Name: "Live"}); err != nil {
		t.Fatalf("seeding the live pipeline: %v", err)
	}
	retired, err := e.Deals.CreatePipeline(e.Admin(), deals.CreatePipelineInput{Name: "Retired"})
	if err != nil {
		t.Fatalf("seeding the pipeline to retire: %v", err)
	}
	// Aged by SQL because no contract operation archives a pipeline today.
	// What makes the parameter answerable is the column and the read's filter
	// on it, and this is the state that filter is about.
	e.WsExec(t, `UPDATE pipeline SET archived_at = now() WHERE id = $1`, retired.Id)

	liveOnly, err := e.Deals.ListPipelines(e.Admin(), storekit.LiveOnly)
	if err != nil {
		t.Fatalf("listing live pipelines: %v", err)
	}
	if !listsPipeline(liveOnly, "Live") || listsPipeline(liveOnly, "Retired") {
		t.Fatalf("the live list = %v, want Live present and Retired absent", pipelineNames(liveOnly))
	}

	withArchived, err := e.Deals.ListPipelines(e.Admin(), storekit.IncludeArchived)
	if err != nil {
		t.Fatalf("listing pipelines including the archived: %v", err)
	}
	if !listsPipeline(withArchived, "Retired") {
		t.Fatalf("include_archived = %v, want the archived pipeline among them — a dropped parameter "+
			"answers the live list either way", pipelineNames(withArchived))
	}
}

// listsPipeline reports whether a page carries the pipeline named.
func listsPipeline(page []crmcontracts.Pipeline, name string) bool {
	return slices.Contains(pipelineNames(page), name)
}

// pipelineNames renders a page for a failure message.
func pipelineNames(page []crmcontracts.Pipeline) []string {
	names := make([]string, 0, len(page))
	for _, p := range page {
		names = append(names, p.Name)
	}
	return names
}
