// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The audit-log governance read (GET /audit-log): an admin human reads the
// workspace trail newest-first with live filters and a stable keyset walk.
// Everyone else is refused outright — a bounded rep, an agent principal, and
// the two roles that hold an unbounded row scope without holding the
// compliance authority. The surface never narrows to a misleading partial
// view.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestAuditLogReadRequiresAdminHuman(t *testing.T) {
	e := Setup(t)

	e.SeedPerson(t, "Audit Subject", nil)

	// A bounded rep is refused — 403, not a narrowed page.
	repCtx := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)
	if _, err := privacy.ListAuditLog(repCtx, e.DB(), privacy.AuditFilter{}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("bounded rep reads audit log: err=%v, want permission denied", err)
	}

	// An unbounded row scope is NOT the predicate. ops and read_only both seed
	// with scope `all` — pinned by policy's own suite — and the governance
	// matrix reserves the compliance read for the admin alone: it is oversight
	// of ops' own machine-origin actions, so it cannot sit with the role it
	// oversees. Ops carries the admin object grid here because production does,
	// which is what makes its refusal evidence about the ROLE rather than about
	// a missing grant.
	for _, unbounded := range []principal.Permissions{OpsPerms, ReadOnlyPerms} {
		ctx := e.As(ids.NewV7(), []ids.UUID{e.Team1}, unbounded)
		if _, err := privacy.ListAuditLog(ctx, e.DB(), privacy.AuditFilter{}); !errors.Is(err, apperrors.ErrPermissionDenied) {
			t.Fatalf("%v reads audit log: err=%v, want permission denied", unbounded.RoleKeys, err)
		}
	}

	// An agent principal is refused even with unbounded grants: the
	// agent gate only fronts mutating routes, so the human-only rule
	// binds at the store.
	agentCtx := principal.WithWorkspaceID(t.Context(), e.WS)
	agentCtx = principal.WithCorrelationID(agentCtx, ids.NewV7())
	agentCtx = principal.WithActor(agentCtx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:" + ids.NewV7().String(),
		UserID: e.Rep1, Permissions: AdminPerms,
	})
	if _, err := privacy.ListAuditLog(agentCtx, e.DB(), privacy.AuditFilter{}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("agent reads audit log: err=%v, want permission denied", err)
	}

	// The unbounded human admin reads it.
	page, err := privacy.ListAuditLog(e.Admin(), e.DB(), privacy.AuditFilter{})
	if err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if len(page.Entries) == 0 {
		t.Fatal("admin sees an empty audit log after a mutation")
	}
}

func TestAuditLogFiltersAndKeysetWalk(t *testing.T) {
	e := Setup(t)

	var personIDs []ids.UUID
	for _, name := range []string{"One", "Two", "Three", "Four", "Five"} {
		personIDs = append(personIDs, e.SeedPerson(t, name, nil))
	}
	admin := e.Admin()

	// Filter: only person creates, and only the one entity.
	action := "create"
	entityType := "person"
	page, err := privacy.ListAuditLog(admin, e.DB(), privacy.AuditFilter{
		Action: &action, EntityType: &entityType, EntityID: &personIDs[2],
	})
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("entity filter returned %d rows, want 1", len(page.Entries))
	}
	if page.Entries[0].EntityID == nil || *page.Entries[0].EntityID != personIDs[2] {
		t.Fatalf("entity filter returned the wrong row: %+v", page.Entries[0])
	}

	// Keyset walk: pages never overlap, order is newest-first, and the
	// walk terminates.
	limit := 2
	seen := map[ids.UUID]bool{}
	var cursor *string
	for range 10 {
		page, err := privacy.ListAuditLog(admin, e.DB(), privacy.AuditFilter{
			EntityType: &entityType, Limit: &limit, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		for i, entry := range page.Entries {
			if seen[entry.ID] {
				t.Fatalf("cursor walk revisited audit row %s", entry.ID)
			}
			seen[entry.ID] = true
			if i > 0 {
				prev := page.Entries[i-1]
				if entry.OccurredAt.After(prev.OccurredAt) {
					t.Fatal("page is not newest-first")
				}
			}
		}
		if !page.HasMore {
			break
		}
		cursor = &page.NextCursor
	}
	if len(seen) < len(personIDs) {
		t.Fatalf("walk saw %d person audit rows, want at least %d", len(seen), len(personIDs))
	}

	// A malformed cursor is a client fault, not a 500.
	bad := "not-a-cursor"
	if _, err := privacy.ListAuditLog(admin, e.DB(), privacy.AuditFilter{Cursor: &bad}); err == nil {
		t.Fatal("malformed cursor accepted")
	}
}

// TestAuditLogResolvesTheHumanBehindEveryRow pins PD-002 on the compliance
// read: attribution names the PERSON, and an identifier is what a reader falls
// back to only when no person resolves. The screen this feeds is the one an
// auditor opens first, and "agent:01a01740-…" is not somebody who can be asked
// about a change.
//
// Three arms, because the read has three honest outcomes and the middle one is
// the trap: a human resolves to a name, a machine resolves to NO name while its
// granting human does, and an id no app_user matches resolves to nothing at all
// rather than to an invented or guessed name.
func TestAuditLogResolvesTheHumanBehindEveryRow(t *testing.T) {
	e := Setup(t)

	// Seeded through the real writer: SeedPerson goes via people.CreatePerson,
	// so the create row's actor_id is whatever storekit actually stamps for the
	// harness admin. A hand-inserted row would prove nothing about production —
	// the spelling of actor_id IS what this read has to match on.
	personID := e.SeedPerson(t, "Attribution Subject", nil)

	page, err := privacy.ListAuditLog(e.Admin(), e.DB(), privacy.AuditFilter{EntityID: &personID})
	if err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if len(page.Entries) == 0 {
		t.Fatal("no audit row for a person the real writer just created")
	}

	var create *privacy.AuditEntry
	for i := range page.Entries {
		if page.Entries[i].Action == "create" {
			create = &page.Entries[i]
			break
		}
	}
	if create == nil {
		t.Fatalf("no create row among %d entries", len(page.Entries))
	}
	if create.ActorType != "human" {
		t.Fatalf("create row actor_type = %q, want human", create.ActorType)
	}
	// Every harness seat is seeded with display_name "Rep", so that is the
	// name the join must return. The point is that a name comes back AT ALL:
	// before this, the wire carried only the opaque 'human:<uuid>'.
	if create.ActorName == nil || *create.ActorName != "Rep" {
		t.Errorf("create row actor_name = %v, want the admin's resolved display name", create.ActorName)
	}
	if create.OnBehalfOfName != nil {
		t.Errorf("human row on_behalf_of_name = %v, want nil — a human acts for themselves",
			create.OnBehalfOfName)
	}

	// An agent row: no actor name (a machine has none), and the granting human
	// named. This is the inversion the issue is about — the passport uuid is
	// the qualifier, the person is the answer.
	// ONE clock reading for the whole fixture, and every seeded row offset from
	// it. Read per row, the rows' order would depend on when each call happened
	// rather than on what the fixture says. It cannot be a fixed literal: the
	// create row above was stamped by the REAL writer at real "now", and these
	// rows have to sort after it.
	base := time.Now().UTC().Truncate(time.Microsecond)

	ada := seedWorkspaceUser(t, e, "Ada Authority")
	seedRecordAuditRow(t, e, "update", personID, "agent",
		"agent:"+ids.NewV7().String(), &ada, nil, map[string]any{"title": "CTO"},
		base.Add(time.Hour))

	// An actor_id no app_user can match: the honest-fallback arm. A read that
	// invented a name here would be worse than one that returns none.
	seedRecordAuditRow(t, e, "update", personID, "human", "human:"+ids.NewV7().String(), nil,
		nil, map[string]any{"title": "VP"},
		base.Add(2*time.Hour))

	page, err = privacy.ListAuditLog(e.Admin(), e.DB(), privacy.AuditFilter{EntityID: &personID})
	if err != nil {
		t.Fatalf("admin re-list: %v", err)
	}
	var sawAgent, sawUnresolvable bool
	for _, entry := range page.Entries {
		switch {
		case entry.ActorType == "agent":
			sawAgent = true
			if entry.ActorName != nil {
				t.Errorf("agent row actor_name = %v, want nil — a machine has no display name",
					entry.ActorName)
			}
			if entry.OnBehalfOfName == nil || *entry.OnBehalfOfName != "Ada Authority" {
				t.Errorf("agent row on_behalf_of_name = %v, want Ada Authority — the person answerable for it",
					entry.OnBehalfOfName)
			}
		case entry.ActorType == "human" && entry.ActorName == nil:
			sawUnresolvable = true
		}
	}
	if !sawAgent {
		t.Error("the seeded agent row never came back from the compliance read")
	}
	if !sawUnresolvable {
		t.Error("a human actor_id matching no app_user must resolve to no name, not be dropped")
	}
}

// The audit image carries an activity's SUBJECT verbatim — LogActivity writes
// {kind, subject}, and the update delta writes subject in full while reducing
// body to presence. An activity's audience is a property of the row that a
// human set, and it deliberately does not yield to row_scope=all, so
// RequireAdmin on this endpoint is not an answer to it: without the audience
// arm, the one reader the limit is chiefly about reads the withheld subject
// straight out of the compliance trail.
//
// The row still comes back. A compliance trail with holes in it is its own
// defect, so the actor, action, entity and timestamp are all answered and only
// the IMAGE is withheld.
func TestAuditLogWithholdsALimitedActivitysImageFromOutsideItsAudience(t *testing.T) {
	e := Setup(t)
	author := e.As(e.Rep1, []ids.UUID{e.Team1}, activityLifecyclePerms)
	contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)

	subject := "Q3 renewal terms"
	body := "confidential pricing"
	logged, _, err := e.Activities.LogActivity(author, activities.LogActivityInput{
		Kind: "email", Subject: &subject, Body: &body, Direction: strPtr("outbound"),
		Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: contact}},
	})
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	activityID := ids.UUID(logged.Id)

	// Before the limit, the admin's compliance read carries the subject — which
	// is correct, and is what makes the assertion after the limit about the
	// AUDIENCE rather than about the image never being there.
	if !auditImageMentions(t, e, "activity", activityID, subject) {
		t.Fatal("the audit image does not carry the subject before the limit; " +
			"the assertions below would pass whatever the audience arm did")
	}

	if _, err := e.Activities.SetAudience(author, ids.From[ids.ActivityKind](activityID),
		activities.SetAudienceInput{Audience: "participants"}); err != nil {
		t.Fatalf("author limiting: %v", err)
	}

	if auditImageMentions(t, e, "activity", activityID, subject) {
		t.Error("the admin reads a limited activity's subject through GET /audit-log — " +
			"the audience limit is exactly the disclosure this prevents")
	}

	// The row is withheld, not dropped, and everything the audience has no
	// claim over still answers.
	entries := auditEntriesFor(t, e, "activity", activityID)
	if len(entries) == 0 {
		t.Fatal("the limited activity's audit rows vanished from the trail; " +
			"withholding the image must not put a hole in the ledger")
	}
	for _, entry := range entries {
		if entry.Action == "" || entry.EntityType != "activity" || entry.OccurredAt.IsZero() {
			t.Errorf("a withheld row lost its safe markers: %+v", entry)
		}
		if entry.ActorID == "" {
			t.Errorf("a withheld row lost its actor, which the audience does not govern: %+v", entry)
		}
	}
}

// The mirror. An arm that withheld from EVERYONE would pass the test above
// while destroying the compliance read, and nothing else here would notice.
func TestAuditLogKeepsTheImageForAReaderInsideTheAudience(t *testing.T) {
	e := Setup(t)
	author := e.As(e.Rep1, []ids.UUID{e.Team1}, activityLifecyclePerms)
	contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)

	subject := "Q3 renewal terms"
	logged, _, err := e.Activities.LogActivity(author, activities.LogActivityInput{
		Kind: "email", Subject: &subject, Direction: strPtr("outbound"),
		Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: contact}},
	})
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	activityID := ids.UUID(logged.Id)

	// `selected`, naming the admin who then runs the compliance read: inside the
	// audience, so the image is theirs to see.
	if _, err := e.Activities.SetAudience(author, ids.From[ids.ActivityKind](activityID),
		activities.SetAudienceInput{
			Audience: "selected",
			Members:  []activities.AudienceMember{{SubjectType: "user", SubjectID: e.AdminUser}},
		}); err != nil {
		t.Fatalf("author limiting to a selected audience: %v", err)
	}

	if !auditImageMentions(t, e, "activity", activityID, subject) {
		t.Error("an admin NAMED in the audience cannot read the image — the arm " +
			"withholds from readers the limit admits, which breaks the compliance read")
	}
}

// A non-activity audit row has no audience to answer to and must be untouched
// by the join. entity_id is a bare uuid across every entity type, so this is
// what proves the entity_type test in the join is load-bearing rather than
// decorative.
func TestAuditLogLeavesANonActivityImageAlone(t *testing.T) {
	e := Setup(t)
	person := e.SeedPerson(t, "Dana Buyer", &e.Rep1)

	entries := auditEntriesFor(t, e, "person", person)
	if len(entries) == 0 {
		t.Fatal("seeding a person wrote no audit row; this test would assert nothing")
	}
	for _, entry := range entries {
		if entry.EntityType != "person" {
			continue
		}
		if len(entry.After) == 0 {
			continue
		}
		if strings.Contains(string(entry.After), "content_state") {
			t.Errorf("a person's audit image came back withheld: %s", entry.After)
		}
	}
}

// auditEntriesFor is the admin's compliance read narrowed to one record. The
// caller names the entity type rather than the helper guessing it: guessing
// makes the SUBJECT of the assertion depend on a database probe, so a seeding
// change could silently move a test from the redacted path to the untouched one
// and it would still pass.
func auditEntriesFor(t *testing.T, e *Env, entityType string, entity ids.UUID) []privacy.AuditEntry {
	t.Helper()
	page, err := privacy.ListAuditLog(e.Admin(), e.DB(), privacy.AuditFilter{
		EntityType: &entityType, EntityID: &entity,
	})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	return page.Entries
}

// auditImageMentions reports whether the admin's compliance read hands back the
// given text in either record image.
func auditImageMentions(t *testing.T, e *Env, entityType string, entity ids.UUID, text string) bool {
	t.Helper()
	for _, entry := range auditEntriesFor(t, e, entityType, entity) {
		if strings.Contains(string(entry.Before), text) || strings.Contains(string(entry.After), text) {
			return true
		}
	}
	return false
}
