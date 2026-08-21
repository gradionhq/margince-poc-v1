// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

// Row-level scoping (B-EP03.3a, features/04 §1): the own/team/all
// visibility predicates, the capture-privacy and manual-grant arms that
// compose with them, and the single-row probes every store calls once its
// object gate in rbac.go has admitted the caller. Object admission answers
// "may this principal touch this KIND of record"; everything here answers
// "which ROWS", and answers a miss with ErrNotFound so existence is not
// disclosed.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Unbounded reports whether the actor sees every row of a permitted
// object: the system principal, or row_scope=all.
func Unbounded(p principal.Principal) bool {
	// A buyer is bounded by its Deal Room, which nothing in Permissions can
	// describe — so RowScopeAll on one is a claim the row-scope vocabulary
	// cannot honour, and reading it as "every row" would hand an external
	// visitor the estate.
	if p.Type == principal.PrincipalBuyer {
		return false
	}
	return p.Type == principal.PrincipalSystem || p.Permissions.RowScope == principal.RowScopeAll
}

// OwnerPredicate renders the own/team visibility test over one table's
// owner_id (qualified by alias when non-empty). It returns a FUNCTION so
// callers embedding the predicate for several tables (the activity link
// walk) register $me/$teams once and reuse the positions.
//
// The predicate is TOTAL: an actor who sees every row renders TRUE rather
// than nothing. Callers still skip the clause entirely via UnboundedFor
// where they can, but one that composes the predicate without asking
// first gets a widening arm instead of an accidental narrowing to `own` —
// row_scope=all matches no branch below.
func OwnerPredicate(p principal.Principal, arg func(any) int) func(alias string) string {
	return ownerPredicate(p, arg, unownedIsShared)
}

// unownedRows says what an ownerless row (owner_id IS NULL) is to the
// predicate. A READ treats it as shared: a row nobody owns is the
// workspace's to see. A WRITE does not: a row nobody owns is nobody's to
// change until somebody claims it — an ownerless customer record that every
// seat could rewrite is how two teams edit one company past each other.
type unownedRows bool

const (
	unownedIsShared  unownedRows = true
	unownedIsNobodys unownedRows = false
)

func ownerPredicate(p principal.Principal, arg func(any) int, unowned unownedRows) func(alias string) string {
	if Unbounded(p) {
		return func(string) string { return "TRUE" }
	}
	me := arg(p.UserID)
	col := func(alias string) string {
		if alias == "" {
			return "owner_id"
		}
		return alias + ".owner_id"
	}
	nullArm := ""
	if unowned == unownedIsShared {
		nullArm = "%[1]s IS NULL OR "
	}
	if p.Permissions.RowScope == principal.RowScopeTeam {
		teams := arg(p.TeamIDs)
		return func(alias string) string {
			return fmt.Sprintf(`(`+nullArm+`%[1]s = $%[2]d OR %[1]s IN (
			   SELECT tm.user_id FROM team_membership tm WHERE tm.team_id = ANY($%[3]d)))`,
				col(alias), me, teams)
		}
	}
	// own — and the zero value: an unresolved scope never widens.
	return func(alias string) string {
		return fmt.Sprintf(`(`+nullArm+`%[1]s = $%[2]d)`, col(alias), me)
	}
}

// shareableTables are the record types manual per-record grants can
// widen (A52/ADR-0039); grants on anything else cannot exist (the
// record_grant CHECK is the schema-side twin of this set).
var shareableTables = map[string]bool{
	tablePerson: true, tableOrganization: true, tableDeal: true, tableLead: true, tableProject: true,
}

// ownerPrivateTables carry capture privacy (migration 0095): a row is either
// 'workspace' — everyone in the workspace, the default — or 'owner', the
// capturing user's alone until a human edit or approval promotes it. Connector
// auto-create writes 'owner' (ADR-0063 §7), so this is the trust boundary
// around an unpromoted inbox: the contacts a mailbox sync invented are not yet
// the workspace's contacts.
//
// Capture privacy is a property of the ROW, not a scope tier, so it does
// NOT yield to row_scope=all. An admin reading a colleague's unpromoted
// captured contacts is precisely the disclosure the boundary exists to
// prevent (founder decision, 2026-07-31: the importing user only, not
// even Admin). Only the system principal — provisioning, the relay, the
// privacy engines — reads these tables unfiltered.
//
// project is deliberately NOT here, and its absence is enforced rather than
// assumed. 0131 gave the table a visibility column as part of the record
// vocabulary it copied, but nothing auto-creates a project — capture reads
// them and never inserts one — so 'owner' was a state no writer could reach.
// Migration 1787320003 narrowed the CHECK to 'workspace' so the schema can no
// longer hold what this map does not enforce.
// TestEveryTableThatCanHoldAnOwnerRowIsOwnerPrivate keeps the two in step: add
// 'owner' back to a table's CHECK and that test demands this map learn about
// it, so the pair cannot drift into a silent disclosure again.
var ownerPrivateTables = map[string]bool{tablePerson: true, tableOrganization: true}

// UnboundedFor reports whether the actor reads the named tables with NO
// predicate at all: an unbounded actor, or an identity table (tableclass.go)
// any human reads whole — both narrowed by capture privacy. Every read
// path that skips its row-scope clause asks THIS, not Unbounded, so that
// adding a visibility column to a table tightens every such path at once.
// Unbounded itself stays what it is — an admission test ("is this an
// all-scope human?") that several engines gate on.
func UnboundedFor(p principal.Principal, tables ...string) bool {
	if p.Type == principal.PrincipalSystem {
		return true
	}
	for _, table := range tables {
		if ownerPrivateTables[table] || !readsEveryRow(p, table) {
			return false
		}
	}
	return true
}

// ownerScopedTables is the closed set of table names the row-scope
// primitives interpolate into SQL. It is NOT every table carrying an
// owner_id column: fifteen carry one and nine are here, because on the
// other six that column names the MEASURED or ATTRIBUTED subject rather
// than an access owner. quota states the ruling in its own doc.go — its
// owner_id/team_id name who the quota is ABOUT, so it stays out of this
// set and anyone holding quota.read sees every target, which is the
// leaderboard read working as specified. Deriving this set from the
// column would add quota back and silently narrow that read: an
// availability regression wearing a security fix's clothes.
//
// TestTheOwnerScopedSetIsNotEveryOwnedTable pins both halves of that
// arithmetic, so the counts above cannot rot into a claim nobody checks.
//
// Several callers pass a table name derived from
// client input (link entity types, grant record types, search anchors);
// they allowlist at their own seam, but the primitive rejects unknown
// names itself so a new caller that forwards an unvalidated string is
// an error, never an injection.
var ownerScopedTables = map[string]bool{
	tablePerson: true, tableOrganization: true, tableDeal: true, tableLead: true, tableProject: true,
	"list": true, "saved_view": true, "automation": true, "voice_profile": true,
}

// VisiblePredicate is the FULL row-visibility test for one table, in
// three arms: capture privacy (an owner-private row answers to its owner
// alone), the own/team owner predicate, and a live manual grant to the
// caller or one of their teams (write satisfies read). This — not
// OwnerPredicate — is what every read path over a shareable table
// composes; both the visibility column and the grant evaluate LIVE, so
// promoting a captured record or revoking a share binds on the next query.
//
// The arms compose as (capture-private ? owner-only : scope) OR grant.
// An explicit share therefore still widens an owner-private row — sharing
// is a deliberate human disclosure by someone who could already read it,
// which is the same act that promotion is. Scope alone never widens one.
func VisiblePredicate(p principal.Principal, table string, arg func(any) int) func(alias string) string {
	return predicateFor(p, table, arg, withCapturePrivacy)
}

// capturePrivacy selects whether a rendered predicate enforces the
// visibility column. Every interactive read enforces it; only the
// subject-rights probe above lifts it, and it says why.
type capturePrivacy bool

const (
	withCapturePrivacy    capturePrivacy = true
	withoutCapturePrivacy capturePrivacy = false
)

func predicateFor(p principal.Principal, table string, arg func(any) int, capture capturePrivacy) func(alias string) string {
	// Customer identity is workspace-readable (tableclass.go): the own/team
	// arm is TRUE for every principal, and only capture privacy and a grant
	// can still say anything about the row. The owner predicate is not even
	// rendered for it — a registered parameter the SQL never names is a
	// Postgres error, not a no-op.
	scope := func(string) string { return "TRUE" }
	if !identityTables[table] {
		scope = OwnerPredicate(p, arg)
	}
	// The system principal is trusted by construction and reads both
	// arms away; an unbounded human still faces capture privacy.
	private := bool(capture) && ownerPrivateTables[table] && p.Type != principal.PrincipalSystem
	// An actor who reads every row needs no grant arm to see a shareable
	// row — unless capture privacy just took it away from them again.
	shareable := shareableTables[table] && (!readsEveryRow(p, table) || private)
	if !private && !shareable {
		return scope
	}
	me := arg(p.UserID)
	// The correlated subqueries below reference the OUTER row's columns;
	// an unqualified name would capture record_grant's own, so the table
	// name qualifies whenever no alias does.
	col := func(alias, name string) string {
		if alias != "" {
			return alias + "." + name
		}
		return table + "." + name
	}

	visible := scope
	if private {
		inner := visible
		visible = func(alias string) string {
			return fmt.Sprintf(`((%s <> 'owner' AND %s) OR %s = $%d)`,
				col(alias, "visibility"), inner(alias), col(alias, "owner_id"), me)
		}
	}
	if !shareable {
		return visible
	}
	teams := arg(p.TeamIDs)
	inner := visible
	return func(alias string) string {
		return fmt.Sprintf(`(%s OR EXISTS (
		   SELECT 1 FROM record_grant rg
		   WHERE rg.record_type = '%s' AND rg.record_id = %s
		     AND (rg.expires_at IS NULL OR rg.expires_at > now())
		     AND ((rg.subject_type = 'user' AND rg.subject_id = $%d)
		       OR (rg.subject_type = 'team' AND rg.subject_id = ANY($%d)))))`,
			inner(alias), table, col(alias, "id"), me, teams)
	}
}

// ScopeClause renders the own/team/all row-visibility predicate over an
// owner_id column (B-EP03.3a). arg registers a query argument and
// returns its 1-based position, matching the list builders' convention.
// An empty clause means unbounded (row_scope=all, or the system actor).
// Ownerless rows (owner_id IS NULL) are workspace-shared and visible at
// every tier.
func ScopeClause(ctx context.Context, arg func(any) int) (string, error) {
	p, err := rbacActor(ctx)
	if err != nil {
		return "", err
	}
	if Unbounded(p) {
		return "", nil
	}
	return OwnerPredicate(p, arg)(""), nil
}

// ScopeClauseFor renders the full visibility predicate (owner scope OR
// live record grant) for one named table with an alias — the spelling
// every list/search/report path over a shareable table uses.
func ScopeClauseFor(ctx context.Context, table, alias string, arg func(any) int) (string, error) {
	if !ownerScopedTables[table] {
		return "", fmt.Errorf("auth: %q is not a row-scoped table", table)
	}
	p, err := rbacActor(ctx)
	if err != nil {
		return "", err
	}
	if UnboundedFor(p, table) {
		return "", nil
	}
	return VisiblePredicate(p, table, arg)(alias), nil
}

// EnsureVisibleLive is the strict row probe: the row must EXIST, be LIVE
// (archived_at IS NULL) and pass the caller's row scope. It differs from
// EnsureVisible in both halves that matter to a caller handing data back —
// an unbounded actor does not skip the existence check, and a soft-deleted
// row never passes.
//
// Both differences are load-bearing where a record is served or referenced
// outside the store that owns it. Art. 17 erasure anonymizes a person in
// place and stamps archived_at while LEAVING owner_id alone, so the
// tombstone still satisfies the original owner's predicate: a probe without
// the live filter answers "yes, still yours" for a record every live read
// path now refuses.
func EnsureVisibleLive(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) error {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)

	clause, err := ScopeClauseFor(ctx, table, "", arg)
	if err != nil {
		return err
	}
	q := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $%d AND archived_at IS NULL`, table, idPos)
	if clause != "" {
		q += " AND " + clause
	}
	q += ")"

	var visible bool
	if err := tx.QueryRow(ctx, q, args...).Scan(&visible); err != nil {
		return err
	}
	if !visible {
		return apperrors.ErrNotFound
	}
	return nil
}

// EnsureVisibleForSubjectRights is EnsureVisible for the GDPR engines:
// it applies the caller's own/team row scope exactly like EnsureVisible,
// but does NOT apply capture privacy. Articles 15 and 17 owe the data
// subject everything the controller holds about them, and an unpromoted
// captured record is still held — a SAR that silently omitted it, or an
// erasure that silently spared it, would be the defect. The crossing is
// authorized by the stronger object gate every caller here passes first
// (person.delete, the same trust level erasure needs) plus, on the SAR
// path, an explicit unbounded-scope check.
//
// It deliberately does not widen the OWNER scope: a rep with person.delete
// still cannot erase a colleague's person. Only the capture-privacy arm
// is lifted, so the caller sees exactly what their scope tier holds.
func EnsureVisibleForSubjectRights(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) error {
	if !ownerScopedTables[table] {
		return fmt.Errorf("auth: %q is not a row-scoped table", table)
	}
	p, err := rbacActor(ctx)
	if err != nil {
		return err
	}
	if Unbounded(p) {
		return nil
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)
	clause := predicateFor(p, table, arg, withoutCapturePrivacy)("")

	var visible bool
	err = tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $%d AND %s)`, table, idPos, clause),
		args...).Scan(&visible)
	if err != nil {
		return err
	}
	if !visible {
		return apperrors.ErrNotFound
	}
	return nil
}

// EnsureLinkTarget verifies an activity link's target row exists AND is
// visible to the caller — an explicit row-scope probe, because the FK that
// would otherwise catch a bad id is checked as the table owner and carries
// no row scope at all: it accepts any id that exists, so without this a
// guessed foreign UUID would persist a link to a record the caller may not
// read. A link to an archived record is equally refused: the link would
// outlive the row it names.
func EnsureLinkTarget(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) error {
	return EnsureVisibleLive(ctx, tx, table, id)
}

// VisibleTo probes whether one row passes the caller's row scope WITHOUT
// erroring — for the dedupe pre-checks, which must answer 409 either way
// but may only disclose the existing row's id when the caller could read
// it (existence-hiding must survive the conflict path).
func VisibleTo(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) (bool, error) {
	err := EnsureVisible(ctx, tx, table, id)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, apperrors.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// EnsureVisible applies the row scope to a single-row operation: get,
// update, archive, advance. Out of scope reads as ErrNotFound — the
// caller cannot distinguish "not yours" from "not there", by design.
// Activities scope through their links (the activities module's
// link-walk clause); pipelines have no owner and are governed by object
// grants only.
func EnsureVisible(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) error {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)

	clause, err := ScopeClauseFor(ctx, table, "", arg)
	if err != nil {
		return err
	}
	if clause == "" {
		return nil
	}

	var visible bool
	err = tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $%d AND %s)`, table, idPos, clause),
		args...).Scan(&visible)
	if err != nil {
		return err
	}
	if !visible {
		return apperrors.ErrNotFound
	}
	return nil
}

// VisibleSubset answers, in ONE statement, which of the given rows of a
// row-scoped table the caller may SEE — the question EnsureVisible asks of one
// row, asked of a whole page at once. It is the read-only twin of
// WritableSubset (fieldmask.go) and exists for the same reason: a projection
// that carries references to OTHER records must withhold the ones its reader
// could not open, and a probe per reference is a round trip per row.
//
// Absent from the answer means withheld, and a row that does not exist is
// absent too — which is what existence-hiding wants a caller to be unable to
// tell apart.
//
// It asks EnsureVisible's question, not EnsureVisibleLive's: an archived
// record is still readable through the archived filter, so naming one is not a
// disclosure. A caller that must not name a soft-deleted row wants the strict
// probe per row and its ErrNotFound.
//
// The OBJECT grant is checked first, and it has to be: row scope answers which
// rows of a table a seat reads, never whether they may read that table at all.
// A seat holding no `project.read` must not learn a project id from a deal it
// is entitled to, and while a project was row-scoped the owner predicate hid
// that by accident. Every caller of the single-row EnsureVisible reaches it
// through a store entry point that called auth.Require first; a subset caller
// is projecting a FOREIGN table's ids and has made no such check, so it is
// made here.
func VisibleSubset(ctx context.Context, tx pgx.Tx, table string, rowIDs []ids.UUID) (map[ids.UUID]bool, error) {
	if !ownerScopedTables[table] {
		return nil, fmt.Errorf("auth: %q is not a row-scoped table", table)
	}
	p, err := rbacActor(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[ids.UUID]bool, len(rowIDs))
	// No grant on the referenced type: every id is withheld, and the caller
	// cannot tell that from the rows simply not being there.
	if err := Require(ctx, table, principal.ActionRead); err != nil {
		if errors.Is(err, apperrors.ErrPermissionDenied) {
			return out, nil
		}
		return nil, err
	}
	// The caller reads this table with no predicate at all. EnsureVisible
	// returns nil for each of these without issuing a query, and so does this.
	if UnboundedFor(p, table) {
		for _, id := range rowIDs {
			out[id] = true
		}
		return out, nil
	}
	if len(rowIDs) == 0 {
		return out, nil
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idsPos := arg(rowIDs)
	// The clause is rendered into its own variable first: it registers
	// parameters through arg, and Go does not fix the order in which a call's
	// operands are evaluated against `args` passed to the same call.
	clause := VisiblePredicate(p, table, arg)("")
	rows, err := tx.Query(ctx,
		fmt.Sprintf(`SELECT id FROM %[1]s WHERE id = ANY($%[2]d) AND %[3]s`, table, idsPos, clause), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id ids.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
