// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The field-history read (GET /field-history): a per-record, per-field
// change timeline projected from the audit spine's before/after images.
// Gated exactly like every other record read — human-only, object-read,
// and row-scope visibility — and paginated on entry count without ever
// splitting one audit row's entries across two pages.

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// seedAuditActionRow inserts a raw audit row with a controlled verb and
// before/after payload — the projection's input. INSERT is the one verb
// the append-only trigger admits, and the workspace tx satisfies RLS.
// before/after are marshaled to jsonb bytes explicitly: pgx does not
// accept a bare map[string]any for a jsonb column without a registered
// type, the same reason storekit.Audit marshals before binding.
func seedAuditActionRow(t *testing.T, e *Env, action, entityType string, entityID ids.UUID,
	actorType string, before, after map[string]any, occurredAt time.Time,
) ids.UUID {
	t.Helper()
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		t.Fatalf("marshal before: %v", err)
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		t.Fatalf("marshal after: %v", err)
	}
	rowID := ids.NewV7()
	ctx := principal.WithWorkspaceID(t.Context(), e.WS)
	err = database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO audit_log (id, workspace_id, actor_type, actor_id, action,
			                        entity_type, entity_id, before, after, occurred_at)
			 VALUES ($1, $2, $3, 'user-1', $4, $5, $6, $7, $8, $9)`,
			rowID, e.WS, actorType, action, entityType, entityID, beforeJSON, afterJSON, occurredAt)
		return err
	})
	if err != nil {
		t.Fatalf("seed audit row: %v", err)
	}
	return rowID
}

// seedAuditDiffRow is seedAuditActionRow fixed to the plain update verb —
// the shape most projection tests exercise.
func seedAuditDiffRow(t *testing.T, e *Env, entityType string, entityID ids.UUID,
	actorType string, before, after map[string]any, occurredAt time.Time,
) ids.UUID {
	t.Helper()
	return seedAuditActionRow(t, e, "update", entityType, entityID, actorType, before, after, occurredAt)
}

func TestFieldHistoryGatesOnReadPermissionAndVisibility(t *testing.T) {
	e := Setup(t)
	// Owned by Rep1 (Team1): an ownerless record is workspace-shared at
	// every tier (decisions/0006), so the out-of-scope assertion below
	// needs a real owner to exclude the Team2-only caller.
	personID := e.SeedPerson(t, "History Subject", &e.Rep1)

	// Rep3 sits only in Team2, which shares no membership with the
	// record's owner: 404, not an empty page — existence-hiding on the
	// row-scope gate like every record read.
	outsiderCtx := e.As(e.Rep3, []ids.UUID{e.Team2}, RepPerms)
	_, err := privacy.ListFieldHistory(outsiderCtx, e.Pool, privacy.FieldHistoryFilter{
		EntityType: "person", EntityID: personID,
	})
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("out-of-scope read: err = %v, want not found", err)
	}

	// A principal without person:read at all: 403 before any row is touched.
	noReadCtx := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{RowScope: principal.RowScopeTeam})
	if _, err := privacy.ListFieldHistory(noReadCtx, e.Pool, privacy.FieldHistoryFilter{
		EntityType: "person", EntityID: personID,
	}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("no-permission read: err = %v, want permission denied", err)
	}
}

func TestFieldHistoryProjectsDiffsNewestFirst(t *testing.T) {
	e := Setup(t)
	personID := e.SeedPerson(t, "Diff Subject", nil)
	// SeedPerson's own create-audit row is stamped at real "now"; the two
	// diff rows below must land unambiguously after it, so they are
	// dated forward rather than back-dated off a since-elapsed "now".
	older := time.Now().Add(1 * time.Hour).UTC().Truncate(time.Microsecond)
	newer := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Microsecond)

	seedAuditDiffRow(t, e, "person", personID, "human",
		map[string]any{"email": "old@x.com", "name": "Same"},
		map[string]any{"email": "new@x.com", "name": "Same"}, older)
	seedAuditDiffRow(t, e, "person", personID, "human",
		map[string]any{"phone": "111"},
		map[string]any{"phone": "222"}, newer)

	page, err := privacy.ListFieldHistory(e.Admin(), e.Pool, privacy.FieldHistoryFilter{
		EntityType: "person", EntityID: personID,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// SeedPerson's own create-audit row may contribute entries; the two
	// seeded rows' fields must appear in newest-first row order with the
	// unchanged key absent.
	var fields []string
	for _, en := range page.Entries {
		fields = append(fields, en.Field)
	}
	if len(fields) < 2 || fields[0] != "phone" {
		t.Fatalf("newest row's field must lead: %v", fields)
	}
	for _, f := range fields {
		if f == "name" {
			t.Error("unchanged field emitted an entry — fabricated timeline")
		}
	}
}

func TestFieldHistoryActorAndFieldFilters(t *testing.T) {
	e := Setup(t)
	personID := e.SeedPerson(t, "Filter Subject", nil)
	base := time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)

	seedAuditDiffRow(t, e, "person", personID, "human",
		map[string]any{"label": "h1"}, map[string]any{"label": "h2"}, base)
	seedAuditDiffRow(t, e, "person", personID, "agent",
		map[string]any{"label": "a1", "score": "1"},
		map[string]any{"label": "a2", "score": "2"}, base.Add(time.Second))

	agent := "agent"
	page, err := privacy.ListFieldHistory(e.Admin(), e.Pool, privacy.FieldHistoryFilter{
		EntityType: "person", EntityID: personID, ActorType: &agent,
	})
	if err != nil {
		t.Fatalf("actor filter: %v", err)
	}
	for _, en := range page.Entries {
		if en.ActorType != "agent" {
			t.Errorf("actor filter leaked a %s entry", en.ActorType)
		}
	}
	if len(page.Entries) != 2 {
		t.Errorf("agent entries = %d, want 2 (label, score)", len(page.Entries))
	}

	label := "label"
	page, err = privacy.ListFieldHistory(e.Admin(), e.Pool, privacy.FieldHistoryFilter{
		EntityType: "person", EntityID: personID, Field: &label,
	})
	if err != nil {
		t.Fatalf("field filter: %v", err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("field filter entries = %d, want 2 (one per seeded row)", len(page.Entries))
	}
	for _, en := range page.Entries {
		if en.Field != "label" {
			t.Errorf("field filter leaked %q", en.Field)
		}
	}
}

func TestFieldHistoryPaginationPreservesRowBoundaries(t *testing.T) {
	e := Setup(t)
	// SeedOrg's own create audit row (before=nil, after={display_name})
	// is a real projected third row — create carries honest field images,
	// so the action allowlist keeps it — so it plays the true oldest row
	// (a one-field genesis) instead of fighting to exclude it. rOldest
	// and rNewest are dated forward from it so ordering is unambiguous
	// regardless of clock skew between the test process and Postgres.
	orgID := e.SeedOrg(t, "Paging Org", nil)
	older := time.Now().Add(1 * time.Hour).UTC().Truncate(time.Microsecond)
	newer := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Microsecond)

	// rOldest is a two-field update: it must fill (and overflow) a
	// limit=1 page whole, and — with the genesis row still following —
	// must honestly report more, not falsely claim exhaustion.
	rOldest := seedAuditDiffRow(t, e, "organization", orgID, "human",
		nil, map[string]any{"industry": "Tech", "name": "Acme"}, older)
	rNewest := seedAuditDiffRow(t, e, "organization", orgID, "human",
		map[string]any{"phone": "1"}, map[string]any{"phone": "2"}, newer)

	one := 1
	page1, err := privacy.ListFieldHistory(e.Admin(), e.Pool, privacy.FieldHistoryFilter{
		EntityType: "organization", EntityID: orgID, Limit: &one,
	})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Entries) != 1 || page1.Entries[0].ID != rNewest {
		t.Fatalf("page1 = %+v, want exactly rNewest's single entry", page1.Entries)
	}
	if !page1.HasMore || page1.NextCursor == "" {
		t.Fatal("page1 must report more (rOldest and the genesis row follow)")
	}

	page2, err := privacy.ListFieldHistory(e.Admin(), e.Pool, privacy.FieldHistoryFilter{
		EntityType: "organization", EntityID: orgID, Limit: &one, Cursor: &page1.NextCursor,
	})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Entries) != 2 {
		t.Fatalf("page2 entries = %d, want 2 — a row's entries never split across pages", len(page2.Entries))
	}
	for _, en := range page2.Entries {
		if en.ID != rOldest {
			t.Errorf("page2 entry from row %v, want rOldest %v", en.ID, rOldest)
		}
	}
	if !page2.HasMore || page2.NextCursor == "" {
		t.Fatal("page2 fills exactly on a row boundary but the genesis row still follows — has_more must not lie")
	}

	// The genesis row is the true last row: a real page boundary with
	// nothing behind it must report genuine exhaustion, empty cursor.
	page3, err := privacy.ListFieldHistory(e.Admin(), e.Pool, privacy.FieldHistoryFilter{
		EntityType: "organization", EntityID: orgID, Limit: &one, Cursor: &page2.NextCursor,
	})
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(page3.Entries) != 1 || page3.Entries[0].Field != "display_name" {
		t.Fatalf("page3 = %+v, want exactly the genesis row's display_name entry", page3.Entries)
	}
	if page3.HasMore || page3.NextCursor != "" {
		t.Error("page3 is genuine exhaustion — has_more must not lie at the true end")
	}
}

// TestFieldHistoryForActivityDispatchesToLinkWalkVisibility covers
// entity_type=activity specifically: activity carries no owner_id, so its
// row-scope goes through the link-walk (auth.EnsureActivityVisible), never
// the generic owner-scoped auth.EnsureVisible, which does not even know
// the "activity" table.
func TestFieldHistoryForActivityDispatchesToLinkWalkVisibility(t *testing.T) {
	e := Setup(t)
	// Owned by Rep1 (Team1): the activity's own visibility rides its
	// link to this person, so a real owner is needed to exclude the
	// Team2-only caller below.
	myPerson := e.SeedPerson(t, "Field History Subject", &e.Rep1)
	admin := e.Admin()

	activity, _, err := e.Activities.LogActivity(admin, activities.LogActivityInput{
		Kind: "note", Subject: strPtr("Pricing call"), Source: "manual",
		Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: myPerson}},
	})
	if err != nil {
		t.Fatal(err)
	}
	activityID := ids.UUID(activity.Id)

	occurredAt := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	seedAuditDiffRow(t, e, "activity", activityID, "human",
		map[string]any{"subject": "Pricing call"},
		map[string]any{"subject": "Pricing call (updated)"}, occurredAt)

	// Rep1 shares Team1 with the linked person's owner: in scope, sees
	// the diff.
	inScope := e.As(e.Rep1, []ids.UUID{e.Team1}, repPermsWithActivity())
	page, err := privacy.ListFieldHistory(inScope, e.Pool, privacy.FieldHistoryFilter{
		EntityType: "activity", EntityID: activityID,
	})
	if err != nil {
		t.Fatalf("in-scope activity field-history: %v", err)
	}
	var sawSubject bool
	for _, en := range page.Entries {
		if en.Field == "subject" {
			sawSubject = true
		}
	}
	if !sawSubject {
		t.Fatalf("in-scope caller did not see the subject diff: %+v", page.Entries)
	}

	// Rep3 sits only in Team2, which shares no membership with the
	// linked person's owner: 404, existence-hiding like every other
	// row-scope miss.
	outOfScope := e.As(e.Rep3, []ids.UUID{e.Team2}, repPermsWithActivity())
	if _, err := privacy.ListFieldHistory(outOfScope, e.Pool, privacy.FieldHistoryFilter{
		EntityType: "activity", EntityID: activityID,
	}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("out-of-scope activity field-history: err = %v, want not found", err)
	}
}

// TestFieldHistoryStopsAtErasureBoundary proves the Art. 17 guarantee:
// the audit spine is append-only, so an erasure cannot rewrite the
// historical before/after images that hold the subject's PII — the
// projection must enforce the scrub instead. After the REAL erasure
// engine runs, the tombstone row and everything older are withheld from
// every caller, including the unbounded admin; only changes made after
// the scrub project.
func TestFieldHistoryStopsAtErasureBoundary(t *testing.T) {
	e := Setup(t)
	personID := e.SeedPerson(t, "Selma Subject", nil)
	// Pre-erasure PII images, backdated so the erasure tombstone (stamped
	// at real now) is unambiguously newer.
	past := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Microsecond)
	seedAuditDiffRow(t, e, "person", personID, "human",
		map[string]any{"email": "selma@example.com", "full_name": "Selma Subject"},
		map[string]any{"email": "selma.subject@example.com", "full_name": "Selma S."}, past)

	if err := privacy.NewEraser(e.Pool).ErasePerson(e.Admin(), personID, "dsr"); err != nil {
		t.Fatalf("erase: %v", err)
	}

	page, err := privacy.ListFieldHistory(e.Admin(), e.Pool, privacy.FieldHistoryFilter{
		EntityType: "person", EntityID: personID,
	})
	if err != nil {
		t.Fatalf("post-erasure list: %v", err)
	}
	if len(page.Entries) != 0 || page.HasMore || page.NextCursor != "" {
		t.Fatalf("pre-erasure history re-surfaced past the tombstone: %+v", page.Entries)
	}

	// The boundary is a cut, not a ban: a change made AFTER the scrub is
	// ordinary history again.
	future := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	seedAuditDiffRow(t, e, "person", personID, "human",
		nil, map[string]any{"owner_id": "rep-2"}, future)
	page, err = privacy.ListFieldHistory(e.Admin(), e.Pool, privacy.FieldHistoryFilter{
		EntityType: "person", EntityID: personID,
	})
	if err != nil {
		t.Fatalf("post-scrub change list: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Field != "owner_id" {
		t.Fatalf("want exactly the post-erasure owner_id entry, got %+v", page.Entries)
	}
}

// TestFieldHistoryProjectsOnlyFieldImageVerbs pins the action allowlist
// end to end: verbs whose payloads are evidence maps (merge relink
// counts, export receipts) must not fabricate field entries, while the
// honest create/update rows around them still project.
func TestFieldHistoryProjectsOnlyFieldImageVerbs(t *testing.T) {
	e := Setup(t)
	personID := e.SeedPerson(t, "Meta Subject", nil)
	base := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)

	seedAuditActionRow(t, e, "merge", "person", personID, "human",
		map[string]any{"merged_into_id": nil},
		map[string]any{
			"merged_into_id": ids.NewV7(),
			"relinked":       map[string]any{"activities": 3},
			"filled":         map[string]any{"title": "CTO"},
		}, base)
	seedAuditActionRow(t, e, "export", "person", personID, "human",
		nil, map[string]any{"format": "sar_json"}, base.Add(time.Second))
	seedAuditDiffRow(t, e, "person", personID, "human",
		map[string]any{"title": "VP"}, map[string]any{"title": "CTO"}, base.Add(2*time.Second))

	page, err := privacy.ListFieldHistory(e.Admin(), e.Pool, privacy.FieldHistoryFilter{
		EntityType: "person", EntityID: personID,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	fields := map[string]bool{}
	for _, en := range page.Entries {
		fields[en.Field] = true
	}
	for _, fabricated := range []string{"merged_into_id", "relinked", "filled", "format"} {
		if fields[fabricated] {
			t.Errorf("meta payload key %q projected as a field change", fabricated)
		}
	}
	// The honest rows still project: the seeded update and SeedPerson's
	// own create-genesis row.
	if !fields["title"] || !fields["full_name"] {
		t.Errorf("honest field images went missing: %v", fields)
	}
}

func TestFieldHistoryHonestEmptyForVisibleRecordWithNoMatches(t *testing.T) {
	e := Setup(t)
	personID := e.SeedPerson(t, "Quiet Subject", nil)
	ghost := "field_that_never_changed"
	page, err := privacy.ListFieldHistory(e.Admin(), e.Pool, privacy.FieldHistoryFilter{
		EntityType: "person", EntityID: personID, Field: &ghost,
	})
	if err != nil {
		t.Fatalf("empty history must not error: %v", err)
	}
	if page.Entries == nil || len(page.Entries) != 0 || page.HasMore {
		t.Fatalf("want honest empty page (non-nil, zero entries): %+v", page)
	}
}
