// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package collections

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

type Store struct {
	// db binds the installation's workspace itself (ADR-0091 §9 step 3).
	db *database.DB
	// catalog widens the filter vocabulary with the workspace's own cf_*
	// columns, and answers which of them are still offered. Optional by design:
	// nil is the port's pass-through, and a store without one filters on core
	// fields alone.
	catalog CatalogReader
}

// NewStore binds the store to the pool every read and write runs through.
func NewStore(db *database.DB) *Store {
	return &Store{db: db}
}

// CatalogReader is what collections needs of the custom-field catalogue, which
// is BOTH readers rather than the filterable one alone — because this module asks
// the catalogue two different questions.
//
// What a filter may SAY includes a retired column, so a saved segment built on
// one keeps evaluating (FilterableColumns). What a builder may OFFER for a new
// clause does not, per CUSTOM-FIELDS-AC-13's "hidden from API + filtering"
// (ActiveColumns). One reader cannot answer both, and the difference between them
// IS the retired set — which is why the fieldcatalog seam's own note that "a
// consumer of one has no use for the other" no longer holds for this consumer.
type CatalogReader interface {
	fieldcatalog.FilterableReader
	fieldcatalog.Reader
}

// WithFieldCatalog injects the custom-field vocabulary source. Compose calls it;
// a caller that never filters (the workflow adapter's list writes) needs no
// catalog and passes none.
func (s *Store) WithFieldCatalog(r CatalogReader) *Store {
	s.catalog = r
	return s
}

// The two list kinds (LVS-DDL-1's list_type CHECK). A static list is an
// explicit membership set; a dynamic one stores a filter its members are
// derived from, so the pair decides which of the two membership paths a
// read takes and whether a definition may be present at all.
//
// dynamicAddedBy carries the same string for a different question — who
// added a computed member, not what kind a list is.
const (
	listTypeStatic  = "static"
	listTypeDynamic = "dynamic"
)

// memberEntityTables is the closed polymorphic target set — the table
// name doubles as the RBAC object and the visibility-probe table. It is
// derived from the canonical record vocabulary rather than restated, so a
// new record type reaches lists, tags and saved views by widening one set.
var memberEntityTables = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range datasource.RecordTypes() {
		m[string(t)] = true
	}
	return m
}()

// entityTypeField names the input every polymorphic refusal on this surface
// points at.
const entityTypeField = "entity_type"

// definitionField names the body key a dynamic list carries its filter tree
// under — the counterpart to a saved view's viewQueryField, and the reason a
// decode refusal takes the name from its caller rather than assuming one.
const definitionField = "definition"

// entityIDField names its sibling — the polymorphic TARGET a member or a tag
// application points at. Named beside entity_type because the two travel
// together in every body on this surface, and a refusal has to name the wire
// path, never prose.
const entityIDField = "entity_id"

// memberEntityVocabulary renders the accepted set for the refusal message.
// Derived from the same map the check uses, because a message that restates
// the vocabulary drifts from it silently — the caller is then told a record
// type is invalid while being shown a list that does not include it.
var memberEntityVocabulary = func() string {
	names := make([]string, 0, len(memberEntityTables))
	for name := range memberEntityTables {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, "|")
}()

const listColumns = `id, name, entity_type, list_type, definition, owner_id, team_id, created_at, updated_at, archived_at`

// catalogCap bounds the un-paginated catalog reads. Lists and tags are
// workspace-curated vocabulary — tens of rows, not record data — which
// is why the contract defines no cursor for them (the missing
// pagination is filed as feedback). The cap keeps a runaway workspace
// from turning the catalog read into an export; truncation is reported
// through the page flag, never silently.
const catalogCap = 1000

type listRow struct {
	ID         ids.ListID
	Name       string
	EntityType string
	ListType   string
	Definition map[string]any
	OwnerID    *ids.UserID
	TeamID     *ids.TeamID
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt *time.Time
}

func scanList(r pgx.Row) (listRow, error) {
	var l listRow
	err := r.Scan(&l.ID, &l.Name, &l.EntityType, &l.ListType,
		&l.Definition, &l.OwnerID, &l.TeamID, &l.CreatedAt, &l.UpdatedAt, &l.ArchivedAt)
	return l, err
}

func (s *Store) ListLists(ctx context.Context, entityType *string, archived storekit.ArchivedFilter) ([]listRow, bool, error) {
	if err := auth.Require(ctx, "list", principal.ActionRead); err != nil {
		return nil, false, err
	}
	var out []listRow
	truncated := false
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		where := []string{"true"}
		if entityType != nil {
			where = append(where, fmt.Sprintf("entity_type = $%d", arg(*entityType)))
		}
		if archived != storekit.IncludeArchived {
			where = append(where, "archived_at IS NULL")
		}
		scope, err := auth.ScopeClause(ctx, arg)
		if err != nil {
			return err
		}
		if scope != "" {
			where = append(where, scope)
		}
		rows, err := tx.Query(ctx,
			"SELECT "+listColumns+" FROM list WHERE "+strings.Join(where, " AND ")+
				fmt.Sprintf(" ORDER BY name LIMIT $%d", arg(catalogCap+1)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanList(rows)
			if err != nil {
				return err
			}
			out = append(out, l)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out) > catalogCap {
			out = out[:catalogCap]
			truncated = true
		}
		return nil
	})
	return out, truncated, err
}

type CreateListInput struct {
	Name       string
	EntityType string
	ListType   string
	Definition map[string]any
	OwnerID    *ids.UserID
	TeamID     *ids.TeamID
}

func (s *Store) CreateList(ctx context.Context, in CreateListInput) (listRow, error) {
	if err := auth.Require(ctx, "list", principal.ActionCreate); err != nil {
		return listRow{}, err
	}
	if !memberEntityTables[in.EntityType] {
		return listRow{}, &BadInputError{Field: entityTypeField, Reason: "must be " + memberEntityVocabulary}
	}
	if in.ListType == "" {
		in.ListType = listTypeStatic
	}
	// A dynamic segment IS its definition; a static set must not carry
	// one — the shape rules out a half-and-half list.
	if in.ListType == listTypeDynamic && len(in.Definition) == 0 {
		return listRow{}, &BadInputError{Field: definitionField, Reason: "a dynamic list needs a query definition"}
	}
	if in.ListType == listTypeStatic && len(in.Definition) > 0 {
		return listRow{}, &BadInputError{Field: definitionField, Reason: "a static list carries no definition"}
	}
	// A dynamic segment's definition is a stored filter the members
	// endpoint later runs through the ONE engine. Validate it against the
	// entity's closed vocabulary NOW so an unknown field or an over-deep
	// tree is rejected at creation (422) rather than at read time — a
	// list cannot store a filter it could never evaluate.
	if in.ListType == listTypeDynamic {
		if err := s.validateSegmentDefinition(ctx, in.EntityType, in.Definition); err != nil {
			return listRow{}, err
		}
	}
	var out listRow
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO list (name, entity_type, list_type, definition, owner_id, team_id)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING `+listColumns,
			in.Name, in.EntityType, in.ListType, in.Definition, in.OwnerID, in.TeamID)
		var err error
		if out, err = scanList(row); err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "create", "list", out.ID.UUID, nil, map[string]any{
			"name": out.Name, "entity_type": out.EntityType, "list_type": out.ListType,
		})
		return err
	})
	return out, err
}

func (s *Store) GetList(ctx context.Context, id ids.ListID) (listRow, error) {
	if err := auth.Require(ctx, "list", principal.ActionRead); err != nil {
		return listRow{}, err
	}
	var out listRow
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := ensureListVisible(ctx, tx, id); err != nil {
			return err
		}
		var err error
		out, err = scanList(tx.QueryRow(ctx, "SELECT "+listColumns+" FROM list WHERE id = $1", id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return listRow{}, apperrors.ErrNotFound
	}
	return out, err
}

func (s *Store) ArchiveList(ctx context.Context, id ids.ListID) (listRow, error) {
	if err := auth.Require(ctx, "list", principal.ActionDelete); err != nil {
		return listRow{}, err
	}
	var out listRow
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := ensureListVisible(ctx, tx, id); err != nil {
			return err
		}
		row := tx.QueryRow(ctx,
			"UPDATE list SET archived_at = now() WHERE id = $1 AND archived_at IS NULL RETURNING "+listColumns, id)
		var err error
		if out, err = scanList(row); errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound // already archived reads as absent
		} else if err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "archive", "list", id.UUID, nil, nil)
		return err
	})
	return out, err
}

// validateSegmentDefinition proves a dynamic list's definition is an evaluable
// filter over the entity's closed vocabulary — core fields, this workspace's
// custom columns, and tags — before it is stored: it compiles the predicate
// (discarding the SQL) so an unknown field, a mistyped value, or an
// over-deep/over-wide tree fails as a PredicateError the transport maps to 422.
func (s *Store) validateSegmentDefinition(ctx context.Context, entityType string, definition map[string]any) error {
	engine, ok, err := s.SegmentEngine(ctx, entityType)
	if err != nil {
		return err
	}
	if !ok {
		return &BadInputError{Field: entityTypeField, Reason: "no dynamic segment engine for " + entityType}
	}
	return compileForValidation(engine, definition, definitionField)
}

// compileForValidation is the proof itself, shared by the two stored filters
// this module accepts — a dynamic list's definition and a saved view's filter
// state. It decodes the tree and compiles it against the resolved vocabulary,
// throwing the SQL away: the point is the refusal, not the statement. An
// unknown field, a mistyped value, or an over-deep/over-wide tree comes back as
// a PredicateError the transport maps to 422.
//
// Two things differ between the callers and both are parameters rather than
// branches here. The ENGINE RESOLUTION, which is why the split falls at this
// seam: a list names its entity type outright and has none if the type is not
// filterable, while a view names a plural resource that may have no engine at
// all and legitimately carry no filter. And the WIRE FIELD a decode failure
// names, because the two surfaces carry the tree under different keys.
func compileForValidation(engine storekit.Query, tree map[string]any, field string) error {
	pred, err := predicateFromDefinition(tree)
	if err != nil {
		// Dressed as the caller's own field, because on THIS path the caller
		// did send the tree. Anything else passes through untouched: the
		// tree's shape is the only part of a refusal a caller can act on.
		if errors.Is(err, errNotAFilterTree) {
			return &BadInputError{Field: field, Reason: "is not a valid filter tree"}
		}
		return err
	}
	discard := 0
	arg := func(any) int { discard++; return discard }
	_, err = storekit.CompilePredicate(pred, engine.Fields, arg)
	return err
}

// ensureListVisible is the list's own row-scope probe (owner_id scoped
// like every other owner-carrying table; ownerless lists are shared).
func ensureListVisible(ctx context.Context, tx pgx.Tx, id ids.ListID) error {
	return auth.EnsureVisible(ctx, tx, "list", id.UUID)
}

// BadInputError maps to a 422 at the transport.
type BadInputError struct {
	Field  string
	Reason string
}

func (e *BadInputError) Error() string { return "collections: " + e.Field + ": " + e.Reason }

// FieldFault names the list or tag argument that was rejected.
func (e *BadInputError) FieldFault() (field, code, message string) {
	return e.Field, "invalid", e.Reason
}

func wireList(l listRow) crmcontracts.List {
	out := crmcontracts.List{
		Id:         openapi_types.UUID(l.ID.UUID),
		Name:       l.Name,
		EntityType: crmcontracts.ListEntityType(l.EntityType),
		ListType:   crmcontracts.ListListType(l.ListType),
		CreatedAt:  &l.CreatedAt,
		UpdatedAt:  &l.UpdatedAt,
		ArchivedAt: l.ArchivedAt,
	}
	if len(l.Definition) > 0 {
		out.Definition = &l.Definition
	}
	if l.OwnerID != nil {
		owner := openapi_types.UUID(l.OwnerID.UUID)
		out.OwnerId = &owner
	}
	if l.TeamID != nil {
		team := openapi_types.UUID(l.TeamID.UUID)
		out.TeamId = &team
	}
	return out
}
