// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The morning digest end to end (CAP-DDL-6/CAP-WIRE-6): the nightly
// worker builds one row per connected user, GET /digest serves it as
// stored, a day nobody built answers the honest 404, and a re-run
// replaces the day's payload instead of stacking rows.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// readDigest invokes the GET /digest handler under ctx for an optional
// day, returning the status and decoded payload.
func (b *backfillWireEnv) readDigest(t *testing.T, day *time.Time) (int, crmcontracts.MorningDigest) {
	t.Helper()
	var params crmcontracts.GetMorningDigestParams
	if day != nil {
		params.Date = &openapi_types.Date{Time: *day}
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/digest", nil).WithContext(b.human)
	rec := httptest.NewRecorder()
	b.handlers.GetMorningDigest(rec, req, params)
	var out crmcontracts.MorningDigest
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decoding digest %q: %v", rec.Body.Bytes(), err)
		}
	}
	return rec.Code, out
}

func TestMorningDigestBuildAndRead(t *testing.T) {
	b := setupBackfillWire(t)
	today := time.Date(2026, 7, 14, 5, 0, 0, 0, time.UTC)

	// Before any build: the honest 404, never a fabricated empty payload.
	if status, _ := b.readDigest(t, nil); status != http.StatusNotFound {
		t.Fatalf("digest before the first build → %d, want 404", status)
	}

	if err := b.registry.BuildDigests(b.human, today); err != nil {
		t.Fatalf("BuildDigests: %v", err)
	}

	status, digest := b.readDigest(t, nil)
	if status != http.StatusOK {
		t.Fatalf("digest after build → %d, want 200", status)
	}
	if digest.Date.Format(time.DateOnly) != "2026-07-14" {
		t.Fatalf("digest date = %s, want 2026-07-14", digest.Date.Format(time.DateOnly))
	}
	// The connector health strip names the connected providers.
	if len(digest.Connectors) == 0 {
		t.Fatal("digest carries no connector health rows for a connected user")
	}

	// The specific-day read finds the same row; a day nobody built is 404.
	if status, _ := b.readDigest(t, &today); status != http.StatusOK {
		t.Fatalf("digest for the built day → %d, want 200", status)
	}
	missing := today.AddDate(0, 0, -7)
	if status, _ := b.readDigest(t, &missing); status != http.StatusNotFound {
		t.Fatalf("digest for an unbuilt day → %d, want 404", status)
	}

	// Idempotent per (user, day): the nightly worker's own re-run (over
	// every live workspace, today's date) replaces rather than stacks, and
	// the latest read still answers exactly one row.
	worker := &captureDigestWorker{registry: b.registry, pool: b.env.Pool, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := worker.Work(context.Background(), nil); err != nil {
		t.Fatalf("digest worker: %v", err)
	}
	if status, _ := b.readDigest(t, nil); status != http.StatusOK {
		t.Fatalf("digest after the worker pass → %d, want 200", status)
	}
}
