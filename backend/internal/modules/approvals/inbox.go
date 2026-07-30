// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The inbox read side: the store row shape and the List/Get queries.
// Every read here runs through decidable (authority.go), so triage
// visibility and the decision gate can never drift apart.

package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// row is the store shape of one approval.
type row struct {
	ID         ids.ApprovalID
	Kind       string
	Status     string
	ProposedBy string
	OnBehalfOf *ids.UserID
	PassportID *ids.PassportID
	// TargetType + TargetID are the polymorphic pointer to the entity the
	// staging acts on (deal, org, person, lead, activity, …); the id stays
	// untyped because the pair IS the discriminated reference.
	TargetType     *string
	TargetID       *ids.UUID
	TargetVersion  *int64
	Summary        *string
	ProposedChange json.RawMessage
	DiffHash       string
	ExpiresAt      time.Time
	DecidedBy      *ids.UserID
	DecidedAt      *time.Time
	ConsumedAt     *time.Time
	CreatedAt      time.Time
}

const columns = `id, kind, status, proposed_by, on_behalf_of, passport_id,
	target_entity_type, target_entity_id, target_version, summary,
	proposed_change, diff_hash, expires_at, decided_by, decided_at, consumed_at, created_at`

func scan(r pgx.Row) (row, error) {
	var a row
	err := r.Scan(&a.ID, &a.Kind, &a.Status, &a.ProposedBy, &a.OnBehalfOf, &a.PassportID,
		&a.TargetType, &a.TargetID, &a.TargetVersion, &a.Summary,
		&a.ProposedChange, &a.DiffHash, &a.ExpiresAt, &a.DecidedBy, &a.DecidedAt, &a.ConsumedAt, &a.CreatedAt)
	return a, err
}

// effectiveStatus folds lazy expiry in: a pending row past its expiry
// reads as expired everywhere without a sweeper process.
func (a row) effectiveStatus(now time.Time) string {
	if a.Status == statusPending && now.After(a.ExpiresAt) {
		return "expired"
	}
	return a.Status
}

// inboxBatch is the scan window List filters per round trip; List keeps
// paging until the display limit is met or the table is exhausted, so a
// burst of undecidable stagings can never starve older visible rows out
// of a caller's inbox.
const inboxBatch = 200

// PendingScanCap bounds how many staged rows PendingForTarget scans for one
// record. Decidability is a per-row probe, so an unbounded target would make
// a single record page pay for the whole backlog. A caller that counts must
// read a full result as "this many or more" rather than as an exact total.
const PendingScanCap = inboxBatch

// statusPending is the status column value a staged, undecided row carries.
const statusPending = "pending"

// ListInput narrows an inbox read. Status and Kind filter the staged rows
// themselves; TargetType + TargetID scope the question to ONE record and are
// a pair — a type alone would match every record of that type and an id alone
// every type of that id, so the transport refuses a half-reference and one
// never reaches here.
type ListInput struct {
	Status     *string
	Kind       *string
	TargetType *string
	TargetID   *ids.UUID
	Limit      int
}

// targeted reports whether the read is scoped to one record.
func (in ListInput) targeted() bool { return in.TargetType != nil && in.TargetID != nil }

// List returns the inbox, newest first — but only the approvals the caller
// could themselves decide. Deciding is human work, and so is triage: an
// agent cannot browse the queue of withheld authority, and neither can a
// human who lacks the grant the staged effect needs or cannot see the
// target row under their own/team scope. Without this filter the inbox is
// a workspace-wide side channel that leaks proposed_change, target ids,
// and diffs to any low-privilege user (C3/ADR-0036).
//
// The bool is has_more: whether rows the caller could decide were left
// unreturned. A record page can carry dozens of stagings — one deep site read
// stages a proposal per person it found — so a client that filtered to one
// record has to be able to tell a full page from a complete answer.
func (s *Service) List(ctx context.Context, in ListInput) ([]row, bool, error) {
	if err := humanOnly(ctx); err != nil {
		return nil, false, err
	}
	p, _ := principal.Actor(ctx)
	if in.Limit <= 0 || in.Limit > inboxBatch {
		in.Limit = 50
	}
	var out []row
	var more bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		if in.targeted() {
			out, more, err = listForTarget(ctx, tx, p, in)
			return err
		}
		out, more, err = scanInbox(ctx, tx, p, in)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return out, more, nil
}

// scanInbox walks the whole table newest-first and filters each keyset batch
// through the per-row decidability probe.
//
// Decidability is role/target/row-scope-shaped, not expressible as one WHERE
// without joining every object grant — so it runs in memory, and the scan
// pages rather than taking one wide LIMIT: a burst of undecidable stagings
// must never starve older visible rows out of a caller's inbox.
//
// It fills one row PAST the display limit so has_more is a fact rather than a
// guess; that row is then dropped, and its existence is what the flag reports.
func scanInbox(ctx context.Context, tx pgx.Tx, p principal.Principal, in ListInput) ([]row, bool, error) {
	decide := func(a row) (bool, error) { return decidable(ctx, tx, p, a) }
	var out []row
	var afterCreated *time.Time
	var afterID *ids.ApprovalID
	for {
		q, args := approvalPageQuery(in, afterCreated, afterID, inboxBatch)
		batch, err := collect(ctx, tx, q, args)
		if err != nil {
			return nil, false, err
		}
		var full bool
		out, full, err = appendDecidable(batch, out, in.Limit+1, decide)
		if err != nil {
			return nil, false, err
		}
		if full || len(batch) < inboxBatch {
			break // a row past the display limit is in hand, or the table is exhausted
		}
		last := batch[len(batch)-1]
		afterCreated, afterID = &last.CreatedAt, &last.ID
	}
	return capPage(out, in.Limit, false)
}

// listForTarget answers the inbox scoped to ONE record.
//
// Every row shares that target, so the target-visibility half of decidable is
// asked ONCE for the record rather than once per row — the inbox's per-row
// probe exists only because its rows point at different records. The per-kind
// grant check still varies by row and stays in the loop.
//
// A target outside the caller's row scope answers an EMPTY list, never a
// refusal: nothing staged against a record they cannot see is decidable, and
// saying so is the same existence-hiding answer the record's own read gives.
//
// The scan is bounded at PendingScanCap, so a full scan is also a reason to
// report has_more: past the cap this read cannot tell a client it has seen
// everything, and claiming otherwise is the lie the flag exists to prevent.
func listForTarget(ctx context.Context, tx pgx.Tx, p principal.Principal, in ListInput) ([]row, bool, error) {
	visible, err := targetVisible(ctx, tx, in.TargetType, in.TargetID)
	if err != nil {
		return nil, false, err
	}
	if !visible {
		return []row{}, false, nil
	}
	q, args := approvalPageQuery(in, nil, nil, PendingScanCap)
	batch, err := collect(ctx, tx, q, args)
	if err != nil {
		return nil, false, err
	}
	granted := func(a row) (bool, error) { return requireDecisionGrants(p, a) == nil, nil }
	out, _, err := appendDecidable(batch, nil, in.Limit+1, granted)
	if err != nil {
		return nil, false, err
	}
	return capPage(out, in.Limit, len(batch) == PendingScanCap)
}

// capPage cuts a filled-one-past result back to the display limit and reports
// has_more. beyondScan is the other reason there may be more: a read whose
// scan hit its own cap has not seen the whole backlog either.
func capPage(out []row, limit int, beyondScan bool) ([]row, bool, error) {
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, beyondScan, nil
}

// approvalWhere is the ONE spelling of "which staged rows this read wants":
// the caller's filters, plus the keyset cursor of the previous batch when the
// scan is paging. Every read of the approval table renders its predicate here,
// so a filter added to the surface reaches the inbox, the target-scoped list
// and the record page together instead of drifting between copies.
func approvalWhere(in ListInput, afterCreated *time.Time, afterID *ids.ApprovalID, arg func(any) int) string {
	var terms []string
	if in.Status != nil {
		terms = append(terms, fmt.Sprintf("status = $%d", arg(*in.Status)))
	}
	if in.Kind != nil {
		terms = append(terms, fmt.Sprintf("kind = $%d", arg(*in.Kind)))
	}
	if in.TargetType != nil {
		terms = append(terms, fmt.Sprintf("target_entity_type = $%d", arg(*in.TargetType)))
	}
	if in.TargetID != nil {
		terms = append(terms, fmt.Sprintf("target_entity_id = $%d", arg(*in.TargetID)))
	}
	if afterCreated != nil {
		terms = append(terms, fmt.Sprintf("(created_at, id) < ($%d, $%d)", arg(*afterCreated), arg(*afterID)))
	}
	if len(terms) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(terms, " AND ")
}

// approvalPageQuery is one newest-first page of the scan under those filters,
// bounded by the caller's scan window.
func approvalPageQuery(in ListInput, afterCreated *time.Time, afterID *ids.ApprovalID, scan int) (string, []any) {
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }
	where := approvalWhere(in, afterCreated, afterID, arg)
	return fmt.Sprintf(`SELECT %s FROM approval%s ORDER BY created_at DESC, id DESC LIMIT %d`,
		columns, where, scan), args
}

// appendDecidable filters one scanned batch through a visibility probe and
// appends the rows that pass, stopping the moment limit is met (full = true)
// so a burst of undecidable stagings cannot starve older visible rows out of
// the caller's inbox.
//
// The probe is a parameter because the two readers differ in exactly one half:
// the inbox asks the whole decidable predicate per row, while a target-scoped
// read has already established that one target's visibility for every row and
// asks only the per-kind grants.
func appendDecidable(batch, out []row, limit int, visible func(row) (bool, error)) ([]row, bool, error) {
	for i := range batch {
		a := batch[i]
		ok, err := visible(a)
		if err != nil {
			return out, false, err
		}
		if !ok {
			continue
		}
		out = append(out, a)
		if len(out) >= limit {
			return out, true, nil
		}
	}
	return out, false, nil
}

// collect materializes one query's rows (the row-scope probes inside the
// filter loop need the connection, so the cursor cannot stay open).
func collect(ctx context.Context, tx pgx.Tx, q string, args []any) ([]row, error) {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PendingForTarget returns the pending approvals staged against ONE record,
// filtered by the same decidability rule the inbox uses — a record page must
// never become the side channel List refuses to be. It takes the caller's
// transaction so a composite record read assembles every section at one
// instant instead of opening a second connection for this one.
//
// It answers the wire shape rather than the store row: the caller is another
// package, and re-deriving the effective status (lazy expiry) outside this
// module is exactly the drift the type keeps unexported to prevent.
//
// Every row here shares ONE target, so the target-visibility half of
// decidable is asked once for the record rather than once per row — the
// inbox's per-row probe exists because its rows point at different records.
// The per-kind grant check still varies by row and stays in the loop.
//
// The scan is bounded at PendingScanCap rows so one record page cannot pay
// for an unbounded backlog. A caller counting rather than listing must treat
// a full result as "this many or more"; limit bounds the RETURNED rows, so
// pass PendingScanCap to mean "everything the scan found".
func (s *Service) PendingForTarget(ctx context.Context, tx pgx.Tx, targetType string, targetID ids.UUID, limit int) ([]crmcontracts.Approval, error) {
	if err := humanOnly(ctx); err != nil {
		return nil, err
	}
	p, _ := principal.Actor(ctx)
	if limit <= 0 || limit > PendingScanCap {
		limit = PendingScanCap
	}
	visible, err := targetVisible(ctx, tx, &targetType, &targetID)
	if err != nil {
		return nil, err
	}
	if !visible {
		// The record itself is outside the caller's row scope, so nothing
		// staged against it is decidable — and saying so is the same
		// existence-hiding answer the record read gives.
		return []crmcontracts.Approval{}, nil
	}
	now := s.now()
	pending := statusPending
	q, args := approvalPageQuery(ListInput{
		Status: &pending, TargetType: &targetType, TargetID: &targetID,
	}, nil, nil, PendingScanCap)
	batch, err := collect(ctx, tx, q, args)
	if err != nil {
		return nil, err
	}
	out := make([]crmcontracts.Approval, 0, len(batch))
	for i := range batch {
		a := batch[i]
		if a.effectiveStatus(now) != statusPending {
			// Lazy expiry: a row past its expiry is not a decision anyone
			// still owes, so it must not appear as one on the record page.
			continue
		}
		if requireDecisionGrants(p, a) != nil {
			continue
		}
		out = append(out, wire(a, now))
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, id ids.ApprovalID) (row, error) {
	if err := humanOnly(ctx); err != nil {
		return row{}, err
	}
	p, _ := principal.Actor(ctx)
	var a row
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		a, err = get(ctx, tx, id)
		if err != nil {
			return err
		}
		// An approval the caller could not decide reads as absent — the
		// same existence-hiding the row-scope convention uses, so Get never
		// becomes a lookup oracle for out-of-scope proposed changes (C3),
		// whether the gap is a missing grant or a target row outside the
		// caller's row scope.
		visible, err := decidable(ctx, tx, p, a)
		if err != nil {
			return err
		}
		if !visible {
			return apperrors.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return row{}, err
	}
	return a, nil
}

func get(ctx context.Context, tx pgx.Tx, id ids.ApprovalID) (row, error) {
	a, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM approval WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return row{}, apperrors.ErrNotFound
	}
	return a, err
}
