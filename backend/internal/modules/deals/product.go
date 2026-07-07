// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The optional rate-card (B-E03.16, data-model §12.6): products are
// priced DATA an offer line snapshots from — no bundles, options or
// pricing rules (ADR-0037), and a price change here never re-prices an
// existing line. Products carry no owner_id: like pipeline config they
// are workspace-shared, governed by the `product` object grants alone.

package deals

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type CreateProductInput struct {
	Name           string
	SKU            *string
	Description    *string
	Unit           *string
	UnitPriceMinor int64
	Currency       string
	DefaultTaxRate *float64
	Active         *bool
	Source         string
}

func (s *Store) CreateProduct(ctx context.Context, in CreateProductInput) (crmcontracts.Product, error) {
	if err := auth.Require(ctx, "product", principal.ActionCreate); err != nil {
		return crmcontracts.Product{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Product{}, err
	}

	unit := "unit"
	if in.Unit != nil && *in.Unit != "" {
		unit = *in.Unit
	}
	taxRate := "0.00"
	if in.DefaultTaxRate != nil {
		taxRate = formatPct(*in.DefaultTaxRate)
	}
	active := in.Active == nil || *in.Active

	var out crmcontracts.Product
	err = s.tx(ctx, func(tx pgx.Tx) error {
		id := ids.NewV7()
		_, err := tx.Exec(ctx,
			`INSERT INTO product (id, workspace_id, name, sku, description, unit, unit_price_minor,
			                      currency, default_tax_rate, active, source, captured_by)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			id, storekit.MustWorkspace(ctx), in.Name, in.SKU, in.Description, unit,
			in.UnitPriceMinor, in.Currency, taxRate, active, in.Source, by)
		if err != nil {
			if storekit.IsUniqueViolation(err) {
				// uq_product_sku: the SKU already names a live product.
				return fmt.Errorf("sku already in use by a live product: %w", apperrors.ErrConflict)
			}
			return fmt.Errorf("insert product: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "create", "product", id, nil, map[string]any{"name": in.Name}); err != nil {
			return fmt.Errorf("audit product create: %w", err)
		}
		if out, err = readProduct(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read created product: %w", err)
		}
		return nil
	})
	return out, err
}

type UpdateProductInput struct {
	Name           *string
	SKU            *string
	Description    *string
	Unit           *string
	UnitPriceMinor *int64
	Currency       *string
	DefaultTaxRate *float64
	Active         *bool
	IfVersion      *int64
}

func (s *Store) UpdateProduct(ctx context.Context, id ids.UUID, in UpdateProductInput) (crmcontracts.Product, error) {
	if err := auth.Require(ctx, "product", principal.ActionUpdate); err != nil {
		return crmcontracts.Product{}, err
	}
	var out crmcontracts.Product
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, err := readProduct(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return err
		}
		p := buildProductPatch(current, in)
		if p.Empty() {
			out = current
			return nil
		}
		if err := p.ApplyGuarded(ctx, tx, "product", id, in.IfVersion); err != nil {
			if storekit.IsUniqueViolation(err) {
				return fmt.Errorf("sku already in use by a live product: %w", apperrors.ErrConflict)
			}
			return fmt.Errorf("apply product patch: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "update", "product", id, p.Before(), p.After()); err != nil {
			return fmt.Errorf("audit product update: %w", err)
		}
		if out, err = readProduct(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read updated product: %w", err)
		}
		return nil
	})
	return out, err
}

// buildProductPatch folds the caller's sparse product edit into a patch —
// every set field carries its before/after image for the audit trail.
func buildProductPatch(current crmcontracts.Product, in UpdateProductInput) *storekit.Patch {
	p := storekit.NewPatch()
	if in.Name != nil {
		p.Set("name", current.Name, *in.Name)
	}
	if in.SKU != nil {
		p.Set("sku", current.Sku, *in.SKU)
	}
	if in.Description != nil {
		p.Set("description", current.Description, *in.Description)
	}
	if in.Unit != nil {
		p.Set("unit", current.Unit, *in.Unit)
	}
	if in.UnitPriceMinor != nil {
		p.Set("unit_price_minor", current.UnitPriceMinor, *in.UnitPriceMinor)
	}
	if in.Currency != nil {
		p.Set("currency", current.Currency, *in.Currency)
	}
	if in.DefaultTaxRate != nil {
		p.Set("default_tax_rate", current.DefaultTaxRate, formatPct(*in.DefaultTaxRate))
	}
	if in.Active != nil {
		p.Set("active", current.Active, *in.Active)
	}
	return p
}

func (s *Store) ArchiveProduct(ctx context.Context, id ids.UUID) (crmcontracts.Product, error) {
	if err := auth.Require(ctx, "product", principal.ActionDelete); err != nil {
		return crmcontracts.Product{}, err
	}
	var out crmcontracts.Product
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := readProduct(ctx, tx, id, storekit.LiveOnly); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE product SET archived_at = now() WHERE id = $1 AND archived_at IS NULL`, id); err != nil {
			return fmt.Errorf("archive product: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "archive", "product", id, nil, nil); err != nil {
			return fmt.Errorf("audit product archive: %w", err)
		}
		var err error
		if out, err = readProduct(ctx, tx, id, storekit.IncludeArchived); err != nil {
			return fmt.Errorf("read archived product: %w", err)
		}
		return nil
	})
	return out, err
}

func (s *Store) GetProduct(ctx context.Context, id ids.UUID, archived storekit.ArchivedFilter) (crmcontracts.Product, error) {
	if err := auth.Require(ctx, "product", principal.ActionRead); err != nil {
		return crmcontracts.Product{}, err
	}
	var out crmcontracts.Product
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		out, err = readProduct(ctx, tx, id, archived)
		return err
	})
	return out, err
}

type ListProductsInput struct {
	Cursor          *string
	Limit           *int
	Query           *string
	Active          *bool
	IncludeArchived bool
}

func (s *Store) ListProducts(ctx context.Context, in ListProductsInput) ([]crmcontracts.Product, storekit.Page, error) {
	if err := auth.Require(ctx, "product", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)

	where := []string{"1=1"}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }
	if !in.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if in.Active != nil {
		where = append(where, storekit.SQLf("active = $%d", arg(*in.Active)))
	}
	if in.Query != nil && *in.Query != "" {
		pos := arg("%" + storekit.EscapeLike(*in.Query) + "%")
		where = append(where, storekit.SQLf("(name ILIKE $%d OR sku ILIKE $%d)", pos, pos))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		c, err := storekit.DecodeCursor(*in.Cursor)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		where = append(where, storekit.SQLf("(created_at, id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID)))
	}

	var products []crmcontracts.Product
	var page storekit.Page
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+productColumns+` FROM product WHERE `+strings.Join(where, " AND ")+
				storekit.SQLf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanProduct(rows)
			if err != nil {
				return err
			}
			products = append(products, p)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(products) > limit {
			products = products[:limit]
			last := products[len(products)-1]
			page = storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(last.CreatedAt, ids.UUID(last.Id))}
		}
		return nil
	})
	if products == nil {
		products = []crmcontracts.Product{}
	}
	return products, page, err
}

const productColumns = `id, workspace_id, name, sku, description, unit, unit_price_minor,
	currency, default_tax_rate::text, active, source, captured_by, version, created_at, updated_at, archived_at`

func readProduct(ctx context.Context, tx pgx.Tx, id ids.UUID, archived storekit.ArchivedFilter) (crmcontracts.Product, error) {
	q := `SELECT ` + productColumns + ` FROM product WHERE id = $1`
	if archived == storekit.LiveOnly {
		q += ` AND archived_at IS NULL`
	}
	p, err := scanProduct(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Product{}, apperrors.ErrNotFound
	}
	return p, err
}

func scanProduct(row pgx.Row) (crmcontracts.Product, error) {
	var p crmcontracts.Product
	var id, wsID ids.UUID
	var taxRate string
	var capturedBy string
	var version int64

	err := row.Scan(&id, &wsID, &p.Name, &p.Sku, &p.Description, &p.Unit, &p.UnitPriceMinor,
		&p.Currency, &taxRate, &p.Active, &p.Source, &capturedBy, &version, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt)
	if err != nil {
		return p, err
	}
	rate, err := strconv.ParseFloat(taxRate, 64)
	if err != nil {
		return p, fmt.Errorf("default_tax_rate is not numeric: %w", err)
	}
	p.Id = openapi_types.UUID(id)
	p.WorkspaceId = openapi_types.UUID(wsID)
	p.DefaultTaxRate = rate
	p.CapturedBy = &capturedBy
	p.Version = &version
	return p, nil
}
