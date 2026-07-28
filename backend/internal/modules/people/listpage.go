// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The ONE list page-read for this module's record lists (person,
// organization): RBAC + row-scope, DM-VOCAB sort validation over core +
// active cf_ columns, the shared optional-filter chain, keyset
// pagination with the limit+1 probe, and per-type child attachment.
// person_list.go / organization_list.go each bind one listPageSpec —
// what varies is data (table, vocabulary, scan, attach), not the read.

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// whereAlways seeds the AND chain so every filter appends uniformly —
// the chain is never empty even when no filter applies.
const whereAlways = "1=1"

// listPageSpec binds one record type into listPage. entity doubles as
// the auth object and the table name — the module's record tables are
// named after their objects.
type listPageSpec[T any] struct {
	entity  string
	columns string
	// fields is the core sortable vocabulary (data-model §13.5); active
	// cf_ columns join it per request.
	fields map[string]string
	// filters appends the request's optional WHERE clauses (their
	// arguments through arg) — typically listFilters.clauses plus any
	// type-specific extras.
	filters func(active []fieldcatalog.Column, sorted *storekit.ListSort, arg func(any) int) ([]string, error)
	// scan drains one page's rows into records plus, under a non-default
	// sort, each row's trailing __cursor_key.
	scan func(rows pgx.Rows, active []fieldcatalog.Column, sorted *storekit.ListSort) ([]T, []*string, error)
	// attach loads the page's child rows (emails/phones, domains) in the
	// same transaction as the page read.
	attach func(ctx context.Context, tx pgx.Tx, recs []T) error
	// cursorKey exposes the last record's keyset identity for the
	// next-page cursor.
	cursorKey func(last T) (time.Time, ids.UUID)
}

// listPage is the shared list read every spec runs through.
func listPage[T any](ctx context.Context, s *Store, sortSpec *string, limitIn *int, spec listPageSpec[T]) ([]T, storekit.Page, error) {
	if err := auth.Require(ctx, spec.entity, principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	active, err := s.activeColumns(ctx, spec.entity)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	sorted, err := storekit.ParseListSort(sortSpec, storekit.SortVocabulary(spec.fields, active))
	if err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(limitIn)

	where := []string{whereAlways}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }

	scope, err := auth.ScopeClauseFor(ctx, spec.entity, "", arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}

	filters, err := spec.filters(active, sorted, arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	where = append(where, filters...)

	var recs []T
	var page storekit.Page
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+spec.columns+storekit.SelectSuffix(active)+sorted.CursorKeySuffix()+
				` FROM `+spec.entity+` WHERE `+strings.Join(where, " AND ")+
				sorted.OrderBy()+storekit.SQLf(` LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()

		var cursorKeys []*string
		if recs, cursorKeys, err = spec.scan(rows, active, sorted); err != nil {
			return err
		}
		if len(recs) > limit {
			recs = recs[:limit]
			createdAt, id := spec.cursorKey(recs[len(recs)-1])
			page = storekit.Page{HasMore: true, NextCursor: sorted.EncodePageCursor(cursorKeys[limit-1], createdAt, id)}
		}
		return spec.attach(ctx, tx, recs)
	})
	if recs == nil {
		recs = []T{}
	}
	return recs, page, err
}

// listFilters is the optional-filter set the record lists share; each
// list's contract input maps onto it, and type-specific extras (e.g. the
// organization classification) append alongside in the spec's filters.
type listFilters struct {
	IncludeArchived bool
	OwnerID         *ids.UserID
	Query           *string
	Cursor          *string
	CustomFilters   map[string]string
	// CapturedByKind filters on WHO created the row, matched against the
	// captured_by prefix. `agent` is the review list for the records an AI
	// created (ADR-0075/A121 §3a). capturedByKindClause checks it against the
	// generated enum, after authorization — declaring the enum in the contract
	// does not enforce it on the wire.
	CapturedByKind *string
	// AiWritten asks whether an AI wrote INTO the record — a different question
	// from who created it (ADR-0075/A121 §3a). aiWrittenClause spells it.
	AiWritten *bool
	// aiWritten* describe where this record type's AI-written values live: the
	// (table, parent column) pairs carrying per-value provenance, plus any
	// column on the record itself that holds an AI-written value.
	aiWrittenChildren [][2]string
	aiWrittenOwn      string
	// entity is the record's table name, used to qualify the predicate above.
	entity string
	// nameColumn is the quick-find target — the record's display column.
	nameColumn string
}

// capturedByKindClause is the ONE spelling of the provenance filter
// (ADR-0075/A121 §3a): it refuses a kind outside the contract's enum and, for
// an accepted one, builds the clause. It lives outside listFilters because the
// lead list builds its own WHERE chain rather than using that struct, and two
// copies of "which prefix counts as an AI" is exactly how the person list and
// the lead list end up disagreeing about what the review list contains.
//
// The check is HERE, in the store, rather than at the handler, because the
// store is where authorization runs. Both list paths call auth.Require before
// they assemble any clause, so an unauthorized caller gets the authorization
// answer whatever they typed. Validating at the handler inverts that: a caller
// with no read on this object learns their enum value was wrong — which is a
// probing oracle, and the opposite of the order the overlay shadows document
// ("Object RBAC before any parameter shaping").
//
// The vocabulary is the GENERATED one, so the accepted values cannot drift from
// the contract that publishes them.
//
// The whole LIKE pattern is ONE bound argument, never concatenated into the
// SQL. The enum values are plain ASCII words carrying no LIKE metacharacter
// today; binding the pattern is what keeps that true of a value the enum gains
// later.
func capturedByKindClause(kind *string, arg func(any) int) (string, bool, error) {
	// ABSENT is the only thing that means "no filter". An empty value is a
	// value, and it is not in the enum: reading it as absent hands an
	// unfiltered list to a caller who did ask to filter — the same
	// confident-wrong-answer failure as an unknown kind, so it gets the same
	// refusal. (The quick-find `q` above may legitimately be empty; a blank
	// search is no search. An enum has no such reading.)
	if kind == nil {
		return "", false, nil
	}
	if !crmcontracts.CapturedByKind(*kind).Valid() {
		return "", false, httperr.Validation("captured_by_kind", "invalid",
			"must be one of human, agent, connector, system")
	}
	return storekit.SQLf("captured_by LIKE $%d", arg(*kind+":%")), true, nil
}

// agentPrefix matches the captured_by grammar's AI namespace. Shared by every
// predicate below so "which prefix counts as an AI" has one answer, and it is
// the same one the partial indexes in migration 0138 are built on — a mismatch
// there silently costs the index rather than the result.
const agentPrefix = "agent:%"

// The parent-id columns the per-value provenance tables key on. Named because
// the predicate below, the relationship edges and the site-read store all spell
// them, and a typo in one place is a silently empty EXISTS.
const (
	personIDColumn = "person_id"
	orgIDColumn    = "organization_id"
)

// aiWrittenClause answers "did an AI write into this record?" for one record
// type, or "" when the caller did not ask.
//
// This is deliberately NOT the same question as capturedByKindClause.
// `captured_by` names who CREATED the row and is never restamped — it is real
// provenance and the whole API reads it that way. But in the connector path the
// AI does not create the record, it FILLS one: Gmail capture mints the
// organization as `connector:gmail`, and then signature enrichment renames it
// and the web dossier writes its profile fields and facts. Asking who created
// it misses exactly the records worth reviewing.
//
// So the predicate is about CONTENT, and it is DERIVED rather than stored: a
// union over every row-set that records who-wrote-what. A denormalised flag
// would be a second copy of a truth those rows already hold, and a stale
// `false` would hide the very records this exists to surface.
//
// `field_provenance` is the GENERAL one and carries the most weight: it holds
// one row per (object, field) written, for every record type, so an agent that
// updates an ordinary column is caught here without this predicate having to
// know which column or which agent. That matters more than the specific tables
// below — a new agent write site is covered the moment it stamps provenance,
// which storekit.StampFields is the one way to do.
//
// childTables are the per-value evidence tables that carry their own
// captured_by WITHOUT stamping field_provenance (the dossier's organization
// profile fields and facts); extraOwn is any column on the record itself that
// records an AI-written value (the organization's promoted display name, whose
// only marker is name_source). Both exist because those writers predate or sit
// beside the general table — they are schema facts, not a design choice.
func aiWrittenClause(want *bool, entity string, childTables [][2]string, extraOwn string, arg func(any) int) string {
	if want == nil {
		return ""
	}
	touched := []string{
		// Created by an agent.
		storekit.SQLf("%s.captured_by LIKE $%d", entity, arg(agentPrefix)),
		// Any column of it written by an agent, whichever column, whichever agent.
		storekit.SQLf(`EXISTS (SELECT 1 FROM field_provenance fp
			 WHERE fp.workspace_id = %s.workspace_id AND fp.object_type = $%d
			   AND fp.object_id = %s.id AND fp.captured_by LIKE $%d)`,
			entity, arg(entity), entity, arg(agentPrefix)),
	}
	for _, child := range childTables {
		touched = append(touched, storekit.SQLf(
			`EXISTS (SELECT 1 FROM %s c WHERE c.workspace_id = %s.workspace_id
			           AND c.%s = %s.id AND c.captured_by LIKE $%d)`,
			child[0], entity, child[1], entity, arg(agentPrefix)))
	}
	if extraOwn != "" {
		touched = append(touched, extraOwn)
	}
	clause := "(" + strings.Join(touched, " OR ") + ")"
	if !*want {
		return "NOT " + clause
	}
	return clause
}

// clauses translates the filters into WHERE clauses, appending their
// arguments through arg — archived visibility, owner, provenance,
// quick-find, custom-field equality, and the keyset cursor.
func (f listFilters) clauses(active []fieldcatalog.Column, sorted *storekit.ListSort, arg func(any) int) ([]string, error) {
	var where []string
	if !f.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if f.OwnerID != nil {
		where = append(where, storekit.SQLf("owner_id = $%d", arg(*f.OwnerID)))
	}
	clause, ok, err := capturedByKindClause(f.CapturedByKind, arg)
	if err != nil {
		return nil, err
	}
	if ok {
		where = append(where, clause)
	}
	if ai := aiWrittenClause(f.AiWritten, f.entity, f.aiWrittenChildren, f.aiWrittenOwn, arg); ai != "" {
		where = append(where, ai)
	}
	if f.Query != nil && *f.Query != "" {
		where = append(where, storekit.QuickFindClause(arg(*f.Query), f.nameColumn))
	}
	cfClauses, err := storekit.CustomFilterClauses(active, f.CustomFilters, arg)
	if err != nil {
		return nil, err
	}
	where = append(where, cfClauses...)
	if f.Cursor != nil && *f.Cursor != "" {
		clause, err := sorted.KeysetClause(*f.Cursor, arg)
		if err != nil {
			return nil, err
		}
		where = append(where, clause)
	}
	return where, nil
}

// capturedByKindArg maps the optional provenance parameter onto the store
// input. The value is checked in capturedByKindClause, after authorization.
func capturedByKindArg[T ~string](v *T) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}
