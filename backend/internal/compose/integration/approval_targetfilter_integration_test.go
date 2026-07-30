// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The inbox filtered to ONE record. A deep site read stages a proposal per
// person it found, plus the read itself, so a freshly created company can carry
// dozens of pending approvals — the filter is what lets a record page ask for
// its own instead of paging the whole workspace queue.
//
// Every gate the unfiltered inbox carries has to survive the filter: an
// out-of-scope target answers empty rather than refusing, and a kind the caller
// holds no decision grant for stays absent even when they asked for it by name.
//
// The parameter binding and the half-pair refusal need no database and live in
// modules/approvals/inbox_test.go.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// siteReadPerms is the rep a deep read's proposals are staged for: they may
// update the organization (a deepread proposal) and create leads (a site_lead
// proposal), which is exactly the pair the two kinds demand to be decidable.
var siteReadPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"organization": {Create: true, Read: true, Update: true},
		"person":       {Create: true, Read: true, Update: true},
		"lead":         {Create: true, Read: true, Update: true},
	},
	RowScope: principal.RowScopeTeam,
}

// siteReadFixture is one account carrying a deep read's whole staging burst,
// and a second account with its own, so a filter that matched everything
// cannot pass.
type siteReadFixture struct {
	org, otherOrg ids.UUID
	deepread      ids.ApprovalID
	siteLeads     []ids.ApprovalID
	otherStaging  ids.ApprovalID
}

func seedSiteReadStagings(t *testing.T, svc *approvals.Service, e *Env) siteReadFixture {
	t.Helper()
	f := siteReadFixture{
		org:      e.SeedOrg(t, "Acme", &e.Rep1),
		otherOrg: e.SeedOrg(t, "Other Account", &e.Rep1),
	}
	f.deepread = stageFor(t, svc, e, "deepread", "organization", f.org)
	for range 3 {
		f.siteLeads = append(f.siteLeads, stageFor(t, svc, e, "site_lead", "organization", f.org))
	}
	f.otherStaging = stageFor(t, svc, e, "deepread", "organization", f.otherOrg)
	return f
}

// listIDs is the id set one inbox read returned, which is how these tests
// state "exactly these and nothing else" without depending on order.
func listIDs(ctx context.Context, t *testing.T, svc *approvals.Service, in approvals.ListInput) map[ids.ApprovalID]bool {
	t.Helper()
	rows, _, err := svc.List(ctx, in)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	out := map[ids.ApprovalID]bool{}
	for _, a := range rows {
		out[a.ID] = true
	}
	return out
}

// The whole point of the filter: a record page asks for its own staged
// actions and gets those, not the workspace's queue.
func TestApprovalListFilteredToOneTarget(t *testing.T) {
	e := Setup(t)
	svc := approvals.NewService(e.Pool)
	f := seedSiteReadStagings(t, svc, e)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, siteReadPerms)
	orgType := "organization"

	got := listIDs(rep, t, svc, approvals.ListInput{TargetType: &orgType, TargetID: &f.org})
	for _, want := range append([]ids.ApprovalID{f.deepread}, f.siteLeads...) {
		if !got[want] {
			t.Errorf("approval %s is missing — it is staged against the filtered account", want)
		}
	}
	if got[f.otherStaging] {
		t.Error("the filter returned a staging against a different account")
	}
	if len(got) != len(f.siteLeads)+1 {
		t.Errorf("returned %d approvals, want the account's %d", len(got), len(f.siteLeads)+1)
	}

	// The kind sub-filter narrows within the target — the parameter the
	// contract declared and the server ignored until it was threaded through.
	kind := "site_lead"
	got = listIDs(rep, t, svc, approvals.ListInput{TargetType: &orgType, TargetID: &f.org, Kind: &kind})
	if len(got) != len(f.siteLeads) {
		t.Fatalf("kind-filtered read returned %d approvals, want the %d site leads", len(got), len(f.siteLeads))
	}
	if got[f.deepread] {
		t.Error("the deep read came back from a read filtered to site_lead")
	}

	// And the status sub-filter: nothing here is decided, so a decided read
	// is empty while the pending read is full.
	decided := "approved"
	if got := listIDs(rep, t, svc, approvals.ListInput{
		TargetType: &orgType, TargetID: &f.org, Status: &decided,
	}); len(got) != 0 {
		t.Errorf("status=approved returned %d approvals, want none — every staging is still pending", len(got))
	}
	pending := "pending"
	if got := listIDs(rep, t, svc, approvals.ListInput{
		TargetType: &orgType, TargetID: &f.org, Status: &pending,
	}); len(got) != len(f.siteLeads)+1 {
		t.Errorf("status=pending returned %d approvals, want the account's %d", len(got), len(f.siteLeads)+1)
	}
}

// The kind filter must narrow the UNFILTERED inbox too: it was declared for
// the whole surface, not for the target-scoped read alone.
func TestApprovalListKindFilterNarrowsTheWholeInbox(t *testing.T) {
	e := Setup(t)
	svc := approvals.NewService(e.Pool)
	f := seedSiteReadStagings(t, svc, e)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, siteReadPerms)

	kind := "site_lead"
	got := listIDs(rep, t, svc, approvals.ListInput{Kind: &kind})
	if len(got) != len(f.siteLeads) {
		t.Fatalf("kind-filtered inbox returned %d approvals, want the %d site leads", len(got), len(f.siteLeads))
	}
	for _, want := range f.siteLeads {
		if !got[want] {
			t.Errorf("site lead %s is missing from a read filtered to its own kind", want)
		}
	}
}

// A target the caller's row scope hides answers an EMPTY list, never a 403:
// a refusal would confirm that something is staged against a record they are
// not allowed to know exists. Existence-hiding is the same answer the record's
// own read gives.
func TestApprovalListFilteredToAnOutOfScopeTargetIsEmpty(t *testing.T) {
	e := Setup(t)
	svc := approvals.NewService(e.Pool)

	theirs := e.SeedOrg(t, "Other Team Account", &e.Rep3)
	staged := stageFor(t, svc, e, "deepread", "organization", theirs)
	orgType := "organization"

	// Rep1 holds organization.update — the deepread decision grant — but sits
	// in the other team, so the row scope hides the account itself.
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, siteReadPerms)
	rows, hasMore, err := svc.List(rep, approvals.ListInput{TargetType: &orgType, TargetID: &theirs})
	if err != nil {
		t.Fatalf("list filtered to an out-of-scope target → %v, want an empty list", err)
	}
	if len(rows) != 0 {
		t.Errorf("returned %d approvals for an account outside the caller's row scope", len(rows))
	}
	if hasMore {
		t.Error("has_more is true for an empty answer — a client would page for rows that do not exist")
	}

	// The positive control: the owning team sees it, so the empty answer above
	// is the row scope and not a broken filter.
	owner := e.As(e.Rep3, []ids.UUID{e.Team2}, siteReadPerms)
	if got := listIDs(owner, t, svc, approvals.ListInput{TargetType: &orgType, TargetID: &theirs}); !got[staged] {
		t.Errorf("the owning team's read is missing %s", staged)
	}
}

// Decidability still prunes per row inside a filtered read: a kind whose
// decision grant the caller lacks is absent even when they name the record and
// the kind. Triage is the decision gate — what you cannot decide you cannot
// browse (C3/ADR-0036).
func TestApprovalListFilteredStillPrunesUndecidableKinds(t *testing.T) {
	e := Setup(t)
	svc := approvals.NewService(e.Pool)
	f := seedSiteReadStagings(t, svc, e)
	orgType := "organization"

	// The lead create grant is what a site_lead proposal needs to be decided;
	// without it the proposals must not appear, while the deep read still does.
	noLeads := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"organization": {Read: true, Update: true},
			"person":       {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	})
	got := listIDs(noLeads, t, svc, approvals.ListInput{TargetType: &orgType, TargetID: &f.org})
	if !got[f.deepread] {
		t.Error("the deep read is missing for a caller who holds organization.update")
	}
	for _, hidden := range f.siteLeads {
		if got[hidden] {
			t.Errorf("site lead %s is disclosed to a caller who could not decide it", hidden)
		}
	}

	// Naming the kind explicitly does not unlock it.
	kind := "site_lead"
	if got := listIDs(noLeads, t, svc, approvals.ListInput{
		TargetType: &orgType, TargetID: &f.org, Kind: &kind,
	}); len(got) != 0 {
		t.Errorf("asking for site_lead by name returned %d approvals to a caller with no lead grant", len(got))
	}
}

// has_more is what tells a record page it is showing a slice: a filter that
// always reported false would let a client draw four of a company's forty
// staged contacts and call it the whole list.
func TestApprovalListFilteredReportsHasMore(t *testing.T) {
	e := Setup(t)
	svc := approvals.NewService(e.Pool)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	for range 5 {
		stageFor(t, svc, e, "site_lead", "organization", org)
	}
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, siteReadPerms)
	orgType := "organization"

	rows, hasMore, err := svc.List(rep, approvals.ListInput{TargetType: &orgType, TargetID: &org, Limit: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("returned %d approvals, want the requested 2", len(rows))
	}
	if !hasMore {
		t.Error("has_more is false with three staged approvals left unreturned")
	}

	rows, hasMore, err = svc.List(rep, approvals.ListInput{TargetType: &orgType, TargetID: &org, Limit: 50})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 5 {
		t.Errorf("returned %d approvals, want all 5", len(rows))
	}
	if hasMore {
		t.Error("has_more is true when every staged approval was returned")
	}
}
