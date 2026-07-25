// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/capture/mailmap"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// The shared capture harness: the production registry and resolver wiring, a
// connected gmail connection, and the message fixtures every capture suite
// drives. Lives on its own because both the auto-create suite and the tier-gate
// suite are its callers, and neither owns it.

const autoCreateOwner = "owner@myco.example"

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
		msg, err := mailmap.Parse(raw, autoCreateOwner)
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

// captureEnv is the production capture wiring one test drives: the real
// registry and resolver (not a bare sink — the auto-create resolver and the
// tier gate are what these tests prove), a connected gmail connection, and the
// two pull shapes. Built per test so each starts from a clean mailbox.
type captureEnv struct {
	e        *searchEnv
	sync     func(t *testing.T, raws ...[]byte)
	syncSent func(t *testing.T, sent map[string]bool, raws ...[]byte)
}

func newCaptureEnv(t *testing.T) captureEnv {
	t.Helper()
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

	// The anchor sync seeds the mailbox's own domain as internal — every tier
	// below depends on the workspace knowing which domain is its own.
	sync(t)
	if n := countRows(t, e, `SELECT count(*) FROM workspace_email_domain WHERE domain = 'myco.example'`); n != 1 {
		t.Fatalf("workspace domain seeded %d times, want 1", n)
	}
	return captureEnv{e: e, sync: sync, syncSent: syncSent}
}
