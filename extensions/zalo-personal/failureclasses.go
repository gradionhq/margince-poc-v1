// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// This unit's failure vocabulary: one entry for every way its drain can fail
// that an operator would act on DIFFERENTLY.
//
// WHY THE SENTENCES LIVE HERE rather than being composed from the cause: a job
// failure reaches an admin through river_job.errors, a column with no workspace
// and so no RLS, which every workspace's admin reads. This connector holds
// PERSONAL conversations, so the provider's own prose is the last text in the
// product that may travel that way — it routinely names the account or the
// person it refused. The job layer therefore persists nothing but a sentence
// from a closed vocabulary, and until this unit declared one, the classification
// the drain had already computed died at that seam: an operator was told the
// failure could not be classified and sent to read a log with no key to find the
// line by.
//
// So every string below is one this unit WROTE. Nothing Zalo said can reach that
// column through this file, because nothing Zalo said is in it; Zalo's own text
// stays in the process log, where the audience and the retention are the
// operator's own.
//
// THE CLASS TOKEN IS ALSO WHAT LANDS IN last_error_class on the connection row,
// and that is deliberate rather than convenient: api/crm.yaml publishes the same
// four tokens as a closed set, the member's own screen renders them through
// frontend/i18n, and the Maintenance screen renders these sentences. All three
// describe one outage, so an operator comparing them reads one fact rather than
// three vocabularies — and contractparity_test.go holds the contract's enum
// equal to what failureClass actually answers.
//
// THE REMEDIES ARE WHERE THE CATALOG EARNS ITS KEEP, and for this unit they
// differ from the other two connectors' in one way worth knowing before copying
// any of them: a personal session can only ever be restored by the ONE member
// who owns it, phone in hand. No administrator can do it for them, so a remedy
// that sent an operator to Settings would be sending them to a screen that
// cannot help.

import "github.com/gradionhq/margince/backend/pkg/extension"

var (
	// classSessionWithdrawn is the member's own login no longer being held here,
	// or no longer being permitted to land what it reads. It is the one class
	// whose remedy names a specific human and admits that nobody else will do.
	classSessionWithdrawn = extension.FailureClass{
		Class:    "session_withdrawn",
		Sentence: "a member's Zalo login is no longer usable here, so nothing of theirs was read",
		Remedy:   "That member scans a fresh QR code with their own phone; a personal session cannot be restored for them by an administrator or by anybody else.",
	}
	// classConnectionUnusable is the core refusing what this unit handed it as
	// invalid, on a path that is not one unrepresentable message (those are
	// dropped and recorded, never failed). What is left is a connection whose own
	// state no drain can repair.
	classConnectionUnusable = extension.FailureClass{
		Class:    "connection_unusable",
		Sentence: "a member's connection was refused as unusable, so their drain had nothing valid to act on",
		Remedy:   "That member disconnects Zalo and scans again: a drain cannot repair the connection record it was handed, and the refused value is in the process log.",
	}
	// classProviderUnavailable is the outage that needs no intervention to
	// RESUME — and, unlike the other two connectors', it is not an outage that
	// needs no intervention at all. See the remedy: this protocol has no
	// acknowledgement and no since-parameter, so the provider's queue IS the
	// backlog and it expires. An outage longer than that queue's retention costs
	// messages nothing can recover, which is a fact an operator reading this line
	// has to be told rather than reassured out of.
	classProviderUnavailable = extension.FailureClass{
		Class:    "provider_unavailable",
		Sentence: "Zalo could not be reached from this installation, so no member's inbox was drained",
		Remedy:   "Nothing to re-scan: the next reachable tick re-reads the same backlog. Chase a LONG outage — Zalo's own queue expires, and what expires in it is not recoverable.",
	}
	// classPollFailed is the catch-all, and it is honest about being one. Its
	// remedy has to say where to look next, because a class that names nothing and
	// points nowhere is the sentence this whole file exists to stop printing.
	classPollFailed = extension.FailureClass{
		Class:    "poll_failed",
		Sentence: "the Zalo inbox drain failed for a cause this connector does not yet classify",
		Remedy:   "Read the cause in this job's process log. A failure that keeps landing here is one this connector owes a name, so report it with that line.",
	}
	// classEveryMemberFailed is the FLEET-WIDE class, and it exists because this
	// unit drains many members in one tick.
	//
	// It is reported ONLY when every member failed AND they did not fail the same
	// way. Every member failing identically is not a fleet condition needing its
	// own name — it is that one condition, happening everywhere, and reporting the
	// shared class is what turns a screenful of dead jobs into a sentence naming
	// the thing to go fix (see fleetFailure). Members failing for DIFFERENT reasons
	// is the genuinely different situation: nothing is common to them, so there is
	// no single outage to chase and the class must not imply there is.
	//
	// Unlike every class above it, this token lands on NO connection row and is in
	// NO contract enum: each row carries its own member's class, which is the more
	// specific truth. What this one describes is the TICK, and the tick has no row.
	classEveryMemberFailed = extension.FailureClass{
		Class:    "every_connection_failed",
		Sentence: "every connected member failed this tick, and they did not all fail the same way",
		Remedy:   "There is no single outage to chase here. Read each member's own class on the Zalo connections screen; those name the specific problems and who owns each one.",
	}
)

// failureClasses is the set this unit declares, in the order an operator meets
// them: what a specific human must fix first, then what fixes itself, then the
// two catch-alls.
//
// It is ONE list and every other reference is to it, so a class that exists in
// the code and not in the declaration cannot happen — an undeclared class reaches
// the wire as the unclassified substitute, which is exactly the vague sentence
// this unit is getting rid of.
var failureClasses = []extension.FailureClass{
	classSessionWithdrawn,
	classConnectionUnusable,
	classProviderUnavailable,
	classPollFailed,
	classEveryMemberFailed,
}
