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
