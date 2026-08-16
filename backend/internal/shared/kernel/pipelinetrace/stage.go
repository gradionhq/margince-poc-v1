// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package pipelinetrace is the vocabulary of the ingress pipeline as a member
// reads it: which stages a captured message passes through, what each can
// honestly say about itself, and why a stage that says nothing says nothing.
//
// It is vocabulary and nothing else — no storage, no queries, no port. The
// stages are answered from two different places (a trace row capture already
// writes, or live product state a module already owns), and both the modules
// that answer and the compose layer that assembles them import this. That is
// why it sits in kernel rather than in either of them.
//
// WHAT THIS EXISTS TO PREVENT. A stage could previously decline to run and
// leave nothing a member could read: the attention classifier reads
// `kind = 'email'`, so a message that arrived over a chat transport was never
// eligible, and no surface said so. The answer took a code read. A registry
// whose gates refuse an unexplained stage is the structural fix — a list is a
// thing to forget, and three stages had already been forgotten.
package pipelinetrace

// Stage is one step of the ingress pipeline, in the order a message meets it.
//
// The vocabulary is CLOSED and ordered by Registration.Order rather than by
// declaration, because a stage inserted in the middle later must not renumber
// the ones around it.
type Stage string

const (
	// StageConnectorFilter is what a connector dropped before handing anything
	// over. Never traced, and that is deliberate rather than missing: what one
	// connector filters is not comparable to what another does, so a shared
	// count would be a fiction.
	StageConnectorFilter Stage = "connector_filter"

	// StageIngressGate is the admission check on the call itself.
	StageIngressGate Stage = "ingress_gate"

	// StageErasureCheck refuses a message naming an erased account. It cannot be
	// traced at all: writing the row would re-store what the erasure removed.
	StageErasureCheck Stage = "erasure_check"

	// StageInternalDrop is the colleagues-only drop. It writes NO activity row,
	// so its trace row is the only record that the message ever existed — which
	// is why this stage is stored rather than derived.
	StageInternalDrop Stage = "internal_drop"

	// StageActivityWrite is the single capture transaction. Its success is
	// derived (the activity exists), but a replay whose incumbent row sits
	// outside the reader's scope leaves a fault row and no activity, so this
	// stage answers from both sources.
	StageActivityWrite Stage = "activity_write"

	// StageTierLadder is the T0-T4 contact decision. Stored: the decision is
	// made in memory during capture and deliberately not re-derivable — the
	// ladder's own comment refuses to recover it downstream, because that would
	// be a query per captured message to learn what one function just decided.
	StageTierLadder Stage = "tier_ladder"

	// StagePersonCreate is the post-commit contact write and the nightly repair
	// that re-runs it. Derived BY ELIMINATION from the person link and the
	// ladder's answer; there is no stored "the ladder decided to create".
	StagePersonCreate Stage = "person_create"

	// StageVerdict is the per-SENDER disposition. Derived: the ledger already
	// records it with an owner, a status and its timestamps, and a copy would
	// collide with itself the moment a sender were re-judged inside the window.
	StageVerdict Stage = "verdict"

	// StageCompanyTriage is the per-DOMAIN question of whether a company record
	// is warranted.
	StageCompanyTriage Stage = "company_triage"

	// StageAttentionLabel is the batched commitment|meeting|noise classification.
	// This is the stage whose silence motivated the whole surface.
	StageAttentionLabel Stage = "attention_label"

	// StageMaterialEvents is the per-THREAD reading of what a conversation says.
	StageMaterialEvents Stage = "material_events"

	// StageClaimExtraction fills the commitments and open-loops cards. No
	// automated writer exists yet, which is a state this vocabulary can express
	// rather than an absence it renders as nothing.
	StageClaimExtraction Stage = "claim_extraction"
)

// Status is what a stage can honestly report about one message.
//
// Every value here is COMPUTED AT READ TIME. None is persisted, and no writer
// takes a Status: the stored rows carry capture's own outcome vocabulary, which
// this package maps onto these. Offering a writer a render state is how one
// would eventually be persisted and then contradict the state it was derived
// from.
type Status string

const (
	// StatusDone: the stage ran and concluded.
	StatusDone Status = "done"
	// StatusSkipped: the stage was reached and declined. Always carries a Reason
	// — a skip without one is the silence this surface exists to remove.
	StatusSkipped Status = "skipped"
	// StatusPending: the stage has not run yet but is expected to.
	StatusPending Status = "pending"
	// StatusFailed: the stage ran and could not finish.
	StatusFailed Status = "failed"
	// StatusNotApplicable: the stage does not apply to this message. Distinct
	// from skipped: nothing declined, there was simply no question to answer.
	StatusNotApplicable Status = "not_applicable"
	// StatusWithheld: the reader may not see this rung. It keeps its place and
	// says so; omitting it would state that the stage did not happen.
	//
	// Rendered UNCONDITIONALLY to a non-owner, whether or not a row exists.
	// Conditional withholding is a row-existence oracle: an activity proves
	// capture ran, but a coexisting fault row's existence is not derivable from
	// it, so a caller comparing two messages would learn which one faulted on a
	// colleague's mailbox.
	StatusWithheld Status = "withheld"
	// StatusExpired: the stage ran, and the detail has been swept. Permitted
	// ONLY where the run is provable from durable state; where absence and
	// never-happened are indistinguishable, StatusUnknown is the honest answer.
	StatusExpired Status = "expired"
	// StatusUnknown: outside the retention window, and whether this stage ran
	// cannot be established. Distinct from StatusNotApplicable, which claims
	// the stage did not apply — a claim swept data cannot support.
	StatusUnknown Status = "unknown"
	// StatusNotReported: this surface does not report this stage. The rung's
	// reason says which kind of not-reported it is — never shown by design,
	// untraceable without breaching something, not read yet, or a step that
	// does not exist. Collapsing those four would tell a member the wrong one.
	StatusNotReported Status = "not_reported"
)

// SubjectKind is WHAT a stage's answer is about.
//
// It is required on every rung because the stages do not share a subject: the
// verdict is asked once per sender, not per message. A rung rendering "judged a
// real contact" without saying whose reads as a claim about this message when
// it is a claim about the sender across all their mail.
type SubjectKind string

const (
	SubjectMessage SubjectKind = "message"
	SubjectSender  SubjectKind = "sender"
	SubjectDomain  SubjectKind = "domain"
	SubjectThread  SubjectKind = "thread"
)
