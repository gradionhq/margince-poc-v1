// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The HubSpot webhook receiver (OVA-WIRE-10): the incumbent push lane that
// turns a change in HubSpot into a targeted mirror re-fetch. It is
// authenticated NOT by session but by HubSpot's v3 request signature (HMAC
// over our app secret — proving the sender) AND the payload's portalId bound
// to an active connection (OVA-DDL-3 — proving the tenant); the signature
// authenticates our one app across every portal it is installed on, so the
// portal binding is what stops cross-tenant mis-attribution. A bad signature
// or an unbound portal is rejected fail-closed, never ingested. The route is
// mounted only when the overlay app secret is configured (like the Gmail push
// webhook), never an open unverified endpoint. The receiver returns 2xx fast
// and does the re-fetch async (a coalesced River job); the body is a minimal
// invalidation signal, not trusted content — nothing from it is written
// directly, only a re-fetch of the named record through the idempotent ingest.

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// webhookMaxBody caps the request body the receiver reads: HubSpot batches are
// small, so anything past this is not a legitimate delivery. It is enforced
// with a MaxBytesReader so an oversized body is a distinct 413 (below), never a
// silent truncation that then mis-reports as a bad signature.
const webhookMaxBody = 1 << 20

// webhookMaxSkew rejects a request whose timestamp is too far from now — the
// replay guard HubSpot's v3 scheme pairs with the signature (the timestamp is
// part of the signed basis, so an attacker cannot backdate a captured request
// without breaking the HMAC, but a stale-but-valid replay is still refused).
const webhookMaxSkew = 5 * time.Minute

// webhookCoalesceWindow is OVA-PARAM-10: multiple signals for the same
// (workspace, object_class, external_id) within this window collapse to ONE
// re-fetch. The re-fetch job is scheduled this far ahead with unique-by-args,
// so a burst of edits to one record enqueues once (the later signals hit the
// still-scheduled unique job) instead of N reads against the shared budget.
const webhookCoalesceWindow = 5 * time.Second

// portalBinder resolves a webhook's portalId to the workspace whose active
// connection recorded it (OVA-DDL-3), or ErrNotFound fail-closed. A seam so
// the handler is unit-testable without the fleet-walk's Postgres.
type portalBinder func(ctx context.Context, incumbent, portalID string) (ids.WorkspaceID, error)

// refetchEnqueuer schedules the coalesced re-fetch job. *jobs.Runner satisfies
// it; a test substitutes a capturing fake.
type refetchEnqueuer interface {
	Enqueue(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) error
}

type hubspotWebhookHandler struct {
	bind         portalBinder
	enqueue      refetchEnqueuer
	clientSecret string
	log          *slog.Logger
}

// WithOverlayWebhook mounts POST /webhooks/hubspot. An empty client secret (or
// no inserter) leaves the route absent — never an open, unverified endpoint.
func WithOverlayWebhook(inserter *jobs.Runner, clientSecret string) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		if clientSecret == "" || inserter == nil {
			return
		}
		s.overlayWebhook = &hubspotWebhookHandler{
			bind: func(ctx context.Context, incumbent, portalID string) (ids.WorkspaceID, error) {
				return overlay.WorkspaceForPortal(ctx, pool, incumbent, portalID)
			},
			enqueue:      inserter,
			clientSecret: clientSecret,
			log:          s.log,
		}
	}
}

func (h *hubspotWebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// MaxBytesReader (not a bare LimitReader): an over-cap body surfaces as a
	// read error we answer 413 for, rather than being silently truncated and
	// then mis-rejected as a bad signature (which would make a valid oversized
	// batch retry forever against the wrong reason).
	r.Body = http.MaxBytesReader(w, r.Body, webhookMaxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// Replay guard: the timestamp is part of the signed basis, so reject a
	// stale one before spending an HMAC on it. 401 (not 400) so a transient
	// clock blip is retried by HubSpot rather than dropped.
	ts := r.Header.Get("X-HubSpot-Request-Timestamp")
	if !freshTimestamp(ts) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// Verify against the URL HubSpot signed: it always POSTs https to a public
	// host (webhooks require https), so the basis is https://<host><uri>.
	uri := "https://" + r.Host + r.URL.RequestURI()
	if !hubspot.VerifyWebhookSignature(h.clientSecret, http.MethodPost, uri, body, ts, r.Header.Get("X-HubSpot-Signature-v3")) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var events []hubspot.WebhookEvent
	if err := json.Unmarshal(body, &events); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Resolve each distinct portal once (the fleet-walk is not free), then
	// enqueue a coalesced re-fetch per bound, mapped event. A portal bound to
	// no active connection is skipped fail-closed — nothing ingested, no
	// cross-tenant write; an unmapped subscription type is likewise dropped.
	// A per-event enqueue failure answers 500 so HubSpot redelivers (the
	// enqueue is unique-by-args, so a redelivery cannot double-run).
	portalWS := map[string]string{} // portalId → workspace id ("" = unbound)
	for _, ev := range events {
		wsID, ok, err := h.resolveWorkspace(r, portalWS, ev)
		if err != nil {
			// A transient binding failure (DB/transaction) is NOT an unbound
			// portal: answer 500 so HubSpot redelivers rather than dropping the
			// signal on the floor (the coalesced enqueue is unique-by-args, so a
			// redelivery cannot double-run the re-fetch).
			h.log.ErrorContext(r.Context(), "overlay webhook: resolving the portal binding",
				"portal", ev.PortalIDString(), "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !ok {
			continue
		}
		class, ok := hubspot.ObjectClassForSubscription(ev.SubscriptionType)
		if !ok {
			continue
		}
		if err := h.enqueueRefetch(r, wsID, class, ev.ObjectIDString()); err != nil {
			h.log.ErrorContext(r.Context(), "overlay webhook: enqueueing re-fetch",
				"workspace", wsID, "class", class, "id", ev.ObjectIDString(), "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveWorkspace returns the workspace bound to the event's portal (via the
// per-request cache). ok=false with a nil error is a genuinely unbound portal
// (fail-closed — skip, ingest nothing); a non-nil error is a transient lookup
// failure the caller turns into a 500 so the signal is redelivered rather than
// lost. Only ErrNotFound is treated as unbound; every other error propagates.
func (h *hubspotWebhookHandler) resolveWorkspace(r *http.Request, cache map[string]string, ev hubspot.WebhookEvent) (string, bool, error) {
	portal := ev.PortalIDString()
	if ws, seen := cache[portal]; seen {
		return ws, ws != "", nil
	}
	ws, err := h.bind(r.Context(), incumbentHubSpot, portal)
	if err != nil {
		if !errors.Is(err, apperrors.ErrNotFound) {
			// Transient failure — do NOT cache it (a redelivery must re-probe)
			// and do NOT treat it as unbound; propagate so the caller 500s.
			return "", false, err
		}
		// A genuinely unbound portal: cache the miss so a batch of events for
		// the same portal costs one fleet-walk, and never treat it as a tenant.
		cache[portal] = ""
		h.log.WarnContext(r.Context(), "overlay webhook: portal not bound to an active connection, ignoring",
			"portal", portal)
		return "", false, nil
	}
	cache[portal] = ws.String()
	return ws.String(), true, nil
}

// enqueueRefetch schedules the coalesced re-fetch job.
func (h *hubspotWebhookHandler) enqueueRefetch(r *http.Request, workspace, class, externalID string) error {
	return h.enqueue.Enqueue(r.Context(), OverlayRefetchArgs{
		Workspace:      workspace,
		IncumbentClass: class,
		ExternalID:     externalID,
	}, &river.InsertOpts{
		ScheduledAt: time.Now().Add(webhookCoalesceWindow),
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByState: activeSweepStates},
	})
}

// freshTimestamp reports whether the millis-epoch request timestamp is within
// webhookMaxSkew of now. A missing or unparseable timestamp is not fresh.
func freshTimestamp(ts string) bool {
	ms, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	delta := time.Since(time.UnixMilli(ms))
	if delta < 0 {
		delta = -delta
	}
	return delta <= webhookMaxSkew
}
