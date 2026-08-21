// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The record-history line names the TOOL a delegated change came through, not
// "an agent".
//
// This suite exists because the join that resolves that name reaches four
// tables — audit_log → passport → oauth_grant → oauth_client — and nothing
// unit-testable runs it. The first version of that join named
// oauth_client.workspace_id, a column migration 1787109970 had dropped, and
// every record-history read answered 500. It shipped because the only tests
// touching this path seeded audit rows with no passport at all, so the join
// matched nothing and its column names were never resolved.

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestRecordHistoryNamesTheClientADelegatedChangeCameThrough(t *testing.T) {
	e := Setup(t)
	person := e.SeedPerson(t, "History Subject", nil)
	human := seedWorkspaceUser(t, e, "Ada Authority")

	passport := seedGrantedPassport(t, e, human, "Claude")
	seedDelegatedAuditRow(t, e, person, human, passport, // Dated FORWARD: SeedPerson's own create row is stamped at real now, and
		// the read is chronological, so a row dated backward would not be last.
		time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond))

	page := readRecordHistory(t, e, person)
	if len(page.Entries) == 0 {
		t.Fatal("the history is empty; the seeded row should be in it")
	}
	last := page.Entries[len(page.Entries)-1]
	if last.Summary != "Ada Authority, via Claude, updated the record" {
		t.Errorf("the line reads %q, want the client named rather than \"via an agent\"", last.Summary)
	}
	if last.AgentClient == nil || *last.AgentClient != "Claude" {
		t.Errorf("agent_client = %v, want Claude — a client rendering its own attribution "+
			"must not have to parse the sentence", last.AgentClient)
	}
}

// A passport minted by hand has no OAuth grant behind it, so there is no
// registered client to name and the generic qualifier is the honest answer.
func TestAHandMintedPassportKeepsTheGenericQualifier(t *testing.T) {
	e := Setup(t)
	person := e.SeedPerson(t, "History Subject", nil)
	human := seedWorkspaceUser(t, e, "Ada Authority")

	passport := seedUngrantedPassport(t, e, human)
	seedDelegatedAuditRow(t, e, person, human, passport, // Dated FORWARD: SeedPerson's own create row is stamped at real now, and
		// the read is chronological, so a row dated backward would not be last.
		time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond))

	page := readRecordHistory(t, e, person)
	last := page.Entries[len(page.Entries)-1]
	if last.Summary != "Ada Authority, via an agent, updated the record" {
		t.Errorf("the line reads %q, want the generic qualifier", last.Summary)
	}
	if last.AgentClient != nil {
		t.Errorf("agent_client = %v, want nil — there is no registered client to name", *last.AgentClient)
	}
}

// seedGrantedPassport writes the whole chain the join walks: a client, a grant
// under it, and a passport issued for that grant.
func seedGrantedPassport(t *testing.T, e *Env, human ids.UUID, clientName string) ids.UUID {
	t.Helper()
	clientID := "client-" + ids.NewV7().String()
	grantID, passportID := ids.NewV7(), ids.NewV7()
	ctx := principal.WithWorkspaceID(t.Context(), e.WS)
	err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO oauth_client (client_id, client_name, redirect_uris)
			 VALUES ($1, $2, ARRAY['https://example.test/cb'])`, clientID, clientName); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO oauth_grant (id, client_id, user_id, scopes)
			 VALUES ($1, $2, $3, ARRAY['read'])`, grantID, clientID, human); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO passport (id, on_behalf_of, granted_by, label, scopes, token_hash,
			                       expires_at, oauth_grant_id)
			 VALUES ($1, $2, $2, $3, ARRAY['read'], $4, now() + interval '1 day', $5)`,
			passportID, human, "oauth:"+clientID, "hash-"+passportID.String(), grantID)
		return err
	})
	if err != nil {
		t.Fatalf("seeding the granted passport: %v", err)
	}
	return passportID
}

// seedUngrantedPassport writes a passport with NO oauth_grant_id — what
// minting one by hand in Settings produces.
func seedUngrantedPassport(t *testing.T, e *Env, human ids.UUID) ids.UUID {
	t.Helper()
	passportID := ids.NewV7()
	ctx := principal.WithWorkspaceID(t.Context(), e.WS)
	err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO passport (id, on_behalf_of, granted_by, label, scopes, token_hash, expires_at)
			 VALUES ($1, $2, $2, 'My laptop', ARRAY['read'], $3, now() + interval '1 day')`,
			passportID, human, "hash-"+passportID.String())
		return err
	})
	if err != nil {
		t.Fatalf("seeding the hand-minted passport: %v", err)
	}
	return passportID
}

// seedDelegatedAuditRow writes the audit row the join reads: an agent actor,
// the human it acted for, and the passport it presented.
func seedDelegatedAuditRow(t *testing.T, e *Env, person, human, passport ids.UUID, at time.Time) {
	t.Helper()
	ctx := principal.WithWorkspaceID(t.Context(), e.WS)
	err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO audit_log (id, actor_type, actor_id, on_behalf_of, passport_id,
			                        action, entity_type, entity_id, occurred_at)
			 VALUES ($1, 'agent', $2, $3, $4, 'update', 'person', $5, $6)`, ids.NewV7(), "agent:"+passport.String(), human, passport, person, at)
		return err
	})
	if err != nil {
		t.Fatalf("seeding the delegated audit row: %v", err)
	}
}

// readRecordHistory reads the whole page through the same call the transport
// makes.
func readRecordHistory(t *testing.T, e *Env, person ids.UUID) privacy.RecordHistoryPage {
	t.Helper()
	page, err := privacy.ListRecordHistory(e.Admin(), e.DB(), privacy.RecordHistoryFilter{
		EntityType: "person", EntityID: person,
	})
	if err != nil {
		t.Fatalf("reading the record history: %v", err)
	}
	return page
}
