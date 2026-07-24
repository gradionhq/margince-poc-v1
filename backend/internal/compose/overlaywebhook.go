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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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

// echoClassifier classifies one inbound propertyChange against the our-write
// ledger (OVA-DDL-6). overlay.WriteLedger.Classify satisfies it; a seam so the
// receiver's suppression decision is unit-testable without Postgres.
type echoClassifier func(ctx context.Context, objectClass, externalID, property, value string) (overlay.Classification, error)

// haltChecker reports whether a workspace's mirror is halted (a ledger collision
// tripped the fail-safe). overlay.WriteLedger.Halted satisfies it.
type haltChecker func(ctx context.Context) (bool, error)

type hubspotWebhookHandler struct {
	bind         portalBinder
	enqueue      refetchEnqueuer
	clientSecret string
	log          *slog.Logger
	// classify and halted are the echo-suppression ledger seams (OVA-DDL-6).
	// Both nil when no ledger is wired — the receiver then skips suppression and
	// the halt gate (the receiver still functions; it just cannot drop echoes).
	classify echoClassifier
	halted   haltChecker
}

// WithOverlayWebhook mounts POST /webhooks/hubspot. An empty client secret (or
// no inserter) leaves the route absent — never an open, unverified endpoint.
func WithOverlayWebhook(inserter *jobs.Runner, clientSecret string) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		if clientSecret == "" || inserter == nil {
			return
		}
		ledger := overlay.NewWriteLedger(pool)
		s.overlayWebhook = &hubspotWebhookHandler{
			bind: func(ctx context.Context, incumbent, portalID string) (ids.WorkspaceID, error) {
				return overlay.WorkspaceForPortal(ctx, pool, incumbent, portalID)
			},
			enqueue:      inserter,
			clientSecret: clientSecret,
			log:          s.log,
			classify:     ledger.Classify,
			halted:       ledger.Halted,
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
	cache := map[string]portalResolution{}
	haltByWS := map[string]bool{} // per-request halt cache (workspace → halted)
	for _, ev := range events {
		wsID, ok, err := h.resolveWorkspace(r, cache, ev)
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
		// Recognize what this lane handles BEFORE any ledger transaction: an
		// unmapped object type or a deletion-family action is dropped here, so a
		// dropped event never opens a halt/ledger transaction or risks a 500.
		class, ok := hubspot.ObjectClassForSubscription(ev.SubscriptionType)
		if !ok {
			continue
		}
		// Every ledger read/write is workspace-scoped (RLS): bind the resolved
		// tenant onto the context for the halt gate and the echo classification.
		wsCtx := principal.WithWorkspaceID(r.Context(), wsID.UUID)
		// Halt gate (OVA-AC-3 fail-safe): a mirror halted by a prior ledger
		// collision refuses further signals. Cached per workspace per request so
		// a batch reads the flag once; a mid-batch collision flips the cache
		// immediately so the rest of the batch is skipped without re-querying.
		if h.halted != nil {
			halted, cached := haltByWS[wsID.String()]
			if !cached {
				halted, err = h.halted(wsCtx)
				if err != nil {
					h.log.ErrorContext(wsCtx, "overlay webhook: reading the mirror-halt flag", "err", err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				haltByWS[wsID.String()] = halted
			}
			if halted {
				h.log.WarnContext(wsCtx, "overlay webhook: mirror is halted (ledger collision), ignoring the signal",
					"workspace", wsID.String())
				continue
			}
		}
		// Echo suppression (OVA-DDL-6): a propertyChange carrying a property we
		// just wrote (same value, inside the open window) is our own echo — drop
		// it so the overlay does not re-fetch its own write. A collision halts
		// the mirror (inside Classify) and is never silently suppressed.
		if h.classify != nil && ev.PropertyName != "" {
			switch verdict, err := h.classify(wsCtx, class, ev.ObjectIDString(), ev.PropertyName, ev.PropertyValue); {
			case err != nil:
				h.log.ErrorContext(wsCtx, "overlay webhook: classifying against the write ledger",
					"workspace", wsID.String(), "id", ev.ObjectIDString(), "property", ev.PropertyName, "err", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			case verdict == overlay.ClassEcho:
				continue // our own write-back echoing — suppress, no re-fetch
			case verdict == overlay.ClassCollision:
				// The mirror was just halted inside Classify. Reflect it in the
				// per-request cache so the rest of the batch is skipped, and do
				// NOT re-fetch — an operator must review before sync resumes.
				haltByWS[wsID.String()] = true
				h.log.ErrorContext(wsCtx, "overlay webhook: write-ledger value-hash collision — mirror halted",
					"workspace", wsID.String(), "id", ev.ObjectIDString(), "property", ev.PropertyName)
				continue
			}
		}
		if err := h.enqueueRefetch(r, wsID.String(), class, ev.ObjectIDString()); err != nil {
			h.log.ErrorContext(r.Context(), "overlay webhook: enqueueing re-fetch",
				"workspace", wsID.String(), "class", class, "id", ev.ObjectIDString(), "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// portalResolution is the per-request cache entry for one portal id: the
// resolved workspace and whether the portal is bound at all (an unbound portal
// is cached so a batch of its events costs one fleet-walk, never re-probed).
type portalResolution struct {
	ws    ids.WorkspaceID
	bound bool
}

// resolveWorkspace returns the workspace bound to the event's portal (via the
// per-request cache). ok=false with a nil error is a genuinely unbound portal
// (fail-closed — skip, ingest nothing); a non-nil error is a transient lookup
// failure the caller turns into a 500 so the signal is redelivered rather than
// lost. Only ErrNotFound is treated as unbound; every other error propagates.
func (h *hubspotWebhookHandler) resolveWorkspace(r *http.Request, cache map[string]portalResolution, ev hubspot.WebhookEvent) (ids.WorkspaceID, bool, error) {
	portal := ev.PortalIDString()
	if got, seen := cache[portal]; seen {
		return got.ws, got.bound, nil
	}
	ws, err := h.bind(r.Context(), incumbentHubSpot, portal)
	if err != nil {
		if !errors.Is(err, apperrors.ErrNotFound) {
			// Transient failure — do NOT cache it (a redelivery must re-probe)
			// and do NOT treat it as unbound; propagate so the caller 500s.
			return ids.WorkspaceID{}, false, err
		}
		// A genuinely unbound portal: cache the miss so a batch of events for
		// the same portal costs one fleet-walk, and never treat it as a tenant.
		cache[portal] = portalResolution{}
		h.log.WarnContext(r.Context(), "overlay webhook: portal not bound to an active connection, ignoring",
			"portal", portal)
		return ids.WorkspaceID{}, false, nil
	}
	cache[portal] = portalResolution{ws: ws, bound: true}
	return ws, true, nil
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
