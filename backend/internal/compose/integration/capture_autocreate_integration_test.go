// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The auto-create pipeline end to end over a real migrated Postgres
// (ADR-0063, AC3.1/3.2): a captured thread yields exactly one person, one
// company, one employment edge and person-linked activities — idempotent
// across replays; free-mail yields the person but never a company; the
// workspace's own domain (seeded from the synced mailbox) creates nothing;
// an erased address stays dead; and an inbound message above a prior
// outbound emits exactly one engagement.reply.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/mailmap"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

const autoCreateOwner = "owner@myco.example"

// mailBatchConnector replays a fixed batch of RFC822 messages through the
// production mailmap → Sink path — the provider I/O faked, nothing else.
type mailBatchConnector struct {
	raws [][]byte
}

func (m *mailBatchConnector) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		Name: "gmail", Version: "1",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierAutoExecute,
		Produces: []datasource.EntityType{datasource.EntityActivity},
	}
}

func (m *mailBatchConnector) Authenticate(context.Context, connector.AuthRequest) (connector.Auth, error) {
	return connector.Auth("token"), nil
}

func (m *mailBatchConnector) Sync(ctx context.Context, _ connector.Auth, _ connector.Cursor, sink connector.Sink) (connector.Cursor, error) {
	for _, raw := range m.raws {
		msg, err := mailmap.Parse(raw, autoCreateOwner)
		if err != nil {
			return nil, err
		}
		if _, drop := msg.SkipReason(); drop {
			continue
		}
		if _, err := sink.Upsert(ctx, msg.ToRecord("gmail", raw)); err != nil {
			return nil, err
		}
	}
	return connector.Cursor(fmt.Sprintf(`{"email":%q}`, autoCreateOwner)), nil
}

func (m *mailBatchConnector) Normalize(context.Context, connector.RawRecord) ([]connector.NormalizedRecord, error) {
	return nil, connector.ErrSkip
}

func (m *mailBatchConnector) HealthCheck(context.Context, connector.Auth) error { return nil }

func email(from, fromName, to, msgID, refs string) []byte {
	fromHeader := from
	if fromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", fromName, from)
	}
	lines := []string{
		"From: " + fromHeader,
		"To: " + to,
		"Subject: project",
		"Date: Wed, 04 Jun 2026 08:00:00 +0000",
		"Message-ID: <" + msgID + ">",
	}
	if refs != "" {
		lines = append(lines, "References: <"+refs+">")
	}
	lines = append(lines, "Content-Type: text/plain", "", "hello", "")
	return []byte(strings.Join(lines, "\r\n"))
}

// emailWithListUnsub builds a message carrying an RFC 2369 List-Unsubscribe
// header — the bulk-mail corroboration the transactional prefix rule needs.
func emailWithListUnsub(from, fromName, to, msgID string) []byte {
	lines := []string{
		fmt.Sprintf("From: %s <%s>", fromName, from),
		"To: " + to,
		"Subject: newsletter",
		"Date: Wed, 04 Jun 2026 08:00:00 +0000",
		"Message-ID: <" + msgID + ">",
		"List-Unsubscribe: <https://example.com/unsub>",
		"Content-Type: text/plain", "", "hello", "",
	}
	return []byte(strings.Join(lines, "\r\n"))
}

func countRows(t *testing.T, e *searchEnv, query string) int {
	t.Helper()
	var n int
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), query).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAutoCreateFromCapturedMail(t *testing.T) {
	e := setupSearch(t)
	conn := &mailBatchConnector{}
	// The PRODUCTION wiring, not the bare test sink: the auto-create
	// resolver and the free-mail gate are exactly what this test proves.
	registry := compose.NewCaptureRegistry(e.Pool, newTestKeyvault(t, e), compose.CaptureConfig{})
	registry.Register(conn)

	// The production authority resolves the granting human's LIVE role, so
	// the rep needs a real one: capture writes activities and the ensure
	// path creates people/organizations under the same derived principal.
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		var roleID string
		if err := tx.QueryRow(context.Background(), `
			INSERT INTO role (workspace_id, key, name, permissions)
			VALUES ($1, 'capture_rep', 'Capture Rep',
			        '{"objects":{"activity":{"create":true,"read":true},"person":{"create":true,"read":true},"organization":{"create":true,"read":true}},"row_scope":"all"}'::jsonb)
			RETURNING id`, e.WS).Scan(&roleID); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(),
			`INSERT INTO role_assignment (workspace_id, role_id, user_id) VALUES ($1, $2, $3)`,
			e.WS, roleID, e.Rep1)
		return err
	})
	if err != nil {
		t.Fatalf("seeding the capture role: %v", err)
	}

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "gmail", connector.Auth("refresh"))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	sync := func(t *testing.T, raws ...[]byte) {
		t.Helper()
		conn.raws = raws
		if err := registry.SyncOnce(wsCtx, connID); err != nil {
			t.Fatalf("SyncOnce: %v", err)
		}
	}

	// The anchor sync seeds the mailbox's own domain as internal.
	sync(t)
	if n := countRows(t, e, `SELECT count(*) FROM workspace_email_domain WHERE domain = 'myco.example'`); n != 1 {
		t.Fatalf("workspace domain seeded %d times, want 1", n)
	}

	t.Run("a thread becomes one person, one company, one employment", func(t *testing.T) {
		sync(t,
			email("alice@acme.example", "Alice Example", autoCreateOwner, "m1@acme.example", ""),
			email(autoCreateOwner, "", "alice@acme.example", "m2@myco.example", "m1@acme.example"),
			email("alice@acme.example", "Alice Example", autoCreateOwner, "m3@acme.example", "m1@acme.example"),
		)
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'alice@acme.example'`); n != 1 {
			t.Fatalf("%d persons for alice, want exactly 1", n)
		}
		// The org is named from the domain's registrable label ("Acme"), not
		// the raw eSLD, and marked name_source='domain' so a later enrichment
		// may overwrite it (ADR-0072/A118).
		if n := countRows(t, e, `SELECT count(*) FROM organization WHERE display_name = 'Acme' AND name_source = 'domain'`); n != 1 {
			t.Fatalf("%d organizations named 'Acme' with name_source='domain', want exactly 1", n)
		}
		if n := countRows(t, e, `SELECT count(*) FROM organization WHERE display_name = 'acme.example'`); n != 0 {
			t.Fatal("the raw eSLD must no longer be used as the display name")
		}
		if n := countRows(t, e, `
			SELECT count(*) FROM relationship r JOIN person_email pe ON pe.person_id = r.person_id
			WHERE r.kind = 'employment' AND r.is_current_primary AND pe.email = 'alice@acme.example'`); n != 1 {
			t.Fatalf("%d employment edges, want exactly 1", n)
		}
		// Person-only links: every captured message links alice, none links the org.
		if n := countRows(t, e, `
			SELECT count(*) FROM activity_link al JOIN person_email pe ON pe.person_id = al.person_id
			WHERE al.entity_type = 'person' AND pe.email = 'alice@acme.example'`); n != 3 {
			t.Fatalf("%d person links, want 3 (one per captured message)", n)
		}
		if n := countRows(t, e, `SELECT count(*) FROM activity_link WHERE entity_type = 'organization'`); n != 0 {
			t.Fatalf("%d org links, want 0 — the org rolls up through employment", n)
		}
		// Connector-created rows start owner-visible — asserted on alice
		// herself, so an unrelated owner-visible row can never green this.
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'alice@acme.example' AND p.visibility = 'owner'`); n != 1 {
			t.Fatal("the connector-created person must start visibility='owner'")
		}
		// The inbound reply above our outbound emitted exactly one engagement.reply.
		if n := countRows(t, e, `SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'engagement.reply'`); n != 1 {
			t.Fatalf("%d engagement.reply events, want exactly 1", n)
		}
	})

	t.Run("a replay creates nothing new", func(t *testing.T) {
		sync(t, email("alice@acme.example", "Alice Example", autoCreateOwner, "m1@acme.example", ""))
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'alice@acme.example'`); n != 1 {
			t.Fatalf("replay grew alice to %d rows", n)
		}
		if n := countRows(t, e, `SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'engagement.reply'`); n != 1 {
			t.Fatalf("replay re-emitted engagement.reply (%d total)", n)
		}
	})

	t.Run("free-mail creates the person, never a company", func(t *testing.T) {
		sync(t, email("bob@gmail.com", "Bob Person", autoCreateOwner, "b1@gmail.com", ""))
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'bob@gmail.com'`); n != 1 {
			t.Fatalf("%d persons for bob, want 1", n)
		}
		if n := countRows(t, e, `SELECT count(*) FROM organization WHERE display_name = 'gmail.com'`); n != 0 {
			t.Fatal("gmail.com must never become an organization")
		}
	})

	t.Run("transactional infrastructure keeps the activity, derives no counterparty", func(t *testing.T) {
		// A DocuSign envelope (exact infra eSLD, no corroboration needed) and a
		// conference blast on a prefix subdomain WITH a List-Unsubscribe header
		// (corroborated) both suppress person+org while the timeline row stands
		// (ADR-0072/A118, CAP-PARAM-6).
		sync(t,
			email("dse@eu.docusign.net", "DocuSign EU", autoCreateOwner, "ds1@docusign.net", ""),
			emailWithListUnsub("hello@event.gitex.com", "GITEX", autoCreateOwner, "gx1@event.gitex.com"),
		)
		if n := countRows(t, e, `SELECT count(*) FROM activity WHERE source_id IN ('ds1@docusign.net', 'gx1@event.gitex.com')`); n != 2 {
			t.Fatalf("%d transactional activities captured, want 2 — the timeline row must stand", n)
		}
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email IN ('dse@eu.docusign.net', 'hello@event.gitex.com')`); n != 0 {
			t.Fatal("transactional infrastructure must derive no person")
		}
		if n := countRows(t, e, `SELECT count(*) FROM organization WHERE display_name IN ('Docusign', 'Gitex')`); n != 0 {
			t.Fatal("transactional infrastructure must derive no organization")
		}
		if n := countRows(t, e, `
			SELECT count(*) FROM system_log
			WHERE action = 'capture_transactional_suppressed' AND detail->>'source_id' IN ('ds1@docusign.net', 'gx1@event.gitex.com')`); n != 2 {
			t.Fatalf("%d transactional-suppression breadcrumbs, want 2", n)
		}
	})

	t.Run("a conference blast WITHOUT corroboration is a normal counterparty", func(t *testing.T) {
		// The same prefix subdomain, but no List-Unsubscribe and a human
		// localpart: not suppressed — a real company can live at event.*.
		sync(t, email("ada@event.realco.example", "Ada Real", autoCreateOwner, "rc1@event.realco.example", ""))
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'ada@event.realco.example'`); n != 1 {
			t.Fatal("an uncorroborated prefix sender must create a normal person")
		}
	})

	t.Run("the workspace's own domain creates nothing", func(t *testing.T) {
		sync(t, email("carol@myco.example", "Carol Colleague", autoCreateOwner, "c1@myco.example", ""))
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'carol@myco.example'`); n != 0 {
			t.Fatal("a colleague must not become a CRM person")
		}
		if n := countRows(t, e, `SELECT count(*) FROM organization WHERE display_name = 'myco.example'`); n != 0 {
			t.Fatal("the workspace's own domain must not become a CRM organization")
		}
	})

	t.Run("an erased address stays dead", func(t *testing.T) {
		err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `
				INSERT INTO erasure_suppression (workspace_id, kind, value_hash)
				VALUES ($1, 'email', $2)`, e.WS, storekit.SuppressionHash("dave@dead.example"))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		sync(t, email("dave@dead.example", "Dave Gone", autoCreateOwner, "d1@dead.example", ""))
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'dave@dead.example'`); n != 0 {
			t.Fatal("an erased address must never re-create a person (A13)")
		}
		// The activity itself is still captured — suppression stops the
		// person, not the timeline row.
		if n := countRows(t, e, `SELECT count(*) FROM activity WHERE source_id = 'd1@dead.example'`); n != 1 {
			t.Fatal("suppression must not drop the captured activity")
		}
	})

	t.Run("a fuzzy near-match creates anyway and queues the pair", func(t *testing.T) {
		// A near-identical name on the SAME employer domain: the PO-F-1
		// score (0.55·name + 0.45·org) crosses the review threshold.
		sync(t, email("alice2@acme.example", "Alice Exampel", autoCreateOwner, "f1@acme.example", ""))
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'alice2@acme.example'`); n != 1 {
			t.Fatal("fuzzy must create — capture never blocks on a human")
		}
		if n := countRows(t, e, `SELECT count(*) FROM dedupe_candidate WHERE entity_type = 'person' AND disposition = 'open'`); n != 1 {
			t.Fatalf("%d open dedupe candidates, want exactly 1", n)
		}
	})

	t.Run("a connector with no granting human records the fault, never a person", func(t *testing.T) {
		// A bare sink with the ensure seam wired but an ownerless connector
		// principal: the capture itself must land, the ensure must refuse
		// honestly (RC-8 — created rows need a human owner), and the fault
		// must be a system_log line the nightly reconcile can find.
		sink := capture.NewSink(e.Pool).WithEnsurer(recordingEnsurer{}, capture.NewFreemailList(nil), capture.NewTransactionalList(nil, nil))
		ownerless := principal.WithCorrelationID(principal.WithActor(
			principal.WithWorkspaceID(context.Background(), e.WS), principal.Principal{
				Type: principal.PrincipalConnector, ID: "connector:gmail",
				Scopes: principal.NewScopeSet(principal.ScopeRead),
				Permissions: principal.Permissions{
					Objects:  map[string]principal.ObjectGrant{"activity": {Create: true, Read: true}},
					RowScope: principal.RowScopeAll,
				},
			}), ids.NewV7())
		raw := email("ghost@nowhere.example", "Ghost Sender", autoCreateOwner, "g1@nowhere.example", "")
		msg, err := mailmap.Parse(raw, autoCreateOwner)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sink.Upsert(ownerless, msg.ToRecord("gmail", raw)); err != nil {
			t.Fatalf("the capture itself must not fail: %v", err)
		}
		if n := countRows(t, e, `
			SELECT count(*) FROM system_log
			WHERE action = 'capture_ensure_fault' AND detail->>'source_id' = 'g1@nowhere.example'`); n != 1 {
			t.Fatalf("%d ensure-fault ledger lines, want exactly 1", n)
		}
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'ghost@nowhere.example'`); n != 0 {
			t.Fatal("an ownerless connector must not create a person")
		}
	})
}

// recordingEnsurer satisfies the ensure seam for the ownerless-connector
// case; the sink's own gates must refuse before it is ever reached.
type recordingEnsurer struct{}

func (recordingEnsurer) EnsureCounterparty(context.Context, capture.EnsureRequest) error {
	return nil
}
