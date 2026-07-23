// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

// This file pins each registered type's wire SHAPE with a golden snapshot
// under testdata/wire/<type>.v<n>.json. The snapshot is an additive-only
// ratchet: a field renamed or removed changes the marshaled bytes and fails
// the comparison, forcing a reviewed regeneration (UPDATE_SNAPSHOTS=1)
// rather than letting a breaking wire change slip through unnoticed. The
// cross-cutting registry gates it used to pair with — whole-catalog payload
// coverage, no-orphan, and version agreement with events.VersionOf — now
// live in the authoritative root fitness test backend/publicevents_test.go.

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
	sample := crmcontracts.PublicEventDealStageChanged{
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
	sample := crmcontracts.PublicEventOfferCreated{
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
	sample := crmcontracts.PublicEventOfferSent{
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
	sample := crmcontracts.PublicEventOfferAccepted{
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
	sample := crmcontracts.PublicEventOfferRejected{
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
	sample := crmcontracts.PublicEventOfferSuperseded{
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
	sample := crmcontracts.PublicEventPipelineCreated{
		Name:      "Sales",
		IsDefault: true,
		Stages: []crmcontracts.PublicEventPipelineCreatedStage{
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
	sample := crmcontracts.PublicEventPipelineUpdated{
		ChangedFields: map[string]any{"name": "Enterprise Sales", "is_default": isDefault},
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestStageCreatedWireSnapshot pins the stage.created wire shape.
func TestStageCreatedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventStageCreated{
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
	sample := crmcontracts.PublicEventStageUpdated{
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
	sample := crmcontracts.PublicEventPersonCreated{FullName: "Ada Lovelace"}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestPersonMergedWireSnapshot pins the person.merged wire shape.
func TestPersonMergedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventPersonMerged{
		MergedFromId: personSnapshotSource,
		MergedIntoId: personSnapshotTarget,
		Relinked: crmcontracts.PublicEventPersonMergedRelinkCounts{
			Emails: 2, Phones: 1, Relationships: 3, ActivityLinks: 5,
		},
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestPersonUpdatedWireSnapshot pins the person.updated wire shape — the
// OPEN changed_fields envelope, sampled with a flat column patch.
func TestPersonUpdatedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventPersonUpdated{
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
	sample := crmcontracts.PublicEventOrganizationCreated{DisplayName: &displayName}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestOrganizationMergedWireSnapshot pins the organization.merged wire shape.
func TestOrganizationMergedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventOrganizationMerged{
		MergedFromId: personSnapshotSource,
		MergedIntoId: personSnapshotTarget,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestOrganizationUpdatedWireSnapshot pins the organization.updated wire
// shape — the OPEN changed_fields envelope, sampled with a flat column
// patch.
func TestOrganizationUpdatedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventOrganizationUpdated{
		ChangedFields: map[string]any{"industry": "software"},
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// leadSnapshotPersonID is a fixed, memorable UUID so the lead family's
// golden snapshot (webhooks Task 5b-lead) is stable across test runs —
// a real ids.NewV7() would churn the fixture on every regeneration for
// no reason.
var leadSnapshotPersonID = uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

// TestLeadPromotedWireSnapshot pins the lead.promoted wire shape
// (webhooks Task 5b-lead, lead family), sampled with an evidence_ref set.
func TestLeadPromotedWireSnapshot(t *testing.T) {
	evidenceRef := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	sample := crmcontracts.PublicEventLeadPromoted{
		PromotedPersonId: leadSnapshotPersonID,
		DedupeOutcome:    "created",
		Trigger:          "inbound_reply",
		EvidenceRef:      &evidenceRef,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestLeadUpdatedWireSnapshot pins the lead.updated wire shape — the
// OPEN changed_fields envelope, sampled with a runtime cf_* custom-field
// key alongside a routing delta, proving the open map carries both
// verbatim.
func TestLeadUpdatedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventLeadUpdated{
		ChangedFields: map[string]any{
			"delta":              map[string]any{"owner_id": leadSnapshotPersonID},
			"cf_lead_source_ref": "partner-9f2",
		},
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// activitySnapshotMatched is a fixed, memorable UUID so the activities
// family's golden snapshots (webhooks Task 5c) are stable across test
// runs — a real ids.NewV7() would churn the fixtures on every regeneration
// for no reason.
var activitySnapshotMatched = uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")

// TestActivityCapturedWireSnapshot pins the activity.captured wire shape
// (webhooks Task 5c, activities family), sampled with the capture-site
// subset (kind + source_system both set).
func TestActivityCapturedWireSnapshot(t *testing.T) {
	sourceSystem := "gmail"
	sample := crmcontracts.PublicEventActivityCaptured{
		Kind:         "email",
		SourceSystem: &sourceSystem,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestActivityUpdatedWireSnapshot pins the activity.updated wire shape —
// the BOUNDED changed_fields struct, sampled with subject and is_done
// touched so the snapshot also pins that an untouched field is OMITTED,
// not nulled.
func TestActivityUpdatedWireSnapshot(t *testing.T) {
	subject := "Follow-up call"
	isDone := true
	sample := crmcontracts.PublicEventActivityUpdated{
		ChangedFields: crmcontracts.PublicEventActivityChangedFields{
			Subject: &subject,
			IsDone:  &isDone,
		},
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestEngagementReplyWireSnapshot pins the engagement.reply wire shape.
func TestEngagementReplyWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventEngagementReply{
		MatchedOutboundActivityId: activitySnapshotMatched,
		Channel:                   "email",
		OccurredAt:                time.Date(2026, 7, 22, 9, 30, 0, 0, time.UTC),
		IdempotencyKey:            "gmail:msg-42",
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// consentSnapshotPurposeID is a fixed, memorable UUID so the
// consent/privacy family's golden snapshot (webhooks Task 5d) is stable
// across test runs — a real ids.NewV7() would churn the fixture on every
// regeneration for no reason.
var consentSnapshotPurposeID = uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")

// TestConsentChangedWireSnapshot pins the consent.changed wire shape
// (webhooks Task 5d) — this event's entity is dynamic (person XOR lead),
// so unlike every prior family the subject never appears in the payload
// itself, only in the envelope's entity ref (storekit.EmitEventForEntity's
// separate entityType argument).
func TestConsentChangedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventConsentChanged{
		PurposeId: consentSnapshotPurposeID,
		Purpose:   "marketing_email",
		NewState:  "granted",
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestRetentionAppliedWireSnapshot pins the retention.applied wire shape —
// like consent.changed, its entity is dynamic (ai_call / a policy's object
// type / person, one per site), so the subject never appears in the
// payload. Sampled with the policy-driven sweep's subset (action + policy,
// no reason) since that is the only site that sets both optional fields
// together.
func TestRetentionAppliedWireSnapshot(t *testing.T) {
	policy := consentSnapshotPurposeID
	sample := crmcontracts.PublicEventRetentionApplied{
		Action: "archive",
		Policy: &policy,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// signalSnapshotID/signalSnapshotOrgID are fixed, memorable UUIDs so the
// signals family's golden snapshots (webhooks Task 5e) are stable across
// test runs — a real ids.NewV7() would churn the fixtures on every
// regeneration for no reason.
var (
	signalSnapshotID    = uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")
	signalSnapshotOrgID = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
)

// TestSignalDetectedWireSnapshot pins the signal.detected wire shape
// (webhooks Task 5e) — sampled with a subject already known at creation
// time, so subject_entity_type/subject_entity_id's wire keys (entity_type/
// entity_id) both appear. This event's entity is static (signal), unlike
// consent.changed/retention.applied: entity_type/entity_id here are DATA
// fields naming the signal's SUBJECT, not the envelope's own entity ref.
func TestSignalDetectedWireSnapshot(t *testing.T) {
	entityType := "organization"
	confidence := float32(0.95)
	sample := crmcontracts.PublicEventSignalDetected{
		SignalId:             signalSnapshotID,
		Kind:                 "champion_left",
		SourceChannel:        "inbound",
		ResolutionState:      "resolved",
		Severity:             "warn",
		SubjectEntityType:    &entityType,
		SubjectEntityId:      &signalSnapshotOrgID,
		ResolutionConfidence: &confidence,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestSignalResolvedWireSnapshot pins the signal.resolved wire shape —
// sampled with the single-candidate (resolved-to-org) shape, the branch
// that sets every optional field.
func TestSignalResolvedWireSnapshot(t *testing.T) {
	matchedOn := "domain"
	confidence := float32(0.95)
	sample := crmcontracts.PublicEventSignalResolved{
		SignalId:        signalSnapshotID,
		ResolutionState: "resolved",
		ResolvedOrgId:   &signalSnapshotOrgID,
		MatchedOn:       &matchedOn,
		MatchConfidence: &confidence,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// voiceSnapshotProfileID/voiceSnapshotOwnerID/voiceSnapshotSourceID/
// voiceSnapshotBuildID are fixed, memorable UUIDs so the ai voice family's
// golden snapshots (webhooks Task 5f) are stable across test runs.
var (
	voiceSnapshotProfileID = uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	voiceSnapshotOwnerID   = uuid.MustParse("00000000-0000-0000-0000-0000000000a2")
	voiceSnapshotSourceID  = uuid.MustParse("00000000-0000-0000-0000-0000000000a3")
	voiceSnapshotBuildID   = uuid.MustParse("00000000-0000-0000-0000-0000000000a4")
)

// TestVoiceProfileCreatedWireSnapshot pins voice.profile_created's wire shape.
func TestVoiceProfileCreatedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventVoiceProfileCreated{
		ProfileId:           voiceSnapshotProfileID,
		OwnerId:             voiceSnapshotOwnerID,
		Maturity:            "collecting",
		AutoLearningEnabled: false,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestVoiceProfileUpdatedWireSnapshot pins voice.profile_updated's wire shape.
func TestVoiceProfileUpdatedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventVoiceProfileUpdated{
		ProfileId: voiceSnapshotProfileID,
		Action:    "preferences_replaced",
		Version:   3,
		Maturity:  "developing",
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestVoiceProfileArchivedWireSnapshot pins voice.profile_archived's wire shape.
func TestVoiceProfileArchivedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventVoiceProfileArchived{
		ProfileId:      voiceSnapshotProfileID,
		OwnerId:        voiceSnapshotOwnerID,
		ProfileVersion: 2,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestVoiceCorpusChangedWireSnapshot pins voice.corpus_changed's wire shape
// — sampled with the source-touching (not clear) branch, which sets every
// optional field.
func TestVoiceCorpusChangedWireSnapshot(t *testing.T) {
	origin, register := "manual", "email"
	sample := crmcontracts.PublicEventVoiceCorpusChanged{
		ProfileId:   voiceSnapshotProfileID,
		SourceId:    &voiceSnapshotSourceID,
		Action:      "included",
		Origin:      &origin,
		Register:    &register,
		WordDelta:   120,
		SourceCount: 4,
		SourceHash:  "abc123",
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestVoiceBuildChangedWireSnapshot pins voice.build_changed's wire shape —
// sampled with the deferred branch, which sets every optional field.
func TestVoiceBuildChangedWireSnapshot(t *testing.T) {
	stage := "extracting"
	resultVersion := 5
	statusCode := "rate_limited"
	nextAttempt := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	sample := crmcontracts.PublicEventVoiceBuildChanged{
		ProfileId:       voiceSnapshotProfileID,
		BuildId:         voiceSnapshotBuildID,
		Reason:          "onboarding",
		Status:          "deferred",
		Stage:           &stage,
		SourceHash:      "abc123",
		SourceCount:     4,
		ResultVersion:   &resultVersion,
		CandidateAction: "retry",
		StatusCode:      &statusCode,
		NextAttemptAt:   &nextAttempt,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestVoiceVersionChangedWireSnapshot pins voice.version_changed's wire
// shape — sampled with the rollback branch, which sets predecessor_version.
func TestVoiceVersionChangedWireSnapshot(t *testing.T) {
	predecessor := 4
	sample := crmcontracts.PublicEventVoiceVersionChanged{
		ProfileId:          voiceSnapshotProfileID,
		ProfileVersion:     5,
		Status:             "active",
		Reason:             "rollback",
		PredecessorVersion: &predecessor,
		Classification:     "routine",
		ActivationOutcome:  "rollback",
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestVoiceDraftOutcomeRecordedWireSnapshot pins
// voice.draft_outcome_recorded's wire shape.
func TestVoiceDraftOutcomeRecordedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventVoiceDraftOutcomeRecorded{
		ProfileId:           voiceSnapshotProfileID,
		Outcome:             "sent_edited",
		QualifiesAsSource:   true,
		TransformationCount: 2,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// identitySnapshotUserID/identitySnapshotActorID/identitySnapshotPassportID
// are fixed, memorable UUIDs so the identity family's golden snapshots
// (webhooks Task 5g) are stable across test runs — a real ids.NewV7()
// would churn the fixtures on every regeneration for no reason.
var (
	identitySnapshotUserID     = uuid.MustParse("00000000-0000-0000-0000-0000000000b1")
	identitySnapshotActorID    = uuid.MustParse("00000000-0000-0000-0000-0000000000b2")
	identitySnapshotPassportID = uuid.MustParse("00000000-0000-0000-0000-0000000000b3")
)

// TestUserInvitedWireSnapshot pins user.invited's wire shape (webhooks
// Task 5g, identity family).
func TestUserInvitedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventUserInvited{
		UserId: identitySnapshotUserID,
		Role:   "manager",
		By:     identitySnapshotActorID,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestUserDeactivatedWireSnapshot pins user.deactivated's wire shape —
// sampled with reason set, the branch that carries every optional field.
func TestUserDeactivatedWireSnapshot(t *testing.T) {
	reason := "policy violation"
	sample := crmcontracts.PublicEventUserDeactivated{
		UserId: identitySnapshotUserID,
		By:     identitySnapshotActorID,
		Reason: &reason,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestUserReactivatedWireSnapshot pins user.reactivated's wire shape.
func TestUserReactivatedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventUserReactivated{
		UserId: identitySnapshotUserID,
		By:     identitySnapshotActorID,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestRoleChangedWireSnapshot pins role.changed's wire shape — sampled
// with from_role set, the branch that carries every optional field.
func TestRoleChangedWireSnapshot(t *testing.T) {
	fromRole := "member"
	sample := crmcontracts.PublicEventRoleChanged{
		UserId:   identitySnapshotUserID,
		ToRole:   "manager",
		By:       identitySnapshotActorID,
		FromRole: &fromRole,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestPassportRevokedWireSnapshot pins passport.revoked's wire shape.
func TestPassportRevokedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventPassportRevoked{
		PassportId: identitySnapshotPassportID,
		By:         identitySnapshotActorID,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestOnboardingStateChangedWireSnapshot pins onboarding.state_changed's
// wire shape.
func TestOnboardingStateChangedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventOnboardingStateChanged{
		UserId:         identitySnapshotUserID,
		Path:           "member",
		Step:           "connect",
		Version:        3,
		VoiceSkipped:   true,
		ConnectSkipped: false,
		Completed:      false,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestMirrorConflictWireSnapshot pins mirror.conflict's wire shape
// (webhooks Task 5h, overlay family) — this event's entity is dynamic
// (the runtime object class the reconcile sweep observed), so unlike the
// static-entity families above, the subject class rides INSIDE the
// payload (object_class) rather than only in the envelope's entity ref.
func TestMirrorConflictWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventMirrorConflict{
		ObjectClass:        "deal",
		ExternalId:         "hs-4821",
		PriorUpdatedAt:     time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC),
		IncumbentUpdatedAt: time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC),
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestMirrorBudgetDegradedWireSnapshot pins mirror.budget_degraded's wire
// shape — like mirror.conflict, dynamic-entity, so the subject class is
// carried only by the envelope's entity ref, not this payload.
func TestMirrorBudgetDegradedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventMirrorBudgetDegraded{Band: "shed"}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestMirrorDeletedWireSnapshot pins mirror.deleted's wire shape.
func TestMirrorDeletedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventMirrorDeleted{
		ObjectClass: "person",
		ExternalId:  "hs-9931",
		DeletedAt:   time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC),
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestIncumbentConnectedWireSnapshot pins incumbent.connected's wire
// shape — this event's entity is static (incumbent_connection), unlike
// the mirror.* family above.
func TestIncumbentConnectedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventIncumbentConnected{
		Incumbent: "hubspot",
		Region:    "eu",
		Scopes:    []string{"crm.objects.contacts.read", "crm.objects.deals.read"},
		Status:    "active",
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestIncumbentDisconnectedWireSnapshot pins incumbent.disconnected's
// wire shape.
func TestIncumbentDisconnectedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventIncumbentDisconnected{
		Incumbent: "hubspot",
		Region:    "eu",
		Status:    "revoked",
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// approvalSnapshotTargetID/approvalSnapshotDecidedBy are fixed, memorable
// UUIDs so the approvals/coldstart family's golden snapshots (webhooks
// Task 5-approvals, the second emit path — approvals.Service.emit) are
// stable across test runs — a real ids.NewV7() would churn the fixtures
// on every regeneration for no reason.
var (
	approvalSnapshotTargetID  = uuid.MustParse("00000000-0000-0000-0000-0000000000c1")
	approvalSnapshotDecidedBy = uuid.MustParse("00000000-0000-0000-0000-0000000000c2")
	approvalSnapshotID        = uuid.MustParse("00000000-0000-0000-0000-0000000000c3")
)

// TestApprovalRequestedWireSnapshot pins approval.requested's wire shape
// — sampled with target_entity_id set, the branch that carries every
// optional field.
func TestApprovalRequestedWireSnapshot(t *testing.T) {
	targetID := openapi_types.UUID(approvalSnapshotTargetID)
	sample := crmcontracts.PublicEventApprovalRequested{
		Kind:             "advance_deal",
		Summary:          "Advance Acme GmbH to Negotiation",
		TargetEntityType: "deal",
		TargetEntityId:   &targetID,
		ExpiresAt:        time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC),
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestApprovalDecidedWireSnapshot pins approval.decided's wire shape —
// sampled with the ADR-0036 §4 modify-then-approve arm (edited set),
// which carries every optional field.
func TestApprovalDecidedWireSnapshot(t *testing.T) {
	edited := true
	diffHash := "sha256:abc123"
	editedChange := map[string]interface{}{"stage_id": approvalSnapshotTargetID.String()}
	sample := crmcontracts.PublicEventApprovalDecided{
		Kind:         "advance_deal",
		Verdict:      "approved",
		DecidedBy:    openapi_types.UUID(approvalSnapshotDecidedBy),
		Edited:       &edited,
		DiffHash:     &diffHash,
		EditedChange: &editedChange,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestApprovalDecidedKeyBindingIsStable is the A9 key-binding regression
// test: automation/engine_blocked.go's HandleApprovalDecided decodes
// approval.decided's outbox payload by the literal JSON key "verdict",
// and compose/runnerservice.go's HandleEvent decodes "edited_change" —
// both read the generic map/struct the bus hands every subscriber, never
// crmcontracts.PublicEventApprovalDecided itself (automation and
// compose/runnerservice cannot import each other's payload-construction
// module). A future rename of either field must break THIS test, not
// silently stop those consumers from matching. edited_change must also
// stay a raw/open JSON object (not a narrowly typed struct) — this test
// proves an arbitrary edited-change shape survives round-tripping
// undisturbed.
func TestApprovalDecidedKeyBindingIsStable(t *testing.T) {
	editedChange := map[string]interface{}{
		"stage_id": approvalSnapshotTargetID.String(),
		"note":     "moved after the call",
		"nested":   map[string]interface{}{"amount_minor": float64(50000)},
	}
	sample := crmcontracts.PublicEventApprovalDecided{
		Kind:         "advance_deal",
		Verdict:      "rejected",
		DecidedBy:    openapi_types.UUID(approvalSnapshotDecidedBy),
		EditedChange: &editedChange,
	}
	raw, err := json.Marshal(sample)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	verdict, ok := decoded["verdict"]
	require.True(t, ok, `expected JSON key "verdict" (bound in automation/engine_blocked.go) in %s`, raw)
	require.Equal(t, "rejected", verdict)

	got, ok := decoded["edited_change"]
	require.True(t, ok, `expected JSON key "edited_change" (bound in compose/runnerservice.go) in %s`, raw)
	gotObj, ok := got.(map[string]any)
	require.True(t, ok, "edited_change must decode as an open JSON object, got %T", got)
	require.Equal(t, "moved after the call", gotObj["note"], "edited_change must carry an arbitrary shape verbatim, not a narrowly typed struct")
}

// TestColdstartReadBackProposedWireSnapshot pins
// coldstart.read_back_proposed's wire shape — sampled with the url-kind
// read-back branch (source_url set, source_kind absent).
func TestColdstartReadBackProposedWireSnapshot(t *testing.T) {
	sourceURL := "https://acme.example"
	sample := crmcontracts.PublicEventColdstartReadBackProposed{
		FieldCount: 4,
		SourceUrl:  &sourceURL,
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestColdstartAcceptedWireSnapshot pins coldstart.accepted's wire shape.
func TestColdstartAcceptedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventColdstartAccepted{
		ApprovalId: openapi_types.UUID(approvalSnapshotID),
		DecidedBy:  openapi_types.UUID(approvalSnapshotDecidedBy),
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}

// TestColdstartRejectedWireSnapshot pins coldstart.rejected's wire shape.
func TestColdstartRejectedWireSnapshot(t *testing.T) {
	sample := crmcontracts.PublicEventColdstartRejected{
		ApprovalId: openapi_types.UUID(approvalSnapshotID),
		DecidedBy:  openapi_types.UUID(approvalSnapshotDecidedBy),
	}
	assertWireSnapshot(t, sample.EventType(), events.VersionOf(sample.EventType()), sample)
}
