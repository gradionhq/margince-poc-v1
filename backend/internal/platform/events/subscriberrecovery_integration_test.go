// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package events

// A purge deletes stream keys, which takes their consumer groups with them.
// A subscriber that cannot recover from that logs NOGROUP once a second for
// the life of the process, so the recovery is part of the bus's contract.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestSubscriberSurvivesItsGroupBeingPurged(t *testing.T) {
	ctx, rdb := purgeTestRedis(t)
	group := kevents.Groups()[0]

	delivered := make(chan kevents.Envelope, 1)
	sub := NewSubscriber(rdb, group, func(_ context.Context, env kevents.Envelope) error {
		delivered <- env
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		if err := sub.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run: %v", err)
		}
	}()

	// Run calls ensureGroups exactly once, at startup, before entering its
	// read loop. Proving one delivery here is the only race-free way to know
	// that call has already happened: purging any earlier would let the
	// startup call recreate the group fresh on its own, and the test would
	// pass without ever exercising the mid-loop recovery it targets.
	publishProbe(t, ctx, rdb, group.Streams[0])
	select {
	case <-delivered:
	case <-time.After(10 * time.Second):
		t.Fatal("no delivery before the purge; the subscriber never reached steady state")
	}

	// Destroy the groups underneath the running subscriber, then publish.
	if _, err := PurgeStreams(ctx, rdb, nil); err != nil {
		t.Fatalf("PurgeStreams: %v", err)
	}
	publishProbe(t, ctx, rdb, group.Streams[0])

	select {
	case <-delivered:
	case <-time.After(10 * time.Second):
		t.Fatal("no delivery after the group was purged; the read loop never re-created it")
	}
}

// publishProbe XAdds one valid, decodable envelope straight onto a stream —
// bypassing the outbox and relay so the probe exercises only the
// subscriber's own read loop, not everything upstream of it.
func publishProbe(t *testing.T, ctx context.Context, rdb *redis.Client, stream string) {
	t.Helper()
	const probeType = "activity.captured"
	env := kevents.Envelope{
		EventID:     ids.NewV7(),
		Type:        probeType,
		Version:     kevents.VersionOf(probeType),
		WorkspaceID: ids.NewV7(),
		OccurredAt:  time.Now().UTC(),
		Actor:       kevents.Actor{Type: "system", ID: "system"},
		Entity:      kevents.EntityRef{Type: "activity", ID: ids.NewV7()},
		Trace:       kevents.Trace{CorrelationID: ids.NewV7(), AuditLogID: ids.NewV7()},
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("probe envelope: %v", err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshaling probe envelope: %v", err)
	}
	if err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{envelopeField: string(raw)}}).Err(); err != nil {
		t.Fatalf("XAdd probe to %s: %v", stream, err)
	}
}
