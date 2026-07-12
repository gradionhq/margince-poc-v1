// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package quotas

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// Store owns the quota table (data-seam ownership, ADR-0014 Am.1); every
// mutation rides the storekit audit shape in one transaction.
type Store struct {
	pool *pgxpool.Pool
	// now is the store's clock: the attainment read's as-of instant,
	// pace window, and FX as-of day all evaluate at it, so a pinned test
	// reads the same moment it seeded against.
	now func() time.Time
}

// NewStore wires the store over the RLS-bound app pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return NewStoreWithClock(pool, time.Now)
}

// NewStoreWithClock is NewStore with an explicit clock (the
// ratelimit.NewWithClock precedent) — the attainment suites pin it.
func NewStoreWithClock(pool *pgxpool.Pool, now func() time.Time) *Store {
	return &Store{pool: pool, now: now}
}

func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return database.WithWorkspaceTx(ctx, s.pool, fn)
}

// OwnerXorTeamError is RD-DDL-2's refusal: a quota state with both
// owner_id and team_id set, or neither. The transport maps it to the
// contract's 422 validation_error with the distinct
// owner_xor_team_required details.errors[].code.
type OwnerXorTeamError struct{}

func (*OwnerXorTeamError) Error() string {
	return "exactly one of owner_id and team_id must be set"
}

// ownerXorTeam mirrors the quota_owner_xor_team CHECK in Go, so both
// create and the update's MERGED state are refused before the row is
// ever written.
func ownerXorTeam(owner, team *ids.UUID) bool {
	return (owner != nil) != (team != nil)
}

// CreateQuotaInput is a new quota: exactly one of OwnerID/TeamID names
// the measured subject, and TargetMinor is always the human's number
// (RD-PARAM-3 — the transport never defaults or derives it).
type CreateQuotaInput struct {
	OwnerID     *ids.UUID
	TeamID      *ids.UUID
	PeriodStart time.Time
	PeriodEnd   time.Time
	TargetMinor int64
	Currency    string
}

// CreateQuota inserts the quota after refusing an invalid owner-XOR-team
// state — the refusal happens before the INSERT, so nothing (row, audit)
// is ever written for it.
func (s *Store) CreateQuota(ctx context.Context, in CreateQuotaInput) (crmcontracts.Quota, error) {
	if err := auth.Require(ctx, "quota", principal.ActionCreate); err != nil {
		return crmcontracts.Quota{}, err
	}
	if !ownerXorTeam(in.OwnerID, in.TeamID) {
		return crmcontracts.Quota{}, &OwnerXorTeamError{}
	}
	var out crmcontracts.Quota
	err := s.tx(ctx, func(tx pgx.Tx) error {
		id := ids.NewV7()
		_, err := tx.Exec(ctx,
			`INSERT INTO quota (id, workspace_id, owner_id, team_id, period_start, period_end, target_minor, currency)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, storekit.MustWorkspace(ctx), in.OwnerID, in.TeamID,
			in.PeriodStart, in.PeriodEnd, in.TargetMinor, in.Currency)
		if err != nil {
			return mapQuotaWriteError(err, "insert quota")
		}
		if _, err := storekit.Audit(ctx, tx, "create", "quota", id, nil, map[string]any{
			"owner_id":     in.OwnerID,
			"team_id":      in.TeamID,
			"period_start": in.PeriodStart.Format(time.DateOnly),
			"period_end":   in.PeriodEnd.Format(time.DateOnly),
			"target_minor": in.TargetMinor,
			"currency":     in.Currency,
		}); err != nil {
			return fmt.Errorf("audit quota create: %w", err)
		}
		if out, err = readQuota(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read created quota: %w", err)
		}
		return nil
	})
	return out, err
}

// GetQuota resolves one quota by id. No row-scope probe runs — quota is
// workspace-shared config gated by the object grant alone (see the
// package doc); RLS bounds the read to the caller's tenant.
func (s *Store) GetQuota(ctx context.Context, id ids.UUID, archived storekit.ArchivedFilter) (crmcontracts.Quota, error) {
	if err := auth.Require(ctx, "quota", principal.ActionRead); err != nil {
		return crmcontracts.Quota{}, err
	}
	var out crmcontracts.Quota
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		out, err = readQuota(ctx, tx, id, archived)
		return err
	})
	return out, err
}

// UpdateQuotaInput is a sparse merge-patch: nil keeps the stored value.
// IfVersion carries the If-Match optimistic-concurrency check; nil falls
// back to the row-lock guard (storekit.ApplyGuarded).
type UpdateQuotaInput struct {
	OwnerID     *ids.UUID
	TeamID      *ids.UUID
	PeriodStart *time.Time
	PeriodEnd   *time.Time
	TargetMinor *int64
	Currency    *string
	IfVersion   *int64
}

// UpdateQuota applies the sparse patch, re-validating the owner-XOR-team
// contract on the MERGED state before anything is written.
func (s *Store) UpdateQuota(ctx context.Context, id ids.UUID, in UpdateQuotaInput) (crmcontracts.Quota, error) {
	if err := auth.Require(ctx, "quota", principal.ActionUpdate); err != nil {
		return crmcontracts.Quota{}, err
	}
	var out crmcontracts.Quota
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, err := readQuota(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return err
		}
		// The XOR contract holds on what the row WOULD carry after the
		// merge, not on the patch body — patching a team onto an
		// owner-quota (or vice versa) is a refusal, same 422 as create.
		if !ownerXorTeam(mergedRef(in.OwnerID, current.OwnerId), mergedRef(in.TeamID, current.TeamId)) {
			return &OwnerXorTeamError{}
		}
		p := buildQuotaPatch(current, in)
		if p.Empty() {
			out = current
			return nil
		}
		if err := p.ApplyGuarded(ctx, tx, "quota", id, in.IfVersion); err != nil {
			return mapQuotaWriteError(err, "apply quota patch")
		}
		if _, err := storekit.Audit(ctx, tx, "update", "quota", id, p.Before(), p.After()); err != nil {
			return fmt.Errorf("audit quota update: %w", err)
		}
		if out, err = readQuota(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read updated quota: %w", err)
		}
		return nil
	})
	return out, err
}

// buildQuotaPatch folds the caller's sparse edit into a patch — every
// set field carries its before/after image for the audit trail.
func buildQuotaPatch(current crmcontracts.Quota, in UpdateQuotaInput) *storekit.Patch {
	p := storekit.NewPatch()
	if in.OwnerID != nil {
		p.Set("owner_id", current.OwnerId, *in.OwnerID)
	}
	if in.TeamID != nil {
		p.Set("team_id", current.TeamId, *in.TeamID)
	}
	if in.PeriodStart != nil {
		p.Set("period_start", current.PeriodStart, in.PeriodStart.Format(time.DateOnly))
	}
	if in.PeriodEnd != nil {
		p.Set("period_end", current.PeriodEnd, in.PeriodEnd.Format(time.DateOnly))
	}
	if in.TargetMinor != nil {
		p.Set("target_minor", current.TargetMinor, *in.TargetMinor)
	}
	if in.Currency != nil {
		p.Set("currency", current.Currency, *in.Currency)
	}
	return p
}

// mergedRef answers the post-merge value of one nullable reference
// column: the patch's value when the field is set, the stored one
// otherwise. (The contract's merge-PATCH cannot express an explicit
// clear — omitted and null are the same wire shape — so "set" always
// means "set to this id".)
func mergedRef(patch *ids.UUID, current *openapi_types.UUID) *ids.UUID {
	if patch != nil {
		return patch
	}
	if current == nil {
		return nil
	}
	stored := ids.UUID(*current)
	return &stored
}

// ArchiveQuota soft-deletes the quota and returns the full archived
// entity (200 + entity per the contract, never 204). A repeat archive is
// a no-op: same answer, no second audit row.
func (s *Store) ArchiveQuota(ctx context.Context, id ids.UUID) (crmcontracts.Quota, error) {
	if err := auth.Require(ctx, "quota", principal.ActionDelete); err != nil {
		return crmcontracts.Quota{}, err
	}
	var out crmcontracts.Quota
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, err := readQuota(ctx, tx, id, storekit.IncludeArchived)
		if err != nil {
			return err
		}
		if current.ArchivedAt != nil {
			out = current
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE quota SET archived_at = now() WHERE id = $1 AND archived_at IS NULL`, id); err != nil {
			return fmt.Errorf("archive quota: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "archive", "quota", id, nil, nil); err != nil {
			return fmt.Errorf("audit quota archive: %w", err)
		}
		if out, err = readQuota(ctx, tx, id, storekit.IncludeArchived); err != nil {
			return fmt.Errorf("read archived quota: %w", err)
		}
		return nil
	})
	return out, err
}

// ListQuotasInput narrows the keyset list: optional owner/team filters,
// the CAP-PAGE cursor/limit pair, the archived-row toggle, and the
// contract's sort spec (validated against quotaListFields).
type ListQuotasInput struct {
	Cursor          *string
	Limit           *int
	OwnerID         *ids.UUID
	TeamID          *ids.UUID
	IncludeArchived bool
	Sort            *string
}

// quotaListFields is the quota list's closed sortable vocabulary: the
// period bounds, the target, and the house timestamps. No cf_ columns
// join it — quota is workspace config, not a custom-field object.
var quotaListFields = map[string]string{
	"period_start": fieldcatalog.TypeDate,
	"period_end":   fieldcatalog.TypeDate,
	"target_minor": fieldcatalog.TypeCurrency,
	"created_at":   storekit.KindTimestamp,
	"updated_at":   storekit.KindTimestamp,
}

// ListQuotas pages the workspace's quotas keyset-style (-created_at,id
// by default, or the validated sort field first), live rows by default.
func (s *Store) ListQuotas(ctx context.Context, in ListQuotasInput) ([]crmcontracts.Quota, storekit.Page, error) {
	if err := auth.Require(ctx, "quota", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	sorted, err := storekit.ParseListSort(in.Sort, quotaListFields)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)

	where := []string{"1=1"}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }
	if !in.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if in.OwnerID != nil {
		where = append(where, storekit.SQLf("owner_id = $%d", arg(*in.OwnerID)))
	}
	if in.TeamID != nil {
		where = append(where, storekit.SQLf("team_id = $%d", arg(*in.TeamID)))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		clause, err := sorted.KeysetClause(*in.Cursor, arg)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		where = append(where, clause)
	}

	var out []crmcontracts.Quota
	var page storekit.Page
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+quotaColumns+sorted.CursorKeySuffix()+
				` FROM quota WHERE `+strings.Join(where, " AND ")+
				sorted.OrderBy()+storekit.SQLf(` LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var cursorKeys []*string
		if out, cursorKeys, err = scanQuotaPage(rows, sorted); err != nil {
			return err
		}
		if len(out) > limit {
			out = out[:limit]
			last := out[len(out)-1]
			page = storekit.Page{HasMore: true, NextCursor: sorted.EncodePageCursor(cursorKeys[limit-1], last.CreatedAt, ids.UUID(last.Id))}
		}
		return nil
	})
	if out == nil {
		out = []crmcontracts.Quota{}
	}
	return out, page, err
}

// scanQuotaPage drains one list query's rows: each quota plus, under a
// non-default sort, the row's cursor key (the trailing __cursor_key
// column CursorKeySuffix appended — the scanDealPage precedent).
func scanQuotaPage(rows pgx.Rows, sorted *storekit.ListSort) ([]crmcontracts.Quota, []*string, error) {
	var out []crmcontracts.Quota
	var cursorKeys []*string
	for rows.Next() {
		var key *string
		extra := []any{}
		if sorted != nil {
			extra = append(extra, &key)
		}
		q, err := scanQuota(rows, extra...)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, q)
		cursorKeys = append(cursorKeys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return out, cursorKeys, nil
}

// mapQuotaWriteError translates the database's refusals into sentinels:
// the composite tenant FKs (0067) fire when the named owner/team does not
// exist in THIS workspace — indistinguishable from a foreign tenant's
// id, so the answer is absence, the same existence-hiding as a row-scope
// miss.
func mapQuotaWriteError(err error, op string) error {
	if storekit.IsForeignKeyViolation(err) {
		return fmt.Errorf("quota owner or team not found in this workspace: %w", apperrors.ErrNotFound)
	}
	return fmt.Errorf("%s: %w", op, err)
}

const quotaColumns = `id, workspace_id, owner_id, team_id, period_start, period_end,
	target_minor, currency, version, created_at, updated_at, archived_at`

func readQuota(ctx context.Context, tx pgx.Tx, id ids.UUID, archived storekit.ArchivedFilter) (crmcontracts.Quota, error) {
	q := `SELECT ` + quotaColumns + ` FROM quota WHERE id = $1`
	if archived == storekit.LiveOnly {
		q += ` AND archived_at IS NULL`
	}
	quota, err := scanQuota(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Quota{}, apperrors.ErrNotFound
	}
	return quota, err
}

// scanQuota scans the quotaColumns row shape; extra receives any
// trailing expressions the caller's SELECT appended (the sorted list's
// cursor key — the scanDeal precedent).
func scanQuota(row pgx.Row, extra ...any) (crmcontracts.Quota, error) {
	var q crmcontracts.Quota
	var id, wsID ids.UUID
	var ownerID, teamID *ids.UUID
	var periodStart, periodEnd time.Time
	var version int64

	dests := []any{
		&id, &wsID, &ownerID, &teamID, &periodStart, &periodEnd,
		&q.TargetMinor, &q.Currency, &version, &q.CreatedAt, &q.UpdatedAt, &q.ArchivedAt,
	}
	err := row.Scan(append(dests, extra...)...)
	if err != nil {
		return q, err
	}
	q.Id = openapi_types.UUID(id)
	q.WorkspaceId = openapi_types.UUID(wsID)
	q.OwnerId = uuidPtr(ownerID)
	q.TeamId = uuidPtr(teamID)
	q.PeriodStart = openapi_types.Date{Time: periodStart}
	q.PeriodEnd = openapi_types.Date{Time: periodEnd}
	q.Version = &version
	return q, nil
}

func uuidPtr(id *ids.UUID) *openapi_types.UUID {
	if id == nil {
		return nil
	}
	converted := openapi_types.UUID(*id)
	return &converted
}
