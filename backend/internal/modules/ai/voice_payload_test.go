// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The ai voice family's payload builders: drives the functions this
// package's eight emit sites call —
// voiceProfileCreatedPayload (voice.go's CreateProfile),
// voiceProfileUpdatedPayload (voice.go's emitVoiceProfileUpdated),
// voiceProfileArchivedPayload (voice.go's ArchiveProfile),
// voiceCorpusChangedPayload (voice_source_mutations.go's recordSourceUpdate/
// RemoveSource/ClearCorpus and voice_source_store.go's recordSourceIngest),
// voiceBuildChangedPayload (voice_lifecycle.go's emitVoiceBuild),
// voiceVersionChangedPayload (voice_versions.go's emitVoiceVersion), and
// voiceDraftOutcomeRecordedPayload (voice_history.go's RecordDraftOutcome AND
// voice_draftread.go's RecordDraftedSignal) — then round-trips each result
// through JSON exactly as storekit.EmitEvent marshals it into the outbox
// envelope's payload column. There is no non-integration harness in this
// repo that drives a Store method against a real Postgres; testing the
// production payload-construction functions directly — the one place a
// schema/code mismatch would show up — is the honest substitute.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

var (
	voicePayloadTestProfileID = ids.MustParse("11111111-1111-1111-1111-111111111111")
	voicePayloadTestOwnerID   = ids.MustParse("22222222-2222-2222-2222-222222222222")
	voicePayloadTestSourceID  = ids.MustParse("33333333-3333-3333-3333-333333333333")
	voicePayloadTestBuildID   = ids.MustParse("44444444-4444-4444-4444-444444444444")
)

func TestVoiceProfileCreatedPayload(t *testing.T) {
	payload := voiceProfileCreatedPayload(voicePayloadTestProfileID, voicePayloadTestOwnerID, "collecting", false)

	if !reflect.DeepEqual(payload.EventType(), "voice.profile_created") {
		t.Errorf("got %v, want %v", payload.EventType(), "voice.profile_created")
	}
	if !reflect.DeepEqual(payload.EntityType(), "voice_profile") {
		t.Errorf("got %v, want %v", payload.EntityType(), "voice_profile")
	}
	if !reflect.DeepEqual(payload.ProfileId, openapi_types.UUID(voicePayloadTestProfileID)) {
		t.Errorf("got %v, want %v", payload.ProfileId, openapi_types.UUID(voicePayloadTestProfileID))
	}
	if !reflect.DeepEqual(payload.OwnerId, openapi_types.UUID(voicePayloadTestOwnerID)) {
		t.Errorf("got %v, want %v", payload.OwnerId, openapi_types.UUID(voicePayloadTestOwnerID))
	}
	if !reflect.DeepEqual(payload.Maturity, "collecting") {
		t.Errorf("got %v, want %v", payload.Maturity, "collecting")
	}
	if payload.AutoLearningEnabled {
		t.Error("expected the condition to be false")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventVoiceProfileCreated
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

func TestVoiceProfileUpdatedPayload(t *testing.T) {
	payload := voiceProfileUpdatedPayload(voicePayloadTestProfileID, "learning_enabled", 3, "developing")

	if !reflect.DeepEqual(payload.EventType(), "voice.profile_updated") {
		t.Errorf("got %v, want %v", payload.EventType(), "voice.profile_updated")
	}
	if !reflect.DeepEqual(payload.EntityType(), "voice_profile") {
		t.Errorf("got %v, want %v", payload.EntityType(), "voice_profile")
	}
	if !reflect.DeepEqual(payload.ProfileId, openapi_types.UUID(voicePayloadTestProfileID)) {
		t.Errorf("got %v, want %v", payload.ProfileId, openapi_types.UUID(voicePayloadTestProfileID))
	}
	if !reflect.DeepEqual(payload.Action, "learning_enabled") {
		t.Errorf("got %v, want %v", payload.Action, "learning_enabled")
	}
	if !reflect.DeepEqual(payload.Version, int64(3)) {
		t.Errorf("got %v, want %v", payload.Version, int64(3))
	}
	if !reflect.DeepEqual(payload.Maturity, "developing") {
		t.Errorf("got %v, want %v", payload.Maturity, "developing")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventVoiceProfileUpdated
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

func TestVoiceProfileArchivedPayload(t *testing.T) {
	payload := voiceProfileArchivedPayload(voicePayloadTestProfileID, voicePayloadTestOwnerID, 2)

	if !reflect.DeepEqual(payload.EventType(), "voice.profile_archived") {
		t.Errorf("got %v, want %v", payload.EventType(), "voice.profile_archived")
	}
	if !reflect.DeepEqual(payload.EntityType(), "voice_profile") {
		t.Errorf("got %v, want %v", payload.EntityType(), "voice_profile")
	}
	if !reflect.DeepEqual(payload.ProfileId, openapi_types.UUID(voicePayloadTestProfileID)) {
		t.Errorf("got %v, want %v", payload.ProfileId, openapi_types.UUID(voicePayloadTestProfileID))
	}
	if !reflect.DeepEqual(payload.OwnerId, openapi_types.UUID(voicePayloadTestOwnerID)) {
		t.Errorf("got %v, want %v", payload.OwnerId, openapi_types.UUID(voicePayloadTestOwnerID))
	}
	if !reflect.DeepEqual(payload.ProfileVersion, 2) {
		t.Errorf("got %v, want %v", payload.ProfileVersion, 2)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventVoiceProfileArchived
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

// TestVoiceCorpusChangedPayload_WithSource proves the three source-touching
// sites (update/remove/ingest) carry source_id/origin/register.
func TestVoiceCorpusChangedPayload_WithSource(t *testing.T) {
	origin, register := "manual", "email"
	payload := voiceCorpusChangedPayload(voicePayloadTestProfileID, &voicePayloadTestSourceID,
		"included", &origin, &register, 120, 4, "abc123")

	if !reflect.DeepEqual(payload.EventType(), "voice.corpus_changed") {
		t.Errorf("got %v, want %v", payload.EventType(), "voice.corpus_changed")
	}
	if !reflect.DeepEqual(payload.EntityType(), "voice_profile") {
		t.Errorf("got %v, want %v", payload.EntityType(), "voice_profile")
	}
	if !reflect.DeepEqual(payload.ProfileId, openapi_types.UUID(voicePayloadTestProfileID)) {
		t.Errorf("got %v, want %v", payload.ProfileId, openapi_types.UUID(voicePayloadTestProfileID))
	}
	if payload.SourceId == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.SourceId, openapi_types.UUID(voicePayloadTestSourceID)) {
		t.Errorf("got %v, want %v", *payload.SourceId, openapi_types.UUID(voicePayloadTestSourceID))
	}
	if !reflect.DeepEqual(payload.Action, "included") {
		t.Errorf("got %v, want %v", payload.Action, "included")
	}
	if payload.Origin == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.Origin, "manual") {
		t.Errorf("got %v, want %v", *payload.Origin, "manual")
	}
	if payload.Register == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.Register, "email") {
		t.Errorf("got %v, want %v", *payload.Register, "email")
	}
	if !reflect.DeepEqual(payload.WordDelta, 120) {
		t.Errorf("got %v, want %v", payload.WordDelta, 120)
	}
	if !reflect.DeepEqual(payload.SourceCount, 4) {
		t.Errorf("got %v, want %v", payload.SourceCount, 4)
	}
	if !reflect.DeepEqual(payload.SourceHash, "abc123") {
		t.Errorf("got %v, want %v", payload.SourceHash, "abc123")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventVoiceCorpusChanged
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

// TestVoiceCorpusChangedPayload_Clear proves ClearCorpus's site omits
// source_id/origin/register — there is no single source, the whole corpus
// was scrubbed.
func TestVoiceCorpusChangedPayload_Clear(t *testing.T) {
	payload := voiceCorpusChangedPayload(voicePayloadTestProfileID, nil, "cleared", nil, nil, 0, 0,
		"d41d8cd98f00b204e9800998ecf8427e")

	if payload.SourceId != nil {
		t.Errorf("expected nil, got %v", payload.SourceId)
	}
	if payload.Origin != nil {
		t.Errorf("expected nil, got %v", payload.Origin)
	}
	if payload.Register != nil {
		t.Errorf("expected nil, got %v", payload.Register)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(raw), "source_id") {
		t.Errorf("the clear site has no single touched source — source_id must be omitted, not null: should not contain %v", "source_id")
	}
	if strings.Contains(string(raw), "origin") {
		t.Errorf("%q should not contain %q", string(raw), "origin")
	}
	if strings.Contains(string(raw), "register") {
		t.Errorf("%q should not contain %q", string(raw), "register")
	}
	var decoded crmcontracts.PublicEventVoiceCorpusChanged
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

// TestVoiceBuildChangedPayload_Queued proves a freshly-queued build carries
// no stage/result_version/status_code/next_attempt_at — none of that exists
// until the build starts running.
func TestVoiceBuildChangedPayload_Queued(t *testing.T) {
	build := VoiceBuild{
		ID: voicePayloadTestBuildID, ProfileID: voicePayloadTestProfileID,
		Reason: "manual", Status: "queued", SourceHash: "abc123", SourceCount: 4,
		CandidateAction: "wait",
	}
	payload := voiceBuildChangedPayload(build)

	if !reflect.DeepEqual(payload.EventType(), "voice.build_changed") {
		t.Errorf("got %v, want %v", payload.EventType(), "voice.build_changed")
	}
	if !reflect.DeepEqual(payload.EntityType(), "voice_profile") {
		t.Errorf("got %v, want %v", payload.EntityType(), "voice_profile")
	}
	if !reflect.DeepEqual(payload.ProfileId, openapi_types.UUID(voicePayloadTestProfileID)) {
		t.Errorf("got %v, want %v", payload.ProfileId, openapi_types.UUID(voicePayloadTestProfileID))
	}
	if !reflect.DeepEqual(payload.BuildId, openapi_types.UUID(voicePayloadTestBuildID)) {
		t.Errorf("got %v, want %v", payload.BuildId, openapi_types.UUID(voicePayloadTestBuildID))
	}
	if !reflect.DeepEqual(payload.Reason, "manual") {
		t.Errorf("got %v, want %v", payload.Reason, "manual")
	}
	if !reflect.DeepEqual(payload.Status, "queued") {
		t.Errorf("got %v, want %v", payload.Status, "queued")
	}
	if !reflect.DeepEqual(payload.SourceHash, "abc123") {
		t.Errorf("got %v, want %v", payload.SourceHash, "abc123")
	}
	if !reflect.DeepEqual(payload.SourceCount, 4) {
		t.Errorf("got %v, want %v", payload.SourceCount, 4)
	}
	if !reflect.DeepEqual(payload.CandidateAction, "wait") {
		t.Errorf("got %v, want %v", payload.CandidateAction, "wait")
	}
	if payload.Stage != nil {
		t.Errorf("expected nil, got %v", payload.Stage)
	}
	if payload.ResultVersion != nil {
		t.Errorf("expected nil, got %v", payload.ResultVersion)
	}
	if payload.StatusCode != nil {
		t.Errorf("expected nil, got %v", payload.StatusCode)
	}
	if payload.NextAttemptAt != nil {
		t.Errorf("expected nil, got %v", payload.NextAttemptAt)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(raw), "stage") {
		t.Errorf("a freshly-queued build has no stage yet — it must be omitted, not null: should not contain %v", "stage")
	}
	var decoded crmcontracts.PublicEventVoiceBuildChanged
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

// TestVoiceBuildChangedPayload_Deferred proves a returned in-flight build
// carries its stage/result_version/status_code/next_attempt_at when set.
func TestVoiceBuildChangedPayload_Deferred(t *testing.T) {
	stage := "extracting"
	resultVersion := 5
	statusCode := "rate_limited"
	nextAttempt := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	build := VoiceBuild{
		ID: voicePayloadTestBuildID, ProfileID: voicePayloadTestProfileID,
		Reason: "onboarding", Status: "deferred", Stage: &stage, SourceHash: "abc123",
		SourceCount: 4, ResultVersion: &resultVersion, CandidateAction: "retry",
		StatusCode: &statusCode, NextAttemptAt: &nextAttempt,
	}
	payload := voiceBuildChangedPayload(build)

	if payload.Stage == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.Stage, "extracting") {
		t.Errorf("got %v, want %v", *payload.Stage, "extracting")
	}
	if payload.ResultVersion == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.ResultVersion, 5) {
		t.Errorf("got %v, want %v", *payload.ResultVersion, 5)
	}
	if payload.StatusCode == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.StatusCode, "rate_limited") {
		t.Errorf("got %v, want %v", *payload.StatusCode, "rate_limited")
	}
	if payload.NextAttemptAt == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.NextAttemptAt, nextAttempt) {
		t.Errorf("got %v, want %v", *payload.NextAttemptAt, nextAttempt)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventVoiceBuildChanged
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

// TestVoiceVersionChangedPayload_FirstVersion proves a profile's first
// version carries no predecessor_version — there is none.
func TestVoiceVersionChangedPayload_FirstVersion(t *testing.T) {
	version := VoiceProfileVersion{ProfileID: voicePayloadTestProfileID, ProfileVersion: 1, Status: "active", Reason: "build"}
	payload := voiceVersionChangedPayload(version, "material", "auto_activated")

	if !reflect.DeepEqual(payload.EventType(), "voice.version_changed") {
		t.Errorf("got %v, want %v", payload.EventType(), "voice.version_changed")
	}
	if !reflect.DeepEqual(payload.EntityType(), "voice_profile") {
		t.Errorf("got %v, want %v", payload.EntityType(), "voice_profile")
	}
	if !reflect.DeepEqual(payload.ProfileId, openapi_types.UUID(voicePayloadTestProfileID)) {
		t.Errorf("got %v, want %v", payload.ProfileId, openapi_types.UUID(voicePayloadTestProfileID))
	}
	if !reflect.DeepEqual(payload.ProfileVersion, 1) {
		t.Errorf("got %v, want %v", payload.ProfileVersion, 1)
	}
	if !reflect.DeepEqual(payload.Status, "active") {
		t.Errorf("got %v, want %v", payload.Status, "active")
	}
	if !reflect.DeepEqual(payload.Reason, "build") {
		t.Errorf("got %v, want %v", payload.Reason, "build")
	}
	if payload.PredecessorVersion != nil {
		t.Errorf("expected nil, got %v", payload.PredecessorVersion)
	}
	if !reflect.DeepEqual(payload.Classification, "material") {
		t.Errorf("got %v, want %v", payload.Classification, "material")
	}
	if !reflect.DeepEqual(payload.ActivationOutcome, "auto_activated") {
		t.Errorf("got %v, want %v", payload.ActivationOutcome, "auto_activated")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(raw), "predecessor_version") {
		t.Errorf("a profile's first version has no predecessor — it must be omitted, not null: should not contain %v", "predecessor_version")
	}
	var decoded crmcontracts.PublicEventVoiceVersionChanged
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

// TestVoiceVersionChangedPayload_Rollback proves a rollback-restored version
// carries its predecessor.
func TestVoiceVersionChangedPayload_Rollback(t *testing.T) {
	predecessor := 4
	version := VoiceProfileVersion{
		ProfileID: voicePayloadTestProfileID, ProfileVersion: 5, Status: "active",
		Reason: "rollback", PredecessorVersion: &predecessor,
	}
	payload := voiceVersionChangedPayload(version, "routine", "rollback")

	if payload.PredecessorVersion == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.PredecessorVersion, 4) {
		t.Errorf("got %v, want %v", *payload.PredecessorVersion, 4)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventVoiceVersionChanged
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

func TestVoiceDraftOutcomeRecordedPayload(t *testing.T) {
	payload := voiceDraftOutcomeRecordedPayload(voicePayloadTestProfileID, "rejected")

	if !reflect.DeepEqual(payload.EventType(), "voice.draft_outcome_recorded") {
		t.Errorf("got %v, want %v", payload.EventType(), "voice.draft_outcome_recorded")
	}
	if !reflect.DeepEqual(payload.EntityType(), "voice_profile") {
		t.Errorf("got %v, want %v", payload.EntityType(), "voice_profile")
	}
	if !reflect.DeepEqual(payload.ProfileId, openapi_types.UUID(voicePayloadTestProfileID)) {
		t.Errorf("got %v, want %v", payload.ProfileId, openapi_types.UUID(voicePayloadTestProfileID))
	}
	if !reflect.DeepEqual(payload.Outcome, "rejected") {
		t.Errorf("got %v, want %v", payload.Outcome, "rejected")
	}
	if payload.QualifiesAsSource {
		t.Error("expected the condition to be false")
	}
	if !reflect.DeepEqual(payload.TransformationCount, 0) {
		t.Errorf("got %v, want %v", payload.TransformationCount, 0)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventVoiceDraftOutcomeRecorded
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

// TestVoiceDraftedSignalPayload pins the payload RecordDraftedSignal
// (voice_draftread.go) emits: a just-served draft has not been sent, so it
// qualifies as no learning source and carries no transformations. This is
// the eighth voice emit site — the one that must ride the typed builder
// (not a hand-built map) so the wire body carries the schema's required
// qualifies_as_source/transformation_count and never the forbidden
// profile_version.
func TestVoiceDraftedSignalPayload(t *testing.T) {
	payload := voiceDraftOutcomeRecordedPayload(voicePayloadTestProfileID, voiceOutcomeDrafted)

	if payload.EventType() != "voice.draft_outcome_recorded" {
		t.Errorf("event type = %q, want voice.draft_outcome_recorded", payload.EventType())
	}
	if payload.EntityType() != "voice_profile" {
		t.Errorf("entity type = %q, want voice_profile", payload.EntityType())
	}
	if payload.Outcome != voiceOutcomeDrafted {
		t.Errorf("outcome = %q, want %q", payload.Outcome, voiceOutcomeDrafted)
	}
	if payload.QualifiesAsSource {
		t.Error("qualifies_as_source = true, want false for a just-served draft")
	}
	if payload.TransformationCount != 0 {
		t.Errorf("transformation_count = %d, want 0 for a just-served draft", payload.TransformationCount)
	}

	// Round-trip through JSON and assert the wire body carries exactly the
	// schema's fields — required ones present, forbidden profile_version
	// absent (additionalProperties: false).
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshaling the drafted-signal payload: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshaling the drafted-signal payload: %v", err)
	}
	for _, required := range []string{"profile_id", "outcome", "qualifies_as_source", "transformation_count"} {
		if _, ok := wire[required]; !ok {
			t.Errorf("wire body is missing required field %q: %s", required, raw)
		}
	}
	if _, forbidden := wire["profile_version"]; forbidden {
		t.Errorf("wire body carries the schema-forbidden profile_version: %s", raw)
	}
}
