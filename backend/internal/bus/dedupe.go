package bus

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
)

// DedupeTTL is how long a processed event_id is remembered: ≥ the stream
// retention horizon, so a consumer offline for the whole trim window can
// never re-see an event after its dedupe entry expired (events.md §4.3
// pins 96h against the ≥72h retention default).
const DedupeTTL = 96 * time.Hour

// Dedupe is the at-least-once safety net (events.md §3): it wraps a
// handler so a redelivered event_id is not re-processed by its consumer
// group. The order is run-THEN-mark, and the order is load-bearing: a
// mark written before the effect would, on a crash in between, survive as
// a claim with no effect — the redelivery would be swallowed and the
// event silently dropped. Marking after means a crash can only cause a
// re-run, which the authoritative idempotency layer (effects upsert by
// natural key — uq_activity_source and kin) absorbs as a no-op. This
// wrapper is therefore an optimization over that layer, never a
// correctness substitute for it.
func Dedupe(rdb *redis.Client, group string, next Handler) Handler {
	return func(ctx context.Context, env events.Envelope) error {
		key := dedupeKey(group, env)
		seen, err := rdb.Exists(ctx, key).Result()
		if err != nil {
			return fmt.Errorf("bus: dedupe check for %s: %w", env.EventID, err)
		}
		if seen == 1 {
			return nil // already processed: ack, no second effect
		}

		if err := next(ctx, env); err != nil {
			return err // no mark yet, so the redelivery retries naturally
		}

		// A failed mark is surfaced: the entry stays pending, the
		// redelivery re-runs the effect into the natural-key no-op and
		// retries the mark — strictly better than acking with the mark
		// unwritten and eating a re-run per redelivery forever.
		if err := rdb.Set(ctx, key, 1, DedupeTTL).Err(); err != nil {
			return fmt.Errorf("bus: marking %s processed: %w", env.EventID, err)
		}
		return nil
	}
}

// dedupeKey is per group: each consumer group owns its own processed set
// (events.md §4.3 — every group sees every event once).
func dedupeKey(group string, env events.Envelope) string {
	return "gw:dedupe:" + group + ":" + env.EventID.String()
}
