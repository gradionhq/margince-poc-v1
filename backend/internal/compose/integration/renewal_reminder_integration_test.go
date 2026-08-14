// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The end-to-end proof for renewal_reminder's candidate source
// (customfields.DateFieldCandidates) over a real, migrated Postgres —
// timescan_integration_test.go's structural precedent, but for the
// DateFieldScan seam rather than ActivityScan. A real date-typed custom
// field is defined and written through the customfields engine and the
// people store (never a hand-inserted cf_* column or a raw UPDATE — the
// review-loop rule that a test supplying its own version of production
// proves nothing about production), and TimeScanner.ScanWorkspace runs
// against it exactly as the worker's periodic job would.
//
// Two properties this suite pins that handlers_clock_test.go's unit tests
// (which hand-build their events) cannot: that the real SQL window in
// customfields/candidates.go actually includes an in-window record and
// excludes an out-of-window one, and that its recurring MMDD projection —
// not a hand-built payload standing in for it — produces a genuinely new
// anchor (and so a genuinely new task, not a suppressed duplicate) a full
// simulated year later over the SAME stored field value.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// TestRenewalReminderFiresWithinWindowButNotBeyondIt seeds one real date
// field on person and one renewal_reminder instance watching it with
// days_before: 30 — a person 10 days out is a candidate and gets a
// reminder task, a person 40 days out is outside the window and gets
// none, proving DateFieldCandidates' literal BETWEEN shape end to end.
func TestRenewalReminderFiresWithinWindowButNotBeyondIt(t *testing.T) {
	e := Setup(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	store := people.NewStore(e.DB()).WithFieldCatalog(svc)
	fieldCtx := e.As(e.Rep1, nil, CustomFieldAdminPerms)

	field, err := svc.Create(fieldCtx, customfields.FieldSpec{
		Object: "person", Label: "Renewal Date", Type: customfields.TypeDate, Source: "ui",
	})
	if err != nil {
		t.Fatalf("defining the renewal-date field: %v", err)
	}
	col := *field.ColumnName

	// The scan evaluates against this pinned instant; days_before: 30
	// below makes [scanNow, scanNow+30d] the in-window range.
	scanNow := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	now := func() time.Time { return scanNow }

	dueSoon, err := store.CreatePerson(fieldCtx, people.CreatePersonInput{
		FullName: "Due Soon", Source: "manual",
		CustomFields: map[string]any{col: scanNow.AddDate(0, 0, 10).Format(time.DateOnly)},
	})
	if err != nil {
		t.Fatalf("creating the in-window person: %v", err)
	}
	dueLater, err := store.CreatePerson(fieldCtx, people.CreatePersonInput{
		FullName: "Due Later", Source: "manual",
		CustomFields: map[string]any{col: scanNow.AddDate(0, 0, 40).Format(time.DateOnly)},
	})
	if err != nil {
		t.Fatalf("creating the out-of-window person: %v", err)
	}

	owner := OwnerConn(t)
	seedRenewalReminder(t, owner, col, false)

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	scanner := compose.NewTimeScannerWithClock(e.DB(), now, quiet)
	if err := scanner.ScanWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), e.WS); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if got := personTaskCount(t, e, ids.UUID(dueSoon.Id)); got != 1 {
		t.Fatalf("reminder tasks for the in-window person = %d, want exactly 1", got)
	}
	if got := personTaskCount(t, e, ids.UUID(dueLater.Id)); got != 0 {
		t.Fatalf("reminder tasks for the out-of-window person = %d, want 0 — 40 days out is past the 30-day horizon", got)
	}
}

// TestRenewalReminderPreviewMatchesTheRealSeededRows is the real-Postgres
// proof PLAN.md's Task 3 asked for and the unit-only
// TestResolvePreviewRecipeRenewalReminder (automations_preview_test.go)
// cannot give: that the dynamically built previewDef's predicate — a
// literal storekit.FieldDate expression over a workspace's own cf_*
// column, quoted and validated against the LIVE customfields catalog —
// actually matches real seeded rows under storekit.CompilePredicate AND
// the row-scope clause AutomationStore.Preview applies, not just that
// resolvePreviewRecipe's pre-database refusal logic is internally
// consistent.
func TestRenewalReminderPreviewMatchesTheRealSeededRows(t *testing.T) {
	e := Setup(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	peopleStore := people.NewStore(e.DB()).WithFieldCatalog(svc)
	fieldCtx := e.As(e.Rep1, nil, CustomFieldAdminPerms)

	field, err := svc.Create(fieldCtx, customfields.FieldSpec{
		Object: "person", Label: "Renewal Date", Type: customfields.TypeDate, Source: "ui",
	})
	if err != nil {
		t.Fatalf("defining the renewal-date field: %v", err)
	}
	col := *field.ColumnName

	previewNow := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	dueSoon, err := peopleStore.CreatePerson(fieldCtx, people.CreatePersonInput{
		FullName: "Due Soon", Source: "manual",
		CustomFields: map[string]any{col: previewNow.AddDate(0, 0, 10).Format(time.DateOnly)},
	})
	if err != nil {
		t.Fatalf("creating the in-window person: %v", err)
	}
	dueLater, err := peopleStore.CreatePerson(fieldCtx, people.CreatePersonInput{
		FullName: "Due Later", Source: "manual",
		CustomFields: map[string]any{col: previewNow.AddDate(0, 0, 40).Format(time.DateOnly)},
	})
	if err != nil {
		t.Fatalf("creating the out-of-window person: %v", err)
	}

	owner := OwnerConn(t)
	id := seedRenewalReminder(t, owner, col, false)

	automationStore := automation.NewAutomationStore(e.DB()).
		WithClock(func() time.Time { return previewNow }).
		WithFieldCatalog(svc)
	// automation:Read authorizes reading the instance being previewed;
	// person:Read is the target-table read gate Preview applies on top
	// (automations_preview.go's own doc: "a preview is a read... gated
	// like a read"). RowScopeAll matches CustomFieldAdminPerms — this
	// test is proving the predicate/scope wiring works, not exercising a
	// row-scope denial (that is gate_integration_test.go's job).
	previewCtx := e.As(e.Rep1, nil, principal.Permissions{
		RoleKeys: []string{"test"},
		RowScope: principal.RowScopeAll,
		Objects: map[string]principal.ObjectGrant{
			"automation": {Read: true},
			"person":     {Read: true},
		},
	})
	result, err := automationStore.Preview(previewCtx, id, automation.AutomationPreviewInput{})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if result.MatchesNow != 1 {
		t.Fatalf("MatchesNow = %d, want exactly 1 (the in-window person only)", result.MatchesNow)
	}
	if len(result.Sample) != 1 || result.Sample[0] != ids.UUID(dueSoon.Id) {
		t.Fatalf("Sample = %v, want exactly [%s] (the in-window person, never the out-of-window one)", result.Sample, dueSoon.Id)
	}
	if result.ExcludedByPermission != 0 {
		t.Errorf("ExcludedByPermission = %d, want 0 — this caller can see everything it matched", result.ExcludedByPermission)
	}
	// WouldHaveFired asks a DIFFERENT question than MatchesNow: not "is
	// the value in [now, now+days_before] RIGHT NOW" but "did the value
	// fall in that window at ANY point in the trailing window_days" — the
	// due-soon person's (10 days out) active-match span opens
	// days_before=30 days before its OWN value, i.e. ~20 days AGO
	// (previewNow-20), which is inside the trailing 30-day estimate
	// window [previewNow-30, previewNow] even though the person was not
	// yet a MatchesNow candidate 30 days ago — so it must count here too,
	// not just in MatchesNow.
	if result.WouldHaveFired == nil || *result.WouldHaveFired != 1 {
		t.Fatalf("WouldHaveFired = %v, want exactly 1 (the due-soon person's active-match span overlaps the trailing estimate window)", result.WouldHaveFired)
	}
	_ = dueLater // asserted by absence: MatchesNow/Sample above already exclude it
}

// TestRenewalReminderRecurringAnchorReArmsEachYear proves
// TestRenewalReminderRecurringAnchorReArmsEachYear (handlers_clock_test.go)'s
// property against the REAL candidate source: a birthday-shaped field
// whose stored value never changes fires once in a scan pinned to one
// simulated year, then fires AGAIN — a genuinely new task, not a
// suppressed duplicate — when the identical stored value is rescanned a
// full year later, because customfields.DateFieldCandidates projects the
// month/day onto each scan window's own year (candidates.go's
// projectOccurrence) rather than handing back the field's literal stored
// year.
func TestRenewalReminderRecurringAnchorReArmsEachYear(t *testing.T) {
	e := Setup(t)
	svc := customfields.NewService(e.Pool, SchemaPool(t))
	store := people.NewStore(e.DB()).WithFieldCatalog(svc)
	fieldCtx := e.As(e.Rep1, nil, CustomFieldAdminPerms)

	field, err := svc.Create(fieldCtx, customfields.FieldSpec{
		Object: "person", Label: "Birthday", Type: customfields.TypeDate, Source: "ui",
	})
	if err != nil {
		t.Fatalf("defining the birthday field: %v", err)
	}
	col := *field.ColumnName

	// The stored year (1990) carries no meaning for a recurring field —
	// only the August 1st month/day does. It never changes across either
	// simulated scan below.
	celebrant, err := store.CreatePerson(fieldCtx, people.CreatePersonInput{
		FullName: "Has A Birthday", Source: "manual",
		CustomFields: map[string]any{col: "1990-08-01"},
	})
	if err != nil {
		t.Fatalf("creating the birthday-bearing person: %v", err)
	}

	owner := OwnerConn(t)
	seedRenewalReminder(t, owner, col, true)

	// The clock is mutable across two scans with the SAME scanner — the
	// same "one clock, captured per call" contract timescan_integration_test.go
	// pins, but reassigned between calls to simulate the year passing.
	var scanNow time.Time
	now := func() time.Time { return scanNow }
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	scanner := compose.NewTimeScannerWithClock(e.DB(), now, quiet)

	// Year one: August 1st projects onto 2026, inside the 30-day horizon
	// from July 20th.
	scanNow = time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	if err := scanner.ScanWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), e.WS); err != nil {
		t.Fatalf("year-one scan: %v", err)
	}
	if got := personTaskCount(t, e, ids.UUID(celebrant.Id)); got != 1 {
		t.Fatalf("reminder tasks after year one = %d, want exactly 1", got)
	}

	// Year two: the IDENTICAL stored "1990-08-01" value, rescanned a full
	// year later. A one-time field's occurrence key would suppress this as
	// the same anchor; the recurring projection re-derives August 1st 2027
	// instead, a genuinely new anchor, so a second task lands.
	scanNow = time.Date(2027, 7, 20, 9, 0, 0, 0, time.UTC)
	if err := scanner.ScanWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), e.WS); err != nil {
		t.Fatalf("year-two scan: %v", err)
	}
	if got := personTaskCount(t, e, ids.UUID(celebrant.Id)); got != 2 {
		t.Fatalf("reminder tasks after year two = %d, want exactly 2 — the recurring anchor must re-arm, not suppress as a duplicate", got)
	}
}

// TestRenewalReminderMisconfiguredInstanceDoesNotAbortTheWorkspacePass
// proves the fleet-isolation property a misconfigured renewal_reminder
// instance must never break: a workspace admin can retire a custom field
// after an automation instance already named it (or an instance can simply
// name a column that was never created), so this instance's date_field
// resolves to nothing customfields.Service.ActiveColumns recognizes as an
// active date-typed column. Before the fix, DateFieldCandidates' resulting
// customfields.ErrUnknownDateColumn propagated all the way out of
// ScanWorkspace, failing the WHOLE workspace pass — a healthy
// no_activity_reminder instance seeded in the SAME workspace never got its
// turn. The scan must instead skip the broken renewal_reminder instance
// alone and still converge the healthy instance's candidate in the same
// pass.
func TestRenewalReminderMisconfiguredInstanceDoesNotAbortTheWorkspacePass(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Gone Quiet Deal", pipeline, open, nil)

	owner := OwnerConn(t)

	scanNow := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	now := func() time.Time { return scanNow }

	// A genuine human touch old enough to make the deal a no_activity_reminder
	// candidate under the default 7-day threshold.
	firstTouch := scanNow.AddDate(0, 0, -10)
	seedGenuineTouch(t, owner, e.WS, dealID, "call", firstTouch)
	backdateCreatedAt(t, owner, "deal", dealID, firstTouch)

	seedNoActivityReminder(t, owner, e.WS)
	// A renewal_reminder instance naming a column that is not (and never
	// was) an active date-typed custom field on person — the same shape of
	// failure a retired field leaves behind, since both reach
	// DateFieldCandidates as customfields.ErrUnknownDateColumn.
	seedRenewalReminder(t, owner, "cf_does_not_exist", false)

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	scanner := compose.NewTimeScannerWithClock(e.DB(), now, quiet)

	if err := scanner.ScanWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), e.WS); err != nil {
		t.Fatalf("ScanWorkspace: %v, want nil — one misconfigured renewal_reminder instance must not fail the whole pass", err)
	}
	if got := reminderTaskCount(t, e, dealID); got != 1 {
		t.Fatalf("no_activity_reminder tasks = %d, want exactly 1 — the healthy instance must still fire despite the broken renewal_reminder instance", got)
	}
}

// seedRenewalReminder enrolls one enabled, ownerless renewal_reminder
// instance watching the given column on person — the DateFieldScan-driven
// counterpart of timescan_integration_test.go's seedNoActivityReminder.
// Ownerless (no owner_id) skips the match-time owner gate exactly like
// that precedent, since gate.go's own RBAC path is proven separately.
// object is always "person" and days_before always 30 here: every
// scenario in this suite watches a person field on the same 30-day
// horizon, so caller-given values for either would be unexercised
// parameters (T3/T8) until a test actually needs to vary one.
func seedRenewalReminder(t *testing.T, owner *pgx.Conn, dateField string, recursYearly bool) ids.AutomationID {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"object": "person", "date_field": dateField, "days_before": 30, "recurs_yearly": recursYearly,
	})
	if err != nil {
		t.Fatalf("encoding renewal_reminder params: %v", err)
	}
	id := ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO automation (id, key, name, trigger, action, params, enabled)
		 VALUES ($1, 'renewal_reminder', 'Renewal Reminder', '{"schedule":"clock"}', '{"kind":"create_task"}', $2::jsonb, true)`,
		id, params); err != nil {
		t.Fatalf("seeding the renewal_reminder instance: %v", err)
	}
	return ids.AutomationID{UUID: id}
}

// personTaskCount counts the create_task activities renewal_reminder
// minted on a person's timeline — reminderTaskCount's (timescan_integration_test.go)
// counterpart for the person entity type rather than deal.
func personTaskCount(t *testing.T, e *Env, personID ids.UUID) int {
	t.Helper()
	return e.WsCount(t, `
		SELECT count(*) FROM activity a
		JOIN activity_link al ON al.activity_id = a.id
		WHERE al.entity_type = 'person' AND al.person_id = $1 AND a.kind = 'task'`, personID)
}
