// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

// The version gate pairs with payload_coverage_test.go: coverage proves
// every subscribable type HAS a schema; this file proves the schema's
// declared version agrees with the runtime catalog (events.VersionOf), and
// pins each registered type's wire SHAPE with a golden snapshot under
// testdata/wire/<type>.v<n>.json. The snapshot is an additive-only ratchet:
// a field renamed or removed changes the marshaled bytes and fails the
// comparison, forcing a reviewed regeneration (UPDATE_SNAPSHOTS=1) rather
// than letting a breaking wire change slip through unnoticed.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
)

// TestWebhookPayloadVersionsMatchEventCatalog proves the generated
// registry's version for every registered type agrees with the runtime
// event catalog (internal/shared/kernel/events) that publishers actually
// stamp envelopes from. A mismatch here means the contract's x-version and
// catalog.go's version drifted apart — exactly the split-source-of-truth
// bug this registry exists to prevent.
func TestWebhookPayloadVersionsMatchEventCatalog(t *testing.T) {
	for tp, wantVersion := range crmcontracts.WebhookPayloadVersions {
		require.Equal(t, events.VersionOf(tp), wantVersion,
			"event catalog version for %q disagrees with the generated WebhookPayloadVersions entry", tp)
	}
}

// assertWireSnapshot marshals value (a payload struct) and compares it
// byte-for-byte against the committed golden file at
// testdata/wire/<eventType>.v<version>.json. Run with UPDATE_SNAPSHOTS=1 to
// (re)write the golden file — a deliberate, reviewed action, never
// automatic: the whole point of the ratchet is that a shape change must be
// looked at, not silently absorbed.
//
//craft:ignore naked-any the snapshot helper is generic over every event family's own payload struct shape — there is no shared interface to constrain it to
func assertWireSnapshot(t *testing.T, eventType string, version int, value any) {
	t.Helper()
	got, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err, "marshaling the %s sample payload", eventType)
	got = append(got, '\n')

	path := filepath.Join("testdata", "wire", eventType+".v"+strconv.Itoa(version)+".json")
	if os.Getenv("UPDATE_SNAPSHOTS") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755), "creating testdata/wire")
		require.NoError(t, os.WriteFile(path, got, 0o644), "writing snapshot %s", path)
		return
	}

	want, err := os.ReadFile(path) // #nosec G304 -- fixed test-owned path under testdata/wire
	require.NoErrorf(t, err, "reading golden snapshot %s (run with UPDATE_SNAPSHOTS=1 to create it after a REVIEWED shape change)", path)
	require.Equal(t, string(want), string(got),
		"wire shape for %s v%d drifted from the committed snapshot %s — a deliberate, reviewed shape change regenerates it with UPDATE_SNAPSHOTS=1; an accidental one is fixed instead", eventType, version, path)
}

// dealSnapshotFromStage/dealSnapshotToStage are fixed, memorable UUIDs so the
// pilot's golden snapshot is stable across test runs — a real ids.NewV7()
// would churn the fixture on every regeneration for no reason.
var (
	dealSnapshotFromStage = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	dealSnapshotToStage   = uuid.MustParse("44444444-4444-4444-4444-444444444444")
)

// TestDealStageChangedWireSnapshot pins the pilot payload's wire shape —
// the one type Task 4 exercises end-to-end; every Phase-4 family task adds
// its own event's snapshot test alongside its typed payload. Reconciled in
// Task 5a-i (webhooks deal family) from the placeholder
// deal_id/pipeline_id/from_stage_id/to_stage_id shape to the fields
// deal_advance.go actually emits (EMIT-INVENTORY.md): deal_id/pipeline_id
// dropped (the entity ref already carries the deal id; the deal's
// pipeline never changes on a stage move, so it is not part of the delta),
// from_status/to_status/amount_minor_at_change/currency_at_change/
// win_probability added.
func TestDealStageChangedWireSnapshot(t *testing.T) {
	amount := int64(250000)
	currency := "EUR"
	sample := crmcontracts.WebhookPayloadDealStageChanged{
		FromStageId:         &dealSnapshotFromStage,
		ToStageId:           dealSnapshotToStage,
		FromStatus:          "open",
		ToStatus:            "won",
		AmountMinorAtChange: &amount,
		CurrencyAtChange:    &currency,
		WinProbability:      100,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}
