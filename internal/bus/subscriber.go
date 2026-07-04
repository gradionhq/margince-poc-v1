package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/kernel/events"
	"github.com/gradionhq/margince/backend/kernel/ids"
)

// envelopeField is the single stream-entry field holding the envelope
// JSON (events.md §4.1: `XADD <stream> * v <envelope-json>`).
const envelopeField = "v"

// Handler processes one delivered envelope. Returning nil acks the entry;
// returning an error leaves it pending, and the reclaim pass re-delivers
// it after minIdle (events.md §3 at-least-once). Handlers therefore run
// under Dedupe and effect idempotently.
type Handler func(ctx context.Context, env events.Envelope) error

// Subscriber consumes one events.md §4.3 consumer group across its
// declared streams: XREADGROUP for fresh entries, XACK only after the
// handler succeeds, and an XAUTOCLAIM pass that adopts entries a crashed
// consumer left pending. Horizontal scale is more Subscribers with the
// same group and distinct consumer names.
type Subscriber struct {
	rdb      *redis.Client
	group    events.Group
	consumer string
	handler  Handler
	log      *slog.Logger

	// block is the XREADGROUP wait; it bounds shutdown latency.
	block time.Duration
	// minIdle is how long an entry may sit pending (its consumer
	// presumed dead) before the reclaim pass adopts it. It should still
	// exceed the slowest handler's honest runtime: reclaiming an entry
	// whose consumer is merely slow runs the effect twice concurrently —
	// absorbed by the natural-key idempotency layer, but wasted work and
	// a needless conflict path.
	minIdle time.Duration
	batch   int64
}

// NewSubscriber wires a handler to a consumer group. The consumer name is
// host+pid: stable enough to reclaim its own pending entries after a
// restart, unique enough that replicas never collide.
func NewSubscriber(rdb *redis.Client, group events.Group, handler Handler, log *slog.Logger) *Subscriber {
	host, _ := os.Hostname()
	return &Subscriber{
		rdb:      rdb,
		group:    group,
		consumer: fmt.Sprintf("%s-%d", host, os.Getpid()),
		handler:  handler,
		log:      log.With("group", group.Name),
		block:    2 * time.Second,
		minIdle:  5 * time.Minute,
		batch:    64,
	}
}

// Run consumes until ctx is canceled. Like the relay, transport errors
// back off and retry rather than kill the consumer — unprocessed entries
// are durable in the stream/PEL until acked (events.md §3).
func (s *Subscriber) Run(ctx context.Context) error {
	if err := s.ensureGroups(ctx); err != nil {
		return err
	}

	streams := make([]string, 0, 2*len(s.group.Streams))
	streams = append(streams, s.group.Streams...)
	for range s.group.Streams {
		streams = append(streams, ">") // fresh entries only; pending ones ride the reclaim pass
	}

	for ctx.Err() == nil {
		s.reclaim(ctx)

		res, err := s.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    s.group.Name,
			Consumer: s.consumer,
			Streams:  streams,
			Count:    s.batch,
			Block:    s.block,
		}).Result()
		switch {
		case errors.Is(err, redis.Nil): // block timed out, nothing new
			continue
		case err != nil:
			if ctx.Err() != nil {
				break
			}
			s.log.Error("bus: XREADGROUP failed; retrying", "error", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
			continue
		}

		for _, stream := range res {
			for _, entry := range stream.Messages {
				s.deliver(ctx, stream.Stream, entry)
			}
		}
	}
	return ctx.Err()
}

// ensureGroups creates the consumer group on every subscribed stream,
// from position 0 so a group declared after traffic started still sees
// the retained history once (streams trim; audit_log is the permanent
// record a longer gap re-bootstraps from, events.md §4.3/§4.4).
func (s *Subscriber) ensureGroups(ctx context.Context) error {
	for _, stream := range s.group.Streams {
		err := s.rdb.XGroupCreateMkStream(ctx, stream, s.group.Name, "0").Err()
		if err != nil && !isBusyGroup(err) {
			return fmt.Errorf("bus: creating group %s on %s: %w", s.group.Name, stream, err)
		}
	}
	return nil
}

// reclaim adopts entries whose consumer died mid-handling: XAUTOCLAIM
// hands anything pending longer than minIdle to this consumer and we run
// it through the same delivery path. Redelivery can arrive after newer
// entries — order-sensitive consumers key on the entity's version, not
// arrival (events.md §4.3).
func (s *Subscriber) reclaim(ctx context.Context) {
	for _, stream := range s.group.Streams {
		start := "0-0"
		for {
			claimed, next, err := s.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
				Stream:   stream,
				Group:    s.group.Name,
				Consumer: s.consumer,
				MinIdle:  s.minIdle,
				Start:    start,
				Count:    s.batch,
			}).Result()
			if err != nil {
				if ctx.Err() == nil {
					s.log.Error("bus: XAUTOCLAIM failed", "stream", stream, "error", err)
				}
				break
			}
			for _, entry := range claimed {
				s.deliver(ctx, stream, entry)
			}
			if next == "0-0" { // wrapped: the PEL scan is complete
				break
			}
			start = next
		}
	}
}

// deliver decodes, dispatches, and acks one entry. A structurally invalid
// entry is acked away with a loud log: it can never succeed, and leaving
// it pending would poison the reclaim pass forever (the per-group
// dead-letter store is B-EP04.15, not built yet).
func (s *Subscriber) deliver(ctx context.Context, stream string, entry redis.XMessage) {
	env, err := decodeEnvelope(entry)
	if err != nil {
		s.log.Error("bus: dropping undecodable entry", "stream", stream, "entry", entry.ID, "error", err)
		s.ack(ctx, stream, entry.ID)
		return
	}

	if err := s.handler(ctx, env); err != nil {
		// No ack: the entry stays pending and re-delivers via reclaim.
		s.log.Warn("bus: handler failed; entry stays pending",
			"stream", stream, "entry", entry.ID, "event_id", env.EventID.String(), "error", err)
		return
	}
	s.ack(ctx, stream, entry.ID)
}

func (s *Subscriber) ack(ctx context.Context, stream, entryID string) {
	if err := s.rdb.XAck(ctx, stream, s.group.Name, entryID).Err(); err != nil && ctx.Err() == nil {
		// The handler already ran; a lost ack means one redelivery, which
		// Dedupe absorbs. Log so a systematic ack failure is visible.
		s.log.Warn("bus: XACK failed; entry will re-deliver", "stream", stream, "entry", entryID, "error", err)
	}
}

func decodeEnvelope(entry redis.XMessage) (events.Envelope, error) {
	raw, ok := entry.Values[envelopeField].(string)
	if !ok {
		return events.Envelope{}, fmt.Errorf("bus: entry has no %q field", envelopeField)
	}
	var env events.Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return events.Envelope{}, fmt.Errorf("bus: malformed envelope: %w", err)
	}
	if err := env.Validate(); err != nil {
		return events.Envelope{}, err
	}
	return env, nil
}

// isBusyGroup matches Redis's "group already exists" reply — the
// idempotent-create case, not a failure. Prefix match: the BUSYGROUP
// error code is stable, the human tail of the message is not.
func isBusyGroup(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "BUSYGROUP")
}

// ForWorkspace scopes a handler to one tenant: events for any other
// workspace are acked without invoking it. This is the in-process
// analogue of RLS on the bus (events.md §4.1 — workspace is a field, not
// a stream, so the filter is the consumer's responsibility).
//
// Group-topology rule: a consumer GROUP must own ONE
// handler. Redis partitions a group's entries across its consumers, so
// two ForWorkspace handlers sharing a group would each ack away the
// other tenant's events — silently dropping them. Scope per workspace
// either by dispatching inside one group-wide handler or by giving each
// workspace its own group, never by filtered consumers in a shared group.
func ForWorkspace(workspaceID ids.UUID, next Handler) Handler {
	return func(ctx context.Context, env events.Envelope) error {
		if env.WorkspaceID != workspaceID {
			return nil
		}
		return next(ctx, env)
	}
}
