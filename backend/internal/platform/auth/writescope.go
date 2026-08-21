// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

// Write authority over one row (A52/ADR-0039, UC-E11-08 E2). rowscope.go
// answers "which rows may this caller SEE"; this file answers the narrower
// question a mutation asks: "is this caller's own authority over the row
// write-level".
//
// The two differ in exactly one arm. A manual grant widens visibility whatever
// its access level — write satisfies read, so any live grant should let its
// holder open the record — but only `write` widens the authority to CHANGE it.
// The visibility predicate counts every live grant by design, so a mutation
// that gates on visibility alone accepts a read share as a licence to write,
// which is what the sharing screen tells the user it is not.
//
// Everything here is the second half of a pair. A write-authority probe is a
// NARROWING of a visibility probe, never a substitute for one: on its own it
// would answer "yes" for a row the caller cannot see at all (a non-shareable
// table, or a system principal), and it would answer ErrPermissionDenied where
// existence-hiding owes ErrNotFound. So the exported spellings below run the
// visibility probe first and keep its 404, then narrow with the write arm and
// answer ErrPermissionDenied — the caller has already been told the row is
// theirs to read, so there is nothing left for a 404 to hide.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// grantAccessWrite is the wider of record_grant's two access levels. Spelled
// once here because this file both compares against it and embeds it in SQL.
const grantAccessWrite = "write"

// EnsureWritable is EnsureVisible for a path that CHANGES the row: the caller
// must see it, and their authority over it must be write-level. It is the
// spelling every record mutation's row gate uses — update, archive, advance,
// promote, disqualify — so a `read` share cannot pass one.
//
// The two probes run as separate statements in the caller's transaction, and
// that is safe in the direction it has to be: both must pass, and each is a
// narrowing, so a share revoked between them can only turn an admission into a
// refusal. There is no interleaving that admits a caller neither statement
// alone would.
func EnsureWritable(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) error {
	if err := EnsureVisible(ctx, tx, table, id); err != nil {
		return err
	}
	return ensureWriteAuthority(ctx, tx, table, id)
}

// EnsureWritableLive is EnsureVisibleLive's write-authority twin — the row must
// exist, be live, and be the caller's to change. Everything EnsureVisibleLive's
// own comment says about why the live filter is load-bearing applies here
// unchanged; this adds only the grant-access arm.
func EnsureWritableLive(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) error {
	if err := EnsureVisibleLive(ctx, tx, table, id); err != nil {
		return err
	}
	return ensureWriteAuthority(ctx, tx, table, id)
}

// EnsureWritableForSubjectRights is EnsureVisibleForSubjectRights' write-authority
// twin, for the one subject-rights path that DESTROYS rather than reads: Art. 17
// erasure. The capture-privacy arm stays lifted for the reason that function
// gives — an unpromoted captured record is still held, and an erasure that
// silently spared it would be the defect — and the write arm is added on top,
// because a colleague handed a `read` share of a person is not thereby handed
// the authority to erase them.
//
// Its sibling, SAR assembly, deliberately does NOT use this: an export is a
// read, and read authority is the whole of what a read needs.
func EnsureWritableForSubjectRights(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) error {
	if err := EnsureVisibleForSubjectRights(ctx, tx, table, id); err != nil {
		return err
	}
	return ensureWriteAuthority(ctx, tx, table, id)
}

// WritableBy is VisibleTo's write-authority twin: it answers whether the
// caller could CHANGE the row, without erroring. The dedupe and merge paths
// ride it where the answer decides between absorbing an existing record and
// refusing with a bare conflict — a match the caller may read but not rewrite
// is not a match they may merge into, and the conflict must still be reported
// without disclosing the id.
func WritableBy(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) (bool, error) {
	err := EnsureWritable(ctx, tx, table, id)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, apperrors.ErrNotFound), errors.Is(err, apperrors.ErrPermissionDenied):
		return false, nil
	default:
		return false, err
	}
}

// EnsureCanGrant holds ADR-0039's scope-intersection rule — "a granter can never
// share wider than they hold" — and asks it at EVERY access level, because a
// grant is not only a width. It is also a TERM.
//
// A grant assertion is an upsert on
// (record_type, record_id, subject_type, subject_id) and it restates the whole
// grant: access, expiry, reason and granted_by all take the new request's
// values, which is deliberate and is what the contract documents. So a caller
// admitted at `read` does not merely pass on sight they hold — on an existing
// grant they rewrite its terms, including terms somebody else set. The width of
// the access column is the smaller half of what an assertion decides.
//
// The question is answered by the primitive every mutation rides: a caller who
// could not change the row themselves cannot hand that authority to somebody
// else, nor restate the terms on which somebody else handed it on.
//
// This is deliberately NOT "the caller has SOME claim on the row". On the tables
// every seat reads whole a read claim is universal and would admit everyone;
// write authority is owner- or grant-scoped even there, which is what makes the
// rule bite uniformly across the shareable set rather than only on the tables
// that happen to be row-scoped for reads.
func EnsureCanGrant(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) error {
	// The primitive rejects an unknown name itself, like every sibling here:
	// the record type reaching this comes from a request body, and its caller's
	// own allowlist is a second line rather than the only one.
	if !shareableTables[table] {
		return fmt.Errorf("auth: %q is not a shareable table", table)
	}
	return ensureWriteAuthority(ctx, tx, table, id)
}

// ensureWriteAuthority refuses a caller whose only authority over the row is a
// `read` share. It is unexported because it is not a probe on its own: it never
// asks whether the row is visible, or even whether it exists, so a caller that
// reached for it alone would be gating a mutation on a question with no row in
// it. Every exported spelling above pairs it with a visibility probe first.
//
// A non-shareable row-scoped table (a list, a saved view) answers nil: no grant
// can name one, so the visibility probe that ran first applied the owner scope
// and nothing else, and that scope IS the write authority. Saying so here keeps
// the callers uniform — a mutation asks for write authority whatever its table,
// rather than each call site deciding whether its table can be shared.
func ensureWriteAuthority(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) error {
	if !ownerScopedTables[table] {
		return fmt.Errorf("auth: %q is not a row-scoped table", table)
	}
	p, err := rbacActor(ctx)
	if err != nil {
		return err
	}
	if Unbounded(p) || !shareableTables[table] {
		return nil
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)
	clause := writeAuthorityPredicate(p, table, arg)

	var permitted bool
	if err := tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $%d AND %s)`, table, idPos, clause),
		args...).Scan(&permitted); err != nil {
		return err
	}
	if !permitted {
		return apperrors.ErrPermissionDenied
	}
	return nil
}

// writeAuthorityPredicate renders the write arm: own/team scope, or a live
// grant that says `write`.
//
// Capture privacy is absent on purpose, and its absence is not a widening. The
// visibility probe this always follows has already applied it, so re-applying
// it here could only refuse a row that probe admitted — which is precisely what
// the subject-rights path must not do, since it lifts capture privacy
// deliberately and for a reason Art. 17 states.
func writeAuthorityPredicate(p principal.Principal, table string, arg func(any) int) string {
	return writeAuthorityPredicateAs(p, table, table, arg)
}

// writeAuthorityPredicateAs renders the write arm against an aliased row —
// the spelling the activity link walk needs, where the record sits under a
// probe alias rather than its own table name.
func writeAuthorityPredicateAs(p principal.Principal, table, alias string, arg func(any) int) string {
	// An ownerless row is nobody's to change: it is claimed first
	// (EnsureClaimable), then written under the owner scope like any other.
	owner := ownerPredicate(p, arg, unownedIsNobodys)(alias)
	me, teams := arg(p.UserID), arg(p.TeamIDs)
	return fmt.Sprintf(`(%s OR EXISTS (
		   SELECT 1 FROM record_grant rg
		   WHERE rg.record_type = '%s' AND rg.record_id = %s.id
		     AND rg.access = '%s'
		     AND (rg.expires_at IS NULL OR rg.expires_at > now())
		     AND ((rg.subject_type = 'user' AND rg.subject_id = $%d)
		       OR (rg.subject_type = 'team' AND rg.subject_id = ANY($%d)))))`,
		owner, table, alias, grantAccessWrite, me, teams)
}

// EnsureActivityWritable is EnsureWritable for an activity, which has no
// owner_id of its own. The caller must READ it (the content gate — a limited
// conversation is nobody else's to edit), and their authority to CHANGE it is
// any of:
//
//   - they authored or captured it (captured_by names their user id);
//   - it is their task or their meeting (assignee_id / host_user_id);
//   - it is a link-less, workspace-shared note;
//   - at least one linked record is theirs to change — the same own/team
//     scope or `write` grant EnsureWritable takes on that record.
//
// Reads of customer identity are shared across the workspace, so the read
// gate alone would let every seat rewrite every colleague's correspondence;
// this is the arm that keeps activity writes team-shaped. An unbounded human
// edits every activity they can read, as they edit every record.
func EnsureActivityWritable(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	if err := EnsureActivityContentVisibleLive(ctx, tx, id); err != nil {
		return err
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
	idPos, me, author := arg(id), arg(p.UserID), arg("%:"+p.UserID.String())

	var permitted bool
	if err := tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT EXISTS (SELECT 1 FROM activity a WHERE a.id = $%[1]d AND (
		   a.captured_by LIKE $%[3]d
		   OR a.assignee_id = $%[2]d
		   OR a.host_user_id = $%[2]d
		   OR NOT EXISTS (SELECT 1 FROM activity_link l WHERE l.activity_id = a.id)
		   OR EXISTS (SELECT 1 FROM activity_link l WHERE l.activity_id = a.id AND %[4]s)))`,
		idPos, me, author, linkTargetWritable(p, "l", arg)), args...).Scan(&permitted); err != nil {
		return err
	}
	if !permitted {
		return apperrors.ErrPermissionDenied
	}
	return nil
}

// linkTargetWritable is linkTargetVisible's write twin: one arm per
// activity_link column, each asking whether the record it points at is the
// caller's to change.
func linkTargetWritable(p principal.Principal, alias string, arg func(any) int) string {
	arms := make([]string, 0, len(linkTargetTables))
	for _, t := range []struct{ column, table, probe string }{
		{"person_id", tablePerson, "wp"},
		{"organization_id", tableOrganization, "wo"},
		{"deal_id", tableDeal, "wd"},
		{"lead_id", tableLead, "wl"},
		{"project_id", tableProject, "wpr"},
	} {
		arms = append(arms, fmt.Sprintf(
			`(%[1]s.%[2]s IS NOT NULL AND EXISTS (SELECT 1 FROM %[3]s %[4]s WHERE %[4]s.id = %[1]s.%[2]s AND %[5]s))`,
			alias, t.column, t.table, t.probe, writeAuthorityPredicateAs(p, t.table, t.probe, arg)))
	}
	return "(" + strings.Join(arms, " OR ") + ")"
}

// EnsureClaimable is the gate in front of taking ownership of a row. A claim
// is permitted on a row the caller can see that is UNOWNED — the case the
// write arm refuses on purpose, so that somebody has to put their name on a
// record before changing it — or that is already theirs to change (a
// reassignment to oneself under existing write authority). A row owned by
// somebody else, with no write grant, answers ErrPermissionDenied: taking it
// over is what a share or an admin's reassignment is for.
func EnsureClaimable(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) error {
	if err := EnsureVisibleLive(ctx, tx, table, id); err != nil {
		return err
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
	clause := writeAuthorityPredicate(p, table, arg)

	var permitted bool
	if err := tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %[1]s WHERE id = $%[2]d AND (%[1]s.owner_id IS NULL OR %[3]s))`, table, idPos, clause),
		args...).Scan(&permitted); err != nil {
		return err
	}
	if !permitted {
		return apperrors.ErrPermissionDenied
	}
	return nil
}
