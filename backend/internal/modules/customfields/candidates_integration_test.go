// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package customfields

// The real-Postgres proofs for DateFieldCandidates
// (automation/seams.go's DateFieldScan): validation refuses an unknown or
// wrong-typed column BEFORE it reaches SQL, the literal BETWEEN shape
// answers a fixed window correctly, and the recurring MMDD shape matches
// a window that wraps Dec 31 → Jan 1 on both sides of the wrap while
// projecting each match onto the correct occurrence year. Sourced
// against a real database (rather than a fake) because the load-bearing
// behaviour here — the SQL itself, and Postgres's own to_char(...,
// 'MMDD') semantics — is exactly what a fake would paper over; the
// module-level unit tests (service_test.go) cover the pre-database
// refusal paths only. Mirrors automation's own
// autofixture_integration_test.go: seeding is spelled locally because
// the compose-layer harness cannot be imported here (modules never see
// compose, tests included — backend/arch_test.go).

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/testdb"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// candidatesFixture is the real-Postgres rig this suite shares: an owner
// connection for seeding lead rows directly (DateFieldCandidates reads a
// table it does not own the writes to, so there is no store here to seed
// through — the same posture activities/lasttouch.go documents), the
// app-role pool DateFieldCandidates itself runs on, and the
// schema-privileged pool Service.Create's DDL transaction needs to
// define the fields this suite reads back.
type candidatesFixture struct {
	owner   *pgx.Conn
	svc     *Service
	ws      ids.UUID
	ctx     context.Context
	dateCol string
	textCol string
}

func setupCandidates(t *testing.T) *candidatesFixture {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	if err := testdb.Reset(ctx, owner); err != nil {
		t.Fatal(err)
	}

	ws := ids.NewV7()
	if _, err := owner.Exec(ctx, `INSERT INTO workspace (id, slug) VALUES ($1, $2)`, ws, "candidates-"+ws.String()); err != nil {
		t.Fatal(err)
	}
	// custom_field.created_by carries a real FK to app_user (the write
	// shape stamps captured_by/created_by from the authenticated
	// principal, never the request body) — a hand-picked UUID with no
	// backing row fails that constraint, so the principal below MUST be
	// a real seeded user, mirroring automation's own
	// autofixture_integration_test.go.
	userID := ids.NewV7()
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Candidates Test')`,
		userID, ws, "candidates-test-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}

	appPool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(appPool.Close)
	schemaPool, err := database.NewPool(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(schemaPool.Close)

	svc := NewService(appPool, schemaPool)
	fctx := principal.WithActor(principal.WithCorrelationID(principal.WithWorkspaceID(ctx, ws), ids.NewV7()),
		principal.Principal{
			Type: principal.PrincipalHuman, ID: "human:candidates-test", UserID: userID,
			Permissions: principal.Permissions{
				RoleKeys: []string{"test"},
				RowScope: principal.RowScopeAll,
				Objects: map[string]principal.ObjectGrant{
					"custom_field": fullGrant(),
					"lead":         {Read: true},
				},
			},
		})

	dateField, err := svc.Create(fctx, FieldSpec{Object: "lead", Label: "Renewal date", Type: TypeDate, Source: "ui"})
	if err != nil {
		t.Fatalf("defining the date field: %v", err)
	}
	textField, err := svc.Create(fctx, FieldSpec{Object: "lead", Label: "Segment", Type: TypeText, Source: "ui"})
	if err != nil {
		t.Fatalf("defining the text field: %v", err)
	}
	return &candidatesFixture{
		owner: owner, svc: svc, ws: ws, ctx: fctx,
		dateCol: *dateField.ColumnName, textCol: *textField.ColumnName,
	}
}

// seedLead inserts one bare lead row carrying value in the fixture's date
// column — the minimal shape lead's NOT NULL columns require
// (migrations/core/0009_leads.up.sql).
func (f *candidatesFixture) seedLead(t *testing.T, value time.Time) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	query := `INSERT INTO lead (id, workspace_id, source, captured_by, ` + quoteIdentifier(f.dateCol) + `)
		VALUES ($1, $2, 'ui', 'human:test', $3)`
	if _, err := f.owner.Exec(context.Background(), query, id, f.ws, value); err != nil {
		t.Fatalf("seeding lead: %v", err)
	}
	return id
}

func TestDateFieldCandidates_RefusesAnUnknownColumn(t *testing.T) {
	f := setupCandidates(t)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	_, err := f.svc.DateFieldCandidates(f.ctx, "lead", "cf_no_such_field", from, to, false, 50)
	if !errors.Is(err, ErrUnknownDateColumn) {
		t.Fatalf("DateFieldCandidates with an unknown column = %v, want ErrUnknownDateColumn", err)
	}
}

func TestDateFieldCandidates_RefusesANonDateColumn(t *testing.T) {
	f := setupCandidates(t)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	_, err := f.svc.DateFieldCandidates(f.ctx, "lead", f.textCol, from, to, false, 50)
	if !errors.Is(err, ErrUnknownDateColumn) {
		t.Fatalf("DateFieldCandidates against a text-typed column = %v, want ErrUnknownDateColumn", err)
	}
}

func TestDateFieldCandidates_LiteralBetweenMatchesTheStoredValue(t *testing.T) {
	f := setupCandidates(t)
	inWindow := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	outWindow := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	inID := f.seedLead(t, inWindow)
	f.seedLead(t, outWindow)

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	got, err := f.svc.DateFieldCandidates(f.ctx, "lead", f.dateCol, from, to, false, 50)
	if err != nil {
		t.Fatalf("DateFieldCandidates: %v", err)
	}
	if len(got) != 1 || got[0].EntityID != inID {
		t.Fatalf("literal BETWEEN candidates = %+v, want exactly the one lead inside [from,to]", got)
	}
	if !got[0].StoredValue.Equal(inWindow) {
		t.Errorf("StoredValue = %s, want %s (the raw column value)", got[0].StoredValue, inWindow)
	}
	if !got[0].OccurrenceDate.Equal(inWindow) {
		t.Errorf("OccurrenceDate = %s, want %s — a one-time field's occurrence IS its stored value", got[0].OccurrenceDate, inWindow)
	}
}

// TestDateFieldCandidates_RecurringWrapsTheYearBoundary seeds one lead on
// each side of a Dec 20 → Jan 15 window (Dec 24 and Jan 10), plus one
// clearly outside it (Jul 1), and asserts the wraparound OR-of-two-ranges
// shape catches both matches, excludes the outlier, and projects each
// match's occurrence onto the correct year: the December side re-uses
// the window's OWN (earlier) year, the January side advances to the
// window's later year — exactly a birthday recurring across a New Year's
// boundary.
func TestDateFieldCandidates_RecurringWrapsTheYearBoundary(t *testing.T) {
	f := setupCandidates(t)
	// Stored years are deliberately unrelated to the scan window's years —
	// a recurring field's own stored year carries no meaning (candidates.go's
	// projectOccurrence doc).
	decSide := f.seedLead(t, time.Date(1990, 12, 24, 0, 0, 0, 0, time.UTC))
	janSide := f.seedLead(t, time.Date(1985, 1, 10, 0, 0, 0, 0, time.UTC))
	f.seedLead(t, time.Date(2000, 7, 1, 0, 0, 0, 0, time.UTC)) // clearly outside

	from := time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC)
	to := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)
	got, err := f.svc.DateFieldCandidates(f.ctx, "lead", f.dateCol, from, to, true, 50)
	if err != nil {
		t.Fatalf("DateFieldCandidates: %v", err)
	}
	byID := map[ids.UUID]DateFieldCandidate{}
	for _, c := range got {
		byID[c.EntityID] = c
	}
	if len(got) != 2 {
		t.Fatalf("recurring wraparound candidates = %+v, want exactly 2 (the Dec and Jan sides, never the July outlier)", got)
	}
	decCand, ok := byID[decSide]
	if !ok {
		t.Fatal("the Dec 24 lead did not match the Dec 20 → Jan 15 recurring window")
	}
	wantDec := time.Date(2026, 12, 24, 0, 0, 0, 0, decCand.OccurrenceDate.Location())
	if !decCand.OccurrenceDate.Equal(wantDec) {
		t.Errorf("Dec-side OccurrenceDate = %s, want %s (the window's OWN/earlier year)", decCand.OccurrenceDate, wantDec)
	}
	janCand, ok := byID[janSide]
	if !ok {
		t.Fatal("the Jan 10 lead did not match the Dec 20 → Jan 15 recurring window")
	}
	wantJan := time.Date(2027, 1, 10, 0, 0, 0, 0, janCand.OccurrenceDate.Location())
	if !janCand.OccurrenceDate.Equal(wantJan) {
		t.Errorf("Jan-side OccurrenceDate = %s, want %s (the window's later year)", janCand.OccurrenceDate, wantJan)
	}
}
