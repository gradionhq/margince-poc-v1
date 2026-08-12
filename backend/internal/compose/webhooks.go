// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/webhooks"
)

// newWebhookHandlers builds the outbound-webhook transport (E10/S-E10.6,
// B-E10.13). cipher may be nil: the read surface (list/get/deliveries)
// still works, but any path that must seal or use a signing secret
// (create/rotate, and delivery/replay) answers an honest 503 rather than
// shipping an unsigned or guessable delivery. The api role supplies the
// deployment key via WithWebhookSigningKey.
func newWebhookHandlers(pool *pgxpool.Pool, cipher *webhooks.Cipher, log *slog.Logger) webhooks.Handlers {
	store := webhooks.NewStore(pool, cipher)
	// The HTTP-transport deliverer serves replay only (re-sending an
	// already-authorized delivery), so it needs no principal resolver — the
	// owner-scoped fan-out lives on the bus-consumer deliverer wired in the
	// worker/api roles (WithWebhookConsumer).
	deliverer := webhooks.NewDeliverer(store, webhooks.NewGuardedClient(), nil, nil, log)
	return webhooks.NewHandlers(store, deliverer)
}

// NewWebhookDeliverer builds the bus-consumer / retry-sweep deliverer for
// a process role that runs outbound-webhook delivery (worker, or api under
// --inline-relay). It owns the owner-scoped fan-out, so it carries the
// identity-backed principal resolver (authz.Resolver): a webhook only ever
// delivers an event its owner may see (BYO-EVT-4). key is the base64
// 32-byte signing-secret sealing key.
func NewWebhookDeliverer(pool *pgxpool.Pool, key string, log *slog.Logger) (*webhooks.Deliverer, error) {
	raw, err := webhooks.DecodeKey(key)
	if err != nil {
		return nil, fmt.Errorf("webhook signing key: %w", err)
	}
	cipher, err := webhooks.NewCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("webhook cipher: %w", err)
	}
	store := webhooks.NewStore(pool, cipher)
	return webhooks.NewDeliverer(store, webhooks.NewGuardedClient(), nil, identity.NewService(pool), log), nil
}

// WithWebhookSigningKey enables the mutating outbound-webhook surface: the
// 32-byte deployment key seals each subscription's signing secret at rest,
// so create/rotate succeed and a parked delivery can be replayed and
// signed. Without it those paths answer 503; the read surface still lists.
func WithWebhookSigningKey(cipher *webhooks.Cipher) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.webhooksHandlers = newWebhookHandlers(pool, cipher, s.log)
	}
}

// WithWebhookKey is WithWebhookSigningKey from the base64 key string a
// process role sources from its environment — it decodes and builds the
// cipher, failing the boot on an invalid key rather than silently leaving
// the surface at 503.
func WithWebhookKey(key string) (Option, error) {
	raw, err := webhooks.DecodeKey(key)
	if err != nil {
		return nil, fmt.Errorf("webhook signing key: %w", err)
	}
	cipher, err := webhooks.NewCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("webhook cipher: %w", err)
	}
	return WithWebhookSigningKey(cipher), nil
}
