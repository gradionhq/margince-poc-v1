// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The propose-refresh endpoints over the real wire: an admin POST enqueues the
// async job and returns 202 {status:"enqueued"}; the handler never crawls
// in-request. (Non-admin denial is covered by the store-level auth suite.)

import (
	"context"
	"net/http"
	"testing"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose"
)

type fakeRateEnqueue struct {
	calls []river.JobArgs
}

func (f *fakeRateEnqueue) Enqueue(_ context.Context, args river.JobArgs, _ *river.InsertOpts) error {
	f.calls = append(f.calls, args)
	return nil
}

func TestProposeRefreshEndpointsEnqueue(t *testing.T) {
	fake := &fakeRateEnqueue{}
	e := setupWithOptions(t, compose.WithRateRefresh(fake))
	e.bootstrapWorkspace(t)

	var out struct {
		Status string `json:"status"`
	}
	if status := e.call(t, "POST", "/v1/fx-rates/propose-refresh", nil, nil, &out); status != http.StatusAccepted {
		t.Fatalf("POST /fx-rates/propose-refresh → %d, want 202", status)
	}
	if out.Status != "enqueued" {
		t.Fatalf("status = %q, want enqueued", out.Status)
	}
	if status := e.call(t, "POST", "/v1/ai-model-rates/propose-refresh", nil, nil, &out); status != http.StatusAccepted {
		t.Fatalf("POST /ai-model-rates/propose-refresh → %d, want 202", status)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("enqueued %d jobs, want 2", len(fake.calls))
	}
	if _, ok := fake.calls[0].(compose.FxRateRefreshArgs); !ok {
		t.Fatalf("first job = %T, want FxRateRefreshArgs", fake.calls[0])
	}
	if _, ok := fake.calls[1].(compose.AiModelRateRefreshArgs); !ok {
		t.Fatalf("second job = %T, want AiModelRateRefreshArgs", fake.calls[1])
	}
}
