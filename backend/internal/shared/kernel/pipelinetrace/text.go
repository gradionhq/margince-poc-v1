// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package pipelinetrace

import "fmt"

// RetentionHours is how long a STORED rung is kept, reported on every answer so
// a reader can tell an expired rung from one that never happened.
//
// It mirrors capture's own TraceWindowHours rather than importing it — capture
// is a module and this is a Tier-0 leaf, so the dependency can only run the
// other way. A fitness test in capture asserts the two agree, which is the only
// way this can be wrong and the cheapest place to catch it.
const RetentionHours = 24

// StageLabel and ReasonText are the SERVER'S FALLBACK rendering, not the
// product's copy.
//
// The client owns the real wording, in its own catalog, in three locales. These
// exist for one case the catalog cannot cover: a stage or reason added by a
// newer server than the client. Without them that rung would either vanish or
// render a raw identifier at a member — which is exactly how a missing key
// stayed invisible on this surface once already.
//
// So they are deliberately plain. A client that recognises the key never shows
// them, and an English sentence on a German screen is a worse outcome than a
// localised one but a far better outcome than `attention_label.transport_not_read`.

var stageLabels = map[Stage]string{
	StageConnectorFilter: "Connector filtering",
	StageIngressGate:     "Admission check",
	StageErasureCheck:    "Erasure check",
	StageInternalDrop:    "Internal-only check",
	StageActivityWrite:   "Saved to the timeline",
	StageTierLadder:      "Contact decision",
	StagePersonCreate:    "Contact created",
	StageVerdict:         "Sender verdict",
	StageCompanyTriage:   "Company check",
	StageAttentionLabel:  "Attention label",
	StageMaterialEvents:  "Conversation reading",
	StageClaimExtraction: "Commitments and open loops",
}

// StageLabel names a stage for a reader.
func StageLabel(stage Stage) string {
	if label, ok := stageLabels[stage]; ok {
		return label
	}
	// A stage this build does not know. The key beats an empty string, and a
	// client that also does not know it will show the same thing — which at
	// least tells a member that a step exists and nobody has named it.
	return string(stage)
}

var reasonTexts = map[Reason]string{
	ReasonInternalOnly:         "every party was on your own domains",
	ReasonInvisibleIncumbent:   "it matched a record outside what you can see",
	ReasonTransactionalInfra:   "the sender is mail infrastructure, not a company you work with",
	ReasonTransactionalPrefix:  "the sender looks like an automated mailer, not a person",
	ReasonDeferralCapped:       "the open-question limit was reached, so no verdict is coming",
	ReasonNoisePrior:           "a previous verdict judged this sender noise",
	ReasonDecidedPrior:         "this sender was already decided",
	ReasonNoCounterparty:       "no sender this CRM could record",
	ReasonNoGrantingHuman:      "the connection named no member to act for",
	ReasonDerivationFailed:     "the contact step failed; the message itself is unaffected",
	ReasonNotLinkedYet:         "no contact is linked to this message yet",
	ReasonNoContactIntended:    "the contact decision concluded that none was to be made",
	ReasonAwaitingVerdict:      "the sender is still waiting on a verdict",
	ReasonVerdictReached:       "a verdict has been reached for this sender",
	ReasonNoOpenQuestion:       "there was no open question about this sender",
	ReasonTriageQueued:         "the sender's domain is queued for a company check",
	ReasonTriageDecided:        "the sender's domain has been checked",
	ReasonTriageNotAsked:       "no company check was needed",
	ReasonTransportNotRead:     "this step reads email only, and the message arrived over another transport",
	ReasonSenderUndecided:      "the sender is still waiting on a verdict, so the message is held back",
	ReasonArchived:             "the message is archived",
	ReasonNotConnectorCaptured: "the message was not captured by a connector",
	ReasonAwaitingBatch:        "it is eligible and waiting for the next batch",
	ReasonLabelled:             "the message was labelled",
	ReasonNoThread:             "the message carries no conversation this step could read",
	ReasonThreadSettling:       "the conversation is still moving; it is read once it settles",
	ReasonThreadParked:         "repeated readings of this conversation disagreed, so it is parked",
	ReasonThreadRead:           "the conversation has been read",
	AbsentNotComparable: "what a connector filters on its own side is not counted here — " +
		"the numbers mean different things per connector",
	AbsentConnectorDefect:    "admission failures are a fault of the connection, not of one message",
	AbsentWouldRestoreErased: "reporting this would restore data an erasure removed",
	AbsentNoWriterYet:        "this step does not exist yet",
	AbsentNotReportedYet:     "this step runs, but is not reported here yet",
}

// ReasonText renders one reason.
//
// The stage is taken but not currently used to vary the wording: no reason today
// appears under two stages needing different sentences. It is in the signature
// because the CLIENT's catalog is keyed `<stage>.<reason>`, and a server
// fallback that could not express the same scoping would be the one place the
// two vocabularies disagreed the day a shared reason arrived.
func ReasonText(stage Stage, reason Reason) string {
	if text, ok := reasonTexts[reason]; ok {
		return text
	}
	return fmt.Sprintf("%s (%s)", reason, stage)
}
