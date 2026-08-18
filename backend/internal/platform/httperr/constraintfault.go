// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httperr

// The net under every per-path validation: a constraint the DATABASE enforced
// that no handler translated on its way out.
//
// Separate from httperr.go because it answers a different question. That file
// owns the taxonomy — which sentinel means which status — and this one owns a
// single fallback rule: a constraint breach is the caller's input to fix, so it
// must never leave as the 500 whose advice is "retry".

import (
	"errors"
	"net"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// constraintFault answers a foreign-key or CHECK violation that reached the
// transport untranslated.
//
// It exists because the alternative is a 500 telling the caller to retry, and a
// constraint breach is deterministic: the same call fails the same way forever.
// An agent following that advice burns its attempts and then escalates to a
// human for a mistake it could have fixed. storekit's own doc already states the
// rule this enforces — "a CHECK is a business rule, and a business-rule breach is
// never a server fault" — and this is the net that makes it true for the paths
// that forgot.
//
// It names no field, because at this depth the only thing that knows one is the
// CONSTRAINT NAME, and that is schema: `organization_owner_id_fkey` tells a
// caller our table and column names. A path that can name the field should
// refuse before the database does, the way checkLifecycle and checkSizeBand do —
// this answers the ones that do not, and the constraint goes to the operator's
// log through InfraCause instead.
func constraintFault(err error) (Fault, bool) {
	if fault, ok := retentionHoldFault(err); ok {
		return fault, true
	}
	switch {
	case storekit.IsForeignKeyViolation(err):
		return Fault{
			Status: http.StatusUnprocessableEntity, Code: "reference_not_found",
			Detail:     referenceNotFoundDetail(err),
			InfraCause: err,
		}, true
	case isConstrainedValue(err):
		return Fault{
			Status: http.StatusUnprocessableEntity, Code: "value_not_allowed",
			Detail: "a value in this request is outside what its field accepts. Check each value against " +
				"this operation's schema; do not retry unchanged.",
			InfraCause: err,
		}, true
	default:
		return Fault{}, false
	}
}

// activityRestrictedImmutable is the constraint name the data-layer guard
// raises for any write to a held activity (core migration 0289's trigger).
// Named here so the ONE guard maps to the ONE sentinel for every writer —
// an update, a delete, a relink — rather than each store translating it.
const activityRestrictedImmutable = "activity_restricted_immutable"

// retentionHoldUntil reads the deadline out of the guard's own message
// ("… restricted under a statutory retention obligation until <instant>"),
// which is the only place the refusing statement carries it.
//
// It matches a TIMESTAMP rather than "everything after until", so a reworded
// trigger message yields no deadline instead of forwarding a fragment of
// database text to a client. TestRetentionHoldIsLockedNotValueNotAllowed
// pins the shape the migrations actually raise.
var retentionHoldUntil = regexp.MustCompile(`\buntil (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?[+-]\d{2}(?::?\d{2})?)`)

// retentionHoldFault answers the guard's refusal as ErrRetentionHold (423): a
// business rule like the other CHECKs, but not one the caller's input can fix
// — nothing changes until the recorded date, so it must not read as
// value_not_allowed with advice to fix a value. The deadline rides Details as
// `retain_until` (interfaces.md §0) so the refusal states when it lifts.
func retentionHoldFault(err error) (Fault, bool) {
	constraint, ok := storekit.CheckViolation(err)
	if !ok || constraint != activityRestrictedImmutable {
		return Fault{}, false
	}
	fault := Fault{
		Status: http.StatusLocked, Code: "locked",
		Detail: apperrors.ErrRetentionHold.Error() + " and cannot be changed or deleted until its window " +
			"closes. Do not retry: an administrator holding the retention authority may release it, which is its own audited operation.",
		InfraCause: err,
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if m := retentionHoldUntil.FindStringSubmatch(pgErr.Message); m != nil {
			fault.Details = map[string]any{"retain_until": m[1]}
		}
	}
	return fault, true
}

// isConstrainedValue covers the two ways the schema refuses a VALUE: a CHECK on
// what the column may hold, and an EXCLUDE on what it may hold at the same time
// as another row. Both are the caller's input to fix.
func isConstrainedValue(err error) bool {
	if _, ok := storekit.CheckViolation(err); ok {
		return true
	}
	_, ok := storekit.ExclusionViolation(err)
	return ok
}

// infrastructureCause reports whether err's chain contains a raw
// infrastructure failure (Postgres, network) whose message is meant for
// operators, not clients.
func infrastructureCause(err error) bool {
	var pgErr *pgconn.PgError
	var netErr net.Error
	return errors.As(err, &pgErr) || errors.As(err, &netErr)
}

// referenceNotFoundDetail names the FIELD whose id pointed nowhere, when the
// violated constraint says which.
//
// The first version of this message said only "a value in this request names a
// record that does not exist here; check the ids you sent against records this
// workspace actually has". A UAT agent took that advice literally and could not
// act on it twice over: the request carried two ids (the path's and the
// patch's) and it could not tell which was blamed, and the one that was blamed —
// `owner_id` — references a USER, which no tool on this surface enumerates. It
// then sent a person id that genuinely exists and got byte-identical text back.
// Advice that cannot be followed is worse than none: it reads as a transient
// problem and invites the retry the rest of the sentence forbids.
//
// So the field is named where the constraint yields it, and the sentence no
// longer promises that searching this workspace's records will find the answer.
func referenceNotFoundDetail(err error) string {
	if field, ok := storekit.ForeignKeyColumn(err); ok {
		return "`" + field + "` names no record of the kind it references (an owner is a user, a parent " +
			"an organization). Send an id of the right kind; do not retry unchanged."
	}
	return "an id in this request names no record of the kind its field references. Check each id " +
		"against the kind its field expects; do not retry unchanged."
}
