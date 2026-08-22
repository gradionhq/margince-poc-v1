// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// Written copy for the record surface: the generic CRUD verbs, the deal and
// lead lifecycle transitions, and the configuration read the deal writes
// depend on. See toolcopy.go for what each field answers.

var searchRecordsCopy = toolCopy{
	Purpose: "Find people, organizations, deals, leads and projects when you know roughly what " +
		"they are called but not which record they are.",
	Limits: "It matches text stored ON the record. It does not read a timeline: message bodies, " +
		"call notes and meeting content are not searched, so a query describing what someone said " +
		"or did will not find them.",
	Instead: "Use list_records when the question is which records meet a condition rather than " +
		"what one is called, read_record when you already hold the record's id, and run_report " +
		"when the question is a count, a total or a breakdown rather than a set of records.",
	Retain: "Keep each result's record_type and id together: every other tool identifies a record " +
		"by both, and an id alone does not say which type it belongs to.",
}

var listRecordsCopy = toolCopy{
	Purpose: "Enumerate the people, organizations, deals, leads or projects that meet exact " +
		"conditions — every deal in one pipeline, the leads one person owns, the projects still " +
		"being delivered.",
	Limits: "It narrows only by the filters this workspace publishes for that record_type, which " +
		"the schema lists per type, and it answers ONE page: the set continues past it.",
	Instead: "Use search_records when the question is what a record is called rather than which " +
		"records meet a condition, and run_report when the answer is a count or a total rather " +
		"than the records themselves.",
	Retain: "Keep next_cursor and pass it back to read the next page — a second call without it " +
		"re-reads the first one.",
}

var readRecordCopy = toolCopy{
	Purpose: "Read one record's own stored fields — the values a person would see on its detail " +
		"page — when you already know which record you mean.",
	Limits: "It returns that record and nothing around it: no timeline, no related people, no " +
		"deals on the account.",
	Instead: "Use catch_me_up_on when the goal is what has been happening on the record rather " +
		"than what it currently says.",
	Retain: "Keep the version from the result and pass it back as if_version on a later update, " +
		"so a write is refused rather than silently overwriting a change made in between.",
}

var createRecordCopy = toolCopy{
	Purpose: "Create a person, organization, deal, lead, project, activity or relationship that " +
		"does not exist yet.",
	Limits: "Creating a deal requires a pipeline_id and a stage_id, and list_pipelines is what " +
		"yields them for a deal that does not exist yet. Only the fields the chosen record_type " +
		"actually stores are accepted, and a field belonging to a neighbouring type is refused " +
		"rather than dropped.",
	Instead: "Search first when the record might already exist — a second copy of a person or " +
		"account is a problem that then needs merge_records to undo.",
	Retain: "The new record's id comes back in the result; keep it for anything that links to it.",
}

var updateRecordCopy = toolCopy{
	Purpose: "Change stored field values on a record that already exists — a corrected title, an " +
		"amount, an expected close date.",
	Limits: "Only the fields you send change, and only the fields the record type stores (a " +
		"person's email addresses are not among them). A field a HUMAN last set is not " +
		"overwritten: that part is staged for a person and named in the result, and that part " +
		"of the write has not happened. It names the record by id; when a name matches " +
		"two records, a person picks. owner_id is NOT neutral — ownership decides visibility, so " +
		"reassigning moves the record onto someone else's book and can take it off the owner's.",
	Instead: "Use advance_deal or progress_deal to move a deal between stages, and relink_activity " +
		"to change what an activity is about; neither is a field edit.",
	Retain: "Send if_version with the version you read, and keep the staged approval id from the " +
		"result if you intend to retry the same change once a human has released it.",
}

var logActivityCopy = toolCopy{
	Purpose: "Record something that happened — a call, a meeting, a note, a message — on the " +
		"timeline of the records it was about.",
	Limits: "It writes history and changes nothing else: no deal moves, no field updates, nobody " +
		"is notified. Unlinked, it appears on no timeline.",
	Instead: "Use progress_deal when the same event also moves a deal, so move and note are one " +
		"act; create_task for something still owed.",
	Retain: "Keep the activity id — draft_email, send_email and send_message identify a " +
		"conversation by it.",
}

var createTaskCopy = toolCopy{
	Purpose: "Put a to-do on someone's list: what is owed, by whom, on which records.",
	Limits:  "Creates the task only — no reminder, no deal move; unlinked, it sits on no timeline.",
	Instead: "log_activity is for what already happened.",
}

var relinkActivityCopy = toolCopy{
	Purpose: "Fix what a recorded activity is about, when a captured mail or meeting landed on " +
		"the wrong record or on none.",
	Limits: "Changes only the association; content is untouched. By default the new link is " +
		"ADDED beside existing ones.",
	Instead: "log_activity records an event not recorded yet; relink_thread moves a whole " +
		"conversation; relink_activities a picked set.",
	Retain: "Set replace_existing_of_type to move rather than associate.",
}

var relinkThreadCopy = toolCopy{
	Purpose: "Move one whole conversation (by thread_key) onto a record, in one transaction.",
	Limits: "Moves only activities you may write; the rest stay, uncounted. A project " +
		"destination needs a human.",
	Instead: "relink_activity moves one message.",
	Retain:  "The answer lists the ids moved.",
}

var relinkActivitiesCopy = toolCopy{
	Purpose: "Move up to 500 named activities onto one record, all or nothing.",
	Limits:  "Each id must be visible and writable to you. A project destination needs a human.",
	Instead: "relink_thread moves one conversation.",
	Retain:  "The answer lists the ids moved.",
}

var archiveRecordCopy = toolCopy{
	Purpose: "Retire a record that should no longer be worked — a duplicate, a dead account, a " +
		"project that ended.",
	Limits: "Archiving hides the record from day-to-day work; it does not delete it and does not " +
		"move anything attached to it, so an archived duplicate still holds the activities and " +
		"deals that were logged against it.",
	Instead: "Use merge_records when a duplicate's history should end up on the record that " +
		"survives, and disqualify_lead when a lead is going nowhere — a lead's own transition " +
		"records the reason where archiving would not.",
	Retain: "A person approves this call before it runs; do not report the record as archived " +
		"until the retry that carries their approval has answered.",
}

var mergeRecordsCopy = toolCopy{
	Purpose: "Collapse two records for the same real person or company into one, moving the " +
		"source's activities, deals and links onto the record that survives.",
	Limits: "People merge with people and organizations with organizations; the source is " +
		"archived and redirected to the target, and the direction is not reversible by calling " +
		"this again the other way round.",
	Instead: "Use archive_record when the extra record has nothing worth keeping, rather than " +
		"merging to make it disappear.",
	Retain: "target_id is the record that survives and source_id the one merged away — read both " +
		"records before choosing, because a person approves the call as you described it.",
}

var advanceDealCopy = toolCopy{
	Purpose: "Move a deal to a different stage of its pipeline.",
	Limits: "The stage is named by id from list_pipelines — call it first; a deal you read " +
		"carries only its current stage. Moving onto or off a won/lost stage is a person's " +
		"decision: staged for approval, with a lost_reason for a losing stage. Read the target " +
		"stage's semantic rather than guessing from its name.",
	Instead: "Use progress_deal when the move should also leave a note explaining it, which is " +
		"almost always what a person means by moving a deal on.",
	Retain: "Send if_version with the version you read of the deal, and keep the staged approval " +
		"id when a closing move comes back for approval.",
}

var progressDealCopy = toolCopy{
	Purpose: "Move a deal to a new stage and leave a note on its timeline saying why, in one " +
		"call.",
	Limits: "The move commits first and the note follows it, so a note that fails to write does " +
		"not put the deal back — the answer says so, and the note is then log_activity's to " +
		"retry. The note itself is optional. Same rules as the bare move otherwise: call " +
		"list_pipelines for the id of the stage you are moving to, and moving onto or off a " +
		"stage that closes a deal as won or lost is staged for a person to approve.",
	Instead: "Use advance_deal when there is genuinely nothing to say about the move, and " +
		"log_activity when something happened but the deal did not move.",
	Retain: "Send if_version with the version you read of the deal; keep the staged approval id " +
		"if a closing move is sent for approval.",
}

var qualifyLeadCopy = toolCopy{
	Purpose: "Fill in what a lead's own data already implies — today the company name, from the " +
		"domain of its email address — and report which qualification fields are still empty.",
	Limits: "It fills only a field that is currently EMPTY and derivable from the lead itself. It " +
		"never overwrites a value, never invents one, and reaches nothing outside the record, so " +
		"a lead with nothing to derive from comes back unchanged with its gaps named.",
	Instead: "Use enrich to learn about a company from its website, and promote_lead once a real " +
		"engagement means the lead should become a person.",
	Retain: "The gaps in the result are what a human still has to supply; they are the honest " +
		"answer to \"is this lead ready\", not a failure of the call.",
}

var promoteLeadCopy = toolCopy{
	Purpose: "Turn a lead who has genuinely engaged into a person record, carrying their history " +
		"across.",
	Limits: "It requires a trigger naming the engagement that justifies it — a reply, a booked " +
		"or held meeting, or a human's decision. Cold outreach that nobody answered is not a " +
		"promotion, and there is no trigger for it.",
	Instead: "Use qualify_lead when the lead is merely incomplete rather than ready, and " +
		"disqualify_lead when the engagement says the opposite.",
	Retain: "A person approves this call before it runs; the promoted person's id comes back only " +
		"from the retry that carries their approval.",
}

var disqualifyLeadCopy = toolCopy{
	Purpose: "Close out a lead that is not going anywhere, so it stops appearing as live work.",
	Limits: "It is the lead's own terminal state and keeps the record and its history; it is not " +
		"a deletion and not an archive.",
	Instead: "Use promote_lead when engagement says the opposite, and qualify_lead when the lead " +
		"is only missing information.",
	Retain: "A person approves this call before it runs; do not report the lead as disqualified " +
		"until the retry carrying their approval has answered.",
}

var advanceProjectPhaseCopy = toolCopy{
	Purpose: "Move a project to another phase — initiative, pursuing, delivering, closed.",
	Limits: "The four names are fixed but the order is not enforced: a project may go back a " +
		"phase, and a closed one may be reopened. Closing requires a reason, which is recorded on " +
		"the phase history either way.",
	Instead: "Use advance_deal for a deal's pipeline stages; a project's phases are a different " +
		"vocabulary on a different record.",
	Retain: "Send if_version with the version you read; a person approves the move before it " +
		"runs.",
}

var listPipelinesCopy = toolCopy{
	Purpose: "List every pipeline this workspace has with its live stages — the configuration " +
		"the deal-shaped writes are named against.",
	Limits: "It is where the id of a stage a deal could move TO comes from, so a deal cannot be " +
		"created, or moved anywhere new, without calling this first — a deal you have already " +
		"read carries only the stage it is in. Each stage carries a semantic — open, won or lost " +
		"— and that, not its name, is what decides whether moving onto it needs a person's " +
		"approval; a stage called \"Closed\" may be either.",
	Retain: "Keep the pipeline_id and the stage_id of the stage you mean: create_record for a " +
		"deal requires both, and advance_deal and progress_deal take that stage_id as their " +
		"to_stage_id.",
}
