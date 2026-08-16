// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package pipelinetrace

// Reason is WHY a stage reached the status it did — a class this installation
// chose, never a provider's text and never message content.
//
// That restriction is what makes the default posture sufficient. With payload
// capture off the trace stores no address and no subject, so if a skip could
// only be explained by quoting the message, every installation running the
// default would read "we cannot tell you" — which is the state this surface
// exists to remove. A class renders a full sentence from the catalog with no
// payload at all.
//
// SCOPED BY STAGE. The catalog key is `<stage>.<reason>`, so the same code says
// something stage-appropriate in each place it appears: ReasonTransportNotRead
// under the classifier is "reads email only", and under the conversation reader
// it is a different sentence about a different pass. Without the scope the two
// would need differently-spelled constants saying nearly the same thing, and the
// pair would drift.
type Reason string

// Stored reasons. These are capture's own vocabulary, already written to
// capture_trace.reason by the three writers, and are listed here so the registry
// can close the set the surface may render.
const (
	// StageInternalDrop.
	ReasonInternalOnly Reason = "internal_only"

	// StageActivityWrite.
	ReasonInvisibleIncumbent Reason = "invisible_incumbent"

	// StageTierLadder.
	ReasonTransactionalInfra  Reason = "transactional_infra"
	ReasonTransactionalPrefix Reason = "transactional_prefix"
	ReasonDeferralCapped      Reason = "deferral_capped"
	ReasonNoisePrior          Reason = "noise_prior"
	ReasonDecidedPrior        Reason = "decided_prior"
	ReasonNoCounterparty      Reason = "no_counterparty"
	ReasonNoGrantingHuman     Reason = "no_granting_human"
	ReasonDerivationFailed    Reason = "derivation_failed"
)

// Derived reasons. These are computed at read time from live product state, and
// exist so a stage that did NOT run says why rather than rendering as an absence.
const (
	// StagePersonCreate. Neither promises a repair: the nightly reconcile
	// re-runs the resolver over link-less activities, but a channel identity
	// conflict stages a human-review proposal the resolver will never clear, so
	// that message stays link-less until a person acts. Copy that said "tonight"
	// would be false indefinitely for exactly those.
	ReasonNotLinkedYet      Reason = "not_linked_yet"
	ReasonNoContactIntended Reason = "no_contact_intended"

	// Every stored stage, when this surface cannot establish what happened.
	// One class for both causes, because the two are indistinguishable once the
	// rows are gone: a swept window and a reader who does not own the rows
	// produce the same absence, and naming either specifically would state a
	// fact we do not have — or, for a non-owner, disclose one we must not.
	ReasonRecordNotAvailable Reason = "record_not_available"

	// StageVerdict.
	ReasonAwaitingVerdict Reason = "awaiting_verdict"
	ReasonVerdictReached  Reason = "verdict_reached"
	ReasonNoOpenQuestion  Reason = "no_open_question"

	// StageAttentionLabel. Four ways the backlog excludes a message, because a
	// ladder that says "reads email only" about an ARCHIVED email gives a wrong
	// why, which is worse than none.
	ReasonTransportNotRead     Reason = "transport_not_read"
	ReasonSenderUndecided      Reason = "sender_undecided"
	ReasonArchived             Reason = "archived"
	ReasonNotConnectorCaptured Reason = "not_connector_captured"
	ReasonAwaitingBatch        Reason = "awaiting_batch"
	ReasonLabelled             Reason = "labelled"

	// StageCompanyTriage and StageMaterialEvents contribute no reasons yet: both
	// are `planned`, so nothing produces one. Their vocabulary arrives with the
	// derivation that emits it (#1434), rather than sitting here unproducible.
)

// Absence reasons. Why a whole STAGE reports nothing, as against why one message
// took a particular path through it. They are catalog keys for the same reason
// every other reason is: verbatim English on a registration would render
// untranslated on a de or vi surface while the rest of the ladder was localised.
const (
	// StageConnectorFilter.
	AbsentNotComparable Reason = "not_comparable_between_connectors"
	// StageIngressGate.
	AbsentConnectorDefect Reason = "connector_side_defect"
	// StageErasureCheck.
	AbsentWouldRestoreErased Reason = "would_restore_erased"
	// StageClaimExtraction.
	AbsentNoWriterYet Reason = "no_writer_yet"
	// A stage that RUNS but whose state this surface does not read yet. The
	// distinction from AbsentNoWriterYet matters to a member: one says the
	// pipeline step does not exist, the other says it does and we are not
	// showing you. Rendering either as the other is a false statement.
	AbsentNotReportedYet Reason = "not_reported_yet"
)
