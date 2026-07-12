// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Offer templates (data-model §12.6, OFFER-DDL-4): a branded,
// workspace-governed DE/EN PDF layout an offer renders against. Like
// Product/Quota this is workspace-authored config, not captured CRM
// data — no owner_id, no source/captured_by, gated by the
// offer_template object grant alone (no row-scope EnsureVisible probe).
// At most one is_default template per locale (uq_offer_template_default,
// the partial unique index on (workspace_id, locale) WHERE is_default
// AND NOT archived). A second default for the same locale is REJECTED
// with a named 409 (offer_template_default_conflict) — this store never
// auto-demotes an incumbent default; the caller un-defaults or archives
// it first (poc-1's checkDefaultConflict shape, kept verbatim: an
// auto-demote would silently change a template's meaning as a side
// effect of an unrelated create/update).

package deals

import (
	"context"
	"errors"
	"fmt"
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

// defaultOfferTemplateLocale is the DE/EN launch default (WP7/OP-T02):
// an empty locale on create resolves to de-DE, never a blank column.
const defaultOfferTemplateLocale = "de-DE"

// offerTemplateIsDefaultField names the is_default column/audit-payload
// key in the one place both the Patch call and the two audit maps
// (create, update) reference it.
const offerTemplateIsDefaultField = "is_default"

// offerTemplateWhereSeed and offerTemplateArchivedAtClause are the
// list/read query-builder's repeated SQL fragments, named once so this
// file's own occurrences aren't raw duplicated literals (the same
// fragments recur, unnamed, in the package's older files — that backlog
// is untouched here).
const (
	offerTemplateWhereSeed        = "1=1"
	offerTemplateArchivedAtClause = " AND archived_at IS NULL"
)

// DuplicateTemplateNameError reports a live-row name collision
// (offer_template_name_unique). The pre-check ahead of INSERT/UPDATE is
// the clean common-case path and carries ExistingID cheaply; a
// concurrent writer that also passes the pre-check under READ
// COMMITTED is caught by the post-write unique-violation backstop
// instead (offerTemplateUniqueViolation), which leaves ExistingID at
// its zero value since the racing row's id isn't cheaply available there.
type DuplicateTemplateNameError struct{ ExistingID ids.OfferTemplateID }

func (e *DuplicateTemplateNameError) Error() string {
	return "an offer template named this already exists: " + e.ExistingID.String()
}

// Is reports the sentinel this typed error maps onto, so a generic
// errors.Is(err, apperrors.ErrConflict) check still matches it.
func (e *DuplicateTemplateNameError) Is(target error) bool { return target == apperrors.ErrConflict }

// DefaultConflictError reports an existing is_default=true row for the
// same (workspace, locale) — uq_offer_template_default. Pre-checked and
// REJECTED, never auto-demoted (see the file doc comment); the same
// post-write backstop as DuplicateTemplateNameError catches the
// concurrent-race case, again with ExistingID left zero-value.
type DefaultConflictError struct {
	ExistingID ids.OfferTemplateID
	Locale     string
}

func (e *DefaultConflictError) Error() string {
	return "a default template already exists for locale " + e.Locale + ": " + e.ExistingID.String()
}

// Is reports the sentinel this typed error maps onto, so a generic
// errors.Is(err, apperrors.ErrConflict) check still matches it.
func (e *DefaultConflictError) Is(target error) bool { return target == apperrors.ErrConflict }

// checkTemplateNameConflict pre-checks a name collision within the
// workspace, excluding the row being written itself (a zero-value
// excludeID on create matches no real row, since uuidv7 never mints an
// all-zero id). offer_template_name_unique (0071) is NOT partial — it
// spans archived rows too, so this check must too: filtering to live
// rows only would let a false negative slip an INSERT through to the
// real constraint, leaking a raw 23505 instead of the named 409. An
// archived template's name is never freed for reuse; the caller renames
// or truly deletes the row (there is no hard-delete path here).
func (s *Store) checkTemplateNameConflict(ctx context.Context, tx pgx.Tx, excludeID ids.OfferTemplateID, name string) error {
	var existing ids.OfferTemplateID
	err := tx.QueryRow(ctx,
		`SELECT id FROM offer_template WHERE workspace_id = $1 AND name = $2 AND id <> $3`,
		storekit.MustWorkspace(ctx), name, excludeID).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check offer_template name conflict: %w", err)
	}
	return &DuplicateTemplateNameError{ExistingID: existing}
}

// checkTemplateDefaultConflict pre-checks a live is_default collision
// for the given locale, excluding the row being written itself.
func (s *Store) checkTemplateDefaultConflict(ctx context.Context, tx pgx.Tx, excludeID ids.OfferTemplateID, locale string) error {
	var existing ids.OfferTemplateID
	err := tx.QueryRow(ctx,
		`SELECT id FROM offer_template WHERE workspace_id = $1 AND locale = $2 AND is_default AND archived_at IS NULL AND id <> $3`,
		storekit.MustWorkspace(ctx), locale, excludeID).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check offer_template default conflict: %w", err)
	}
	return &DefaultConflictError{ExistingID: existing, Locale: locale}
}

// offerTemplateUniqueViolation is the post-write race backstop for the
// two pre-checks above: under READ COMMITTED, two concurrent
// creates/updates can both pass checkTemplateNameConflict or
// checkTemplateDefaultConflict before either write lands, so the
// second write then hits the real constraint. Without this, that raw
// 23505 would fall through to an unmapped 500 instead of the intended
// 409 (the product.go/lead.go precedent for this exact shape). Returns
// nil if err isn't one of the two named unique violations, so the
// caller falls through to its generic wrap.
func offerTemplateUniqueViolation(err error, locale string) error {
	constraint, ok := storekit.UniqueViolation(err)
	if !ok {
		return nil
	}
	switch constraint {
	case "offer_template_name_unique":
		return &DuplicateTemplateNameError{}
	case "uq_offer_template_default":
		return &DefaultConflictError{Locale: locale}
	default:
		return nil
	}
}

// CreateOfferTemplateInput is a new offer_template row; an empty Locale
// resolves to de-DE.
type CreateOfferTemplateInput struct {
	Name      string
	Locale    string
	IsDefault bool
	Layout    map[string]any
}

// CreateOfferTemplate inserts one offer_template row, pre-checking the
// two live-row conflicts (name, same-locale default) before the INSERT —
// events.md §5 defines no offer_template.* type, so this write is
// audit-only (writeshape_test.go's ratified exception, the
// Product/Quota config precedent).
func (s *Store) CreateOfferTemplate(ctx context.Context, in CreateOfferTemplateInput) (crmcontracts.OfferTemplate, error) {
	if err := auth.Require(ctx, "offer_template", principal.ActionCreate); err != nil {
		return crmcontracts.OfferTemplate{}, err
	}
	locale := in.Locale
	if locale == "" {
		locale = defaultOfferTemplateLocale
	}
	layout := in.Layout
	if layout == nil {
		layout = map[string]any{}
	}

	var out crmcontracts.OfferTemplate
	err := s.tx(ctx, func(tx pgx.Tx) error {
		id := ids.New[ids.OfferTemplateKind]()
		var noExclusion ids.OfferTemplateID
		if err := s.checkTemplateNameConflict(ctx, tx, noExclusion, in.Name); err != nil {
			return err
		}
		if in.IsDefault {
			if err := s.checkTemplateDefaultConflict(ctx, tx, noExclusion, locale); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO offer_template (id, workspace_id, name, locale, is_default, layout, version)
			 VALUES ($1, $2, $3, $4, $5, $6, 1)`,
			id, storekit.MustWorkspace(ctx), in.Name, locale, in.IsDefault, layout); err != nil {
			if conflict := offerTemplateUniqueViolation(err, locale); conflict != nil {
				return conflict
			}
			return fmt.Errorf("insert offer_template: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "create", "offer_template", id.UUID, nil, map[string]any{
			offerTemplateNameField: in.Name, "locale": locale, offerTemplateIsDefaultField: in.IsDefault,
		}); err != nil {
			return fmt.Errorf("audit offer_template create: %w", err)
		}
		var err error
		if out, err = readOfferTemplate(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read created offer_template: %w", err)
		}
		return nil
	})
	return out, err
}

// GetOfferTemplate resolves one live-or-archived template by id, gated
// on the offer_template object grant alone (workspace-shared config, no
// row-scope probe).
func (s *Store) GetOfferTemplate(ctx context.Context, id ids.OfferTemplateID, archived storekit.ArchivedFilter) (crmcontracts.OfferTemplate, error) {
	if err := auth.Require(ctx, "offer_template", principal.ActionRead); err != nil {
		return crmcontracts.OfferTemplate{}, err
	}
	var out crmcontracts.OfferTemplate
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		out, err = readOfferTemplate(ctx, tx, id, archived)
		return err
	})
	return out, err
}

// ListOfferTemplatesInput narrows the keyset list: an optional locale
// filter, the CAP-PAGE cursor/limit pair, and the archived-row toggle.
type ListOfferTemplatesInput struct {
	Cursor          *string
	Limit           *int
	Locale          *string
	IncludeArchived bool
}

// ListOfferTemplates pages the workspace's templates keyset-style
// (-created_at,id, the house default — like product's list, no sort
// vocabulary is exposed for this config object), optionally narrowed to
// one locale.
func (s *Store) ListOfferTemplates(ctx context.Context, in ListOfferTemplatesInput) ([]crmcontracts.OfferTemplate, storekit.Page, error) {
	if err := auth.Require(ctx, "offer_template", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)

	where := []string{offerTemplateWhereSeed}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }
	if !in.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if in.Locale != nil && *in.Locale != "" {
		where = append(where, storekit.SQLf("locale = $%d", arg(*in.Locale)))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		c, err := storekit.DecodeCursor(*in.Cursor)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		where = append(where, storekit.SQLf("(created_at, id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID)))
	}

	var templates []crmcontracts.OfferTemplate
	var page storekit.Page
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+offerTemplateColumns+` FROM offer_template WHERE `+strings.Join(where, " AND ")+
				storekit.SQLf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanOfferTemplate(rows)
			if err != nil {
				return err
			}
			templates = append(templates, t)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(templates) > limit {
			templates = templates[:limit]
			last := templates[len(templates)-1]
			page = storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(last.CreatedAt, ids.UUID(last.Id))}
		}
		return nil
	})
	if templates == nil {
		templates = []crmcontracts.OfferTemplate{}
	}
	return templates, page, err
}

// UpdateOfferTemplateInput is a full-replace PUT: every writable field
// is always supplied (see UpdateOfferTemplate's doc comment).
type UpdateOfferTemplateInput struct {
	Name      string
	Locale    string
	IsDefault bool
	Layout    map[string]any
	IfVersion *int64
}

// UpdateOfferTemplate is a full-replace PUT (unlike Product's
// merge-PATCH): every writable field is always supplied and always
// written, so the patch is never empty — a PUT that repeats the current
// state still bumps version and writes an audit row, honestly recording
// that a write happened. Re-validates the two live-row conflicts against
// the state the row WOULD carry after the replace, before anything is
// written.
func (s *Store) UpdateOfferTemplate(ctx context.Context, id ids.OfferTemplateID, in UpdateOfferTemplateInput) (crmcontracts.OfferTemplate, error) {
	if err := auth.Require(ctx, "offer_template", principal.ActionUpdate); err != nil {
		return crmcontracts.OfferTemplate{}, err
	}
	locale := in.Locale
	if locale == "" {
		locale = defaultOfferTemplateLocale
	}
	layout := in.Layout
	if layout == nil {
		layout = map[string]any{}
	}

	var out crmcontracts.OfferTemplate
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, err := readOfferTemplate(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return err
		}
		if err := s.checkTemplateNameConflict(ctx, tx, id, in.Name); err != nil {
			return err
		}
		if in.IsDefault {
			if err := s.checkTemplateDefaultConflict(ctx, tx, id, locale); err != nil {
				return err
			}
		}
		p := storekit.NewPatch()
		p.Set("name", current.Name, in.Name)
		p.Set("locale", current.Locale, locale)
		p.Set(offerTemplateIsDefaultField, current.IsDefault, in.IsDefault)
		p.Set("layout", current.Layout, layout)
		if err := p.ApplyGuarded(ctx, tx, "offer_template", id.UUID, in.IfVersion); err != nil {
			if conflict := offerTemplateUniqueViolation(err, locale); conflict != nil {
				return conflict
			}
			return fmt.Errorf("apply offer_template patch: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "update", "offer_template", id.UUID, p.Before(), p.After()); err != nil {
			return fmt.Errorf("audit offer_template update: %w", err)
		}
		if out, err = readOfferTemplate(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read updated offer_template: %w", err)
		}
		return nil
	})
	return out, err
}

// ArchiveOfferTemplate soft-deletes the template; a repeat archive is a
// no-op returning the same entity, writing no second audit row (the
// Quota precedent).
func (s *Store) ArchiveOfferTemplate(ctx context.Context, id ids.OfferTemplateID) (crmcontracts.OfferTemplate, error) {
	if err := auth.Require(ctx, "offer_template", principal.ActionDelete); err != nil {
		return crmcontracts.OfferTemplate{}, err
	}
	var out crmcontracts.OfferTemplate
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, err := readOfferTemplate(ctx, tx, id, storekit.IncludeArchived)
		if err != nil {
			return err
		}
		if current.ArchivedAt != nil {
			out = current
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE offer_template SET archived_at = now() WHERE id = $1 AND archived_at IS NULL`, id); err != nil {
			return fmt.Errorf("archive offer_template: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "archive", "offer_template", id.UUID, nil, nil); err != nil {
			return fmt.Errorf("audit offer_template archive: %w", err)
		}
		if out, err = readOfferTemplate(ctx, tx, id, storekit.IncludeArchived); err != nil {
			return fmt.Errorf("read archived offer_template: %w", err)
		}
		return nil
	})
	return out, err
}

const offerTemplateColumns = `id, workspace_id, name, locale, is_default, layout, version, created_at, updated_at, archived_at`

func readOfferTemplate(ctx context.Context, tx pgx.Tx, id ids.OfferTemplateID, archived storekit.ArchivedFilter) (crmcontracts.OfferTemplate, error) {
	q := `SELECT ` + offerTemplateColumns + ` FROM offer_template WHERE id = $1`
	if archived == storekit.LiveOnly {
		q += offerTemplateArchivedAtClause
	}
	t, err := scanOfferTemplate(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.OfferTemplate{}, apperrors.ErrNotFound
	}
	return t, err
}

func scanOfferTemplate(row pgx.Row) (crmcontracts.OfferTemplate, error) {
	var t crmcontracts.OfferTemplate
	var id, wsID ids.UUID
	var version int64

	err := row.Scan(&id, &wsID, &t.Name, &t.Locale, &t.IsDefault, &t.Layout,
		&version, &t.CreatedAt, &t.UpdatedAt, &t.ArchivedAt)
	if err != nil {
		return t, err
	}
	t.Id = openapi_types.UUID(id)
	t.WorkspaceId = openapi_types.UUID(wsID)
	t.Version = &version
	return t, nil
}
