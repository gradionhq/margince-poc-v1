// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The copy for the import verbs. It carries one thing the schema cannot: that
// a file of people becomes LEADS, not contacts. A caller picking `person` for
// a prospect list would be picking a valid enum value and breaking ADR-0008's
// anti-pollution rule at the same time.

var previewImportCopy = toolCopy{
	Purpose: "Bring a spreadsheet in: send the CSV as text and this checks every row " +
		"against the workspace and reports what importing it would do.",
	Limits:  "Writes nothing. People arrive as leads, so `object` is lead or organization.",
	Instead: "create_record for one record you already know.",
	Retain:  "run_id.",
}

var readImportRunCopy = toolCopy{
	Purpose: "Where one import got to: awaiting approval, running, done, or stopped.",
	Limits:  "A stopped run names the row it stopped at and can resume there.",
}

var readImportReportCopy = toolCopy{
	Purpose: "What an import will do, or did: rows created, updated, failed, unusable.",
	Limits:  "These counts are what a person approves. Same shape before and after.",
}

var commitImportCopy = toolCopy{
	Purpose: "Write a checked import into the workspace, once a person approves.",
	Limits:  "Only from awaiting_approval. Undoing one needs the web app.",
	Instead: "read_import_report first; nobody should approve what they have not read.",
}
