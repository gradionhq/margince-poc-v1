// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

import "fmt"

// Actor-type and action literals that recur across this file (map key,
// switch case, occurrence count 3+ once this file joins fieldhistory.go/
// retention.go) — named once so goconst has one extraction target instead
// of flagging each new occurrence.
const (
	actorTypeAgent     = "agent"
	actorTypeHuman     = "human"
	actorTypeSystem    = "system"
	actorTypeConnector = "connector"
	actionArchive      = "archive"
)

// recordHistoryVerbs renders each audit_log action as a past-tense phrase
// for composeRecordSummary. The set here is reconciled to the CHECK
// constraint's admitted vocabulary (migrations/core/0053_audit_verb_
// vocabulary.up.sql, 25 verbs); TestRecordHistoryVerbsCoverTheAuditCheckVocabulary
// parses that file directly and fails if a future widening lands without a
// matching entry here. An action absent from the map (defensive only — the
// CHECK already closes the set at the DB level) falls back to the raw
// string, never an error: an unrenderable phrase is still honest history.
var recordHistoryVerbs = map[string]string{
	"create":           "created",
	"update":           "updated",
	actionArchive:      "archived",
	"merge":            "merged",
	"promote":          "promoted",
	"restore":          "restored",
	"export":           "exported",
	"erase":            "erased",
	"login":            "logged in",
	"assign":           "assigned",
	"advance_stage":    "advanced the stage of",
	"approve":          "approved",
	"reject":           "rejected",
	"consent_grant":    "granted consent for",
	"consent_withdraw": "withdrew consent for",
	"activity_relink":  "relinked",
	"record_share":     "shared",
	"record_unshare":   "unshared",
	"resolve":          "resolved",
	"demote":           "demoted",
	"import":           "imported",
	"import_undo":      "undid the import of",
	"disqualify":       "disqualified",
	"anonymize":        "anonymized",
	"send_email":       "sent an email for",
}

// composeRecordSummary renders one audit row as a plain-language sentence,
// the record-history read's `summary` field. It is pure: callers resolve
// actorDisplayName/onBehalfOfName (app_user lookups) before calling in, so
// this stays testable without a database. onBehalfOfName is set only for
// an agent acting under a human's delegated authority (D2's authority
// weaving); an empty string is treated the same as nil — a resolved-but-
// blank name is not authority to report.
func composeRecordSummary(actorType, actorDisplayName string, onBehalfOfName *string, action string) string {
	verb := recordHistoryVerbs[action]
	if verb == "" {
		verb = action
	}
	switch actorType {
	case actorTypeAgent:
		if onBehalfOfName != nil && *onBehalfOfName != "" {
			return fmt.Sprintf("Agent acting for %s %s the record", *onBehalfOfName, verb)
		}
		return fmt.Sprintf("Agent %s the record", verb)
	case actorTypeHuman:
		return fmt.Sprintf("%s %s the record", actorDisplayName, verb)
	case actorTypeSystem:
		return fmt.Sprintf("System %s the record", verb)
	case actorTypeConnector:
		return fmt.Sprintf("Connector %s the record", verb)
	default:
		return fmt.Sprintf("%s %s the record", actorType, verb)
	}
}
