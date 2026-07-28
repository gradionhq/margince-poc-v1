// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The send-grant pre-flight (DESIGN §8.4) over the real composition: the api
// role's connect registry answers whether the acting human's mailbox may
// transmit, and a send it already knows cannot leave is refused with an
// actionable 422 instead of a 202 and a delivery that can only park.
//
// The assertion that matters is against comms_outbound, not against a stub:
// the pre-flight lives in the store BOTH send transports call, so a refusal
// that leaves the table empty is a refusal on the tool surface too.
//
// The grant, not the connection, is the subject. Every mailbox connected
// before the send scope existed holds read-only access until its owner
// reconnects, so the middle case here — connected, but read-only — is the one
// a connection-only check would wave through and then park.

import (
	"context"
	"crypto/rand"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
)

const (
	gmailReadonlyScope = "https://www.googleapis.com/auth/gmail.readonly"
	gmailSendScope     = "https://www.googleapis.com/auth/gmail.send"
	// preflightBaseURL is this fixture's ONE public base: the composition is
	// booted with it, and the wire assertion on a marketing send's
	// List-Unsubscribe target reads it back. Two literals could drift, leaving
	// that assertion passing against the wrong host.
	preflightBaseURL = "https://mail.example.test"
)

type preflightEnv struct {
	*env
	activityID string
	personID   string
	ws, user   string
}

// inWorkspace runs fn on the owner connection under the bootstrapped
// workspace's GUC. FORCE RLS applies to the owner too, so a tenant table is
// unreachable without it.
func (e *env) inWorkspace(t *testing.T, slug string, fn func(pgx.Tx) error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := e.owner.Begin(ctx)
	if err != nil {
		return err
	}
	//craft:ignore swallowed-errors error-path safety net only — the Commit below is asserted, after which this rollback is a designed no-op
	defer func() { _ = tx.Rollback(ctx) }()
	var wsID string
	if err := tx.QueryRow(ctx, `SELECT id FROM workspace WHERE slug = $1`, slug).Scan(&wsID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, wsID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// preflightAppPool opens a second pool onto the same database, because the
// vault WithGmailCapture needs must exist before setupWithOptions opens the
// harness's own (the separate-connection precedent setupEmbedReindex uses).
func preflightAppPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if appDSN == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	pool, err := database.NewPool(context.Background(), appDSN)
	if err != nil {
		t.Fatalf("opening the vault's pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// setupPreflight boots the api composition WITH the Google app configured, so
// the connect registry — and with it the pre-flight — is actually wired. The
// keyvault rides a separate pool for the same database: WithGmailCapture needs
// a vault before setupWithOptions has opened the harness's own.
func setupPreflight(t *testing.T) *preflightEnv {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generating a test root key: %v", err)
	}
	vaultPool := preflightAppPool(t)
	vault, err := keyvault.New(keyvault.Config{RootKey: key, Pool: vaultPool})
	if err != nil {
		t.Fatalf("building the local vault: %v", err)
	}
	gmailCfg := compose.GmailConfig{
		ClientID: "preflight-id", ClientSecret: "preflight-secret",
		StateKey: "0123456789abcdef0123456789abcdef", PublicBaseURL: preflightBaseURL,
	}
	e := setupWithOptions(t, compose.WithKeyvault(vault),
		compose.WithGmailCapture(gmailCfg, compose.CaptureConfig{}),
		// A marketing send derives a one-click unsubscribe link and refuses
		// without a boot-configured base to build it from, so the fixture
		// carries one — an install that can send at all has one.
		compose.WithPublicBaseURL(preflightBaseURL))
	e.slug = "preflight-e2e"
	bootstrapWorkspaceSession(t, e, "Preflight E2E", "sender@fable.test", "Admin")

	var person struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Consented Buyer",
		"emails":    []anyMap{{"email": "buyer@preflight.test"}},
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}
	var activity struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/activities", anyMap{
		"kind": "email", "subject": "Inbound question", "direction": "inbound",
		"links": []anyMap{{"entity_type": "person", "entity_id": person.ID}},
	}, nil, &activity); status != http.StatusCreated {
		t.Fatalf("log anchor activity → %d", status)
	}

	// Consent is granted so the gate ahead of the pre-flight answers yes:
	// this suite is about the mailbox, and a 409 would prove nothing about it.
	var purposes struct {
		Data []struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/consent-purposes", nil, nil, &purposes); status != http.StatusOK {
		t.Fatalf("list purposes → %d", status)
	}
	var transactional string
	for _, p := range purposes.Data {
		if p.Key == "transactional" {
			transactional = p.ID
		}
	}
	if transactional == "" {
		t.Fatalf("bootstrap seeded no transactional purpose: %+v", purposes.Data)
	}
	if status := e.call(t, "POST", "/v1/people/"+person.ID+"/consent", anyMap{
		"purpose_id": transactional, "new_state": "granted", "lawful_basis": "consent",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("record consent → %d", status)
	}

	var ws, user string
	if err := e.inWorkspace(t, e.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT workspace_id, id FROM app_user WHERE email = $1`, "sender@fable.test").Scan(&ws, &user)
	}); err != nil {
		t.Fatalf("resolving the acting human: %v", err)
	}
	return &preflightEnv{env: e, activityID: activity.ID, personID: person.ID, ws: ws, user: user}
}

// send issues the authenticated send and returns the status plus the
// validation error's own code and message — the words the user is shown, which
// is where the "what do I do about it" has to live.
func (p *preflightEnv) send(t *testing.T) (status int, code, message string) {
	t.Helper()
	var problem struct {
		Details struct {
			Errors []struct {
				Field   string `json:"field"`
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"errors"`
		} `json:"details"`
	}
	status = p.call(t, "POST", "/v1/activities/"+p.activityID+"/send-email", anyMap{
		"subject": "Re: Inbound question", "body": "answer",
		"to": []string{"buyer@preflight.test"}, "consent_purpose": "transactional",
	}, nil, &problem)
	if errs := problem.Details.Errors; len(errs) > 0 {
		return status, errs[0].Code, errs[0].Message
	}
	return status, "", ""
}

// stagedDeliveries counts comms_outbound rows — the fact a refusal must not
// leave behind, whichever transport asked.
func (p *preflightEnv) stagedDeliveries(t *testing.T) int {
	t.Helper()
	var n int
	if err := p.inWorkspace(t, p.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM comms_outbound`).Scan(&n)
	}); err != nil {
		t.Fatalf("counting staged deliveries: %v", err)
	}
	return n
}

// connect writes the acting human's gmail connection with exactly the provider
// grant named. Written directly because the subject is what the SEND path
// reads out of the row, not how the OAuth callback puts it there.
func (p *preflightEnv) connect(t *testing.T, providerScopes ...string) {
	t.Helper()
	if err := p.inWorkspace(t, p.slug, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO capture_connection (workspace_id, provider, user_id, scopes, status, auth, provider_scopes)
			VALUES ($1, 'gmail', $2, '{}', 'connected', $3, $4)
			ON CONFLICT (workspace_id, user_id, provider)
			DO UPDATE SET status = 'connected', provider_scopes = EXCLUDED.provider_scopes`,
			p.ws, p.user, []byte(`{"refresh_token":"r","granted":[]}`), providerScopes)
		return err
	}); err != nil {
		t.Fatalf("seeding the gmail connection: %v", err)
	}
}

// A human with no mailbox at all is told so before anything is staged.
func TestSendRefusesWhenNoMailboxIsConnected(t *testing.T) {
	p := setupPreflight(t)

	status, code, detail := p.send(t)

	if status != http.StatusUnprocessableEntity || code != "mailbox_not_send_capable" {
		t.Fatalf("send with no connected mailbox → %d %q, want 422 mailbox_not_send_capable", status, code)
	}
	if !strings.Contains(detail, "reconnect") {
		t.Fatalf("refusal detail %q does not tell the user what to do about it", detail)
	}
	if n := p.stagedDeliveries(t); n != 0 {
		t.Fatalf("%d deliveries staged behind a refused send, want 0", n)
	}
}

// The case a connection-only check would wave through: a mailbox connected
// before the send scope existed. It holds gmail.readonly and nothing else, so
// accepting the send would hand the user a 202 and a delivery that can only
// park where they cannot see it.
func TestSendRefusesAConnectedMailboxWithoutTheSendGrant(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope)

	status, code, detail := p.send(t)

	if status != http.StatusUnprocessableEntity || code != "mailbox_not_send_capable" {
		t.Fatalf("send through a read-only mailbox → %d %q, want 422 mailbox_not_send_capable", status, code)
	}
	if !strings.Contains(detail, "reconnect") {
		t.Fatalf("refusal detail %q does not tell the user to reconnect", detail)
	}
	if n := p.stagedDeliveries(t); n != 0 {
		t.Fatalf("%d deliveries staged behind a refused send, want 0", n)
	}
}

// …and the same connection with the send scope added goes through, which is
// what proves the two refusals above are about the GRANT and not merely about
// the send path being broken.
func TestSendProceedsOnceTheMailboxHoldsTheSendGrant(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	status, code, _ := p.send(t)

	if status != http.StatusAccepted {
		t.Fatalf("send through a send-capable mailbox → %d %q, want 202", status, code)
	}
	if n := p.stagedDeliveries(t); n != 1 {
		t.Fatalf("%d deliveries staged behind an accepted send, want 1", n)
	}
}
