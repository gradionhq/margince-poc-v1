// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
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
}

func (c *capturingEnqueuer) Enqueue(_ context.Context, args river.JobArgs, opts *river.InsertOpts) error {
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
// body — the same basis the handler reconstructs (https://<host><uri>).
func signedWebhookRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/webhooks/hubspot", strings.NewReader(body))
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
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
