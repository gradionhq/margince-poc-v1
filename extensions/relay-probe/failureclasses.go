// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

// This unit's failure vocabulary: one entry for every way its poll can fail that
// an operator would act on DIFFERENTLY.
//
// WHY THE SENTENCES LIVE HERE rather than being composed from the cause: a job
// failure reaches an admin through river_job.errors, a column with no workspace
// and so no RLS, which every workspace's admin reads. A provider refusing a
// message routinely names the account or the address it refused, so the job layer
// persists nothing but a sentence from a closed vocabulary — and until this unit
// declared one, a tick that had already worked out which member was broken and
// why could report only that its failure was unclassifiable.
//
// Every string below is one this unit WROTE. Nothing the provider said can reach
// that column through this file, because nothing the provider said is in it.
//
// THE PER-CONNECTION TOKENS ARE ALSO last_error_class on the connection row (see
// noteFailure), and that is deliberate: the Relay screen and the Maintenance
// screen describe the same outage, and an operator comparing them must read one
// fact rather than two vocabularies.
//
// WHAT THIS UNIT CAN AND CANNOT DISTINGUISH is narrower than it looks, and the
// list is short for a reason rather than by omission. It reaches ONE provider
// over one token per member, so it has no package tier to lapse and no developer
// app registration to be missing — the classes those would earn do not exist
// here. What it has instead is a FLEET: many members polled in one tick, each
// failing on their own, which is where classEveryMemberFailed comes from — a
// unit polling a single account on one workspace-wide credential has no use for
// that class and should not copy it.

import "github.com/gradionhq/margince/backend/pkg/extension"

var (
	// classTokenRejected is a member's token being refused OR absent, which are
	// one class because they are one remedy: the member pastes a current token.
	// Nobody else can deposit it for them — the deposit IS the consent the
	// ingress port reads.
	classTokenRejected = extension.FailureClass{
		Class:    "token_rejected",
		Sentence: "a member's stored Relay token was refused, or no token is on deposit for them",
		Remedy:   "That member reconnects Relay from their own Connections page and pastes a current token; nobody can deposit one on their behalf.",
	}
	// classProviderUnavailable is the outage that needs NO intervention, and
	// saying so is the point: no cursor moved, so the next tick walks the same
	// region and no message is lost. It is also the class a base URL that does not
	// resolve from this installation lands in, which is the most common way this
	// connector fails and the one nobody should be repairing a token over.
	classProviderUnavailable = extension.FailureClass{
		Class:    "provider_unavailable",
		Sentence: "the Relay server could not be reached, or answered that it was too busy",
		Remedy:   "Nothing to do: the poll catches up by itself and no message is lost. If every tick fails, check this installation's network reach and DNS for the connection's base URL.",
	}
	// classProviderAnswerUnusable is an answer this unit cannot read, which is a
	// unit-side defect far more often than an operator-side one — so the remedy
	// says to report it rather than sending somebody to change a setting.
	classProviderAnswerUnusable = extension.FailureClass{
		Class:    "provider_answer_unusable",
		Sentence: "Relay answered something this connector cannot read",
		Remedy:   "The answer is in the process log. A provider whose format changed needs a change to this connector, so report it rather than reconfiguring anything.",
	}
	// classMemberNotPermitted is the member's OWN authority having gone: every
	// record lands on what they may do right now, so demoting or archiving them
	// stops their poll without anything about Relay changing.
	classMemberNotPermitted = extension.FailureClass{
		Class:    "member_not_permitted",
		Sentence: "the member this poll acted for may no longer capture what it lands",
		Remedy:   "Restore that member's role, or disconnect their Relay connection if they are not meant to capture into the CRM any more.",
	}
	// classConnectionUnusable is the core refusing what this unit handed it as
	// invalid, on a path that is not one unrepresentable notification (those are
	// dropped and recorded, never failed). What is left is a connection whose own
	// state no poll can repair.
	classConnectionUnusable = extension.FailureClass{
		Class:    "connection_unusable",
		Sentence: "this connection was refused as unusable, so the poll had nothing valid to act on",
		Remedy:   "The member disconnects Relay and connects it again: a poll cannot repair the connection record it was handed, and the refused value is in the process log.",
	}
	// classPollFailed is the catch-all, and it is honest about being one. Its
	// remedy has to say where to look next, because a class that names nothing and
	// points nowhere is the sentence this whole file exists to stop printing.
	classPollFailed = extension.FailureClass{
		Class:    "poll_failed",
		Sentence: "the Relay poll failed for a cause this connector does not yet classify",
		Remedy:   "Read the cause in this job's process log. A failure that keeps landing here is one this connector owes a name, so report it with that line.",
	}
	// classEveryMemberFailed is the FLEET-WIDE class, and it exists because this
	// unit polls many connections in one tick rather than exactly one.
	//
	// It is reported ONLY when every connection failed AND they did not fail the
	// same way. Every member failing identically is not a fleet condition needing
	// its own name — it is that one condition, happening everywhere, and reporting
	// the shared class is what turns a screenful of dead jobs into a sentence
	// naming the thing to go fix (see fleetFailure). Members failing for DIFFERENT
	// reasons is the genuinely different situation: nothing is common to them, so
	// there is no single outage to chase and the class must not imply there is.
	//
	// Unlike every class above it, this token lands on NO connection row: each row
	// carries its own member's class, which is the more specific truth. What this
	// class describes is the tick, and the tick has no row.
	classEveryMemberFailed = extension.FailureClass{
		Class:    "every_connection_failed",
		Sentence: "every connected member failed this tick, and not all of them for the same reason",
		Remedy:   "Read each connection's own class on the Relay screen: the failures have nothing in common, so there is no single outage behind them.",
	}
)

// failureClasses is the set this unit declares, in the order an operator meets
// them: what a human must fix first, then what fixes itself, then the two
// catch-alls.
//
// It is ONE list and every other reference is to it, so a class that exists in
// the code and not in the declaration cannot happen — an undeclared class reaches
// the wire as the unvetted substitute, which is exactly the vague sentence this
// unit is getting rid of.
var failureClasses = []extension.FailureClass{
	classTokenRejected,
	classMemberNotPermitted,
	classConnectionUnusable,
	classProviderUnavailable,
	classProviderAnswerUnusable,
	classPollFailed,
	classEveryMemberFailed,
}
