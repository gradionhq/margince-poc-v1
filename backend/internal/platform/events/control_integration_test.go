// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package events

// The cache-flush fanout: a reset in the api process must reach the worker
// process, which no HTTP call can do.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestSubscribeResetReceivesThePublishedWorkspace(t *testing.T) {
	ctx, rdb := purgeTestRedis(t)
	ws := ids.NewV7()
	got := make(chan ids.UUID, 1)

	runCtx, cancel := context.WithCancel(ctx)
	ready := make(chan struct{})
	// The result comes back on a channel and cleanup waits for it. Reporting
	// from an unjoined goroutine races the test's own return, and Go turns that
	// into an opaque "panic(nil) or runtime.Goexit" instead of the failure.
	done := make(chan error, 1)
	go func() {
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		done <- subscribeResetWithReady(runCtx, rdb, log, func(w ids.UUID) { got <- w }, ready)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("SubscribeReset: %v", err)
		}
	})

	// Bounded: a SUBSCRIBE that never confirms would otherwise hang here until
	// the suite timeout, which reads as a stuck run rather than a failure.
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("the subscription ended before it was ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the subscription never became ready")
	}

	if err := PublishReset(ctx, rdb, ws); err != nil {
		t.Fatalf("PublishReset: %v", err)
	}
	select {
	case w := <-got:
		if w != ws {
			t.Errorf("received workspace %s, want %s", w, ws)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the subscriber never saw the reset signal")
	}
}

// A malformed payload must not wedge the loop: the process that skips it has
// to keep delivering every reset that arrives afterward, or one publisher's
// encoding drift silently stops cache flushing for every workspace.
func TestSubscribeResetSkipsAMalformedPayloadAndKeepsDelivering(t *testing.T) {
	ctx, rdb := purgeTestRedis(t)
	ws := ids.NewV7()
	got := make(chan ids.UUID, 1)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ready := make(chan struct{})
	go func() {
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		if err := subscribeResetWithReady(runCtx, rdb, log, func(w ids.UUID) { got <- w }, ready); err != nil &&
			!errors.Is(err, context.Canceled) {
			t.Errorf("SubscribeReset: %v", err)
		}
	}()
	<-ready

	if err := rdb.Publish(ctx, ResetChannel, "not-a-workspace-id").Err(); err != nil {
		t.Fatalf("publishing the malformed payload: %v", err)
	}
	if err := PublishReset(ctx, rdb, ws); err != nil {
		t.Fatalf("PublishReset: %v", err)
	}
	select {
	case w := <-got:
		if w != ws {
			t.Errorf("received workspace %s, want %s", w, ws)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the subscriber never recovered from the malformed payload")
	}
}

// Canceling the caller's context must return the goroutine, not leak it — a
// deleted ctx.Done() case would still let this test's assertions run against
// a subscriber that never comes back, so the done channel is what actually
// proves the exit rather than the test merely returning.
func TestSubscribeResetReturnsPromptlyAfterCancel(t *testing.T) {
	ctx, rdb := purgeTestRedis(t)
	runCtx, cancel := context.WithCancel(ctx)
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		done <- subscribeResetWithReady(runCtx, rdb, log, func(ids.UUID) {}, ready)
	}()
	<-ready

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("subscribeResetWithReady returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the subscriber did not return after cancel — goroutine leaked")
	}
}
