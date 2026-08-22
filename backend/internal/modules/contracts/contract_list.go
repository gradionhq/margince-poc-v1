// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package contracts

// The account read: every agreement on one company.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ListContractsInput selects one account's agreements.
type ListContractsInput struct {
	OrganizationID ids.OrganizationID
	// Status filters to one asserted status. Nil means every status, which
	// includes the superseded predecessors that make a renewal chain readable.
	Status *string
	// UnderContractOnly filters to the DERIVED reading rather than the status
	// column (CONTRACT-FORM-1). The two are different questions and a caller
	// asking for one must not silently get the other.
	UnderContractOnly bool
	Cursor            *string
	Limit             *int
}

// ListOrganizationContracts reads one account's agreements, newest term first.
func (s *Store) ListOrganizationContracts(ctx context.Context, in ListContractsInput) (crmcontracts.ContractListResponse, error) {
	if err := auth.Require(ctx, contractObject, principal.ActionRead); err != nil {
		return crmcontracts.ContractListResponse{}, err
	}

	var out crmcontracts.ContractListResponse
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// Naming the account is a read of it: a caller who cannot see the
		// company does not learn how many agreements it holds.
		if err := auth.EnsureLinkTarget(ctx, tx, "organization", in.OrganizationID.UUID); err != nil {
			return err
		}
		var err error
		out, err = listContractsTx(ctx, tx, in, s.today())
		return err
	})
	return out, err
}

func listContractsTx(ctx context.Context, tx pgx.Tx, in ListContractsInput, asOf time.Time) (crmcontracts.ContractListResponse, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(in.OrganizationID)
	asOfPos := arg(asOf)

	where := []string{storekit.SQLf("organization_id = $%d", orgPos), "archived_at IS NULL"}

	scope, err := VisibleClause(ctx, "", arg)
	if err != nil {
		return crmcontracts.ContractListResponse{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}
	if in.Status != nil {
		where = append(where, storekit.SQLf("status = $%d", arg(*in.Status)))
	}
	if in.UnderContractOnly {
		where = append(where, underContractSQL(asOfPos))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		cursor, err := storekit.DecodeCursor(*in.Cursor)
		if err != nil {
			return crmcontracts.ContractListResponse{}, err
		}
		where = append(where, storekit.SQLf("(created_at, id) < ($%d, $%d)",
			arg(cursor.CreatedAt), arg(cursor.ID)))
	}

	limit := storekit.ClampLimit(in.Limit)
	rows, err := tx.Query(ctx, storekit.SQLf(
		`SELECT %s, %s FROM contract WHERE %s ORDER BY created_at DESC, id DESC LIMIT $%d`,
		contractColumns, underContractSQL(asOfPos), strings.Join(where, " AND "), arg(limit+1)), args...)
	if err != nil {
		return crmcontracts.ContractListResponse{}, fmt.Errorf("list contracts: %w", err)
	}
	defer rows.Close()

	contracts := make([]crmcontracts.Contract, 0, limit)
	for rows.Next() {
		c, err := scanContract(rows)
		if err != nil {
			return crmcontracts.ContractListResponse{}, fmt.Errorf("scan contract: %w", err)
		}
		contracts = append(contracts, c)
	}
	if err := rows.Err(); err != nil {
		return crmcontracts.ContractListResponse{}, fmt.Errorf("read contract page: %w", err)
	}

	page := crmcontracts.PageInfo{}
	// One row beyond the page proves another page exists without a second
	// count query, which would answer a different question under concurrency.
	if len(contracts) > limit {
		contracts = contracts[:limit]
		last := contracts[len(contracts)-1]
		next := storekit.EncodeCursor(cursorTime(last.CreatedAt), ids.UUID(last.Id))
		page.NextCursor = &next
		page.HasMore = true
	}
	return crmcontracts.ContractListResponse{Data: contracts, Page: page}, nil
}

// cursorTime reads a row's creation instant for the keyset token. The
// generated type carries it as optional; a persisted row always has one.
func cursorTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// ListProjectContractsTx reads the agreements attached to one project, newest
// first, inside a caller-opened transaction — the project page's contracts
// section. The project itself is the caller's to see before its paper is:
// naming it is a read of it, the same rule the account list keeps for the
// organization.
func (s *Store) ListProjectContractsTx(ctx context.Context, tx pgx.Tx, projectID ids.ProjectID, limit *int) (crmcontracts.ContractListResponse, error) {
	if err := auth.Require(ctx, contractObject, principal.ActionRead); err != nil {
		return crmcontracts.ContractListResponse{}, err
	}
	if err := auth.EnsureLinkTarget(ctx, tx, "project", projectID.UUID); err != nil {
		return crmcontracts.ContractListResponse{}, err
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	asOfPos := arg(s.today())
	where := []string{storekit.SQLf("project_id = $%d", arg(projectID)), "archived_at IS NULL"}
	scope, err := VisibleClause(ctx, "", arg)
	if err != nil {
		return crmcontracts.ContractListResponse{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}
	lim := storekit.ClampLimit(limit)
	rows, err := tx.Query(ctx, storekit.SQLf(
		`SELECT %s, %s FROM contract WHERE %s ORDER BY created_at DESC, id DESC LIMIT $%d`,
		contractColumns, underContractSQL(asOfPos), strings.Join(where, " AND "), arg(lim+1)), args...)
	if err != nil {
		return crmcontracts.ContractListResponse{}, fmt.Errorf("list project contracts: %w", err)
	}
	defer rows.Close()
	contracts := make([]crmcontracts.Contract, 0, lim)
	for rows.Next() {
		c, err := scanContract(rows)
		if err != nil {
			return crmcontracts.ContractListResponse{}, fmt.Errorf("scan contract: %w", err)
		}
		contracts = append(contracts, c)
	}
	if err := rows.Err(); err != nil {
		return crmcontracts.ContractListResponse{}, fmt.Errorf("read contract page: %w", err)
	}
	page := crmcontracts.PageInfo{}
	if len(contracts) > lim {
		contracts = contracts[:lim]
		page.HasMore = true
	}
	return crmcontracts.ContractListResponse{Data: contracts, Page: page}, nil
}
