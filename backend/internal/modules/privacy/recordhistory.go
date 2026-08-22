// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

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
// constraint's admitted vocabulary;
// TestRecordHistoryVerbsCoverTheAuditCheckVocabulary derives that
// vocabulary from the highest-numbered migration restating
// audit_log_action_check and fails if a future widening lands without a
// matching entry here. An action absent from the map (defensive only — the
// CHECK already closes the set at the DB level) falls back to the raw
// string, never an error: an unrenderable phrase is still honest history.
var recordHistoryVerbs = map[string]string{ // #nosec G101 -- audit verbs and the English phrases they render as; "password_link_issued" names an action, and no value here is a secret
	"create":        "created",
	"update":        "updated",
	actionArchive:   "archived",
	"merge":         "merged",
	"promote":       "promoted",
	"restore":       "restored",
	"export":        "exported",
	"erase":         "erased",
	"login":         "logged in",
	"assign":        "assigned",
	"advance_stage": "advanced the stage of",
	"advance_phase": "advanced the phase of",
	// Deal Room access. "invited" and "revoked access for" rather than
	// "created"/"archived": the row is incidental, the access is the fact.
	"invite": "invited",
	"revoke": "revoked access for",
	// The Deal Room lifecycle. "closed" rather than "ended", because closing a
	// room keeps the buyer reading it — the phrase should not suggest access
	// went away with the content freeze.
	"publish":          "published",
	"pause":            "paused",
	"resume":           "resumed",
	"close":            "closed",
	"approve":          "approved",
	"accrue":           "accrued commission on",
	"pay":              "paid",
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
	"reset_data":       "reset",
	// Reads as "<admin> issued a set-password link for <member>" — the phrase
	// names what was issued, because on a member's history "issued" alone
	// would not distinguish a credential from anything else granted to them.
	"password_link_issued": "issued a set-password link for",
	// The subject is the provider connection, so the phrases name what the act
	// did to the installation's relationship with that provider rather than to
	// a record: connecting binds a paid credential, disconnecting destroys it
	// and stops all egress.
	"connect":    "connected",
	"disconnect": "disconnected",
	// A message the sender chose to send later, through its four acts
	// (ADR-0104/A155). "released" rather than "sent": the provider has not been
	// called when this is written.
	"schedule":   "scheduled",
	"reschedule": "rescheduled",
	"cancel":     "cancelled",
	"release":    "released for sending",
	"hold":       "held for review",
	// Nobody did this, and the phrase has to say so while still composing into
	// "<actor> <verb> the record" — "expired unactioned the record" is not a
	// sentence. Not "rejected", because the ledger's job here is to tell a
	// reader that no one ANSWERED: a column of these is work going unattended,
	// which no count of rejections would show.
	"expire": "let the decision window close on",
	// The statutory-retention pair (A165/ADR-0114, migration 0287). The
	// formatter is "<actor> <verb> the record", so a phrase has to take "the
	// record" as its direct object and read as a sentence: "System withheld the
	// record" and "Lars pinned the record" both do. The longer phrasing this
	// pair wants — naming the statutory obligation — cannot go here, because it
	// would strand the object ("withheld under a statutory retention obligation
	// the record"). It belongs in the evidence the restricted-records list
	// renders, which has room for the basis and the deadline.
	//
	// "withheld" rather than "restricted", which a reader hears as an
	// access-control decision the business made; this is the opposite — an
	// obligation the business is under, holding a record it would otherwise
	// have had to erase.
	//
	// A165's other two verbs, `release` and `expire`, are NOT re-keyed here and
	// cannot be: this map keys on the verb alone, and both already carry
	// unrelated phrases from the scheduled-send feature ("released for sending",
	// written on scheduled_send by activities/scheduledsendfire.go) and from
	// approvals ("let the decision window close on"). A retention release would
	// render as a sending event. Resolving that needs the phrase to key on
	// (verb, entity_type) rather than verb, which is a change to this map's
	// shape and to composeRecordSummary — it belongs with the code that first
	// writes a retention release, not ahead of it.
	"restrict": "withheld",
	"pin":      "pinned for statutory retention",
}

// auditActorNameJoins resolves the two display names every audit read owes
// its reader: the human who acted, and the human whose authority a machine
// acted under. It is ONE spelling shared by the two audit read paths in this
// package — the per-record history here and the workspace-wide compliance log
// in auditlog.go — because two surfaces resolving attribution differently is
// how a reader ends up trusting one and doubting the other.
//
// The audit row is aliased `a`; the caller supplies that alias and selects
// `actor_user.display_name, obo.display_name` in that order.
//
// The actor join builds the prefixed key FROM app_user ('human:' || id) rather
// than casting actor_id, so a non-uuid actor id (agent:*, connector:*, system)
// simply resolves to no name instead of raising a cast error. A LEFT JOIN both
// times, and matching app_user.id (a primary key) both times: a deactivated or
// deleted member still has audit rows, no name is honest where an invented one
// would not be, and neither join can duplicate or drop an audit row.
const auditActorNameJoins = `
		LEFT JOIN app_user actor_user
		  ON a.actor_type = 'human' AND a.actor_id = 'human:' || actor_user.id::text
		LEFT JOIN app_user obo ON obo.id = a.on_behalf_of`

// agentClientNameJoin resolves the NAME of the tool a delegated change was
// typed through — "Claude", "Cursor" — from the passport the row recorded.
//
// Three hops, all of them nullable, and each null means something different:
// a passport minted by hand in Settings has no oauth_grant_id (a person made
// it for themselves, and its own label is whatever they typed); a grant whose
// client row was deleted resolves nothing; and a row with no passport at all
// was not a delegated write. Every one of those falls back to the generic
// "via an agent", which is why this is a LEFT JOIN chain and not a filter.
//
// The join is on client_id ALONE. oauth_client used to be keyed
// (workspace_id, client_id) and this join said so; migration 1787109970 dropped
// the workspace column from every credential table, so client_id is the key
// now and naming the old one made every record-history read a 500.
const agentClientNameJoin = `
		LEFT JOIN passport p ON p.id = a.passport_id
		LEFT JOIN oauth_grant g ON g.id = p.oauth_grant_id
		LEFT JOIN oauth_client oc ON oc.client_id = g.client_id`

// RecordHistoryFilter carries the validated query surface of
// (GET /records/{entity_type}/{id}/history).
type RecordHistoryFilter struct {
	EntityType string
	EntityID   ids.UUID
	Cursor     *string
	Limit      *int
}

// RecordHistoryEntry is one audit_log row rendered as a history line —
// the whole-mutation view, one entry per row (field-history's per-field
// projection is the sibling read). Before/After are the row's own field
// images with the entity's mask applied by omission. Scrub tombstones and
// retention rows carry their operational tallies on audit_log.evidence,
// which this read never selects; other meta verbs' before/after payloads
// are served verbatim—workspace-operational context behind the same
// record-read gate, never subject PII.
type RecordHistoryEntry struct {
	ID                ids.UUID
	ActorType         string
	ActorID           string
	ActorName         *string
	OnBehalfOf        *ids.UUID
	OnBehalfOfName    *string
	Action            string
	OccurredAt        time.Time
	AuthorizationRule *string
	Before            map[string]any
	After             map[string]any
	Summary           string
	// AgentClient is the tool's registered name when the write came through an
	// OAuth grant. Nil for a hand-minted passport and for anything undelegated;
	// the Summary line already reads correctly either way, and this is here so
	// a client rendering its own sentence does not have to parse one.
	AgentClient *string
}

// RecordHistoryPage is one keyset window of the timeline, chronological
// (oldest first — the reading order of a story, unlike field-history's
// newest-first diff feed).
type RecordHistoryPage struct {
	Entries    []RecordHistoryEntry
	NextCursor string
	HasMore    bool
}

// recordAuditRow carries one scanned audit_log row plus its resolved
// display names, ready for the pure entry transform.
type recordAuditRow struct {
	id                ids.UUID
	actorType         string
	actorID           string
	onBehalfOf        *ids.UUID
	action            string
	occurredAt        time.Time
	authorizationRule *string
	before            map[string]any
	after             map[string]any
	passportID        *ids.UUID
	actorDisplayName  *string
	onBehalfOfName    *string
	// agentClientName is the tool's own name, when the passport behind the row
	// came from an OAuth grant. Nil for a hand-minted passport, for a deleted
	// client, and for every write that was not delegated.
	agentClientName *string
}

// recordHistoryEntry renders one audit row as a history entry: mask both
// payload sides by omission (hiding history and live value is one motion),
// then compose the plain-language line. The actor display falls back to
// the raw prefixed actor_id when no app_user resolves — an honest
// identifier, never an invented name; agent/connector/system ids never
// resolve, and their lines render from their own branch (an agent's human
// context is the on-behalf-of authority, nothing else).
func recordHistoryEntry(row recordAuditRow, mask entityFieldMask) RecordHistoryEntry {
	display := row.actorID
	if row.actorDisplayName != nil && *row.actorDisplayName != "" {
		display = *row.actorDisplayName
	}
	return RecordHistoryEntry{
		AgentClient:       row.agentClientName,
		ID:                row.id,
		ActorType:         row.actorType,
		ActorID:           row.actorID,
		ActorName:         row.actorDisplayName,
		OnBehalfOf:        row.onBehalfOf,
		OnBehalfOfName:    row.onBehalfOfName,
		Action:            row.action,
		OccurredAt:        row.occurredAt,
		AuthorizationRule: row.authorizationRule,
		Before:            applyFieldMask(row.before, mask),
		After:             applyFieldMask(row.after, mask),
		Summary: composeRecordSummary(row.actorType, display, row.onBehalfOfName,
			row.action, row.passportID != nil, row.agentClientName),
	}
}

// admitRecordHistoryRead is the object-level half of this read's gate stack:
// a human session, a known entity kind, and the read grant on it. The ROW-level
// half runs inside the transaction, because visibility is a statement.
//
// Separated from ListRecordHistory so the admission rules read as one list
// rather than as a preamble to paging. Both jobs are short; together they were
// one function doing two.
func admitRecordHistoryRead(ctx context.Context, f RecordHistoryFilter) error {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return apperrors.ErrPermissionDenied
	}
	if !fieldHistoryEntityTypes[f.EntityType] {
		return fmt.Errorf("record-history entity %q: %w", f.EntityType, apperrors.ErrNotFound)
	}
	return auth.Require(ctx, f.EntityType, principal.ActionRead)
}

// ListRecordHistory reads one record's whole-mutation timeline — every
// audit verb, one line per row — inside a single workspace tx. The gate
// stack is ListFieldHistory's, verbatim: a human session, object-level
// read on the entity type, and the row-scope visibility check (out of
// scope reads as not-found, indistinguishable from not-there).
//
// Unlike field-history there is no action allowlist: a merge or export
// line IS the point of this view. Payload honesty is inherited instead —
// scrub tombstones and retention rows carry operational tallies on
// audit_log.evidence, which this read never selects; other meta verbs
// (merge, promote, export, record_share, enrich, coldstart) serve
// operational context verbatim from before/after, workspace-operational
// data behind the same record-read gate, never subject PII.
func ListRecordHistory(ctx context.Context, db *database.DB, f RecordHistoryFilter) (RecordHistoryPage, error) {
	if err := admitRecordHistoryRead(ctx, f); err != nil {
		return RecordHistoryPage{}, err
	}

	limit := storekit.ClampLimit(f.Limit)
	var cursor storekit.Cursor
	useCursor := false
	if f.Cursor != nil && *f.Cursor != "" {
		c, err := storekit.DecodeCursor(*f.Cursor)
		if err != nil {
			return RecordHistoryPage{}, err
		}
		cursor, useCursor = c, true
	}
	mask := defaultFieldMasks[f.EntityType]

	page := RecordHistoryPage{Entries: []RecordHistoryEntry{}}
	err := db.Tx(ctx, func(tx pgx.Tx) error {
		// activity carries no owner_id — it row-scopes through its links,
		// so its visibility check dispatches to EnsureActivityContentVisible.
		var visErr error
		if f.EntityType == entityTypeActivity {
			visErr = auth.EnsureActivityContentVisible(ctx, tx, f.EntityID)
		} else {
			visErr = auth.EnsureVisible(ctx, tx, f.EntityType, f.EntityID)
		}
		if visErr != nil {
			return visErr
		}
		boundary, err := latestScrubTombstone(ctx, tx, f.EntityType, f.EntityID)
		if err != nil {
			return err
		}
		rows, err := queryRecordHistoryWindow(ctx, tx, f, boundary, cursor, useCursor, limit+1)
		if err != nil {
			return err
		}
		if len(rows) > limit {
			rows = rows[:limit]
			page.HasMore = true
			page.NextCursor = storekit.EncodeCursor(rows[limit-1].occurredAt, rows[limit-1].id)
		}
		for _, row := range rows {
			page.Entries = append(page.Entries, recordHistoryEntry(row, mask))
		}
		return nil
	})
	if err != nil {
		return RecordHistoryPage{}, err
	}
	return page, nil
}

// queryRecordHistoryWindow fetches one chronological keyset window of the
// record's audit spine, with both display names resolved in SQL by the
// shared auditActorNameJoins.
//
// Neither join carries a workspace predicate, and does not need one: both
// match app_user.id, a global primary key, so a uuid can only ever resolve
// the one person it names. What bounds the ROWS is the caller's gate — the
// row-scope visibility check above for this read, admin-only for the
// compliance log — not anything on the joined table.
func queryRecordHistoryWindow(ctx context.Context, tx pgx.Tx, f RecordHistoryFilter,
	boundary scrubBoundary, cursor storekit.Cursor, useCursor bool, fetch int,
) ([]recordAuditRow, error) {
	conds := []string{"a.entity_type = $1", "a.entity_id = $2"}
	args := []any{f.EntityType, f.EntityID}
	if boundary.exists() {
		// Tombstone-INCLUSIVE (>=), where field-history cuts strictly
		// after (>): projecting a tombstone's payload as field diffs would
		// fabricate changes, so that read withholds the row — but here the
		// tombstone renders as its own honest line ("… erased the
		// record"), and its images are empty since the scrub meta rides
		// evidence. Everything strictly older is still the PII the scrub
		// certified gone, and stays withheld.
		conds = append(conds, fmt.Sprintf("(a.occurred_at, a.id) >= ($%d, $%d)", len(args)+1, len(args)+2))
		args = append(args, boundary.occurredAt, boundary.id)
	}
	if useCursor {
		conds = append(conds, fmt.Sprintf("(a.occurred_at, a.id) > ($%d, $%d)", len(args)+1, len(args)+2))
		args = append(args, cursor.CreatedAt, cursor.ID)
	}
	args = append(args, fetch)
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT a.id, a.actor_type, a.actor_id, a.on_behalf_of, a.action, a.occurred_at,
		       a.authorization_rule, a.before, a.after, a.passport_id,
		       actor_user.display_name, obo.display_name, oc.client_name
		FROM audit_log a`+auditActorNameJoins+agentClientNameJoin+`
		WHERE %s
		ORDER BY a.occurred_at ASC, a.id ASC
		LIMIT $%d`, strings.Join(conds, " AND "), len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []recordAuditRow
	for rows.Next() {
		var r recordAuditRow
		var beforeJSON, afterJSON []byte
		if err := rows.Scan(&r.id, &r.actorType, &r.actorID, &r.onBehalfOf, &r.action, &r.occurredAt,
			&r.authorizationRule, &beforeJSON, &afterJSON, &r.passportID,
			&r.actorDisplayName, &r.onBehalfOfName, &r.agentClientName); err != nil {
			return nil, err
		}
		if err := unmarshalJSONBMap(beforeJSON, &r.before); err != nil {
			return nil, fmt.Errorf("audit row %s before: %w", r.id, err)
		}
		if err := unmarshalJSONBMap(afterJSON, &r.after); err != nil {
			return nil, fmt.Errorf("audit row %s after: %w", r.id, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// machineQualifier names the tool a delegated change was typed through, as
// the phrase that qualifies the PERSON who authorized it — "via an agent",
// not "an agent".
//
// It is the FALLBACK now. When the passport came from an OAuth grant the line
// names the client instead — "Demo Admin, via Claude, created the record" —
// because "via an agent" on every row of a company's history tells a rep
// nothing they did not already know, and the question they actually have is
// which tool did it.
//
// The name comes from oauth_client.client_name, not from passport.label. The
// concern the generic word was protecting against — a revoked passport's label
// outliving the grant on every row it ever wrote — does not apply: client_name
// is the registered identity of the tool, it does not change when a grant is
// revoked, and a client row that is gone falls back to this map.
var machineQualifier = map[string]string{
	actorTypeAgent:     "via an agent",
	actorTypeConnector: "via a connector",
}

// composeRecordSummary renders one audit row as a plain-language sentence,
// the record-history read's `summary` field. It is pure: callers resolve
// actorDisplayName/onBehalfOfName (app_user lookups) before calling in, so
// this stays testable without a database. onBehalfOfName is set only for a
// machine acting under a human's delegated authority (D2's authority
// weaving); an empty string is treated the same as nil — a resolved-but-
// blank name is not authority to report.
//
// The sentence NAMES THE PERSON FIRST and says a machine did the typing
// second (PD-002). A rep working through a passport is the rep: the line
// reads "Devin, via an agent, archived the record", never "an agent archived
// the record" with the person demoted to a trailing phrase. Attribution
// exists so somebody can be asked about a change, and a machine is not a
// party to anything — a line whose subject is the tool lets every human in
// the chain disclaim it.
func composeRecordSummary(actorType, actorDisplayName string, onBehalfOfName *string,
	action string, passportBacked bool, agentClientName *string,
) string {
	verb := recordHistoryVerbs[action]
	if verb == "" {
		verb = action
	}
	if qualifier, delegated := machineQualifier[actorType]; delegated && onBehalfOfName != nil && *onBehalfOfName != "" {
		if agentClientName != nil && *agentClientName != "" {
			qualifier = "via " + *agentClientName
		}
		return fmt.Sprintf("%s, %s, %s the record", *onBehalfOfName, qualifier, verb)
	}
	switch {
	case actorType == actorTypeHuman:
		return fmt.Sprintf("%s %s the record", actorDisplayName, verb)
	case passportBacked:
		// A PASSPORT was presented and yet no human resolved behind it. That
		// is a gap: passport.on_behalf_of is NOT NULL, so the authority
		// existed when the grant was made and the row failed to carry it (the
		// pre-0260 scheduled sends in compose/commsscheduled.go are the live
		// example). The line says so rather than falling back to "System",
		// which is reserved for a change that genuinely has nobody behind it —
		// letting system absorb a failed attribution would hide the gap on the
		// one surface that exists to expose it.
		return fmt.Sprintf("A machine with no recorded human authority %s the record", verb)
	case actorType == actorTypeSystem:
		return fmt.Sprintf("System %s the record", verb)
	case actorType == actorTypeAgent:
		// A background writer with no passport: an installation-wide pass that
		// nobody's context ran, so there is no human to name and no gap to
		// report. compose/extjobsrun.go writes one per extension job tick.
		// Calling this a missing authority would report a defect where there
		// is none — the distinction that matters is whether a grant was
		// presented, not which machine word the actor_type happens to carry.
		return fmt.Sprintf("Agent %s the record", verb)
	case actorType == actorTypeConnector:
		// Same reasoning as the bare agent above: some connectors have no
		// connect flow and therefore no granting human by design
		// (compose/jobs_finance.go writes one).
		return fmt.Sprintf("Connector %s the record", verb)
	default:
		return fmt.Sprintf("%s %s the record", actorType, verb)
	}
}
