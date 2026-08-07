// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

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

// The shared capture harness: the production registry and resolver wiring, a
// connected gmail connection, and the message fixtures every capture suite
// drives. Lives on its own because both the auto-create suite and the tier-gate
// suite are its callers, and neither owns it.

const captureOwner = "owner@myco.example"

// mailBatchConnector replays a fixed batch of RFC822 messages through the
// production mailmap → Sink path — the provider I/O faked, nothing else.
type mailBatchConnector struct {
	raws [][]byte
	sent map[string]bool // Message-IDs the provider filed as the owner's own sent mail
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
		msg, err := mailmap.Parse(raw, captureOwner)
		if err != nil {
			return nil, err
		}
		if _, drop := msg.SkipReason(); drop {
			continue
		}
		// The provider's own attestation, which a real Gmail sync reads off the
		// message's SENT label. Keyed by Message-ID so a fixture can be the
		// owner's outgoing mail without the test forging a From header — which
		// is precisely what the attestation must not be derivable from.
		msg = msg.AttestSentByOwner(m.sent[msg.ID()])
		if _, err := sink.Upsert(ctx, msg.ToRecord("gmail", raw)); err != nil {
			// A skip is an outcome, not a fault: the writer decided this
			// message produces no rows. Every real connector counts it and
			// carries on, and a fake that failed the whole sync instead would
			// make the harness disagree with the thing it stands in for.
			if errors.Is(err, connector.ErrSkip) {
				continue
			}
			return nil, err
		}
	}
	return connector.Cursor(fmt.Sprintf(`{"email":%q}`, captureOwner)), nil
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

func countRows(t *testing.T, e *SearchEnv, query string) int {
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

// seedCaptureRole gives Rep1 a live role that can create the records capture
// derives. The production authority resolves the granting human's LIVE role, so
// without it the ensure path is denied and every counterparty assertion reads as
// a resolver bug.
func seedCaptureRole(t *testing.T, e *SearchEnv) {
	t.Helper()
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
}

// captureEnv is the production capture wiring one test drives: the real
// registry and resolver (not a bare sink — the auto-create resolver and the
// tier gate are what these tests prove), a connected gmail connection, and the
// two pull shapes. Built per test so each starts from a clean mailbox.
type captureEnv struct {
	e        *SearchEnv
	sync     func(t *testing.T, raws ...[]byte)
	syncSent func(t *testing.T, sent map[string]bool, raws ...[]byte)
}

func newCaptureEnv(t *testing.T) captureEnv {
	t.Helper()
	e := SetupSearch(t)
	conn := &mailBatchConnector{}
	registry := compose.NewCaptureRegistry(e.Pool, newTestKeyvault(t, e), compose.CaptureConfig{})
	registry.Register(conn)

	seedCaptureRole(t, e)

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "gmail", connector.Auth("refresh"))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	sync := func(t *testing.T, raws ...[]byte) {
		t.Helper()
		conn.raws, conn.sent = raws, nil
		if err := registry.SyncOnce(wsCtx, connID); err != nil {
			t.Fatalf("SyncOnce: %v", err)
		}
	}
	// syncSent is the same pull with the provider attesting the listed
	// Message-IDs as mail the mailbox owner sent.
	syncSent := func(t *testing.T, sent map[string]bool, raws ...[]byte) {
		t.Helper()
		conn.raws, conn.sent = raws, sent
		if err := registry.SyncOnce(wsCtx, connID); err != nil {
			t.Fatalf("SyncOnce: %v", err)
		}
	}

	// The installation's own company, as cold start leaves it: a human confirmed
	// this domain is ours. It is what lets the mailbox seed below count as
	// verified — a connected mailbox alone proves whose mailbox it is, never
	// whose domain it is (ADR-0082/A127 §2).
	if err := database.WithWorkspaceTx(wsCtx, e.Pool, func(tx pgx.Tx) error {
		orgID := ids.NewV7()
		if _, err := tx.Exec(wsCtx, `
			INSERT INTO organization (id, workspace_id, display_name, is_anchor, source, captured_by)
			VALUES ($1, $2, 'Our Company', true, 'manual', 'human:test')`, orgID, e.WS); err != nil {
			return err
		}
		_, err := tx.Exec(wsCtx, `
			INSERT INTO organization_domain (workspace_id, organization_id, domain, is_primary, source, captured_by)
			VALUES ($1, $2, 'myco.example', true, 'manual', 'human:test')`, e.WS, orgID)
		return err
	}); err != nil {
		t.Fatalf("seeding the anchor company: %v", err)
	}

	// The first sync records the mailbox's domain as a candidate. The row is
	// deliberately unverified: what makes a domain ours is the company's own
	// claim, asked at read time, so nothing here freezes an answer that could
	// later be wrong.
	sync(t)
	var seeded, verified bool
	if err := database.WithWorkspaceTx(wsCtx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(wsCtx, `
			SELECT true, verified FROM workspace_email_domain WHERE domain = 'myco.example'`).
			Scan(&seeded, &verified)
	}); err != nil {
		t.Fatalf("reading the seeded own domain: %v", err)
	}
	if !seeded {
		t.Fatal("the connected mailbox's domain must be recorded as a candidate")
	}
	if verified {
		t.Fatal("a mailbox must not verify its own domain — the company's claim does that")
	}
	return captureEnv{e: e, sync: sync, syncSent: syncSent}
}

// emailCC builds a message that copies a third party — the introduction shape:
// a colleague writes, an outsider is on Cc, and the message is correspondence
// because of that outsider.
func emailCC(from, fromName, to, cc, msgID string) []byte {
	lines := []string{
		fmt.Sprintf("From: %s <%s>", fromName, from),
		"To: " + to,
		"Cc: " + cc,
		"Subject: intro",
		"Date: Wed, 04 Jun 2026 08:00:00 +0000",
		"Message-ID: <" + msgID + ">",
		"Content-Type: text/plain", "", "hello", "",
	}
	return []byte(strings.Join(lines, "\r\n"))
}

// AccountLabel names the mailbox this fake authenticates as, exactly as the
// real mail connectors do — the registry seeds the workspace's own domain from
// it before pulling a single message, so a fake without it would leave every
// tier below testing against an empty own-domain set.
func (m *mailBatchConnector) AccountLabel(connector.Auth) (string, error) {
	return captureOwner, nil
}
