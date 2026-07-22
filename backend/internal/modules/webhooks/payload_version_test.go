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
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
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

// offerSnapshotOfferID/offerSnapshotDealID are fixed, memorable UUIDs so the
// offer family's golden snapshots (Task 5a-ii) are stable across test
// runs — a real ids.NewV7() would churn the fixtures on every regeneration
// for no reason.
var (
	offerSnapshotOfferID = uuid.MustParse("55555555-5555-5555-5555-555555555555")
	offerSnapshotDealID  = uuid.MustParse("66666666-6666-6666-6666-666666666666")
)

// TestOfferCreatedWireSnapshot pins the offer.created wire shape (webhooks
// Task 5a-ii, offer family).
func TestOfferCreatedWireSnapshot(t *testing.T) {
	sample := crmcontracts.WebhookPayloadOfferCreated{
		OfferId:    offerSnapshotOfferID,
		DealId:     offerSnapshotDealID,
		Revision:   1,
		Currency:   "EUR",
		Source:     "manual",
		CapturedBy: "user_123",
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestOfferSentWireSnapshot pins the offer.sent wire shape.
func TestOfferSentWireSnapshot(t *testing.T) {
	revision := 1
	gross := int64(500000)
	validUntil := openapi_types.Date{Time: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}
	sample := crmcontracts.WebhookPayloadOfferSent{
		OfferId:      offerSnapshotOfferID,
		DealId:       offerSnapshotDealID,
		Revision:     &revision,
		GrossMinor:   &gross,
		FxRateToBase: "1.0842",
		ValidUntil:   &validUntil,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestOfferAcceptedWireSnapshot pins the offer.accepted wire shape.
func TestOfferAcceptedWireSnapshot(t *testing.T) {
	revision := 1
	gross := int64(500000)
	sample := crmcontracts.WebhookPayloadOfferAccepted{
		OfferId:    offerSnapshotOfferID,
		DealId:     offerSnapshotDealID,
		Revision:   &revision,
		GrossMinor: &gross,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestOfferRejectedWireSnapshot pins the offer.rejected wire shape.
func TestOfferRejectedWireSnapshot(t *testing.T) {
	revision := 1
	reason := "price too high"
	sample := crmcontracts.WebhookPayloadOfferRejected{
		OfferId:  offerSnapshotOfferID,
		DealId:   offerSnapshotDealID,
		Revision: &revision,
		Reason:   &reason,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestOfferSupersededWireSnapshot pins the offer.superseded wire shape.
func TestOfferSupersededWireSnapshot(t *testing.T) {
	fromRevision := 1
	sample := crmcontracts.WebhookPayloadOfferSuperseded{
		OfferId:      offerSnapshotOfferID,
		DealId:       offerSnapshotDealID,
		FromRevision: &fromRevision,
		ToRevision:   2,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// pipelineSnapshotID/pipelineSnapshotStageID are fixed, memorable UUIDs so
// the pipeline/stage config family's golden snapshots (Task 5a-iii) are
// stable across test runs — a real ids.NewV7() would churn the fixtures
// on every regeneration for no reason.
var pipelineSnapshotStageID = uuid.MustParse("77777777-7777-7777-7777-777777777777")

// TestPipelineCreatedWireSnapshot pins the pipeline.created wire shape
// (webhooks Task 5a-iii, pipeline/stage config family).
func TestPipelineCreatedWireSnapshot(t *testing.T) {
	sample := crmcontracts.WebhookPayloadPipelineCreated{
		Name:      "Sales",
		IsDefault: true,
		Stages: []crmcontracts.WebhookPipelineCreatedStage{
			{Name: "New", Position: 0, Semantic: "open"},
			{Name: "Won", Position: 1, Semantic: "won"},
		},
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestPipelineUpdatedWireSnapshot pins the pipeline.updated wire shape —
// the OPEN changed_fields envelope, sampled with the flat-patch shape
// UpdatePipeline emits (the stage_positions reorder shape is the same
// schema, just a different map value, so it needs no separate snapshot).
func TestPipelineUpdatedWireSnapshot(t *testing.T) {
	isDefault := true
	sample := crmcontracts.WebhookPayloadPipelineUpdated{
		ChangedFields: map[string]any{"name": "Enterprise Sales", "is_default": isDefault},
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestStageCreatedWireSnapshot pins the stage.created wire shape.
func TestStageCreatedWireSnapshot(t *testing.T) {
	sample := crmcontracts.WebhookPayloadStageCreated{
		PipelineId:     pipelineSnapshotStageID,
		Name:           "Negotiation",
		Position:       2,
		Semantic:       "open",
		WinProbability: 40,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestStageUpdatedWireSnapshot pins the stage.updated wire shape — the
// BOUNDED delta, sampled with only name touched so the snapshot also pins
// that an untouched semantic/win_probability is OMITTED, not nulled.
func TestStageUpdatedWireSnapshot(t *testing.T) {
	name := "Qualified"
	sample := crmcontracts.WebhookPayloadStageUpdated{
		PipelineId: pipelineSnapshotStageID,
		Name:       &name,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// personSnapshotSource/personSnapshotTarget are fixed, memorable UUIDs so
// the person/organization family's golden snapshots (webhooks Task
// 5b-personorg) are stable across test runs — a real ids.NewV7() would
// churn the fixtures on every regeneration for no reason.
var (
	personSnapshotSource = uuid.MustParse("88888888-8888-8888-8888-888888888888")
	personSnapshotTarget = uuid.MustParse("99999999-9999-9999-9999-999999999999")
)

// TestPersonCreatedWireSnapshot pins the person.created wire shape
// (webhooks Task 5b-personorg, person/organization family).
func TestPersonCreatedWireSnapshot(t *testing.T) {
	sample := crmcontracts.WebhookPayloadPersonCreated{FullName: "Ada Lovelace"}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestPersonMergedWireSnapshot pins the person.merged wire shape.
func TestPersonMergedWireSnapshot(t *testing.T) {
	sample := crmcontracts.WebhookPayloadPersonMerged{
		MergedFromId: personSnapshotSource,
		MergedIntoId: personSnapshotTarget,
		Relinked: crmcontracts.WebhookPersonMergedRelinkCounts{
			Emails: 2, Phones: 1, Relationships: 3, ActivityLinks: 5,
		},
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestPersonUpdatedWireSnapshot pins the person.updated wire shape — the
// OPEN changed_fields envelope, sampled with a flat column patch.
func TestPersonUpdatedWireSnapshot(t *testing.T) {
	sample := crmcontracts.WebhookPayloadPersonUpdated{
		ChangedFields: map[string]any{"title": "VP Sales"},
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestOrganizationCreatedWireSnapshot pins the organization.created wire
// shape — the UNION struct, sampled with the direct-create subset
// (display_name only; the other four sites' fields are exercised by the
// people-package payload-builder unit tests).
func TestOrganizationCreatedWireSnapshot(t *testing.T) {
	displayName := "Acme GmbH"
	sample := crmcontracts.WebhookPayloadOrganizationCreated{DisplayName: &displayName}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestOrganizationMergedWireSnapshot pins the organization.merged wire shape.
func TestOrganizationMergedWireSnapshot(t *testing.T) {
	sample := crmcontracts.WebhookPayloadOrganizationMerged{
		MergedFromId: personSnapshotSource,
		MergedIntoId: personSnapshotTarget,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestOrganizationUpdatedWireSnapshot pins the organization.updated wire
// shape — the OPEN changed_fields envelope, sampled with a flat column
// patch.
func TestOrganizationUpdatedWireSnapshot(t *testing.T) {
	sample := crmcontracts.WebhookPayloadOrganizationUpdated{
		ChangedFields: map[string]any{"industry": "software"},
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}
