// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Outbound webhooks (B-E10.13a/b/c + B-E10.15) over the real stack: the
// CRUD surface through HTTP (secret once, never again; workspace-scoped),
// and the delivery engine driven directly against the migrated Postgres
// with an httptest receiver and an injected clock — a matching event is
// delivered exactly once as an HMAC-signed POST, a failing endpoint is
// retried with backoff then dead-lettered, a parked delivery replays to
// 200, and the fan-out is bounded to the subscription owner's live
// visibility (a revoked owner receives nothing — BYO-EVT-4, enforced at
// delivery time). The bus subscriber is thin plumbing (tested in
// platform/events); what matters here is the delivery LOGIC, so it is
// exercised via the deliverer's own entry points, not through Redis.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/webhooks"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// webhookEnv bundles the HTTP surface, the app pool and the shared cipher
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
	// One key for both roles: the HTTP surface seals the secret, the
	// deliverer opens it — they must share the deployment key.
	key := bytes.Repeat([]byte{0x5a}, webhooks.WebhookKeyBytes)
	cipher, err := webhooks.NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	e := setupWithOptions(t, compose.WithWebhookSigningKey(cipher))
	e.bootstrapWorkspace(t)

	var wsID ids.UUID
	if err := e.owner.QueryRow(context.Background(),
		`SELECT id FROM workspace WHERE slug = $1`, e.slug).Scan(&wsID); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}
	return &webhookEnv{env: e, pool: e.pool, cipher: cipher, wsID: wsID}
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
	webhookID string
	timestamp string
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
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		r.mu.Lock()
		r.hits = append(r.hits, receivedHit{
			event:     req.Header.Get(webhooks.HeaderEvent),
			webhookID: req.Header.Get(webhooks.HeaderWebhookID),
			timestamp: req.Header.Get(webhooks.HeaderWebhookTimestamp),
			signature: req.Header.Get(webhooks.HeaderWebhookSignature),
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
// which is exactly why the delivery client is an injectable seam), a
// controllable clock, and the real identity-backed principal resolver so
// the owner-scoped fan-out (BYO-EVT-4) runs against live grants.
func newTestDeliverer(we *webhookEnv, now *time.Time, client *http.Client) *webhooks.Deliverer {
	store := webhooks.NewStore(we.pool, we.cipher)
	clock := func() time.Time { return *now }
	return webhooks.NewDeliverer(store, client, clock, identity.NewService(we.pool),
		slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

// makeEnvelope builds a matching bus envelope naming a deal subject. The
// bootstrap admin owns the subscription and holds row_scope=all, so the
// subject is visible and delivery proceeds; the owner-scope suppression
// path is exercised separately by revoking the owner.
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

// makeEnvelopeFor is makeEnvelope with an explicit subject entity type —
// used to prove the fan-out's fail-closed classification (an unclassified
// subject is never delivered; a workspace-level subject is).
func makeEnvelopeFor(wsID ids.UUID, eventType, entityType string) kevents.Envelope {
	env := makeEnvelope(wsID, eventType)
	env.Entity = kevents.EntityRef{Type: entityType, ID: ids.NewV7()}
	return env
}

// createSubscription registers a subscription over HTTP and returns its id
// and the one-time signing secret.
func (we *webhookEnv) createSubscription(t *testing.T, target string, eventTypes []string) (string, string) {
	t.Helper()
	var created struct {
		Subscription struct {
			ID string `json:"id"`
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
	return created.Subscription.ID, created.SigningSecret
}

// TestNewWebhookDelivererBuildsFromKey covers the process-role deliverer
// builder both roles use: a valid key yields a deliverer; a non-base64 or
// wrong-length key fails the boot loudly.
func TestNewWebhookDelivererBuildsFromKey(t *testing.T) {
	we := setupWebhooks(t)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	valid := base64.StdEncoding.EncodeToString(make([]byte, webhooks.WebhookKeyBytes))
	d, err := compose.NewWebhookDeliverer(we.pool, valid, log)
	if err != nil || d == nil {
		t.Fatalf("NewWebhookDeliverer(valid) d=%v err=%v", d, err)
	}
	if _, err := compose.NewWebhookDeliverer(we.pool, "not base64!!!", log); err == nil {
		t.Fatal("NewWebhookDeliverer must reject a non-base64 key")
	}
	if _, err := compose.NewWebhookDeliverer(we.pool, base64.StdEncoding.EncodeToString(make([]byte, 16)), log); err == nil {
		t.Fatal("NewWebhookDeliverer must reject a wrong-length key")
	}
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
	// A pipeline (entity-less) event type is not subscribable (BYO-EVT-4).
	if status := we.call(t, "POST", "/v1/webhook-subscriptions", anyMap{
		"target_url": "https://ok.example/hook", "event_types": []string{"capture.received"},
	}, nil, nil); status != 422 {
		t.Fatalf("pipeline event type → %d, want 422", status)
	}

	subID, secret := we.createSubscription(t, "https://ok.example/hook", []string{"deal.created"})

	// The list surface returns the subscription (and never the secret).
	var list struct {
		Data []struct {
			ID            string `json:"id"`
			SigningSecret string `json:"signing_secret"`
		} `json:"data"`
	}
	if status := we.call(t, "GET", "/v1/webhook-subscriptions", nil, nil, &list); status != http.StatusOK {
		t.Fatalf("list → %d", status)
	}
	if len(list.Data) != 1 || list.Data[0].ID != subID {
		t.Fatalf("list did not return the created subscription: %+v", list.Data)
	}
	if list.Data[0].SigningSecret != "" {
		t.Fatal("list leaked the signing secret")
	}

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

	// An empty update body is a 422 at runtime, matching the contract's
	// minProperties:1 — never a silent no-op.
	if status := we.call(t, "PATCH", "/v1/webhook-subscriptions/"+subID, anyMap{}, nil, nil); status != 422 {
		t.Fatalf("empty PATCH → %d, want 422", status)
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
	if hit.webhookID == "" {
		t.Error("webhook-id header missing")
	}
	if hit.timestamp == "" {
		t.Error("webhook-timestamp header missing")
	}
	// The signature verifies against the returned secret over
	// "{webhook-id}.{webhook-timestamp}.{body}" (Standard Webhooks scheme):
	// independently recomputed here (not via webhooks.Sign) so the test
	// would catch a regression in the production signer itself.
	ts, err := strconv.ParseInt(hit.timestamp, 10, 64)
	if err != nil {
		t.Fatalf("webhook-timestamp %q is not a unix-seconds integer: %v", hit.timestamp, err)
	}
	want := verifySWSignature(t, secret, hit.webhookID, ts, hit.body)
	if hit.signature != want {
		t.Errorf("signature = %q, want %q (SW HMAC over id.timestamp.body under the subscription secret)", hit.signature, want)
	}
}

// verifySWSignature independently recomputes the Standard Webhooks
// "webhook-signature" value from the raw wire inputs — using this test's own
// HMAC call, not webhooks.Sign — so the assertion actually exercises the
// wire contract instead of the production code path signing against itself.
func verifySWSignature(t *testing.T, secret, webhookID string, ts int64, body []byte) string {
	t.Helper()
	keyB64 := strings.TrimPrefix(secret, "whsec_")
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		t.Fatalf("decoding signing secret: %v", err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(webhookID))
	mac.Write([]byte("."))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// TestWebhookFanOutStopsAtRevokedOwner proves the delivery-time RBAC gate
// (BYO-EVT-4): once the subscription's owner is no longer a live user, the
// fan-out delivers nothing — no privilege escalation survives a revocation.
func TestWebhookFanOutStopsAtRevokedOwner(t *testing.T) {
	we := setupWebhooks(t)
	rcv := newReceiver(t, http.StatusOK)
	now := time.Now().UTC()
	deliverer := newTestDeliverer(we, &now, rcv.server.Client())

	we.createSubscription(t, rcv.server.URL+"/hook", []string{"deal.created"})

	// Revoke the owner (the bootstrap admin) by archiving the user row,
	// through a workspace-bound owner tx so FORCE RLS admits the update.
	ctx := context.Background()
	tx, err := we.owner.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	//craft:ignore swallowed-errors error-path safety net; the Commit below is asserted, after which this rollback is a designed no-op
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, we.wsID.String()); err != nil {
		t.Fatalf("set guc: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE app_user SET archived_at = now() WHERE workspace_id = $1`, we.wsID); err != nil {
		t.Fatalf("revoke owner: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := deliverer.HandleEvent(ctx, makeEnvelope(we.wsID, "deal.created")); err != nil {
		t.Fatalf("handle after revoke: %v", err)
	}
	if n := rcv.count.Load(); n != 0 {
		t.Fatalf("a revoked owner still received %d POSTs, want 0 (fan-out must stop at delivery time)", n)
	}
}

// TestWebhookFanOutFailsClosedForUnclassifiedSubject proves the delivery
// gate is fail-closed (BYO-EVT-4): a matching event whose subject type has
// no row-scope probe and is not on the workspace-level allow-list is NOT
// delivered — even to a row_scope=all owner — while a genuinely
// workspace-level subject (pipeline config) still is.
func TestWebhookFanOutFailsClosedForUnclassifiedSubject(t *testing.T) {
	we := setupWebhooks(t)
	rcv := newReceiver(t, http.StatusOK)
	now := time.Now().UTC()
	deliverer := newTestDeliverer(we, &now, rcv.server.Client())

	we.createSubscription(t, rcv.server.URL+"/hook", []string{"deal.created", "offer.created"})

	// An unclassified subject type falls through to the fail-closed default
	// → zero deliveries (no silent fan-out-to-everyone for a new subject).
	if err := deliverer.HandleEvent(context.Background(), makeEnvelopeFor(we.wsID, "deal.created", "mystery_object")); err != nil {
		t.Fatalf("handle unclassified: %v", err)
	}
	if n := rcv.count.Load(); n != 0 {
		t.Fatalf("unclassified subject produced %d POSTs, want 0 (fail-closed)", n)
	}

	// An offer is scoped through its parent deal; an offer that does not
	// resolve (no such row) is denied, not fanned out.
	if err := deliverer.HandleEvent(context.Background(), makeEnvelopeFor(we.wsID, "offer.created", "offer")); err != nil {
		t.Fatalf("handle unresolved offer: %v", err)
	}
	if n := rcv.count.Load(); n != 0 {
		t.Fatalf("unresolved offer produced %d POSTs, want 0 (fail-closed via parent deal)", n)
	}

	// A genuinely workspace-level subject (pipeline config, no per-owner
	// scope) IS delivered to a live owner.
	if err := deliverer.HandleEvent(context.Background(), makeEnvelopeFor(we.wsID, "deal.created", "pipeline")); err != nil {
		t.Fatalf("handle workspace-level: %v", err)
	}
	if n := rcv.count.Load(); n != 1 {
		t.Fatalf("workspace-level subject produced %d POSTs, want 1", n)
	}
}

func TestWebhookRetryThenDeadLetterThenReplay(t *testing.T) {
	we := setupWebhooks(t)
	rcv := newReceiver(t, http.StatusInternalServerError) // endpoint is down
	now := time.Now().UTC()
	deliverer := newTestDeliverer(we, &now, rcv.server.Client())

	subID, secret := we.createSubscription(t, rcv.server.URL+"/hook", []string{"deal.created"})

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

	// Same frozen body replayed on every retry: webhook-id is the delivery
	// id and stays STABLE across attempts (a receiver dedupes on it), while
	// webhook-timestamp is FRESH each time (replay defense) and each
	// attempt's signature independently verifies against that attempt's own
	// timestamp — a captured earlier signature would not match a later ts.
	hits := rcv.snapshot()
	if len(hits) != 6 {
		t.Fatalf("recorded %d hits, want 6", len(hits))
	}
	seenTimestamps := map[string]bool{}
	for i, h := range hits {
		if h.webhookID != hits[0].webhookID {
			t.Errorf("attempt %d webhook-id = %q, want stable %q across retries", i, h.webhookID, hits[0].webhookID)
		}
		if seenTimestamps[h.timestamp] {
			t.Errorf("attempt %d reused timestamp %q seen in an earlier attempt (not fresh per attempt)", i, h.timestamp)
		}
		seenTimestamps[h.timestamp] = true
		ts, err := strconv.ParseInt(h.timestamp, 10, 64)
		if err != nil {
			t.Fatalf("attempt %d webhook-timestamp %q not a unix-seconds integer: %v", i, h.timestamp, err)
		}
		if want := verifySWSignature(t, secret, h.webhookID, ts, h.body); h.signature != want {
			t.Errorf("attempt %d signature = %q, want %q", i, h.signature, want)
		}
	}

	// The endpoint recovers; a replay of the parked delivery succeeds.
	rcv.setStatus(http.StatusOK)
	var deliveries struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if status := we.call(t, "GET", "/v1/webhook-subscriptions/"+subID+"/deliveries", nil, nil, &deliveries); status != http.StatusOK {
		t.Fatalf("list deliveries → %d", status)
	}
	if len(deliveries.Data) != 1 || deliveries.Data[0].Status != "dead_lettered" {
		t.Fatalf("dead-letter inspection wrong: %+v", deliveries.Data)
	}
	deliveryID := deliveries.Data[0].ID

	// The HTTP replay endpoint runs the same engine under the api's
	// guarded client (which refuses the loopback receiver by design), so it
	// answers 200 with the re-attempted delivery — exercising the handler
	// path; the direct-engine replay below then proves the delivered path
	// against the injectable (unguarded) test client.
	if status := we.call(t, "POST", "/v1/webhook-subscriptions/"+subID+"/deliveries/"+deliveryID+"/replay", nil, nil, nil); status != http.StatusOK {
		t.Fatalf("http replay → %d, want 200", status)
	}

	// Replay the parked delivery through the engine. A system principal
	// satisfies the gate and the workspace is bound; the direct-engine call
	// reaches the loopback receiver (the api role's deliverer uses the
	// netguard-guarded client, which refuses 127.0.0.1 by design — the same
	// seam the delivery tests use).
	sysCtx := principal.WithActor(
		principal.WithWorkspaceID(context.Background(), we.wsID),
		principal.Principal{Type: principal.PrincipalSystem, ID: "system"})
	replayed, err := deliverer.Replay(sysCtx, mustParseUUID(t, subID), mustParseUUID(t, deliveryID))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replayed.Status != "delivered" {
		t.Fatalf("after replay status = %q, want delivered (no silent loss)", replayed.Status)
	}

	// The replay re-sends the SAME frozen body under the SAME webhook-id
	// (it is the same delivery, not a new one) but with a NEW timestamp —
	// the exact "replayed frozen body still verifies with a new ts" case:
	// a receiver that dedupes on webhook-id and enforces a timestamp
	// tolerance window accepts this as a legitimate re-attempt, not a
	// replay attack.
	replayHits := rcv.snapshot()
	if len(replayHits) == 0 {
		t.Fatal("replay produced no receiver hit")
	}
	last := replayHits[len(replayHits)-1]
	if last.webhookID != hits[0].webhookID {
		t.Fatalf("replay webhook-id = %q, want the original delivery id %q", last.webhookID, hits[0].webhookID)
	}
	if seenTimestamps[last.timestamp] {
		t.Fatalf("replay reused timestamp %q from an earlier attempt, want a fresh one", last.timestamp)
	}
	if !bytes.Equal(last.body, hits[0].body) {
		t.Fatal("replay altered the frozen payload body")
	}
	replayTS, err := strconv.ParseInt(last.timestamp, 10, 64)
	if err != nil {
		t.Fatalf("replay webhook-timestamp %q not a unix-seconds integer: %v", last.timestamp, err)
	}
	if want := verifySWSignature(t, secret, last.webhookID, replayTS, last.body); last.signature != want {
		t.Fatalf("replay signature = %q, want %q", last.signature, want)
	}

	// RunRetrySweep drives SweepOnce on a ticker until its context is done;
	// an already-cancelled context runs one pass then returns, exercising
	// the loop without a real sleep.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	deliverer.RunRetrySweep(cctx, time.Hour)
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

func mustParseUUID(t *testing.T, s string) ids.UUID {
	t.Helper()
	u, err := ids.Parse(s)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return u
}
