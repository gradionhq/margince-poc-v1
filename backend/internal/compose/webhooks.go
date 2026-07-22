// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

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
	deliverer := webhooks.NewDeliverer(store, webhooks.NewGuardedClient(), nil, log)
	return webhooks.NewHandlers(store, deliverer)
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
