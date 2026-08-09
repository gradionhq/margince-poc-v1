// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// Written copy for the reads that answer a question rather than return rows:
// the assembled pictures, the relationship graph, the pipeline-risk set, and
// the report engine. See toolcopy.go for what each field answers.

var catchMeUpOnCopy = toolCopy{
	Purpose: "Answer \"what has been going on with this?\" for one person, account, deal, lead, " +
		"project or captured meeting: the recent activity and the related records, assembled " +
		"into one picture with the evidence each part rests on.",
	Limits: "It is built around ONE record you name, and everything it reports carries a source; " +
		"what cannot be evidenced is absent rather than inferred.",
	Instead: "Use prep_for_meeting when the goal is a meeting about to happen, read_record when " +
		"you only need the record's own stored fields, and search_records when you do not yet " +
		"know which record you mean.",
	Retain: "Each item carries the record_type and record_id it came from — those are what a " +
		"follow-up call acts on.",
}

var prepForMeetingCopy = toolCopy{
	Purpose: "Get ready for a specific meeting: the same assembled picture as a catch-up, plus " +
		"the open items pulled out as the things to raise.",
	// Deliberately silent about anchoring on the meeting record itself. The
	// input schema advertises it, and prose that also recommended it cost more
	// than it bought: two bindings began reading the trigger's `calendar:…`
	// reference as an activity record id and preparing against a record that
	// does not exist. What the tool accepts is the schema's job to say.
	Limits: "It is built around ONE record you name, and everything it reports carries a source; " +
		"what cannot be evidenced is absent rather than inferred. Given a meeting it works out " +
		"which record that meeting is about and names the others alongside.",
	Instead: "Use catch_me_up_on when there is no meeting and the question is simply what has " +
		"been happening, and check_availability when the goal is finding a time rather than " +
		"preparing for one.",
	Retain: "The focus list names the open items by record_id; those are what to act on after " +
		"the meeting. prepared_for names the record the prep was built around.",
}

var whatsSlippingCopy = toolCopy{
	Purpose: "Answer \"what is slipping?\": the deals going quiet or running past their expected " +
		"close date, ranked worst first, each with the evidence that says so.",
	Limits: "It reports only deals whose risk can be evidenced from their own fields — a deal " +
		"nobody can point at a reason for is absent rather than guessed — and it is scoped to " +
		"the deals the caller may see.",
	Instead: "Use run_report for the pipeline as a whole (totals, counts, breakdowns), and " +
		"at_risk_relationships when the question is who a deal rests on rather than whether it " +
		"is moving.",
	Retain: "Keep each deal_id if you intend to act; draft_follow_ups_for works over this same " +
		"ranked set without you re-deriving it.",
}

var whoKnowsCopy = toolCopy{
	Purpose: "Answer \"who here knows this person?\": the colleagues with a relationship to one " +
		"contact, warmest first, with the interaction counts that ground the warmth.",
	Limits: "It reports relationships this workspace can evidence from its own recorded " +
		"interactions, so a genuine relationship nobody has logged does not appear. Never spoken " +
		"is reported as no relationship rather than a score of zero.",
	Instead: "Use intro_path_to when you want a route into a COMPANY rather than the people who " +
		"know one contact.",
	Retain: "Each colleague comes back with a user_id; the strength bucket, not the raw score, " +
		"is what a person should be asked about.",
}

var accountCoverageCopy = toolCopy{
	Purpose: "Answer \"is this deal covered?\": which roles on the account we have a relationship " +
		"with, and where the deal is exposed to a single contact.",
	Limits: "It assesses the relationships recorded against one deal's account, not the deal's " +
		"commercial health — nothing here says whether the deal will close.",
	Instead: "Use whats_slipping_this_week for deals at risk of stalling, and intro_path_to when " +
		"the answer is that a gap needs a warm route filling it.",
	Retain: "Keep the deal_id and the named gaps; they are what a follow-up plan is built from.",
}

var introPathToCopy = toolCopy{
	Purpose: "Find a warm route into a company: who we already know there, and which colleague " +
		"could make the introduction.",
	Limits: "It walks the relationships this workspace has recorded. An account nobody here has " +
		"ever spoken to has no warm path, and saying so is the correct answer rather than a " +
		"failure.",
	Instead: "Use who_knows when you already have the specific person and want the colleagues " +
		"who know THEM, and search_records when you are still looking for the account itself.",
	Retain: "The path names the colleague and the contact by id; both are needed to ask anyone " +
		"for the introduction.",
}

var atRiskRelationshipsCopy = toolCopy{
	Purpose: "Answer \"where are our relationships thin?\": across the caller's OPEN deals, the " +
		"ones resting on a single contact, missing an engaged champion, or carried almost " +
		"entirely by one person on our side.",
	Limits: "It sweeps open deals — a deal already won or lost is not at risk and is left out — " +
		"and it takes no arguments, because the caller's own visibility already decides which " +
		"deals these are. It is about the shape of the relationships around a deal, not about " +
		"the deal's own momentum.",
	Instead: "Use whats_slipping_this_week when the question is about deals losing momentum, and " +
		"account_coverage when the question is about one deal rather than the whole book.",
	Retain: "Each finding names its deal_id and the people it is about; those are what " +
		"intro_path_to and who_knows take next.",
}

var runReportCopy = toolCopy{
	Purpose: "Answer a question about totals, counts or breakdowns — pipeline by stage, deals " +
		"won by owner, activity volume over time — by running one of this workspace's prebuilt " +
		"reports.",
	Limits: "Only the named reports exist, and each accepts only its own filter, grouping and " +
		"measure names; anything outside its lists is refused rather than approximated. It " +
		"aggregates, so it answers how many and how much, never which record.",
	Instead: "Use search_records or whats_slipping_this_week when the answer wanted is the " +
		"records themselves rather than a number over them.",
	Retain: "Call a report with no plan arguments first to see what it answers by default, then " +
		"narrow using the names its own catalog entry lists.",
}

var readBriefCopy = toolCopy{
	Purpose: "Read the ranked queue the person you act for sees when they open their morning " +
		"brief — the deals the workspace decided are worth their attention today, in order, " +
		"with the rows behind each ranking.",
	Limits: "It re-reads the last assembled run rather than building a new one, so its as_of " +
		"says how current it is, and it is that person's own queue: it cannot be asked for " +
		"anyone else's. Acting on, dismissing or snoozing an item is theirs alone.",
	Instead: "Use whats_slipping_this_week when the question is which deals are losing momentum " +
		"regardless of what today's brief chose, and read_record for what one of these deals " +
		"currently says.",
	Retain: "Each item names a deal_id and its evidence_ids; read those to cite what the ranking " +
		"rested on rather than restating the item's own summary.",
}
