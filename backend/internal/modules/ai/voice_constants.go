// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Voice lifecycle values are shared by persistence, audit records, and events.
// Naming them here keeps those representations on the same closed vocabulary.
const (
	voiceProfileStatusCollecting = "collecting"
	voiceProfileStatusReady      = "ready"
	voiceProfileStatusStale      = "stale"
	voiceMaturityCollecting      = "collecting"
	voiceMaturityProvisional     = "provisional"
	voiceMaturityBuilding        = "building"
	voiceVersionStatusActive     = "active"
	voiceVersionStatusCandidate  = "candidate"
	voiceVersionStatusRejected   = "rejected"
	voiceOutcomeDrafted          = "drafted"
	voiceOutcomeRejected         = "rejected"
	voiceBuildReasonOnboarding   = "onboarding"
	voiceBuildReasonManual       = "manual"
	voiceBuildStatusQueued       = "queued"
	voiceBuildStatusDeferred     = "deferred"
	voiceBuildStatusRunning      = "running"
)

// Voice source kinds and registers are the closed ADR-0066 vocabulary.
const (
	voiceSourceKindEmail      = "email"
	voiceSourceKindLinkedIn   = "linkedin"
	voiceSourceKindProposal   = "proposal"
	voiceSourceKindTranscript = "transcript"
	voiceSourceKindDocument   = "document"
	voiceSourceKindOther      = "other"
	voiceRegisterEmail        = "email"
	voiceRegisterSocial       = "social"
	voiceRegisterLongForm     = "long_form"
	voiceRegisterSpoken       = "spoken"
	voiceRegisterGeneral      = "general"
)

// Shared field names keep validation errors and event/audit payloads aligned.
const (
	voiceKeyAction           = "action"
	voiceKeyAutoLearning     = "auto_learning_enabled"
	voiceKeyContent          = "content"
	voiceKeyDocument         = "document"
	voiceKeyDraftRef         = "draft_ref"
	voiceKeyExcluded         = "excluded"
	voiceKeyFormat           = "format"
	voiceKeyIdentityJaccard  = "identity_word_jaccard"
	voiceKeyIncluded         = "included"
	voiceKeyKind             = "kind"
	voiceKeyMaturity         = "maturity"
	voiceKeyOrigin           = "origin"
	voiceKeyOutcome          = "outcome"
	voiceKeyPersonalityMD    = "personality_md"
	voiceKeyProfileID        = "profile_id"
	voiceKeyProfileVersion   = "profile_version"
	voiceKeyReason           = "reason"
	voiceKeyRegister         = "register"
	voiceKeySourceCount      = "source_count"
	voiceKeySourceHash       = "source_hash"
	voiceKeySourceID         = "source_id"
	voiceKeySourceLabel      = "source_label"
	voiceKeySpeakerLabel     = "speaker_label"
	voiceKeyStatus           = "status"
	voiceKeySignatureJaccard = "signature_set_jaccard"
	voiceKeyWeight           = "weight"
	voiceKeyWordDelta        = "word_delta"
	voiceValidationNotEmpty  = "must not be empty"
)
