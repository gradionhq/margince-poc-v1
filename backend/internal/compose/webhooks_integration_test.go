// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose_test

// Outbound webhooks (B-E10.13a/b/c) over the real stack: the CRUD surface
// through HTTP (secret once, never again; human-only; workspace-scoped),
// and the delivery engine driven directly against the migrated Postgres
// with an httptest receiver and an injected clock — a matching event is
// delivered exactly once as an HMAC-signed POST, a failing endpoint is
// retried with backoff then dead-lettered, and a parked delivery replays
// to 200. The bus subscriber is thin plumbing (tested in platform/events);
// what matters here is the delivery LOGIC, so it is exercised via the
// deliverer's own entry points, not through Redis.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/webhooks"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/migrations"
)

// webhookEnv bundles the HTTP surface, the app pool, and the shared cipher
// so a test can both register a subscription (over HTTP) and drive the
// deliverer (against the same DB, sealing under the same key).
type webhookEnv struct {
	*env
	pool   *pgxpool.Pool
	cipher *webhooks.Cipher
	wsID   ids.UUID
}

func setupWebhooks(t *testing.T) *webhookEnv {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()

	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("connecting as owner: %v", err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	if _, err := owner.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT USAGE ON SCHEMA public TO margince_app`); err != nil {
		t.Fatalf("resetting schema: %v", err)
	}
	core, err := migrations.Core()
	if err != nil {
		t.Fatalf("loading migrations: %v", err)
	}
	custom, err := migrations.Custom()
	if err != nil {
		t.Fatalf("loading custom migrations: %v", err)
	}
	if _, err := dbmigrate.Up(ctx, owner, core, custom); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatalf("opening app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	// One key for both roles: the HTTP surface seals the secret, the
	// deliverer opens it — they must share the deployment key.
	key := bytes.Repeat([]byte{0x5a}, webhooks.WebhookKeyBytes)
	cipher, err := webhooks.NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}

	ts := httptest.NewTLSServer(compose.New(pool, slog.New(slog.NewTextHandler(os.Stderr, nil)),
		compose.WithWebhookSigningKey(cipher)))
	t.Cleanup(ts.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := ts.Client()
	client.Jar = jar

	e := &env{ts: ts, client: client, slug: "webhooks-e2e", owner: owner}
	if status := e.call(t, "POST", "/v1/workspaces", anyMap{
		"workspace_name": "Webhooks E2E", "admin_email": "hook@fable.test",
		"admin_display_name": "Hook Admin", "admin_password": "correct-horse-battery",
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("bootstrap → %d", status)
	}
	e.slug = "webhooks-e2e"
	if status := e.call(t, "POST", "/v1/auth/login", anyMap{
		"email": "hook@fable.test", "password": "correct-horse-battery",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("login → %d", status)
	}

	var wsID ids.UUID
	if err := owner.QueryRow(ctx, `SELECT id FROM workspace WHERE slug = $1`, "webhooks-e2e").Scan(&wsID); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}
	return &webhookEnv{env: e, pool: pool, cipher: cipher, wsID: wsID}
}

// receiver is a controllable webhook endpoint: it records every POST and
// answers with the currently-configured status code.
type receiver struct {
	server *httptest.Server
	mu     sync.Mutex
	status int
	hits   []receivedHit
	count  atomic.Int64
}

type receivedHit struct {
	event     string
	delivery  string
	signature string
	body      []byte
}

func newReceiver(t *testing.T, status int) *receiver {
	r := &receiver{status: status}
	// TLS: the create surface is https-only, so the receiver must present
	// https. Its Client() trusts the self-signed cert and is what the
	// deliverer dials (the injectable-client seam — netguard would refuse
	// this loopback address in production).
	r.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, "read", http.StatusInternalServerError)
			return
		}
		r.mu.Lock()
		r.hits = append(r.hits, receivedHit{
			event:     req.Header.Get(webhooks.HeaderEvent),
			delivery:  req.Header.Get(webhooks.HeaderDelivery),
			signature: req.Header.Get(webhooks.HeaderSignature),
			body:      body,
		})
		code := r.status
		r.mu.Unlock()
		r.count.Add(1)
		w.WriteHeader(code)
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *receiver) setStatus(status int) {
	r.mu.Lock()
	r.status = status
	r.mu.Unlock()
}

func (r *receiver) snapshot() []receivedHit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedHit(nil), r.hits...)
}

// newTestDeliverer builds a deliverer over the app pool with a plain HTTP
// client (netguard would refuse the httptest receiver's loopback address,
// which is exactly why the delivery client is an injectable seam) and a
// controllable clock.
func newTestDeliverer(we *webhookEnv, now *time.Time, client *http.Client) *webhooks.Deliverer {
	store := webhooks.NewStore(we.pool, we.cipher)
	clock := func() time.Time { return *now }
	return webhooks.NewDeliverer(store, client, clock, slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

func makeEnvelope(wsID ids.UUID, eventType string) kevents.Envelope {
	return kevents.Envelope{
		EventID:     ids.NewV7(),
		Type:        eventType,
		Version:     kevents.VersionOf(eventType),
		WorkspaceID: wsID,
		OccurredAt:  time.Now().UTC(),
		Actor:       kevents.Actor{Type: "system", ID: "system"},
		Entity:      kevents.EntityRef{Type: "deal", ID: ids.NewV7()},
		Trace:       kevents.Trace{CorrelationID: ids.NewV7(), AuditLogID: ids.NewV7()},
	}
}

// createSubscription registers a subscription over HTTP and returns its id
// and the one-time signing secret.
func (we *webhookEnv) createSubscription(t *testing.T, target string, eventTypes []string) (string, string) {
	t.Helper()
	var created struct {
		Subscription struct {
			Id string `json:"id"`
		} `json:"subscription"`
		SigningSecret string `json:"signing_secret"`
	}
	status := we.call(t, "POST", "/v1/webhook-subscriptions", anyMap{
		"target_url": target, "event_types": eventTypes,
	}, nil, &created)
	if status != http.StatusCreated {
		t.Fatalf("create subscription → %d", status)
	}
	if created.SigningSecret == "" {
		t.Fatal("create did not return the one-time signing secret")
	}
	return created.Subscription.Id, created.SigningSecret
}

func TestWebhookSubscriptionCRUDOverHTTP(t *testing.T) {
	we := setupWebhooks(t)

	// http:// is rejected at create.
	if status := we.call(t, "POST", "/v1/webhook-subscriptions", anyMap{
		"target_url": "http://insecure.example/hook", "event_types": []string{"deal.created"},
	}, nil, nil); status != 422 {
		t.Fatalf("http target → %d, want 422", status)
	}
	// An unknown event type is rejected.
	if status := we.call(t, "POST", "/v1/webhook-subscriptions", anyMap{
		"target_url": "https://ok.example/hook", "event_types": []string{"nonsense.happened"},
	}, nil, nil); status != 422 {
		t.Fatalf("unknown event type → %d, want 422", status)
	}

	subID, secret := we.createSubscription(t, "https://ok.example/hook", []string{"deal.created"})

	// The secret is NEVER returned by a read.
	var got map[string]any
	if status := we.call(t, "GET", "/v1/webhook-subscriptions/"+subID, nil, nil, &got); status != http.StatusOK {
		t.Fatalf("get → %d", status)
	}
	if _, leaked := got["signing_secret"]; leaked {
		t.Fatal("GET leaked the signing secret — it must exist on the wire exactly once")
	}
	if _, leaked := got["signing_secret_ref"]; leaked {
		t.Fatal("GET leaked the sealed secret ref")
	}

	// Rotate returns a NEW secret, once.
	var rotated struct {
		SigningSecret string `json:"signing_secret"`
	}
	if status := we.call(t, "POST", "/v1/webhook-subscriptions/"+subID+"/rotate-secret", nil, nil, &rotated); status != http.StatusOK {
		t.Fatalf("rotate → %d", status)
	}
	if rotated.SigningSecret == "" || rotated.SigningSecret == secret {
		t.Fatal("rotate did not return a fresh secret")
	}

	// Pause via PATCH, then archive.
	if status := we.call(t, "PATCH", "/v1/webhook-subscriptions/"+subID, anyMap{"state": "paused"}, nil, nil); status != http.StatusOK {
		t.Fatalf("pause → %d", status)
	}
	if status := we.call(t, "DELETE", "/v1/webhook-subscriptions/"+subID, nil, nil, nil); status != http.StatusOK {
		t.Fatalf("archive → %d", status)
	}
	if status := we.call(t, "GET", "/v1/webhook-subscriptions/"+subID, nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("archived subscription still visible → %d, want 404", status)
	}
}

func TestWebhookDeliverySignedExactlyOnce(t *testing.T) {
	we := setupWebhooks(t)
	rcv := newReceiver(t, http.StatusOK)
	now := time.Now().UTC()
	deliverer := newTestDeliverer(we, &now, rcv.server.Client())

	_, secret := we.createSubscription(t, rcv.server.URL+"/hook", []string{"deal.created"})

	// A non-matching event delivers nothing.
	if err := deliverer.HandleEvent(context.Background(), makeEnvelope(we.wsID, "deal.updated")); err != nil {
		t.Fatalf("handle non-matching: %v", err)
	}
	if n := rcv.count.Load(); n != 0 {
		t.Fatalf("non-matching event produced %d POSTs, want 0", n)
	}

	// A matching event delivers exactly one signed POST.
	env := makeEnvelope(we.wsID, "deal.created")
	if err := deliverer.HandleEvent(context.Background(), env); err != nil {
		t.Fatalf("handle matching: %v", err)
	}
	// A redelivery of the SAME bus event must not double-POST.
	if err := deliverer.HandleEvent(context.Background(), env); err != nil {
		t.Fatalf("handle redelivery: %v", err)
	}
	if n := rcv.count.Load(); n != 1 {
		t.Fatalf("matching event produced %d POSTs, want exactly 1 (idempotent)", n)
	}

	hit := rcv.snapshot()[0]
	if hit.event != "deal.created" {
		t.Errorf("X-Margince-Event = %q, want deal.created", hit.event)
	}
	if hit.delivery == "" {
		t.Error("X-Margince-Delivery header missing")
	}
	// The signature verifies against the returned secret over the raw body.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(hit.body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if hit.signature != want {
		t.Errorf("signature = %q, want %q (HMAC of the raw body under the subscription secret)", hit.signature, want)
	}
}

func TestWebhookRetryThenDeadLetterThenReplay(t *testing.T) {
	we := setupWebhooks(t)
	rcv := newReceiver(t, http.StatusInternalServerError) // endpoint is down
	now := time.Now().UTC()
	deliverer := newTestDeliverer(we, &now, rcv.server.Client())

	subID, _ := we.createSubscription(t, rcv.server.URL+"/hook", []string{"deal.created"})

	// First attempt fails → the delivery is parked for retry, not dropped.
	if err := deliverer.HandleEvent(context.Background(), makeEnvelope(we.wsID, "deal.created")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	assertDeliveryStatus(t, we, subID, "retrying", 1)

	// Advance past each backoff deadline and sweep, until the budget is
	// spent and the delivery is dead-lettered.
	for i := 0; i < 8; i++ {
		now = now.Add(64 * time.Second) // beyond the largest backoff gap
		if err := deliverer.SweepOnce(context.Background()); err != nil {
			t.Fatalf("sweep: %v", err)
		}
	}
	assertDeliveryStatus(t, we, subID, "dead_lettered", 6)
	if got := rcv.count.Load(); got != 6 {
		t.Fatalf("endpoint saw %d attempts, want the 6-attempt budget", got)
	}

	// The endpoint recovers; a replay of the parked delivery succeeds.
	rcv.setStatus(http.StatusOK)
	var deliveries struct {
		Data []struct {
			Id     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if status := we.call(t, "GET", "/v1/webhook-subscriptions/"+subID+"/deliveries", nil, nil, &deliveries); status != http.StatusOK {
		t.Fatalf("list deliveries → %d", status)
	}
	if len(deliveries.Data) != 1 || deliveries.Data[0].Status != "dead_lettered" {
		t.Fatalf("dead-letter inspection wrong: %+v", deliveries.Data)
	}
	deliveryID := deliveries.Data[0].Id

	// Replay the parked delivery through the engine. The HTTP replay
	// endpoint is human-gated (asserted by the agent-policy + CRUD tests);
	// here we drive the engine directly so it reaches the loopback receiver
	// (the api role's deliverer uses the netguard-guarded client, which
	// refuses 127.0.0.1 by design — the same seam the delivery tests use).
	// A system principal satisfies the gate, and the workspace is bound.
	sysCtx := principal.WithActor(
		principal.WithWorkspaceID(context.Background(), we.wsID),
		principal.Principal{Type: principal.PrincipalSystem, ID: "system"})
	replayed, err := deliverer.Replay(sysCtx, ids.MustParse(subID), ids.MustParse(deliveryID))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replayed.Status != "delivered" {
		t.Fatalf("after replay status = %q, want delivered (no silent loss)", replayed.Status)
	}
}

func assertDeliveryStatus(t *testing.T, we *webhookEnv, subID, wantStatus string, wantAttempts int) {
	t.Helper()
	var deliveries struct {
		Data []struct {
			Status   string `json:"status"`
			Attempts int    `json:"attempts"`
		} `json:"data"`
	}
	if status := we.call(t, "GET", "/v1/webhook-subscriptions/"+subID+"/deliveries", nil, nil, &deliveries); status != http.StatusOK {
		t.Fatalf("list deliveries → %d", status)
	}
	if len(deliveries.Data) != 1 {
		t.Fatalf("want exactly one delivery row, got %d", len(deliveries.Data))
	}
	if deliveries.Data[0].Status != wantStatus || deliveries.Data[0].Attempts != wantAttempts {
		t.Fatalf("delivery = {%s, attempts %d}, want {%s, attempts %d}",
			deliveries.Data[0].Status, deliveries.Data[0].Attempts, wantStatus, wantAttempts)
	}
}
