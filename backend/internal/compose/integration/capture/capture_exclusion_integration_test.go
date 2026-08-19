// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// An exclusion keeps a message out of the CRM before anything is stored: no
// raw capture, no activity, no person — only a breadcrumb and a trace that
// name the kind of rule, never the address it matched. A workspace rule binds
// every connection; a user's own rule binds the connections that user
// granted, and a colleague's mailbox goes on capturing the same sender.
func TestAnExclusionKeepsAMessageOutBeforeAnythingIsStored(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync
	store := capture.NewExclusionStore(e.DB())
	owner := humanWithScopes(e, e.Rep1, []principal.Scope{principal.ScopeRead, principal.ScopeWrite})
	admin := principal.WithActor(principal.WithWorkspaceID(context.Background(), e.WS), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.Rep3.String(), UserID: e.Rep3,
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"capture_settings": {Read: true, Update: true}},
			RowScope: principal.RowScopeAll,
		},
	})

	// The mailbox owner rules out one correspondent; the workspace rules out a
	// whole domain.
	if _, err := store.Add(owner, capture.ExclusionScopeUser, capture.ExclusionKindAddress, "Partner@Home.example"); err != nil {
		t.Fatalf("user rule: %v", err)
	}
	if _, err := store.Add(admin, capture.ExclusionScopeWorkspace, capture.ExclusionKindDomain, "payroll.example"); err != nil {
		t.Fatalf("workspace rule: %v", err)
	}

	sync(t,
		email("partner@home.example", "Partner", captureOwner, "m-x1@mid.example", ""),
		email("hr@mail.payroll.example", "Payroll", captureOwner, "m-x2@mid.example", ""),
		email("dana@acme.example", "Dana Buyer", captureOwner, "x3@acme.example", ""),
	)

	if n := countRows(t, e, `SELECT count(*) FROM activity WHERE source_id IN ('m-x1@mid.example', 'm-x2@mid.example')`); n != 0 {
		t.Errorf("%d activity rows for excluded messages, want 0 — an excluded message is never stored", n)
	}
	if n := countRows(t, e, `SELECT count(*) FROM raw_capture WHERE source_id IN ('m-x1@mid.example', 'm-x2@mid.example')`); n != 0 {
		t.Errorf("%d raw_capture rows for excluded messages, want 0", n)
	}
	if n := countRows(t, e, `SELECT count(*) FROM activity WHERE source_id = 'x3@acme.example'`); n != 1 {
		t.Errorf("the ordinary message beside the excluded ones did not land (%d rows)", n)
	}
	if n := countRows(t, e, `
		SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
		WHERE pe.email IN ('partner@home.example', 'hr@mail.payroll.example')`); n != 0 {
		t.Error("an excluded sender became a person")
	}
	// The trace says a rule fired and which kind; it does not repeat the
	// address or the domain that matched.
	if n := countRows(t, e, `SELECT count(*) FROM capture_trace WHERE reason IN ('excluded_address', 'excluded_domain')`); n != 2 {
		t.Errorf("%d exclusion trace rows, want 2", n)
	}
	if n := countRows(t, e, `SELECT count(*) FROM system_log WHERE action = 'capture_excluded' AND detail::text ILIKE '%home.example%' OR detail::text ILIKE '%payroll.example%'`); n != 0 {
		t.Error("the exclusion breadcrumb carries the excluded address")
	}
	if n := countRows(t, e, `SELECT count(*) FROM system_log WHERE action = 'capture_excluded'`); n != 2 {
		t.Errorf("%d exclusion breadcrumbs, want 2", n)
	}

	// The list answers the workspace's rules and the caller's own; a
	// colleague sees the workspace rule and not the owner's personal one, and
	// cannot lift it.
	mine, err := store.List(owner)
	if err != nil || len(mine) != 2 {
		t.Fatalf("owner's list = %d rules (%v), want 2", len(mine), err)
	}
	colleague := humanWithScopes(e, e.Rep3, []principal.Scope{principal.ScopeRead})
	theirs, err := store.List(colleague)
	if err != nil || len(theirs) != 1 || theirs[0].Scope != capture.ExclusionScopeWorkspace {
		t.Fatalf("colleague's list = %+v (%v), want the workspace rule alone", theirs, err)
	}
	var personal capture.Exclusion
	for _, r := range mine {
		if r.Scope == capture.ExclusionScopeUser {
			personal = r
		}
	}
	if err := store.Remove(colleague, personal.ID); err == nil {
		t.Error("a colleague lifted another user's personal rule")
	}
	if err := store.Remove(owner, personal.ID); err != nil {
		t.Errorf("the owner lifting their own rule: %v", err)
	}
	if _, err := store.Add(owner, capture.ExclusionScopeWorkspace, capture.ExclusionKindDomain, "x.example"); err == nil {
		t.Error("a rep without the capture-settings grant added a workspace rule")
	}
}
