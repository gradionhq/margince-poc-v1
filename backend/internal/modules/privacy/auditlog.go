// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The audit-log read surface (GET /audit-log): the Settings governance
// view over the append-only audit_log table. Reading the installation's
// full attributable history deliberately crosses row scope, and it is
// the admin's alone — distinct from the per-record history in
// recordhistory.go, which every member may read on records they can
// see. A caller without that authority gets 403, never a narrowed page
// that would misread as "nothing happened".

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// AuditFilter narrows the audit page. The actor filter matches the
// stored typed principal id (`human:<uuid>`, `agent:<passport>`,
// `system:*`) verbatim — the column already carries the typed spelling.
type AuditFilter struct {
	Actor      *string
	EntityType *string
	// EntityID stays ids.UUID: it filters the audit envelope's polymorphic
	// (entity_type, entity_id) pair, which addresses any entity kind.
	EntityID *ids.UUID
	Action   *string
	From     *time.Time
	To       *time.Time
	Cursor   *string
	Limit    *int
}

// AuditEntry mirrors one audit_log row (contract AuditLogEntry). ID
// stays ids.UUID — the audit row is a ledger line, not a first-class
// entity — and EntityID stays untyped as the envelope's polymorphic
// target; the concrete passport/on-behalf ids type cleanly.
type AuditEntry struct {
	ID        ids.UUID
	ActorType string
	ActorID   string
	// ActorName and OnBehalfOfName are resolved on the read path, not stored:
	// the ledger row keeps the identifier that was true when it was written,
	// and a display name is looked up when somebody reads it. Both are nil
	// when no app_user resolves — a machine actor, or a member whose account
	// is gone while their audit rows remain.
	ActorName         *string
	OnBehalfOfName    *string
	PassportID        *ids.PassportID
	OnBehalfOf        *ids.UserID
	Action            string
	EntityType        string
	EntityID          *ids.UUID
	Before            []byte
	After             []byte
	AuthorizationRule *string
	Evidence          []byte
	OccurredAt        time.Time
}

// AuditPage is one newest-first keyset page.
type AuditPage struct {
	Entries    []AuditEntry
	NextCursor string
	HasMore    bool
}

// ListAuditLog reads the installation's audit history, newest first.
//
// Human-only: an agent reading the log that records its own governance would
// observe exactly the oversight trail that bounds it. The agent gate refuses
// the route as well, and this is the arm that survives a routing change.
//
// Admin-only, and deliberately NOT "unbounded row scope". This is the
// unrestricted compliance read, distinct from the per-record history every
// member may read on records they can see, and the spec's governance matrix
// reserves it for the admin alone. Row scope is the wrong predicate for it:
// ops and read_only both seed with scope `all`, so an unbounded check handed
// the whole governance trail to two roles the matrix denies — the compliance
// read is oversight OF ops' machine-origin actions and cannot sit with the
// role it oversees.
// auditActivityAlias names the activity an audit row is ABOUT, joined so the
// audience the row's author set can be asked about it. It is deliberately not
// `a` or one of the app_user aliases already in the statement.
const auditActivityAlias = "aud_activity"

// auditActivityJoin reaches the activity an audit row describes, and only for
// rows that describe one. entity_id is a bare uuid across every entity type, so
// the entity_type test is what stops a person's id colliding with an activity's
// and withholding an image that has no audience to answer to.
const auditActivityJoin = `
		LEFT JOIN activity ` + auditActivityAlias + `
		  ON a.entity_type = 'activity' AND ` + auditActivityAlias + `.id = a.entity_id`

// auditImageGovernanceKeys are the keys an activity's audit image may carry
// that describe the MUTATION rather than the activity's content — who the
// audience became, which record a relink pointed at, which fields moved.
//
// The audience governs an activity's CONTENT. It has no claim over the record
// of an administrative act performed on that activity, and withholding one
// destroys governance data rather than protecting anything: the row recording
// "this conversation was limited to its participants, naming 3 members" carries
// nothing a reader outside the audience is not entitled to, and is precisely
// what a compliance reader is looking for.
//
// The map is an ALLOWLIST and the redaction is fail-closed: a key that is not
// here is dropped, so a new writer that starts recording content into an audit
// image cannot leak it by default.
//
// ⚠️ SCOPE, narrower than "the audience is honoured on this endpoint": this map
// is consulted only for an audit row whose entity_type is `activity`. Other
// entity types that hang off an activity — `attachment`, whose image carries
// the user-supplied filename, plus `attachment_extraction`, `transcript_read`
// and `scheduled_send` — are not joined to their activity and are not
// redacted, so a reader outside the audience receives those images whole.
//
// Each entry carries a predicate on the VALUE, because a key alone is not a
// safety property. `body` is the case that forced it: the writer reduces it to a
// presence flag and says so in a comment, but nothing bound the READER to that —
// one plausible edit turning `delta["body"] = true` into the body itself would
// have handed an out-of-audience admin the confidential text of a limited
// conversation through this endpoint, passing every gate in the tree.
//
// `body` is the only entry actually constrained. The rest are anyValue, so the
// "a writer starts nesting content under this key" scenario applies unchanged
// to `kind`, `source_system` or a timestamp: those are judged safe by what the
// activity read surface answers for them, which is a claim about today's
// writers rather than a guard. Tightening one is a one-line predicate.
//
// TestRedactionKeepsGovernanceAndDropsContent pins both directions, including
// a `body` carrying something other than a boolean.
var auditImageGovernanceKeys = map[string]func(json.RawMessage) bool{
	"audience": anyValue, "member_count": anyValue,
	"entity_type": anyValue, "entity_id": anyValue, "replaced": anyValue,
	"merged_into_id": anyValue,
	// Mirrors what the activity READ surface keeps on a withheld row
	// (activityread.go): kind, direction, occurred_at and source_system are
	// markers it answers; subject, body and SOURCE_ID are what it nils. source_id
	// is deliberately absent here for that reason — it identifies the message at
	// the provider, the capture sink writes it onto the audit image, and admitting
	// it would have this endpoint answer what the record's own read refuses.
	"kind": anyValue, "direction": anyValue, "source_system": anyValue,
	"occurred_at": anyValue, "due_at": anyValue, "remind_at": anyValue,
	"assignee_id": anyValue, "is_done": anyValue,
	// Presence only, and enforced HERE rather than trusted from the writer.
	"body": isJSONBool,
}

// anyValue admits a key whose every possible value is metadata about the
// mutation. Named rather than written as an inline closure so the exceptions —
// today just `body` — stand out at a glance in the map above.
func anyValue(json.RawMessage) bool { return true }

// isJSONBool admits only a literal JSON `true` or `false`.
//
// The null check is not redundant: json.Unmarshal into a bool accepts JSON null
// and leaves the bool at its zero value, so a bare Unmarshal admits
// `"body": null` — the one shape where a non-boolean body would survive
// redaction, and survive it with no marker to say anything was examined.
func isJSONBool(raw json.RawMessage) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	var flag bool
	return json.Unmarshal(raw, &flag) == nil
}

// withholdAuditImage redacts the record images on one entry, keeping the row and
// keeping everything the audience has no claim over.
//
// It does NOT blank the whole image. Doing that destroyed the audit record of
// the audience change itself, which is the one act a reader most needs to see
// and the one carrying no content at all.
//
// What is dropped is replaced by a marker rather than silently omitted, because
// an absent key and a withheld one are different answers to the compliance
// question being asked, and a reader cannot tell them apart otherwise. The
// marker reuses the wire spelling the activity read surface already answers
// with, so present-but-withheld has one spelling across the product.
//
// Evidence is untouched. It is context ABOUT the mutation — which retention
// policy fired, which rule admitted it — not the record content the audience
// governs.
func withholdAuditImage(e *AuditEntry) {
	e.Before = redactAuditImage(e.Before)
	e.After = redactAuditImage(e.After)
}

// redactAuditImage keeps the governance keys of one image and marks the rest
// withheld.
//
// An ABSENT image is returned unchanged: there is nothing to redact, and
// inventing a marker for a row that carried no image would answer a question the
// ledger never asked. An UNREADABLE one is withheld whole — it cannot be
// redacted key by key, and passing it through would be the disclosure. The two
// are different answers and must not be collapsed into one word.
func redactAuditImage(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		// Not an object — a scalar or an array image. It cannot be partially
		// redacted, so it is withheld whole rather than passed through.
		return auditWithheldImage
	}
	kept := make(map[string]json.RawMessage, len(fields))
	withheld := false
	for key, value := range fields {
		if admits, ok := auditImageGovernanceKeys[key]; ok && admits(value) {
			kept[key] = value
			continue
		}
		withheld = true
	}
	if !withheld {
		return raw
	}
	kept[auditContentStateKey] = json.RawMessage(`"` + string(crmcontracts.ActivityContentStateWithheld) + `"`)
	redacted, err := json.Marshal(kept)
	if err != nil {
		// Re-marshalling values that were just unmarshalled cannot fail, but a
		// silent pass-through here would be the disclosure itself, so the
		// unreachable branch withholds rather than trusting that.
		return auditWithheldImage
	}
	return redacted
}

// auditContentStateKey names the marker key, spelled once so the reader, the
// contract and the tests cannot disagree about it.
const auditContentStateKey = "content_state"

// auditWithheldImage is the whole-image stand-in, used only where an image
// cannot be partially redacted.
var auditWithheldImage = []byte(`{"` + auditContentStateKey + `":"` + string(crmcontracts.ActivityContentStateWithheld) + `"}`)

func ListAuditLog(ctx context.Context, db *database.DB, f AuditFilter) (AuditPage, error) {
	// Human is spelled out rather than delegated to auth.RequireHuman, which
	// only turns agents away: nothing internal reads this trail, so connector
	// and system principals are refused here too — which is also why
	// RequireAdmin's system bypass below is unreachable.
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return AuditPage{}, fmt.Errorf("human-only compliance read: %w", apperrors.ErrPermissionDenied)
	}
	if err := auth.RequireAdmin(ctx); err != nil {
		return AuditPage{}, err
	}

	limit := storekit.ClampLimit(f.Limit)
	where, args, err := buildAuditWhere(f)
	if err != nil {
		return AuditPage{}, err
	}
	// argN is the platform/auth spelling — a clause helper registers a value and
	// gets its ORDINAL back — and arg is the local "$N" spelling this statement
	// already interpolates. One list, two renderings, so a value registered by
	// the audience arm and one registered here cannot land at different offsets.
	argN := func(v any) int {
		args = append(args, v)
		return len(args)
	}
	arg := func(v any) string {
		return "$" + strconv.Itoa(argN(v))
	}

	// The audience an activity's author set is a property of the ROW, and it
	// does NOT yield to row_scope=all — that is the whole point of the limit,
	// and RequireAdmin above is therefore not an answer to it. The audit image
	// carries the subject verbatim (activities.LogActivity writes
	// {kind, subject}; the update delta writes subject in full while body is
	// reduced to presence), so an admin outside the audience reads through this
	// endpoint exactly what the limit exists to withhold.
	//
	// Projected per row rather than filtered, because a compliance trail with
	// holes in it is its own defect: the row, its actor, its action and its
	// timestamp are all still answered, and only the IMAGE is withheld.
	//
	// A non-activity row has no audience to answer to and is untouched. An
	// activity row whose record is GONE is withheld, not passed through: the
	// join failing is not evidence that the caller may read the image, and a
	// predicate spelled `id IS NULL OR audience` would answer "readable" for
	// exactly the rows whose audience can no longer be checked. Nothing purges
	// activity rows today, so this is the latent direction — but it is the
	// direction that re-opens the disclosure silently, and no test would notice.
	audience, err := auth.ActivityAudienceArm(ctx, auditActivityAlias, argN)
	if err != nil {
		return AuditPage{}, err
	}

	var page AuditPage
	err = db.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT a.id, a.actor_type, a.actor_id, a.passport_id, a.on_behalf_of,
			        a.action, a.entity_type, a.entity_id, a.before, a.after, a.authorization_rule,
			        a.evidence, a.occurred_at,
			        actor_user.display_name, obo.display_name,
			        (a.entity_type <> 'activity'
			          OR (`+auditActivityAlias+`.id IS NOT NULL AND (`+audience+`))) AS content_readable
			 FROM audit_log a`+auditActorNameJoins+auditActivityJoin+`
			 WHERE `+where+`
			 ORDER BY a.occurred_at DESC, a.id DESC
			 LIMIT `+arg(limit+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e AuditEntry
			// The nullable envelope ids scan through untyped locals, then
			// widen to their kind — a NULL column stays a nil typed pointer.
			var passportID, onBehalfOf *ids.UUID
			var contentReadable bool
			if err := rows.Scan(&e.ID, &e.ActorType, &e.ActorID,
				&passportID, &onBehalfOf, &e.Action, &e.EntityType, &e.EntityID,
				&e.Before, &e.After, &e.AuthorizationRule, &e.Evidence, &e.OccurredAt,
				&e.ActorName, &e.OnBehalfOfName, &contentReadable); err != nil {
				return err
			}
			if !contentReadable {
				withholdAuditImage(&e)
			}
			if passportID != nil {
				v := ids.From[ids.PassportKind](*passportID)
				e.PassportID = &v
			}
			if onBehalfOf != nil {
				v := ids.From[ids.UserKind](*onBehalfOf)
				e.OnBehalfOf = &v
			}
			page.Entries = append(page.Entries, e)
		}
		return rows.Err()
	})
	if err != nil {
		return AuditPage{}, err
	}

	if len(page.Entries) > limit {
		page.Entries = page.Entries[:limit]
		last := page.Entries[len(page.Entries)-1]
		page.NextCursor = storekit.EncodeCursor(last.OccurredAt, last.ID)
		page.HasMore = true
	}
	return page, nil
}

// buildAuditWhere renders the filter into a parameterized WHERE clause and
// its ordered args. The keyset predicate matches the newest-first ORDER BY
// the caller appends. It leaves the trailing LIMIT arg to the caller so the
// same $-numbering stays contiguous.
//
// Every column is qualified with the audit row's `a` alias, which is required
// rather than tidy: the read joins app_user twice for the display names, and
// `id` alone would be ambiguous across those relations.
func buildAuditWhere(f AuditFilter) (string, []any, error) {
	where := "TRUE"
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if f.Actor != nil {
		where += " AND a.actor_id = " + arg(*f.Actor)
	}
	if f.EntityType != nil {
		where += " AND a.entity_type = " + arg(*f.EntityType)
	}
	if f.EntityID != nil {
		where += " AND a.entity_id = " + arg(*f.EntityID)
	}
	if f.Action != nil {
		where += " AND a.action = " + arg(*f.Action)
	}
	if f.From != nil {
		where += " AND a.occurred_at >= " + arg(*f.From)
	}
	if f.To != nil {
		where += " AND a.occurred_at <= " + arg(*f.To)
	}
	if f.Cursor != nil && *f.Cursor != "" {
		c, err := storekit.DecodeCursor(*f.Cursor)
		if err != nil {
			return "", nil, err
		}
		where += " AND (a.occurred_at, a.id) < (" + arg(c.CreatedAt) + ", " + arg(c.ID) + ")"
	}
	return where, args, nil
}
