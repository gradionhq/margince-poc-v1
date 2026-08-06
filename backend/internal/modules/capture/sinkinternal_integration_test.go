// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// The internal-mail exclusion over a real migrated Postgres (ADR-0082/A127).
//
// This has to be an integration test and not a unit one. The claim is that a
// colleague's message leaves NO row behind — not in activity, not in
// activity_participant, not in raw_capture, not in audit_log — and "no row in
// four tables" is only provable against the schema that has those tables and
// the transaction that would have written them. The bug being closed was
// exactly a case where one of those four was written while the others were not.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// bootstrapInternalMailWorkspace seeds a workspace that has registered acme.com
// as its own domain, and returns a context bound to it.
func bootstrapInternalMailWorkspace(t *testing.T, ownDomains ...string) (context.Context, *pgxpool.Pool) {
	t.Helper()
	owner, pool := setupCaptureDB(t)
	ctx := context.Background()

	wsUUID := ids.NewV7()
	slug := "internal-mail-" + wsUUID.String()
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Internal Mail', $2, 'EUR')`,
		wsUUID, slug); err != nil {
		t.Fatalf("seeding workspace: %v", err)
	}
	if len(ownDomains) > 0 {
		// Registered the way cold start does it: the installation's own company
		// claims these domains. That claim, not a row in the capture registry,
		// is what makes them count.
		orgID := ids.NewV7()
		if _, err := owner.Exec(ctx, `
			INSERT INTO organization (id, workspace_id, display_name, is_anchor, source, captured_by)
			VALUES ($1, $2, 'Our Company', true, 'manual', 'human:test')`, orgID, wsUUID); err != nil {
			t.Fatalf("seeding the anchor company: %v", err)
		}
		for i, d := range ownDomains {
			if _, err := owner.Exec(ctx, `
				INSERT INTO organization_domain (workspace_id, organization_id, domain, is_primary, source, captured_by)
				VALUES ($1, $2, $3, $4, 'manual', 'human:test')`, wsUUID, orgID, d, i == 0); err != nil {
				t.Fatalf("registering own domain %s: %v", d, err)
			}
		}
	}
	return mailSinkContext(ctx, wsUUID), pool
}

// mailSinkContext binds the per-user mail connector principal the sync loop
// mints.
func mailSinkContext(ctx context.Context, ws ids.UUID) context.Context {
	ctx = principal.WithWorkspaceID(ctx, ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalConnector,
		ID:   "connector:imap",
		Permissions: principal.Permissions{
			RoleKeys: []string{"capture"},
			Objects: map[string]principal.ObjectGrant{
				"activity": {Create: true},
				"person":   {Create: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// mailRecord builds one captured mail record naming addresses.
func mailRecord(sourceID string, counterparty string, addresses ...string) connector.NormalizedRecord {
	return connector.NormalizedRecord{
		EntityType: "activity",
		NaturalKey: connector.NaturalKey{SourceSystem: "imap", SourceID: sourceID},
		Fields: capture.ActivityFields{
			Kind:      "email",
			Subject:   "Salary review for the team",
			Body:      "Confidential: the numbers we agreed for next quarter.",
			Direction: connector.DirectionInbound,
		},
		Source:     "imap:" + sourceID,
		CapturedBy: "connector:imap",
		Raw:        []byte("From: " + counterparty + "\r\nSubject: Salary review\r\n\r\nBody."),
		Counterparty: connector.Counterparty{
			Email:     counterparty,
			Domain:    counterparty[strings.LastIndex(counterparty, "@")+1:],
			Direction: connector.DirectionInbound,
		},
		Addresses: addresses,
	}
}

// countsFor reports how many rows each of the four tables holds for one natural
// key. Four counts rather than one: the defect this closes wrote the activity
// while suppressing the records around it, so any single count would have
// looked correct while the body was on the timeline.
func countsFor(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sourceID string) (activities, participants, raws, audits int) {
	t.Helper()
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM activity WHERE source_id = $1`, sourceID).Scan(&activities); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM activity_participant p
			  JOIN activity a ON a.id = p.activity_id
			 WHERE a.source_id = $1`, sourceID).Scan(&participants); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM raw_capture WHERE source_id = $1`, sourceID).Scan(&raws); err != nil {
			return err
		}
		// Audit rows key on the activity's id, and a dropped message has no
		// activity to key on — so the claim is made over the whole workspace,
		// which each test bootstraps fresh. "This workspace holds no activity
		// audit row at all" is the stronger statement anyway.
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE entity_type = 'activity'`).Scan(&audits)
	}); err != nil {
		t.Fatalf("counting rows for %s: %v", sourceID, err)
	}
	return activities, participants, raws, audits
}

// breadcrumbReasons returns the reasons the operational ledger recorded for one
// natural key.
func breadcrumbReasons(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sourceID string) []string {
	t.Helper()
	var out []string
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT detail->>'reason' FROM system_log
			  WHERE detail->>'source_id' = $1 ORDER BY occurred_at`, sourceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var reason string
			if err := rows.Scan(&reason); err != nil {
				return err
			}
			out = append(out, reason)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("reading the operational ledger: %v", err)
	}
	return out
}

// CAP-AC1.3 — a message whose participants are all internal leaves nothing
// behind, and says so in the ledger.
//
// The subject and body in the fixture are the point: before this gate, that
// text was written to activity, carried no links because the colleague was
// suppressed as a record, and a link-less activity reads as a workspace-shared
// note — so every colleague could open it on the global timeline and find it in
// search.
func TestAnAllInternalMessageLeavesNoRowInAnyTable(t *testing.T) {
	ctx, pool := bootstrapInternalMailWorkspace(t, "acme.com")
	sink := capture.NewSink(pool)

	_, err := sink.Upsert(ctx, mailRecord("internal-1", "boss@acme.com",
		"boss@acme.com", "rep@acme.com", "hr@mail.acme.com"))
	if !isSkip(err) {
		t.Fatalf("Upsert of an all-internal message: got %v, want a skip", err)
	}

	activities, participants, raws, audits := countsFor(ctx, t, pool, "internal-1")
	if activities != 0 || participants != 0 || raws != 0 || audits != 0 {
		t.Errorf("all-internal message left rows behind: activity=%d participant=%d raw_capture=%d audit_log=%d, want 0 in each",
			activities, participants, raws, audits)
	}

	// The drop is provable, not merely asserted: the ledger row is what makes
	// "colleague mail is never ingested" checkable after the fact.
	reasons := breadcrumbReasons(ctx, t, pool, "internal-1")
	if len(reasons) != 1 || reasons[0] != "internal_only" {
		t.Errorf("ledger reasons = %v, want exactly one internal_only", reasons)
	}
}

// CAP-AC1.3a, storage half — a colleague's message copying a prospect is
// captured, and the colleague remains its author.
//
// Authorship is asserted because it drives reply detection: a party substituted
// into it would be reported as having written mail they did not. Which party
// the creation ladder is ABOUT is the other half, and needs a wired ensurer —
// it is asserted in compose's capture_autocreate_integration_test.go.
func TestAColleaguesMessageCopyingAProspectIsCapturedAndKeepsItsAuthor(t *testing.T) {
	ctx, pool := bootstrapInternalMailWorkspace(t, "acme.com")
	sink := capture.NewSink(pool)

	rec := mailRecord("intro-1", "colleague@acme.com",
		"colleague@acme.com", "rep@acme.com", "buyer@customer.example")
	if _, err := sink.Upsert(ctx, rec); err != nil {
		t.Fatalf("an introduction must be captured: %v", err)
	}

	activities, _, raws, _ := countsFor(ctx, t, pool, "intro-1")
	if activities != 1 {
		t.Errorf("activity rows = %d, want 1 — one external party makes the message correspondence", activities)
	}
	if raws != 1 {
		t.Errorf("raw_capture rows = %d, want 1", raws)
	}

	// The stored activity still says the colleague wrote it. Authorship is not
	// the record-creation question, and conflating them is what fakes replies.
	var counterparty string
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT counterparty_email FROM activity WHERE source_id = 'intro-1'`).Scan(&counterparty)
	}); err != nil {
		t.Fatalf("reading the captured activity: %v", err)
	}
	if counterparty != "colleague@acme.com" {
		t.Errorf("counterparty_email = %q, want the colleague who actually wrote it", counterparty)
	}

	if reasons := breadcrumbReasons(ctx, t, pool, "intro-1"); len(reasons) > 0 {
		for _, r := range reasons {
			if r == "internal_only" {
				t.Fatal("an introduction was dropped as internal — the copied prospect makes it external")
			}
		}
	}
}

// CAP-AC1.3d — with no registered domain nothing is internal, so a message
// between two colleagues is captured. The installation has made no claim about
// its own mail, and this gate does not invent one on its behalf.
func TestWithNoRegisteredDomainEvenColleagueMailIsCaptured(t *testing.T) {
	ctx, pool := bootstrapInternalMailWorkspace(t)
	sink := capture.NewSink(pool)

	if _, err := sink.Upsert(ctx, mailRecord("nodomain-1", "boss@acme.com",
		"boss@acme.com", "rep@acme.com")); err != nil {
		t.Fatalf("with an empty own-domain set the message must be captured: %v", err)
	}
	if activities, _, _, _ := countsFor(ctx, t, pool, "nodomain-1"); activities != 1 {
		t.Errorf("activity rows = %d, want 1", activities)
	}
}

// A message the connector could not enumerate is captured. An empty address set
// says "I could not read the parties", which is not the same claim as "there
// were none", and the direction to fail in is toward keeping mail.
func TestAMessageReportingNoAddressesIsCaptured(t *testing.T) {
	ctx, pool := bootstrapInternalMailWorkspace(t, "acme.com")
	sink := capture.NewSink(pool)

	if _, err := sink.Upsert(ctx, mailRecord("unknown-1", "boss@acme.com")); err != nil {
		t.Fatalf("an unenumerable message must be captured: %v", err)
	}
	if activities, _, _, _ := countsFor(ctx, t, pool, "unknown-1"); activities != 1 {
		t.Errorf("activity rows = %d, want 1", activities)
	}
}

// A subdomain of a registered domain is internal. The workspace registered
// acme.com; mail among people at mail.acme.com is still colleague mail.
func TestMailAmongSubdomainsOfARegisteredDomainIsInternal(t *testing.T) {
	ctx, pool := bootstrapInternalMailWorkspace(t, "acme.com")
	sink := capture.NewSink(pool)

	_, err := sink.Upsert(ctx, mailRecord("subdomain-1", "boss@mail.acme.com",
		"boss@mail.acme.com", "rep@eu.acme.com"))
	if !isSkip(err) {
		t.Fatalf("subdomain mail: got %v, want a skip", err)
	}
	if activities, _, raws, _ := countsFor(ctx, t, pool, "subdomain-1"); activities != 0 || raws != 0 {
		t.Errorf("subdomain mail left rows: activity=%d raw_capture=%d, want 0", activities, raws)
	}
}

// isSkip reports whether err is the intentional-skip signal every connector
// sync loop counts without failing.
func isSkip(err error) bool { return errors.Is(err, connector.ErrSkip) }

// A domain nobody vouched for governs no drop.
//
// A connected mailbox proves whose mailbox it is, never whose domain it is: a
// contractor, or anyone whose mail lives at a customer's company, connects a
// genuine account on a domain the workspace does not own. If that alone could
// suppress storage, the workspace would silently stop keeping correspondence
// with that company — irreversibly, since every connector advances past a
// skipped message. Only the installation's own company, or an administrator,
// can make a domain count.
func TestAnUnverifiedOwnDomainSuppressesNothing(t *testing.T) {
	ctx, pool := bootstrapInternalMailWorkspace(t)
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO workspace_email_domain (workspace_id, domain, source, verified)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'acme.com', 'mailbox', false)`)
		return err
	}); err != nil {
		t.Fatalf("seeding an unverified domain: %v", err)
	}
	sink := capture.NewSink(pool)

	if _, err := sink.Upsert(ctx, mailRecord("unverified-1", "boss@acme.com",
		"boss@acme.com", "rep@acme.com")); err != nil {
		t.Fatalf("an unverified domain must not suppress storage: %v", err)
	}
	if activities, _, _, _ := countsFor(ctx, t, pool, "unverified-1"); activities != 1 {
		t.Errorf("activity rows = %d, want 1 — only a vouched-for domain governs the drop", activities)
	}
}

// A domain the own company stops claiming stops suppressing mail.
//
// Whether a domain is ours is derived, not remembered. Stamping the answer onto
// the registry row when a mailbox was first seen would leave a corrected typo —
// or a company that changed its domain — hiding correspondence with an address
// nobody claims any more, with nothing in the system able to revoke it.
func TestADomainTheCompanyNoLongerClaimsStopsSuppressingMail(t *testing.T) {
	ctx, pool := bootstrapInternalMailWorkspace(t, "acme.com")
	sink := capture.NewSink(pool)

	if _, err := sink.Upsert(ctx, mailRecord("revoke-1", "boss@acme.com",
		"boss@acme.com", "rep@acme.com")); !isSkip(err) {
		t.Fatalf("while the company claims acme.com the message is dropped: got %v", err)
	}

	// The company corrects itself: acme.com was never theirs.
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE organization_domain SET domain = 'acmecorp.com'`)
		return err
	}); err != nil {
		t.Fatalf("correcting the company's domain: %v", err)
	}

	if _, err := sink.Upsert(ctx, mailRecord("revoke-2", "boss@acme.com",
		"boss@acme.com", "rep@acme.com")); err != nil {
		t.Fatalf("once the company no longer claims acme.com the mail must be kept: %v", err)
	}
	if activities, _, _, _ := countsFor(ctx, t, pool, "revoke-2"); activities != 1 {
		t.Errorf("activity rows = %d, want 1 — the claim was withdrawn, so the drop must stop", activities)
	}
}
