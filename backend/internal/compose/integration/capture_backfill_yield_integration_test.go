// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// What a backfill reports as its reach must be what IT created. The hero number
// on the activation screen is the user's evidence that the import was worth its
// spend, so it is counted by the pages that did the work — not inferred from
// every connector-created row that happens to share the run's clock window.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/capture/mailmap"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// mailPageConnector serves its whole fixture as ONE backfill page, through the
// production mailmap → Sink path — so the counterparties the run yields are the
// ones the real resolver creates, not seeded rows standing in for them.
type mailPageConnector struct {
	raws [][]byte
	sent map[string]bool // Message-IDs the provider filed as the owner's own sent mail
	// afterMessage hands control to the test once a message has been captured
	// and its counterparties resolved, so the live tally is observable MID-page.
	afterMessage func()
}

func (m *mailPageConnector) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		Name: "gmail", Version: "1",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierAutoExecute,
		Produces: []datasource.EntityType{datasource.EntityActivity},
	}
}

func (m *mailPageConnector) Authenticate(context.Context, connector.AuthRequest) (connector.Auth, error) {
	return connector.Auth("token"), nil
}

func (m *mailPageConnector) Sync(_ context.Context, _ connector.Auth, cursor connector.Cursor, _ connector.Sink) (connector.Cursor, error) {
	return cursor, nil
}

func (m *mailPageConnector) Normalize(context.Context, connector.RawRecord) ([]connector.NormalizedRecord, error) {
	return nil, connector.ErrSkip
}

func (m *mailPageConnector) HealthCheck(context.Context, connector.Auth) error { return nil }

func (m *mailPageConnector) EstimateBackfill(context.Context, connector.Auth, time.Time) (int, error) {
	return len(m.raws), nil
}

func (m *mailPageConnector) BackfillPage(ctx context.Context, _ connector.Auth, _ time.Time, _ string, sink connector.Sink) (connector.BackfillPageResult, error) {
	res := connector.BackfillPageResult{}
	for _, raw := range m.raws {
		msg, err := mailmap.Parse(raw, captureOwner)
		if err != nil {
			return connector.BackfillPageResult{}, err
		}
		res.Scanned++
		if _, drop := msg.SkipReason(); drop {
			res.Skipped++
			continue
		}
		msg = msg.AttestSentByOwner(m.sent[msg.ID()])
		if _, err := sink.Upsert(ctx, msg.ToRecord("gmail", raw)); err != nil {
			return connector.BackfillPageResult{}, err
		}
		res.Captured++
		connector.BackfillProgressFrom(ctx).Observed(ctx, res.Scanned, res.Captured, res.Skipped)
		if m.afterMessage != nil {
			m.afterMessage()
		}
	}
	return res, nil
}

func TestBackfillCountsOnlyTheCounterpartiesItsOwnPagesCreated(t *testing.T) {
	e := setupSearch(t)
	seedCaptureRole(t, e)
	// The production wiring, because the counters under test are filled by the
	// real auto-create resolver: a bare Sink creates nothing to count.
	registry := compose.NewCaptureRegistry(e.Pool, newTestKeyvault(t, e), compose.CaptureConfig{})
	registry.Register(&mailPageConnector{
		raws: [][]byte{
			// Two free-mail senders: a personal mailbox is a person and never a
			// company, so each is one person and no organization.
			email("alice@gmail.com", "Alice Example", captureOwner, "y1@gmail.com", ""),
			email("bob@gmail.com", "Bob Example", captureOwner, "y2@gmail.com", ""),
			// The owner's own attested send makes its recipient a counterparty by
			// demonstrated intent: one person AND their company.
			email(captureOwner, "", "dave@globex.example", "y3@myco.example", ""),
		},
		sent: map[string]bool{"y3@myco.example": true},
	})

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	if _, err := registry.Connect(grantCtx, "gmail", connector.Auth("refresh")); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	rep := ids.From[ids.UserKind](e.Rep1)
	run, err := registry.StartBackfill(grantCtx, "gmail", rep, 6, 3, enqueueNothing)
	if err != nil {
		t.Fatalf("StartBackfill: %v", err)
	}

	// What must NOT be credited to this run, all landing inside its clock
	// window: another gmail connection's captures, and a human's own typing.
	seedForeignCounterparties(t, e)

	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	done, _, retryAfter, err := registry.RunBackfillStep(wsCtx, run.ID)
	if err != nil || !done || retryAfter != 0 {
		t.Fatalf("the single page must finish the run: done=%v retryAfter=%v err=%v", done, retryAfter, err)
	}

	status, err := registry.BackfillStatus(grantCtx, "gmail", rep)
	if err != nil || status == nil {
		t.Fatalf("BackfillStatus: %v (run=%v)", err, status)
	}
	if status.People != 3 {
		t.Fatalf("people = %d, want 3 — the two free-mail senders and the attested recipient, and nothing else", status.People)
	}
	if status.Organizations != 1 {
		t.Fatalf("organizations = %d, want 1 — free mail derives no company, the corporate domain does", status.Organizations)
	}

	// The persisted columns are the proof the run counted at page-commit time
	// rather than the read inferring it: BackfillYields and the cost estimator
	// read these, and a live query could never serve them.
	people, orgs := readBackfillYieldColumns(t, e, run.ID)
	if people != 3 || orgs != 1 {
		t.Fatalf("stored people_created=%d organizations_created=%d, want 3/1", people, orgs)
	}
}

func TestBackfillYieldsAreVisibleWhileThePageRuns(t *testing.T) {
	// The counterparty half of the live tally. The Sink counts a person or an
	// organization as it creates one, so the two numbers beside "emails
	// captured" have to move during the page as well — a screen where only the
	// mail count advances tells the user the import found nobody.
	e := setupSearch(t)
	seedCaptureRole(t, e)
	prov := &mailPageConnector{
		raws: [][]byte{
			email("alice@gmail.com", "Alice Example", captureOwner, "y1@gmail.com", ""),
			email(captureOwner, "", "dave@globex.example", "y3@myco.example", ""),
		},
		sent: map[string]bool{"y3@myco.example": true},
	}
	// The production wiring, because the counters under test are filled by the
	// real auto-create resolver. Unpaced, because a two-message fixture walks
	// well inside one pacing window.
	registry := compose.NewCaptureRegistry(e.Pool, newTestKeyvault(t, e), compose.CaptureConfig{}).
		WithProgressPacing(0)
	registry.Register(prov)

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	if _, err := registry.Connect(grantCtx, "gmail", connector.Auth("refresh")); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	rep := ids.From[ids.UserKind](e.Rep1)
	run, err := registry.StartBackfill(grantCtx, "gmail", rep, 6, 2, enqueueNothing)
	if err != nil {
		t.Fatalf("StartBackfill: %v", err)
	}

	var midPagePeople, midPageOrganizations int
	prov.afterMessage = func() {
		status, err := registry.BackfillStatus(grantCtx, "gmail", rep)
		if err != nil || status == nil {
			t.Fatalf("mid-page status read: %v (run=%v)", err, status)
		}
		midPagePeople, midPageOrganizations = status.People, status.Organizations
	}

	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	if done, _, _, err := registry.RunBackfillStep(wsCtx, run.ID); err != nil || !done {
		t.Fatalf("the single page must finish the run: done=%v err=%v", done, err)
	}

	// Read after the LAST message but before the commit: both counterparty
	// kinds were already visible.
	if midPagePeople != 2 {
		t.Fatalf("mid-page people = %d, want 2 — the free-mail sender and the attested recipient, before the page committed", midPagePeople)
	}
	if midPageOrganizations != 1 {
		t.Fatalf("mid-page organizations = %d, want 1 — the corporate domain, before the page committed", midPageOrganizations)
	}

	// The commit folds the same creations into the committed columns and
	// clears the transient copy, so they are reported once and not twice.
	inflightPeople, inflightOrganizations := readBackfillInflightYields(t, e, run.ID)
	if inflightPeople != 0 || inflightOrganizations != 0 {
		t.Fatalf("after the commit inflight yields = %d people / %d organizations, want them cleared", inflightPeople, inflightOrganizations)
	}
	status, err := registry.BackfillStatus(grantCtx, "gmail", rep)
	if err != nil || status == nil {
		t.Fatalf("BackfillStatus: %v (run=%v)", err, status)
	}
	if status.People != 2 || status.Organizations != 1 {
		t.Fatalf("after the commit = %d people / %d organizations, want exactly the page's 2/1", status.People, status.Organizations)
	}
}

// readBackfillInflightYields reads the transient yield columns directly — the
// proof they are cleared, which the summed status read alone cannot show.
func readBackfillInflightYields(t *testing.T, e *searchEnv, id ids.UUID) (people, organizations int) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.Admin(), `
			SELECT inflight_people, inflight_organizations FROM capture_backfill WHERE id = $1`, id).
			Scan(&people, &organizations)
	})
	if err != nil {
		t.Fatal(err)
	}
	return people, organizations
}

// seedForeignCounterparties lands the rows a workspace-wide, clock-windowed
// count would wrongly credit to the run under test: three counterparties
// indistinguishable from a second gmail connection's captures, and one person a
// human typed in.
func seedForeignCounterparties(t *testing.T, e *searchEnv) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		for _, q := range []string{
			`INSERT INTO person (workspace_id, full_name, source, captured_by)
			   VALUES (current_setting('app.workspace_id')::uuid, 'Other Connection One', 'capture', 'connector:gmail'),
			          (current_setting('app.workspace_id')::uuid, 'Other Connection Two', 'capture', 'connector:gmail'),
			          (current_setting('app.workspace_id')::uuid, 'Other Connection Three', 'capture', 'connector:gmail')`,
			`INSERT INTO person (workspace_id, full_name, source, captured_by)
			   VALUES (current_setting('app.workspace_id')::uuid, 'Manually Typed', 'manual', 'human:someone')`,
			`INSERT INTO organization (workspace_id, display_name, source, captured_by)
			   VALUES (current_setting('app.workspace_id')::uuid, 'Other Connection Co', 'capture', 'connector:gmail')`,
		} {
			if _, execErr := tx.Exec(e.Admin(), q); execErr != nil {
				return execErr
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed foreign counterparties: %v", err)
	}
}

func readBackfillYieldColumns(t *testing.T, e *searchEnv, id ids.UUID) (people, organizations int) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.Admin(), `
			SELECT people_created, organizations_created FROM capture_backfill WHERE id = $1`, id).
			Scan(&people, &organizations)
	})
	if err != nil {
		t.Fatal(err)
	}
	return people, organizations
}
