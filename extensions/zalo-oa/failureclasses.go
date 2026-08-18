// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// This unit's failure vocabulary: one entry for every way its poll can fail
// that an operator would act on DIFFERENTLY.
//
// WHY THE SENTENCES LIVE HERE rather than being composed from the cause: a job
// failure reaches an admin through river_job.errors, a column with no workspace
// and so no RLS, which every workspace's admin reads. Zalo's own prose routinely
// names the account or the number it refused, so the job layer persists nothing
// but a sentence from a closed vocabulary — and until this unit declared one, the
// classification the poll had already computed died at that seam and an operator
// was told to go read a log with no key to find the line by.
//
// So every string below is one this unit WROTE. Nothing the provider said can
// reach that column through this file, because nothing the provider said is in
// it; the provider's own text stays in the process log, where the audience and
// the retention are the operator's own.
//
// THE CLASS TOKEN IS ALSO WHAT LANDS IN last_error_class on the connection row
// (see noteFailure and park), and that is deliberate rather than convenient: the
// connector screen and the Maintenance screen describe the same outage, and an
// operator comparing them must read one fact rather than two vocabularies.
//
// The REMEDIES are where the catalog earns its keep. A rejected credential, a
// package that does not cover the API, and a provider nobody can reach are three
// failures with three different people to go fix them — and one of them is fixed
// by nobody doing anything at all. Sending an operator to re-authorize a working
// credential because a package lapsed is a wasted afternoon in another company.

import "github.com/gradionhq/margince/backend/pkg/extension"

var (
	// classTokenRejected is the credential itself being refused. Zalo will renew
	// an OA token for nobody but the admin who authorized it, so this is the one
	// class whose remedy names a specific human.
	classTokenRejected = extension.FailureClass{
		Class:    "token_rejected",
		Sentence: "the Official Account's stored credential was refused, so this poll read nothing",
		Remedy:   "The administrator who authorized this connection re-authorizes it under Settings → Integrations; the provider renews the credential for nobody else.",
	}
	// classPackageTooLow is the account's SERVICE PACKAGE not covering the API,
	// with a credential that works perfectly. It is the class most worth keeping
	// separate from the one above: the two look identical in a log and are fixed
	// by different departments.
	classPackageTooLow = extension.FailureClass{
		Class:    "package_too_low",
		Sentence: "the Official Account's service package does not include the messaging API this poll reads",
		Remedy:   "Nothing to re-authorize — the credential works. The account's owner upgrades the package with the provider, and the next tick resumes on its own.",
	}
	// classAPINotRegistered is this INSTALLATION's developer app missing an API
	// group, which is neither the account's package nor anybody's credential: it
	// is a console setting, once, for every account this installation connects.
	classAPINotRegistered = extension.FailureClass{
		Class:    "api_not_registered",
		Sentence: "this installation's provider app has not registered the API group this poll reads",
		Remedy:   "Register that API group for the app in the provider's developer console. No credential and no account package is involved, and it is one setting for every connected account.",
	}
	// classProviderUnavailable is the outage that needs NO intervention, and
	// saying so is the point: the cursor did not move, so the next tick walks the
	// same region and nothing is lost. It is also the class an unreachable API host
	// lands in, which is the most common way this connector fails and the one an
	// operator must not be sent to repair a credential over.
	classProviderUnavailable = extension.FailureClass{
		Class:    "provider_unavailable",
		Sentence: "the provider could not be reached, or answered that it was too busy",
		Remedy:   "Nothing to do: the poll catches up by itself and no message is lost. If every tick fails, check this installation's network reach and DNS for the provider's API host.",
	}
	// classProviderAnswerUnusable is an answer this unit cannot read, which is a
	// unit-side defect far more often than an operator-side one — so the remedy
	// says to report it rather than sending somebody to change a setting.
	classProviderAnswerUnusable = extension.FailureClass{
		Class:    "provider_answer_unusable",
		Sentence: "the provider answered something this connector cannot read",
		Remedy:   "The answer is in the process log. A provider whose format changed needs a change to this connector, so report it rather than reconfiguring anything.",
	}
	// classMemberNotPermitted is the AUTHORIZING ADMIN's own authority having
	// gone: every record lands on their live permissions, so demoting or
	// archiving them stops the poll without anything about Zalo changing.
	classMemberNotPermitted = extension.FailureClass{
		Class:    "member_not_permitted",
		Sentence: "the member who authorized this connection may no longer capture what it lands",
		Remedy:   "Restore that member's role, or have another Official Account administrator re-authorize the connection so it runs on a seat that may capture.",
	}
	// classConnectionUnusable is the core refusing what this unit handed it as
	// invalid, on a path that is not one unrepresentable message (those are
	// dropped and recorded, never failed). What is left is a connection whose own
	// state no poll can repair.
	classConnectionUnusable = extension.FailureClass{
		Class:    "connection_unusable",
		Sentence: "this connection was refused as unusable, so the poll had nothing valid to act on",
		Remedy:   "Disconnect the Official Account and connect it again: a poll cannot repair the connection record it was handed, and the refused value is in the process log.",
	}
	// classPollFailed is the catch-all, and it is honest about being one. Its
	// remedy has to say where to look next, because a class that names nothing
	// and points nowhere is the sentence this whole file exists to stop printing.
	classPollFailed = extension.FailureClass{
		Class:    "poll_failed",
		Sentence: "the Zalo poll failed for a cause this connector does not yet classify",
		Remedy:   "Read the cause in this job's process log. A failure that keeps landing here is one this connector owes a name, so report it with that line.",
	}
)

// failureClasses is the set this unit declares, in the order an operator meets
// them: what a human must fix first, then what fixes itself, then the catch-all.
//
// It is ONE list and every other reference is to it, so a class that exists in
// the code and not in the declaration cannot happen — an undeclared class reaches
// the wire as the unvetted substitute, which is exactly the vague sentence this
// unit is getting rid of.
var failureClasses = []extension.FailureClass{
	classTokenRejected,
	classPackageTooLow,
	classAPINotRegistered,
	classMemberNotPermitted,
	classConnectionUnusable,
	classProviderUnavailable,
	classProviderAnswerUnusable,
	classPollFailed,
}
