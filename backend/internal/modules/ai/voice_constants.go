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
	voiceOutcomeAccepted         = "accepted"
	voiceOutcomeEditedSent       = "edited_sent"
	voiceOutcomeRejected         = "rejected"
	voiceBuildReasonOnboarding   = "onboarding"
	voiceBuildReasonManual       = "manual"
	voiceBuildStatusQueued       = "queued"
	voiceBuildStatusDeferred     = "deferred"
	voiceBuildStatusRunning      = "running"
)

// The published event contract spells the two SENT outcomes differently
// from the DDL: (drafted | sent_unedited | sent_edited | rejected) against
// ('drafted','accepted','edited_sent','rejected'). Naming both vocabularies
// keeps voiceOutcomeWireValue's translation readable instead of a bare
// lookup between string literals.
const (
	voiceOutcomeWireSentUnedited = "sent_unedited"
	voiceOutcomeWireSentEdited   = "sent_edited"
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
	voiceKeyFinalCapturedBy  = "final_captured_by"
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
	voiceKeySimilarity       = "similarity"
	voiceKeyStatusCode       = "status_code"
	voiceKeyCandidateAction  = "candidate_action"
	voiceKeyWordsAdded       = "words_added"
	voiceKeySourcesAdded     = "sources_added"
	voiceKeyWeight           = "weight"
	voiceKeyWordDelta        = "word_delta"
	voiceValidationNotEmpty  = "must not be empty"
)
