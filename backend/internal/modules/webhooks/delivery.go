// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
)

const (
	// maxAttempts is the total delivery budget: after the 6th failed
	// attempt the delivery is parked in the dead-letter store (B-E10.13c).
	maxAttempts = 6
	// backoffCap bounds a single retry gap; the exponential schedule
	// (1s, 2s, 4s, 8s, 16s) never reaches it within the budget, but it is
	// the stated ceiling and guards against a future budget increase.
	backoffCap = 32 * time.Second
	// sweepBatch bounds how many due retries one sweep tick claims.
	sweepBatch = 128
)

// backoff is the delay before the next attempt after `attempts` have
// already failed: exponential 1s, 2s, 4s, … capped at backoffCap.
func backoff(attempts int) time.Duration {
	d := time.Second << (attempts - 1)
	if d > backoffCap || d <= 0 {
		return backoffCap
	}
	return d
}

// Deliverer fans matching bus events to their subscribers and drives the
// retry/dead-letter state machine. It is the sole holder of the HTTP
// transport and the signing cipher — the two capabilities a delivery
// needs that a plain CRUD store does not.
type Deliverer struct {
	store    *Store
	client   HTTPDoer
	clock    func() time.Time
	resolver authz.Resolver
	log      *slog.Logger
}

// NewDeliverer wires the delivery engine. A nil clock defaults to the wall
// clock; tests inject a controllable one so the backoff schedule is
// deterministic (no sleeps). resolver bounds the fan-out to each
// subscription owner's row scope (B-E10.15/BYO-EVT-4); it is required on
// the bus-consumer path (HandleEvent) and may be nil on a replay-only
// deliverer (Replay re-sends an already-authorized delivery, it never
// fans out).
func NewDeliverer(store *Store, client HTTPDoer, clock func() time.Time, resolver authz.Resolver, log *slog.Logger) *Deliverer {
	if clock == nil {
		clock = time.Now
	}
	return &Deliverer{store: store, client: client, clock: clock, resolver: resolver, log: log}
}

// HandleEvent is the cg:webhooks consumer entry point: for each active
// subscription matching the event's type, deliver ONLY if the
// subscription's owner may see the event's subject (BYO-EVT-4 — no
// privilege escalation via a webhook), enqueue one pending delivery
// (idempotent on the bus event), then attempt each immediately. Per-target
// HTTP failures are recorded as retrying and left to the sweeper — only an
// enqueue failure (which recorded nothing) is returned, so the bus entry
// redelivers and the idempotent enqueue makes it a no-op.
func (d *Deliverer) HandleEvent(ctx context.Context, env kevents.Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("webhooks: marshaling envelope %s: %w", env.EventID, err)
	}
	wsCtx := d.systemContext(ctx, env.WorkspaceID)
	cands, err := d.store.matchingSubscriptions(wsCtx, env.Type)
	if err != nil {
		return fmt.Errorf("webhooks: matching subscriptions for %s: %w", env.Type, err)
	}
	visible := make([]ids.UUID, 0, len(cands))
	for _, c := range cands {
		ok, err := d.ownerCanSee(wsCtx, env, c.ownerID)
		if err != nil {
			// One owner's resolver/visibility failure must not strand the
			// rest of the fan-out; skip it (fail-closed for this sub) and
			// let the bus redelivery re-evaluate on the next pass.
			d.log.Error("webhooks: owner visibility check", "subscription", c.id, "owner", c.ownerID, "event", env.EventID, "err", err)
			continue
		}
		if ok {
			visible = append(visible, c.id)
		}
	}
	targets, err := d.store.enqueueForSubscriptions(wsCtx, visible, env.Type, env.EventID, body)
	if err != nil {
		return fmt.Errorf("webhooks: enqueue for %s: %w", env.Type, err)
	}
	for _, t := range targets {
		d.deliverOnce(wsCtx, t)
	}
	return nil
}

// ownerCanSee resolves the subscription owner's LIVE RBAC and reports
// whether the event's subject entity is within that principal's row scope
// (BYO-EVT-4). It is the gate at ENQUEUE time: a subscription's fan-out is
// authorized against the owner's grants as they stand when the event
// arrives, so a revocation that lands before the event stops delivery.
// (A delivery, once enqueued, carries its frozen payload through retry and
// replay without re-checking — those re-send an already-authorized
// delivery to the owner's own endpoint, not a fresh fan-out.) A
// deactivated/absent owner (ErrNotFound) sees nothing.
func (d *Deliverer) ownerCanSee(ctx context.Context, env kevents.Envelope, ownerID ids.UUID) (bool, error) {
	if env.Entity.Type == "" || env.Entity.ID.IsZero() {
		// An entity-less event names no subject to scope by; such types are
		// excluded from the subscribable catalog (validateEventTypes), so a
		// subscription can never match one — defensive.
		return false, nil
	}
	if d.resolver == nil {
		return false, errors.New("webhooks: no principal resolver configured for owner-scoped fan-out")
	}
	rbac, err := d.resolver.EffectiveRBAC(ctx, env.WorkspaceID, ownerID)
	if errors.Is(err, apperrors.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ownerCtx := principal.WithActor(ctx, principal.Principal{
		Type:        principal.PrincipalHuman,
		ID:          "human:" + ownerID.String(),
		UserID:      ownerID,
		TeamIDs:     rbac.TeamIDs,
		Permissions: rbac.Permissions,
	})
	return d.store.entityVisibleTo(ownerCtx, env.Entity.Type, env.Entity.ID)
}

// RunRetrySweep re-attempts due retries on a ticker until ctx is
// canceled. Each tick claims a bounded batch of parked deliveries whose
// backoff has elapsed and whose subscription is still active.
func (d *Deliverer) RunRetrySweep(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := d.SweepOnce(ctx); err != nil && ctx.Err() == nil {
			d.log.Error("webhooks: retry sweep", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// SweepOnce runs a single due-retry pass: it claims a bounded batch of
// parked deliveries whose backoff has elapsed and re-attempts each. It is
// the unit the ticker loop drives, exposed so a test can step the schedule
// deterministically under an injected clock (no sleeps).
func (d *Deliverer) SweepOnce(ctx context.Context) error {
	workspaces, err := d.store.liveWorkspaces(ctx)
	if err != nil {
		return err
	}
	now := d.clock()
	for _, wsID := range workspaces {
		wsCtx := d.systemContext(ctx, wsID)
		due, err := d.store.dueRetries(wsCtx, now, sweepBatch)
		if err != nil {
			// One tenant's failure must not starve the rest of the fleet.
			d.log.Error("webhooks: scanning due retries", "workspace", wsID, "err", err)
			continue
		}
		for _, deliveryID := range due {
			t, err := d.store.loadTarget(wsCtx, deliveryID)
			if err != nil {
				d.log.Warn("webhooks: loading due delivery", "delivery", deliveryID, "err", err)
				continue
			}
			d.deliverOnce(wsCtx, t)
		}
	}
	return nil
}

// Replay re-attempts a parked (or any) delivery on demand (B-E10.13c). It
// is a human action: gated, existence-hiding, and audited. The ctx already
// carries the acting human and workspace from the request middleware.
func (d *Deliverer) Replay(ctx context.Context, subID, deliveryID ids.UUID) (Delivery, error) {
	if err := d.store.requireReplay(ctx, subID, deliveryID); err != nil {
		return Delivery{}, err
	}
	t, err := d.store.loadTarget(ctx, deliveryID)
	if err != nil {
		return Delivery{}, err
	}
	if err := d.store.resetForReplay(ctx, deliveryID); err != nil {
		return Delivery{}, err
	}
	// A replay resets attempts to a fresh budget: the operator is
	// asserting the endpoint is fixed, so the exponential clock restarts.
	t.priorAttempts = 0
	d.deliverOnce(ctx, t)
	return d.store.getDelivery(ctx, deliveryID)
}

// deliverOnce performs one attempt and records its outcome. It never
// returns an error: the outcome IS the record, and a failure to persist
// it is logged (the sweeper's re-scan is the recovery, and the row's prior
// state is safe).
func (d *Deliverer) deliverOnce(ctx context.Context, t attemptTarget) {
	res := d.attempt(ctx, t)
	if err := d.store.recordOutcome(ctx, t, res, d.clock()); err != nil {
		d.log.Error("webhooks: recording delivery outcome", "delivery", t.deliveryID, "err", err)
	}
}

// attempt signs and POSTs the stored body, returning the outcome. A
// non-2xx or a transport error is a failure; the receiver's response body
// is read (capped) only to keep the connection reusable and is discarded.
func (d *Deliverer) attempt(ctx context.Context, t attemptTarget) outcome {
	if d.store.cipher == nil {
		return outcome{failure: "signing key not configured"}
	}
	secret, err := d.store.cipher.open(t.sealedSecret)
	if err != nil {
		d.log.Error("webhooks: unsealing signing secret", "subscription", t.subID, "err", err)
		return outcome{failure: "signing secret could not be unsealed"}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.targetURL, bytes.NewReader(t.payload))
	if err != nil {
		return outcome{failure: "building request: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Margince-Webhooks/1")
	req.Header.Set(HeaderEvent, t.eventType)
	req.Header.Set(HeaderDelivery, t.deliveryID.String())
	req.Header.Set(HeaderSignature, Sign(secret, t.payload))

	resp, err := d.client.Do(req)
	if err != nil {
		return outcome{failure: "request failed: " + err.Error()}
	}
	defer func() {
		//craft:ignore swallowed-errors draining the capped body to reuse the connection; a read error here has no recovery and the outcome is already decided
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		//craft:ignore swallowed-errors close of a receiver response we do not read; the outcome is the status code already captured
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return outcome{statusCode: resp.StatusCode}
	}
	return outcome{statusCode: resp.StatusCode, failure: fmt.Sprintf("endpoint returned %d", resp.StatusCode)}
}

// systemContext binds the tenant and a system principal for a bus-driven
// (non-request) write, mirroring the search embed generator: the delivery
// worker acts as the system, not as any human, over the whole workspace.
func (d *Deliverer) systemContext(ctx context.Context, workspaceID ids.UUID) context.Context {
	ctx = principal.WithWorkspaceID(ctx, workspaceID)
	return principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalSystem, ID: "system"})
}
