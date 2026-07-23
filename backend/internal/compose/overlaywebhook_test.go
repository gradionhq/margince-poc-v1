// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// capturingEnqueuer records the re-fetch jobs the handler enqueues, standing in
// for the real River inserter so the handler's verify→bind→enqueue decision is
// unit-testable without River or Postgres.
type capturingEnqueuer struct {
	jobs []OverlayRefetchArgs
	opts []*river.InsertOpts
	err  error // when set, Enqueue fails with it (the redelivery path)
}

func (c *capturingEnqueuer) Enqueue(_ context.Context, args river.JobArgs, opts *river.InsertOpts) error {
	if c.err != nil {
		return c.err
	}
	c.jobs = append(c.jobs, args.(OverlayRefetchArgs))
	c.opts = append(c.opts, opts)
	return nil
}

const testAppSecret = "test-app-secret"

// boundPortal is the portalId every test payload carries ("portalId":777) and
// the one newTestWebhookHandler's bind resolves to a workspace — a foreign
// portal returns ErrNotFound (fail-closed).
const boundPortal = "777"

// signedWebhookRequest builds a POST with a valid HubSpot v3 signature over the
// body at the current time.
func signedWebhookRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	return signedWebhookRequestAt(t, body, time.Now())
}

// signedWebhookRequestAt is signedWebhookRequest with an explicit signing
// timestamp, so a test can present a validly-signed request at a chosen time —
// e.g. a correctly-signed but stale one, isolating the replay guard from the
// signature check. The signature covers the same basis the handler
// reconstructs (https://<host><uri> + body + timestamp).
func signedWebhookRequestAt(t *testing.T, body string, at time.Time) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/webhooks/hubspot", strings.NewReader(body))
	ts := strconv.FormatInt(at.UnixMilli(), 10)
	uri := "https://" + r.Host + r.URL.RequestURI()
	mac := hmac.New(sha256.New, []byte(testAppSecret))
	mac.Write([]byte(http.MethodPost))
	mac.Write([]byte(uri))
	mac.Write([]byte(body))
	mac.Write([]byte(ts))
	r.Header.Set("X-HubSpot-Request-Timestamp", ts)
	r.Header.Set("X-HubSpot-Signature-v3", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return r
}

func newTestWebhookHandler(enq *capturingEnqueuer, boundWS ids.WorkspaceID) *hubspotWebhookHandler {
	return &hubspotWebhookHandler{
		clientSecret: testAppSecret,
		enqueue:      enq,
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		bind: func(_ context.Context, _, portalID string) (ids.WorkspaceID, error) {
			if portalID == boundPortal {
				return boundWS, nil
			}
			return ids.WorkspaceID{}, apperrors.ErrNotFound
		},
	}
}

// TestWebhookReceiverBoundSignalEnqueuesCoalescedRefetch (AC-OV-13 a/d): a
// validly-signed, portal-bound signal enqueues ONE re-fetch of the named
// record with coalescing opts (unique-by-args, scheduled a window ahead).
func TestWebhookReceiverBoundSignalEnqueuesCoalescedRefetch(t *testing.T) {
	enq := &capturingEnqueuer{}
	ws := ids.New[ids.WorkspaceKind]()
	h := newTestWebhookHandler(enq, ws)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedWebhookRequest(t, `[{"portalId":777,"objectId":42,"subscriptionType":"contact.propertyChange"}]`))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if len(enq.jobs) != 1 {
		t.Fatalf("enqueued %d jobs, want 1", len(enq.jobs))
	}
	if enq.jobs[0] != (OverlayRefetchArgs{Workspace: ws.String(), IncumbentClass: "contacts", ExternalID: "42"}) {
		t.Errorf("enqueued %+v, want contacts/42 for the bound workspace", enq.jobs[0])
	}
	// Coalescing (OVA-PARAM-10): unique-by-args + scheduled a window ahead so a
	// burst of edits to the same record collapses to one re-fetch.
	if !enq.opts[0].UniqueOpts.ByArgs || enq.opts[0].ScheduledAt.Before(time.Now()) {
		t.Errorf("enqueue opts = %+v, want ByArgs unique + a future ScheduledAt", enq.opts[0])
	}
}

// TestWebhookReceiverRejectsUnboundPortal (AC-OV-13 b): a signal whose portal
// maps to no active connection ingests nothing (fail-closed, no cross-tenant).
func TestWebhookReceiverRejectsUnboundPortal(t *testing.T) {
	enq := &capturingEnqueuer{}
	h := newTestWebhookHandler(enq, ids.New[ids.WorkspaceKind]())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedWebhookRequest(t, `[{"portalId":999,"objectId":42,"subscriptionType":"contact.propertyChange"}]`))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (accepted-but-ignored)", rec.Code)
	}
	if len(enq.jobs) != 0 {
		t.Errorf("an unbound portal must enqueue nothing, got %d jobs", len(enq.jobs))
	}
}

// TestWebhookReceiverRejectsBadSignature (AC-OV-13 c): a bad v3 signature is
// rejected, nothing ingested.
func TestWebhookReceiverRejectsBadSignature(t *testing.T) {
	enq := &capturingEnqueuer{}
	h := newTestWebhookHandler(enq, ids.New[ids.WorkspaceKind]())

	r := signedWebhookRequest(t, `[{"portalId":777,"objectId":42,"subscriptionType":"contact.propertyChange"}]`)
	r.Header.Set("X-HubSpot-Signature-v3", "deadbeef") // tamper
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(enq.jobs) != 0 {
		t.Errorf("a bad signature must enqueue nothing, got %d jobs", len(enq.jobs))
	}
}

// TestWebhookReceiverDropsUnmappedSubscription: a subscription type the mirror
// does not model is dropped (no guessed class), still 204.
func TestWebhookReceiverDropsUnmappedSubscription(t *testing.T) {
	enq := &capturingEnqueuer{}
	h := newTestWebhookHandler(enq, ids.New[ids.WorkspaceKind]())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedWebhookRequest(t, `[{"portalId":777,"objectId":42,"subscriptionType":"ticket.creation"}]`))

	if rec.Code != http.StatusNoContent || len(enq.jobs) != 0 {
		t.Errorf("an unmapped subscription must be dropped (204, no enqueue), got %d / %d jobs", rec.Code, len(enq.jobs))
	}
}

// TestOverlayRefetchWorkerRejectsMalformedWorkspace: a job carrying an
// unparseable workspace id is a permanent defect — the worker returns nil (no
// retry) rather than looping on an unfixable arg. This exercises the guard
// before any DB access.
func TestOverlayRefetchWorkerRejectsMalformedWorkspace(t *testing.T) {
	w := &overlayRefetchWorker{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	err := w.Work(context.Background(), &river.Job[OverlayRefetchArgs]{
		Args: OverlayRefetchArgs{Workspace: "not-a-uuid", IncumbentClass: "contacts", ExternalID: "1"},
	})
	if err != nil {
		t.Errorf("a malformed workspace id must return nil (not retryable), got %v", err)
	}
}

// TestWebhookReceiverRejectsNonPOST: only POST is a webhook delivery; any other
// method is 405 before any body/signature work.
func TestWebhookReceiverRejectsNonPOST(t *testing.T) {
	h := newTestWebhookHandler(&capturingEnqueuer{}, ids.New[ids.WorkspaceKind]())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/webhooks/hubspot", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// TestWebhookReceiverRejectsStaleOrMissingTimestamp (replay guard): a missing or
// too-old X-HubSpot-Request-Timestamp is 401 before the signature is even
// checked, and nothing is enqueued.
func TestWebhookReceiverRejectsStaleOrMissingTimestamp(t *testing.T) {
	enq := &capturingEnqueuer{}
	h := newTestWebhookHandler(enq, ids.New[ids.WorkspaceKind]())

	// No timestamp header at all.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/webhooks/hubspot", strings.NewReader(`[]`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing timestamp: status = %d, want 401", rec.Code)
	}

	// A correctly-signed but too-old request is a refused replay. Signing it
	// with the stale timestamp isolates the replay guard from the signature
	// check — the 401 can only come from the timestamp being stale, so this
	// regresses the guard's wiring, not merely the shared 401.
	stale := signedWebhookRequestAt(t, `[]`, time.Now().Add(-10*time.Minute))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, stale)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale timestamp: status = %d, want 401", rec.Code)
	}
	if len(enq.jobs) != 0 {
		t.Errorf("a rejected request must enqueue nothing, got %d", len(enq.jobs))
	}
}

// TestWebhookReceiverAnswers500OnEnqueueFailure: a transient enqueue failure
// answers 500 so HubSpot redelivers — safe because the enqueue is
// unique-by-args, so a redelivery cannot double-run the re-fetch.
func TestWebhookReceiverAnswers500OnEnqueueFailure(t *testing.T) {
	enq := &capturingEnqueuer{err: errors.New("river unavailable")}
	h := newTestWebhookHandler(enq, ids.New[ids.WorkspaceKind]())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedWebhookRequest(t, `[{"portalId":777,"objectId":42,"subscriptionType":"contact.propertyChange"}]`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 so HubSpot redelivers", rec.Code)
	}
}

// TestWebhookReceiverBindsOncePerPortalInABatch: a batch of events for the same
// bound portal costs ONE fleet-walk (the per-request cache) yet enqueues a
// re-fetch for each distinct mapped event.
func TestWebhookReceiverBindsOncePerPortalInABatch(t *testing.T) {
	var binds int
	ws := ids.New[ids.WorkspaceKind]()
	enq := &capturingEnqueuer{}
	h := &hubspotWebhookHandler{
		clientSecret: testAppSecret,
		enqueue:      enq,
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		bind: func(_ context.Context, _, portalID string) (ids.WorkspaceID, error) {
			binds++
			if portalID == boundPortal {
				return ws, nil
			}
			return ids.WorkspaceID{}, apperrors.ErrNotFound
		},
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedWebhookRequest(t,
		`[{"portalId":777,"objectId":1,"subscriptionType":"contact.creation"},{"portalId":777,"objectId":2,"subscriptionType":"deal.propertyChange"}]`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if binds != 1 {
		t.Errorf("a batch for one portal must bind once (cached), got %d binds", binds)
	}
	if len(enq.jobs) != 2 {
		t.Fatalf("two mapped events must enqueue two re-fetches, got %d", len(enq.jobs))
	}
	// Each event re-fetches its OWN record under its own class — a regression
	// that enqueued the same args twice, or mapped the deal event to the wrong
	// class, must fail here (not merely miscount).
	want := []OverlayRefetchArgs{
		{Workspace: ws.String(), IncumbentClass: "contacts", ExternalID: "1"},
		{Workspace: ws.String(), IncumbentClass: "deals", ExternalID: "2"},
	}
	for i, w := range want {
		if enq.jobs[i] != w {
			t.Errorf("job[%d] = %+v, want %+v", i, enq.jobs[i], w)
		}
	}
}

// TestFreshTimestamp exercises the replay-window predicate directly: now is
// fresh, a slightly-future timestamp is fresh (clock skew is symmetric), a
// too-old one is stale, and a missing/unparseable one is never fresh.
func TestFreshTimestamp(t *testing.T) {
	now := time.Now()
	fresh := map[string]string{
		"now":           strconv.FormatInt(now.UnixMilli(), 10),
		"future within": strconv.FormatInt(now.Add(time.Minute).UnixMilli(), 10),
	}
	for name, ts := range fresh {
		if !freshTimestamp(ts) {
			t.Errorf("%s (%s) must be fresh", name, ts)
		}
	}
	stale := map[string]string{
		"too old":       strconv.FormatInt(now.Add(-10*time.Minute).UnixMilli(), 10),
		"too far ahead": strconv.FormatInt(now.Add(10*time.Minute).UnixMilli(), 10),
		"empty":         "",
		"non-numeric":   "not-a-timestamp",
	}
	for name, ts := range stale {
		if freshTimestamp(ts) {
			t.Errorf("%s (%q) must not be fresh", name, ts)
		}
	}
}

// TestWithOverlayWebhookAbsentWithoutSecretOrInserter: the route is mounted only
// when BOTH an app secret and an inserter are present — never an open,
// unverified endpoint.
func TestWithOverlayWebhookAbsentWithoutSecretOrInserter(t *testing.T) {
	s := &Server{}
	WithOverlayWebhook(nil, "")(s, nil)
	if s.overlayWebhook != nil {
		t.Error("no app secret must leave the webhook route unmounted")
	}
	WithOverlayWebhook(nil, "a-secret")(s, nil)
	if s.overlayWebhook != nil {
		t.Error("a nil inserter must leave the webhook route unmounted")
	}
}
