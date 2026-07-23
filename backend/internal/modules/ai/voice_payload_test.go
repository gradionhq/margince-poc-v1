// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// TDD Step 1 of the webhooks Task 5f migration (ai voice family): drives the
// payload-builder functions this package's seven emit sites call —
// voiceProfileCreatedPayload (voice.go's CreateProfile),
// voiceProfileUpdatedPayload (voice.go's emitVoiceProfileUpdated),
// voiceProfileArchivedPayload (voice.go's ArchiveProfile),
// voiceCorpusChangedPayload (voice_source_mutations.go's recordSourceUpdate/
// RemoveSource/ClearCorpus and voice_source_store.go's recordSourceIngest),
// voiceBuildChangedPayload (voice_lifecycle.go's emitVoiceBuild),
// voiceVersionChangedPayload (voice_versions.go's emitVoiceVersion), and
// voiceDraftOutcomeRecordedPayload (voice_history.go's RecordDraftOutcome) —
// then round-trips each result through JSON exactly as storekit.EmitEvent
// marshals it into the outbox envelope's payload column, mirroring the
// signals family's TestDetectedPayload_* (webhooks Task 5e).
//
// Before this migration none of crmcontracts.PublicEventVoice* existed,
// and none of the builder functions existed (every site inlined a
// map[string]any), so this test failed to compile (RED) until
// public-events.yaml gained the schemas, `make gen` regenerated the
// structs, and voice.go/voice_source_mutations.go/voice_source_store.go/
// voice_lifecycle.go/voice_versions.go/voice_history.go grew the builders.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/stretchr/testify/require"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

var (
	voicePayloadTestProfileID = ids.UUID(uuid.MustParse("11111111-1111-1111-1111-111111111111"))
	voicePayloadTestOwnerID   = ids.UUID(uuid.MustParse("22222222-2222-2222-2222-222222222222"))
	voicePayloadTestSourceID  = ids.UUID(uuid.MustParse("33333333-3333-3333-3333-333333333333"))
	voicePayloadTestBuildID   = ids.UUID(uuid.MustParse("44444444-4444-4444-4444-444444444444"))
)

func TestVoiceProfileCreatedPayload(t *testing.T) {
	payload := voiceProfileCreatedPayload(voicePayloadTestProfileID, voicePayloadTestOwnerID, "collecting", false)

	require.Equal(t, "voice.profile_created", payload.EventType())
	require.Equal(t, "voice_profile", payload.EntityType())
	require.Equal(t, openapi_types.UUID(voicePayloadTestProfileID), payload.ProfileId)
	require.Equal(t, openapi_types.UUID(voicePayloadTestOwnerID), payload.OwnerId)
	require.Equal(t, "collecting", payload.Maturity)
	require.False(t, payload.AutoLearningEnabled)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.PublicEventVoiceProfileCreated
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

func TestVoiceProfileUpdatedPayload(t *testing.T) {
	payload := voiceProfileUpdatedPayload(voicePayloadTestProfileID, "learning_enabled", 3, "developing")

	require.Equal(t, "voice.profile_updated", payload.EventType())
	require.Equal(t, "voice_profile", payload.EntityType())
	require.Equal(t, openapi_types.UUID(voicePayloadTestProfileID), payload.ProfileId)
	require.Equal(t, "learning_enabled", payload.Action)
	require.Equal(t, int64(3), payload.Version)
	require.Equal(t, "developing", payload.Maturity)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.PublicEventVoiceProfileUpdated
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

func TestVoiceProfileArchivedPayload(t *testing.T) {
	payload := voiceProfileArchivedPayload(voicePayloadTestProfileID, voicePayloadTestOwnerID, 2)

	require.Equal(t, "voice.profile_archived", payload.EventType())
	require.Equal(t, "voice_profile", payload.EntityType())
	require.Equal(t, openapi_types.UUID(voicePayloadTestProfileID), payload.ProfileId)
	require.Equal(t, openapi_types.UUID(voicePayloadTestOwnerID), payload.OwnerId)
	require.Equal(t, 2, payload.ProfileVersion)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.PublicEventVoiceProfileArchived
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

// TestVoiceCorpusChangedPayload_WithSource proves the three source-touching
// sites (update/remove/ingest) carry source_id/origin/register.
func TestVoiceCorpusChangedPayload_WithSource(t *testing.T) {
	origin, register := "manual", "email"
	payload := voiceCorpusChangedPayload(voicePayloadTestProfileID, &voicePayloadTestSourceID,
		"included", &origin, &register, 120, 4, "abc123")

	require.Equal(t, "voice.corpus_changed", payload.EventType())
	require.Equal(t, "voice_profile", payload.EntityType())
	require.Equal(t, openapi_types.UUID(voicePayloadTestProfileID), payload.ProfileId)
	require.NotNil(t, payload.SourceId)
	require.Equal(t, openapi_types.UUID(voicePayloadTestSourceID), *payload.SourceId)
	require.Equal(t, "included", payload.Action)
	require.NotNil(t, payload.Origin)
	require.Equal(t, "manual", *payload.Origin)
	require.NotNil(t, payload.Register)
	require.Equal(t, "email", *payload.Register)
	require.Equal(t, 120, payload.WordDelta)
	require.Equal(t, 4, payload.SourceCount)
	require.Equal(t, "abc123", payload.SourceHash)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.PublicEventVoiceCorpusChanged
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

// TestVoiceCorpusChangedPayload_Clear proves ClearCorpus's site omits
// source_id/origin/register — there is no single source, the whole corpus
// was scrubbed.
func TestVoiceCorpusChangedPayload_Clear(t *testing.T) {
	payload := voiceCorpusChangedPayload(voicePayloadTestProfileID, nil, "cleared", nil, nil, 0, 0,
		"d41d8cd98f00b204e9800998ecf8427e")

	require.Nil(t, payload.SourceId)
	require.Nil(t, payload.Origin)
	require.Nil(t, payload.Register)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "source_id",
		"the clear site has no single touched source — source_id must be omitted, not null")
	require.NotContains(t, string(raw), "origin")
	require.NotContains(t, string(raw), "register")
	var decoded crmcontracts.PublicEventVoiceCorpusChanged
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
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

	require.Equal(t, "voice.build_changed", payload.EventType())
	require.Equal(t, "voice_profile", payload.EntityType())
	require.Equal(t, openapi_types.UUID(voicePayloadTestProfileID), payload.ProfileId)
	require.Equal(t, openapi_types.UUID(voicePayloadTestBuildID), payload.BuildId)
	require.Equal(t, "manual", payload.Reason)
	require.Equal(t, "queued", payload.Status)
	require.Equal(t, "abc123", payload.SourceHash)
	require.Equal(t, 4, payload.SourceCount)
	require.Equal(t, "wait", payload.CandidateAction)
	require.Nil(t, payload.Stage)
	require.Nil(t, payload.ResultVersion)
	require.Nil(t, payload.StatusCode)
	require.Nil(t, payload.NextAttemptAt)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "stage",
		"a freshly-queued build has no stage yet — it must be omitted, not null")
	var decoded crmcontracts.PublicEventVoiceBuildChanged
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
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

	require.NotNil(t, payload.Stage)
	require.Equal(t, "extracting", *payload.Stage)
	require.NotNil(t, payload.ResultVersion)
	require.Equal(t, 5, *payload.ResultVersion)
	require.NotNil(t, payload.StatusCode)
	require.Equal(t, "rate_limited", *payload.StatusCode)
	require.NotNil(t, payload.NextAttemptAt)
	require.Equal(t, nextAttempt, *payload.NextAttemptAt)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.PublicEventVoiceBuildChanged
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

// TestVoiceVersionChangedPayload_FirstVersion proves a profile's first
// version carries no predecessor_version — there is none.
func TestVoiceVersionChangedPayload_FirstVersion(t *testing.T) {
	version := VoiceProfileVersion{ProfileID: voicePayloadTestProfileID, ProfileVersion: 1, Status: "active", Reason: "build"}
	payload := voiceVersionChangedPayload(version, "material", "auto_activated")

	require.Equal(t, "voice.version_changed", payload.EventType())
	require.Equal(t, "voice_profile", payload.EntityType())
	require.Equal(t, openapi_types.UUID(voicePayloadTestProfileID), payload.ProfileId)
	require.Equal(t, 1, payload.ProfileVersion)
	require.Equal(t, "active", payload.Status)
	require.Equal(t, "build", payload.Reason)
	require.Nil(t, payload.PredecessorVersion)
	require.Equal(t, "material", payload.Classification)
	require.Equal(t, "auto_activated", payload.ActivationOutcome)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "predecessor_version",
		"a profile's first version has no predecessor — it must be omitted, not null")
	var decoded crmcontracts.PublicEventVoiceVersionChanged
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
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

	require.NotNil(t, payload.PredecessorVersion)
	require.Equal(t, 4, *payload.PredecessorVersion)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.PublicEventVoiceVersionChanged
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

func TestVoiceDraftOutcomeRecordedPayload(t *testing.T) {
	payload := voiceDraftOutcomeRecordedPayload(voicePayloadTestProfileID, "rejected", false, 0)

	require.Equal(t, "voice.draft_outcome_recorded", payload.EventType())
	require.Equal(t, "voice_profile", payload.EntityType())
	require.Equal(t, openapi_types.UUID(voicePayloadTestProfileID), payload.ProfileId)
	require.Equal(t, "rejected", payload.Outcome)
	require.False(t, payload.QualifiesAsSource)
	require.Equal(t, 0, payload.TransformationCount)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.PublicEventVoiceDraftOutcomeRecorded
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}
