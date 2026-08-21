// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Manual per-record sharing (A52/ADR-0039): identity owns the grant
// rows because a grant IS access administration — platform/auth's
// visibility predicates read the table by SQL, never by import. A
// grant widens own/team base scope for exactly one record; revocation
// binds on the next query because the predicate evaluates live.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

var shareableRecordTypes = map[string]bool{
	"person": true, "organization": true, "deal": true, "lead": true, "project": true,
}

const grantColumns = `id, record_type, record_id, subject_type, subject_id, access, granted_by, reason, expires_at, created_at`

type grantRow struct {
	ID          ids.UUID
	RecordType  string
	RecordID    ids.UUID
	SubjectType string
	SubjectID   ids.UUID
	Access      string
	GrantedBy   ids.UUID
	Reason      *string
	ExpiresAt   *time.Time
	CreatedAt   time.Time
}

func scanGrant(r pgx.Row) (grantRow, error) {
	var g grantRow
	err := r.Scan(&g.ID, &g.RecordType, &g.RecordID, &g.SubjectType, &g.SubjectID,
		&g.Access, &g.GrantedBy, &g.Reason, &g.ExpiresAt, &g.CreatedAt)
	return g, err
}

type ListGrantsInput struct {
	RecordType  *string
	RecordID    *ids.UUID
	SubjectType *string
	SubjectID   *ids.UUID
}

func (s *Service) ListRecordGrants(ctx context.Context, in ListGrantsInput) ([]grantRow, error) {
	var out []grantRow
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		where := "(expires_at IS NULL OR expires_at > now())"
		if in.RecordType != nil {
			where += storekit.SQLf(" AND record_type = $%d", arg(*in.RecordType))
		}
		if in.RecordID != nil {
			where += storekit.SQLf(" AND record_id = $%d", arg(*in.RecordID))
		}
		if in.SubjectType != nil {
			where += storekit.SQLf(" AND subject_type = $%d", arg(*in.SubjectType))
		}
		if in.SubjectID != nil {
			where += storekit.SQLf(" AND subject_id = $%d", arg(*in.SubjectID))
		}
		rows, err := tx.Query(ctx,
			"SELECT "+grantColumns+" FROM record_grant WHERE "+where+" ORDER BY created_at DESC", args...)
		if err != nil {
			return err
		}
		var candidates []grantRow
		for rows.Next() {
			g, err := scanGrant(rows)
			if err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, g)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		// The visibility probe runs AFTER the cursor is drained and closed,
		// never inside the scan loop: it issues its own query on this same
		// transaction, and pgx refuses a second query while rows are open
		// ("conn busy"). The probe used to be a no-op for an unbounded
		// caller, which hid the collision until a caller whose row scope
		// renders a real clause came along.
		for _, g := range candidates {
			// A grant row names a row-scoped record: only grants whose
			// target the caller could read are disclosed.
			visible, err := auth.VisibleTo(ctx, tx, g.RecordType, g.RecordID)
			if err != nil {
				return err
			}
			if visible {
				out = append(out, g)
			}
		}
		return nil
	})
	return out, err
}

type CreateGrantInput struct {
	RecordType  string
	RecordID    ids.UUID
	SubjectType string
	SubjectID   ids.UUID
	Access      string
	Reason      *string
	ExpiresAt   *time.Time
}

func (s *Service) CreateRecordGrant(ctx context.Context, in CreateGrantInput) (grantRow, error) {
	// Both ids, and both before anything else: a grant is a triple of record,
	// subject and access, and each id is required by the contract — a claim only
	// this check makes true. Unguarded, a zero record_id reaches the row-scope
	// probe and a zero subject_id reaches the subject lookup, and each answers
	// not-found for something the caller never named. The record refusal comes
	// first because it is what the grant is ABOUT.
	if err := httperr.RequireBodyID("record_id", in.RecordID); err != nil {
		return grantRow{}, err
	}
	if err := httperr.RequireBodyID("subject_id", in.SubjectID); err != nil {
		return grantRow{}, err
	}
	if !shareableRecordTypes[in.RecordType] {
		return grantRow{}, &InvalidScopeError{Scope: "record_type " + in.RecordType}
	}
	// Sharing widens who sees the record — the grantor needs the
	// record's own write grant (the spec's manage_sharing permission,
	// ADR-0039, is not yet in this build's policy vocabulary).
	if err := auth.Require(ctx, in.RecordType, principal.ActionUpdate); err != nil {
		return grantRow{}, err
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return grantRow{}, errors.New("crmauth: only a human shares records directly; agents stage through the approval gate")
	}
	var out grantRow
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		// Scope-intersection: you can only share what you can see (H1
		// probe on the client-supplied record reference).
		if err := auth.EnsureLinkTarget(ctx, tx, in.RecordType, in.RecordID); err != nil {
			return err
		}
		subjectTable := "app_user"
		if in.SubjectType == "team" {
			subjectTable = "team"
		}
		var subjectExists bool
		if err := tx.QueryRow(ctx,
			storekit.SQLf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1)`, subjectTable),
			in.SubjectID).Scan(&subjectExists); err != nil {
			return err
		}
		if !subjectExists {
			return apperrors.ErrNotFound
		}
		// Two rules bind a grant and they judge different people.
		//
		// Scope-intersection (ADR-0039) judges the GRANTOR, at every access
		// level. EnsureLinkTarget above is satisfied by the grant arm, so a
		// caller whose only claim is a share passes IT — which is why the
		// authority question is asked separately here. It is asked whatever
		// access is asserted, because the upsert restates the whole grant: its
		// TERM as well as its width, so a `read` assertion rewrites an existing
		// grant's expiry, reason and access just as a `write` one does.
		//
		// The seat ceiling (AAD-AC-4) judges the RECIPIENT, and it reads the
		// asserted access — a `read` grant to a read seat is fine.
		//
		// Both bind a re-assert exactly as they bind a first share: the upsert
		// makes the second call a real write where the unique constraint used
		// to end it.
		if err := auth.EnsureCanGrant(ctx, tx, in.RecordType, in.RecordID); err != nil {
			return err
		}
		if err := refuseWriteGrantToReadSeat(ctx, tx, in); err != nil {
			return err
		}
		// The precondition read and the write it feeds must run under one lock:
		// FOR UPDATE cannot order two callers CREATING this grant at once,
		// because an absent row locks nothing, and the loser would then audit a
		// first share over a row it actually displaced.
		if err := storekit.LockWriteIdentity(ctx, tx, "record_grant", grantIdentity(in)); err != nil {
			return err
		}
		prior, replaced, err := replacedGrant(ctx, tx, in)
		if err != nil {
			return err
		}
		var before map[string]any
		if replaced {
			before = grantImage(prior)
		}
		// A grant is identified by its natural key, not by its id, so a
		// re-assert restates the SAME row: `expires_at` takes the proposed
		// value even when that value is NULL (the contract says a re-assert
		// resets it, and a COALESCE here would make an expiry unclearable), and
		// `granted_by` moves to the caller, who is accountable for the access
		// now in force.
		if out, err = scanGrant(tx.QueryRow(ctx, `
			INSERT INTO record_grant (record_type, record_id, subject_type, subject_id,
			                          access, granted_by, reason, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (record_type, record_id, subject_type, subject_id) DO UPDATE
			SET access     = EXCLUDED.access,
			    expires_at = EXCLUDED.expires_at,
			    reason     = EXCLUDED.reason,
			    granted_by = EXCLUDED.granted_by,
			    version    = record_grant.version + 1
			RETURNING `+grantColumns,
			in.RecordType, in.RecordID, in.SubjectType, in.SubjectID,
			in.Access, actor.UserID, in.Reason, in.ExpiresAt)); err != nil {
			return fmt.Errorf("upsert record_grant: %w", err)
		}
		// `record_share` in both directions, downgrades included: the contract
		// pins the verb, and `record_unshare` belongs to revocation. Which way
		// the access moved is the image pair's job to say, so both images
		// render the PERSISTED row through one shape — an image omitting a
		// field the upsert can change would report nothing moved when it did.
		if _, err := storekit.Audit(ctx, tx, "record_share",
			in.RecordType, in.RecordID, before, grantImage(out)); err != nil {
			return fmt.Errorf("audit record_share: %w", err)
		}
		return nil
	})
	return out, err
}

// grantIdentity renders the natural key the contract identifies a grant by —
// the same four columns the unique constraint and the upsert's conflict target
// use, so the lock and the row it protects can never key on different things.
func grantIdentity(in CreateGrantInput) string {
	return in.RecordType + ":" + in.RecordID.String() + ":" + in.SubjectType + ":" + in.SubjectID.String()
}

// replacedGrant reads the grant this assertion is about to displace, keyed the
// way the contract identifies one. `replaced` is false for a tuple never
// granted before, which is the audit's own distinction between a first share
// and a re-statement — it is not derivable from the row, because an absent
// grant and a grant with every field empty are different facts.
//
// It reads FOR UPDATE, which holds an existing row against a concurrent
// re-assert; the caller takes the write-identity lock first, which is what
// covers the case where there is no row to hold yet.
func replacedGrant(ctx context.Context, tx pgx.Tx, in CreateGrantInput) (prior grantRow, replaced bool, err error) {
	prior, err = scanGrant(tx.QueryRow(ctx, "SELECT "+grantColumns+` FROM record_grant
		WHERE record_type = $1 AND record_id = $2 AND subject_type = $3 AND subject_id = $4
		FOR UPDATE`,
		in.RecordType, in.RecordID, in.SubjectType, in.SubjectID))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return grantRow{}, false, nil
	case err != nil:
		return grantRow{}, false, fmt.Errorf("read the displaced record_grant: %w", err)
	}
	return prior, true, nil
}

// grantImage renders one audit image of a grant. Before and after share it so
// the pair diffs to exactly what a re-assert moved, which means it has to carry
// every field the upsert can change — `granted_by` included, because a
// re-assert moves accountability to the caller and the audit row's own actor
// records only the after side of that.
func grantImage(g grantRow) map[string]any {
	return map[string]any{
		"subject_type": g.SubjectType, "subject_id": g.SubjectID, "access": g.Access,
		"reason": g.Reason, "expires_at": g.ExpiresAt, "granted_by": g.GrantedBy,
	}
}

// refuseWriteGrantToReadSeat holds the receiving half of the seat ceiling
// (AAD-AC-4): a read seat may be handed a record to READ, which is exactly
// the authority its licence already carries, but never one to write.
//
// The granting half is enforced a layer up — sharing is a POST, so a
// read-seat grantor never reaches here — and it is not the same rule. A
// full-seat admin sharing a deal for editing with a read-seat colleague is
// the case this closes: the grant would have widened that colleague's row
// scope onto a record the seat ceiling then refuses every write to, which
// is a grant that reads as authority and cannot be exercised.
//
// A `team` subject is deliberately out of scope: a team is not a seat, and
// refusing a whole team because one member reads is a wider rule than the
// AC states. The read seats inside it are still refused every write at
// their own admission, so no authority leaks — the grant is just less
// useful to them than to their colleagues.
func refuseWriteGrantToReadSeat(ctx context.Context, tx pgx.Tx, in CreateGrantInput) error {
	if in.Access != string(crmcontracts.RecordGrantAccessWrite) || in.SubjectType == "team" {
		return nil
	}
	var seat string
	if err := tx.QueryRow(ctx,
		`SELECT seat_type FROM app_user WHERE id = $1`, in.SubjectID).Scan(&seat); err != nil {
		return fmt.Errorf("read the subject's seat: %w", err)
	}
	if !principal.SeatType(seat).CanMutate() {
		return fmt.Errorf("a write grant needs a full seat; the subject holds %q: %w",
			seat, apperrors.ErrSeatTierInsufficient)
	}
	return nil
}

// mayRevoke answers who may take a share away, and it is deliberately not the
// same question as who may see the record.
//
// Revoking is administration OF the sharing, so it wants the same authority
// asserting one does (ADR-0039's scope-intersection rule, EnsureCanGrant):
// otherwise anyone the record was ever shared with — read-only — could delete a
// colleague's `write` grant on it, which is not an escalation but is a way to
// take work away from people who are doing it. The write probe is what stops
// that, and it keeps the read half's 404 first, so a caller who cannot see the
// record still learns nothing from the shape of the refusal.
//
// The one arm that is NOT about authority over the record is a subject
// declining their own share. That was possible before this rule and stays
// possible: the grant names them, taking it away costs nobody anything, and
// nothing else in the product lets a person get out from under a share they did
// not ask for. A TEAM grant is not covered — its subject is the team, not the
// member reading it — so removing one is the record's business.
func mayRevoke(ctx context.Context, tx pgx.Tx, actor principal.Principal, grant grantRow) error {
	if grant.SubjectType == "user" && grant.SubjectID == actor.UserID {
		return nil
	}
	// Both gates sit INSIDE this branch, and that placement is the exception
	// working rather than a tidy-up. The object grant reads "may this role
	// change records of this kind" — a question declining your own share does
	// not ask, and one a read-only seat answers no to. Checking it before the
	// self arm would have left exactly the seat most likely to be handed a
	// share it did not want with no way to give it back.
	if err := auth.Require(ctx, grant.RecordType, principal.ActionUpdate); err != nil {
		return err
	}
	return auth.EnsureWritableLive(ctx, tx, grant.RecordType, grant.RecordID)
}

func (s *Service) RevokeRecordGrant(ctx context.Context, id ids.UUID) error {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return errors.New("crmauth: only a human revokes shares directly; agents stage through the approval gate")
	}
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		// FOR UPDATE for the reason the assertion path takes it: this row is
		// both the thing being deleted and the audit's before image, and a
		// concurrent re-assert would otherwise let the trail record an access
		// level that was already displaced by the time the DELETE ran.
		grant, err := scanGrant(tx.QueryRow(ctx,
			"SELECT "+grantColumns+" FROM record_grant WHERE id = $1 FOR UPDATE", id))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := mayRevoke(ctx, tx, actor, grant); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM record_grant WHERE id = $1`, id); err != nil {
			return err
		}
		// The same image the assertion path renders, so a record's timeline
		// carries one shape: a share and an unshare of the same grant differ in
		// which side is absent, not in which fields they thought worth naming.
		_, err = storekit.Audit(ctx, tx, "record_unshare",
			grant.RecordType, grant.RecordID, grantImage(grant), nil)
		return err
	})
}

func wireGrant(g grantRow) crmcontracts.RecordGrant {
	out := crmcontracts.RecordGrant{
		Id:          openapi_types.UUID(g.ID),
		RecordType:  crmcontracts.RecordGrantRecordType(g.RecordType),
		RecordId:    openapi_types.UUID(g.RecordID),
		SubjectType: crmcontracts.RecordGrantSubjectType(g.SubjectType),
		SubjectId:   openapi_types.UUID(g.SubjectID),
		Access:      crmcontracts.RecordGrantAccess(g.Access),
		GrantedBy:   openapi_types.UUID(g.GrantedBy),
		Reason:      g.Reason,
		ExpiresAt:   g.ExpiresAt,
		CreatedAt:   g.CreatedAt,
	}
	return out
}
