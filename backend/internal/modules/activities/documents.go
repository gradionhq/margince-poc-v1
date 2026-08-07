// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The account's document library, and the metadata a human asserts on a file.
//
// A document reachable from a company may hang off a deal, a person, an activity
// or the company itself, and each of those has its OWN visibility. So the
// roll-up scopes every candidate through its own primary parent rather than
// filtering afterwards: a contract on a deal the viewer cannot see contributes
// neither a row nor a count. A total that includes invisible rows tells the
// viewer something about them, which is the disclosure the parent gate exists to
// prevent (DOC-AC-2).
//
// `organization_id` on the row is a READ PATH, not a second parent. It makes the
// roll-up affordable at a hundred documents; it never decides who may see one.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// DocumentFilters narrows the account library. Each is a selection; omitted
// means no filter, which is the one input that is not a choice.
type DocumentFilters struct {
	Category *string
	DocState *string
	// PinnedOnly is the "what matters here" view. False is not a filter for
	// unpinned — it is the absence of one.
	PinnedOnly bool
	Cursor     *string
	Limit      *int
}

// ListOrganizationDocuments returns every document rolling up to one account,
// pinned first and then newest.
//
// The caller must be able to read the ACCOUNT to ask the question at all; each
// row then passes its own parent's gate. Both are needed: the first stops the
// endpoint being an oracle for accounts the caller cannot see, the second stops
// the roll-up widening what a parent already refuses.
func (s *Store) ListOrganizationDocuments(
	ctx context.Context, orgID ids.UUID, in DocumentFilters,
) ([]crmcontracts.Attachment, storekit.Page, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	lim := storekit.ClampLimit(in.Limit)
	var (
		out  []crmcontracts.Attachment
		page storekit.Page
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", orgID); err != nil {
			return err
		}
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		where := []string{
			fmt.Sprintf("at.organization_id = $%d", arg(orgID)),
			"at.archived_at IS NULL",
		}
		// Keyset, not offset: the library is ordered pinned-then-newest and a
		// page boundary has to survive a pin being added between two reads.
		if in.Cursor != nil && *in.Cursor != "" {
			c, err := storekit.DecodeCursor(*in.Cursor)
			if err != nil {
				return err
			}
			where = append(where, fmt.Sprintf("(at.created_at, at.id) < ($%d, $%d)",
				arg(c.CreatedAt), arg(c.ID)))
		}
		where = append(where, filterClauses(in, arg)...)
		visible, err := visibleParentClause(ctx, arg)
		if err != nil {
			return err
		}
		where = append(where, visible)

		rows, err := tx.Query(ctx, fmt.Sprintf(`
			SELECT %s FROM attachment at
			 WHERE %s
			 ORDER BY at.pinned DESC, at.created_at DESC, at.id DESC
			 LIMIT %d`,
			attachmentColumns, strings.Join(where, " AND "), lim+1), args...)
		if err != nil {
			return fmt.Errorf("activities: listing the account's documents: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			att, err := scanAttachment(rows)
			if err != nil {
				return err
			}
			out = append(out, att)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("activities: iterating the account's documents: %w", err)
		}
		if len(out) > lim {
			out = out[:lim]
			last := out[len(out)-1]
			page = storekit.Page{
				HasMore:    true,
				NextCursor: storekit.EncodeCursor(last.CreatedAt, ids.UUID(last.Id)),
			}
		}
		return nil
	})
	if out == nil {
		out = []crmcontracts.Attachment{}
	}
	return out, page, err
}

// filterClauses renders the caller's selections. Each is a SELECTION: an omitted
// filter is the absence of one, never a filter for the opposite value.
func filterClauses(in DocumentFilters, arg func(any) int) []string {
	var where []string
	if in.Category != nil {
		where = append(where, fmt.Sprintf("at.category = $%d", arg(*in.Category)))
	}
	if in.DocState != nil {
		where = append(where, fmt.Sprintf("at.doc_state = $%d", arg(*in.DocState)))
	}
	if in.PinnedOnly {
		where = append(where, "at.pinned")
	}
	return where
}

// visibleParentClause renders "this file's own primary parent is one the caller
// may read", per entity type.
//
// Written as a disjunction over the parent kinds rather than a join, because
// each kind has a different gate: an activity scopes through the link walk, the
// owner-scoped records through their own row-scope clause. A single join could
// only express one of them, and whichever it chose would be wrong for the rest.
func visibleParentClause(ctx context.Context, arg func(any) int) (string, error) {
	arms := make([]string, 0, len(documentParentKinds))
	for _, kind := range documentParentKinds {
		// No grant on this parent type is not an error: it removes that arm, so
		// the caller sees the account's other documents and never learns a file
		// of that kind exists. Denial here would refuse the whole library over
		// one kind the reader was never entitled to.
		if err := auth.Require(ctx, kind, principal.ActionRead); err != nil {
			if errors.Is(err, apperrors.ErrPermissionDenied) {
				continue
			}
			return "", err
		}
		clause, err := auth.ScopeClauseFor(ctx, kind, "p", arg)
		if err != nil {
			return "", err
		}
		if clause == "" {
			clause = scopeUnbounded
		}
		arms = append(arms, fmt.Sprintf(
			"(at.entity_type = '%s' AND EXISTS (SELECT 1 FROM %s p WHERE p.id = at.entity_id AND %s))",
			kind, kind, clause))
	}
	if len(arms) == 0 {
		// The caller may read the account and none of the kinds a document can
		// hang off. An empty library is the honest answer, and FALSE is how it
		// is spelled without the query pretending there is nothing there.
		return "FALSE", nil
	}
	return "(" + strings.Join(arms, " OR ") + ")", nil
}

// scopeUnbounded is the predicate an unbounded caller's empty row-scope clause
// renders as, so a disjunction arm stays a valid boolean expression.
const scopeUnbounded = "TRUE"

// documentParentKinds are the records a document can hang off and be rolled up
// to an account from. `activity` is deliberately absent: its visibility is the
// link walk, not a row-scope clause, and expressing it as one here would widen
// it. Activity-borne files reach the library through the account pointer their
// activity carries, and are gated in ListAttachments on that activity.
var documentParentKinds = []string{linkEntityOrganization, linkEntityDeal, linkEntityPerson}

// DocumentMetadata is the sparse patch a human makes over what a file MEANS.
// The bytes, the filename, the checksum and the scan state are absent on
// purpose: they are what arrived, and letting a human edit them would make the
// record a description of itself rather than of the file.
type DocumentMetadata struct {
	Category   *string
	Title      *string
	ClearTitle bool
	DocState   *string
	Pinned     *bool
	Supersedes *ids.UUID
	// ClearSupersedes distinguishes "leave the pointer alone" from "this
	// document replaces nothing after all". A nil pointer cannot say which.
	ClearSupersedes bool
	IfVersion       *int64
}

// UpdateAttachmentMetadata sets what a document is, what state it is in, and
// what it replaces.
//
// Authority inherits from the parent, like every other attachment operation: the
// caller must hold Update on the parent's object type and be able to see the
// parent row. A file whose parent is out of scope answers not-found rather than
// denied, because learning that a document exists is the disclosure.
func (s *Store) UpdateAttachmentMetadata(
	ctx context.Context, id ids.UUID, in DocumentMetadata,
) (crmcontracts.Attachment, error) {
	var out crmcontracts.Attachment
	err := s.tx(ctx, func(tx pgx.Tx) error {
		entityType, err := resolveVisibleAttachmentParent(ctx, tx, id, principal.ActionUpdate)
		if err != nil {
			return err
		}
		before, err := readAttachment(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := refuseSupersedesCycle(ctx, tx, id, in); err != nil {
			return err
		}

		p := storekit.NewPatch()
		if in.Category != nil {
			p.Set("category", before.Category, *in.Category)
		}
		if in.Title != nil || in.ClearTitle {
			p.Set("title", before.Title, in.Title)
		}
		if in.DocState != nil {
			p.Set("doc_state", before.DocState, *in.DocState)
		}
		if in.Pinned != nil {
			p.Set("pinned", before.Pinned, *in.Pinned)
		}
		if in.Supersedes != nil || in.ClearSupersedes {
			p.Set("supersedes_id", before.SupersedesId, in.Supersedes)
		}
		if p.Empty() {
			out = before
			return nil
		}
		if err := p.ApplyGuarded(ctx, tx, "attachment", id, in.IfVersion); err != nil {
			return err
		}
		// Audited against the PARENT's object type, which is where the authority
		// came from: an auditor asking who may change this file reads the same
		// answer the gate above applied.
		if _, err := storekit.Audit(ctx, tx, "update", "attachment", id,
			p.Before(), p.After()); err != nil {
			return fmt.Errorf("activities: auditing document metadata on a %s attachment: %w", entityType, err)
		}
		out, err = readAttachment(ctx, tx, id)
		return err
	})
	return out, err
}

// refuseSupersedesCycle stops a document from replacing something that already
// replaces it, directly or through a chain.
//
// The one-step case is a row CHECK; a chain is not expressible as one, and a
// cycle here is not cosmetic — every reader that walks "what replaced this"
// would loop forever on it.
func refuseSupersedesCycle(ctx context.Context, tx pgx.Tx, id ids.UUID, in DocumentMetadata) error {
	if in.Supersedes == nil {
		return nil
	}
	var closes bool
	if err := tx.QueryRow(ctx, `
		WITH RECURSIVE chain AS (
			SELECT id, supersedes_id FROM attachment WHERE id = $1
			UNION ALL
			SELECT a.id, a.supersedes_id
			  FROM attachment a JOIN chain c ON a.id = c.supersedes_id
		)
		SELECT EXISTS (SELECT 1 FROM chain WHERE id = $2)`,
		*in.Supersedes, id).Scan(&closes); err != nil {
		return fmt.Errorf("activities: checking the supersedes chain: %w", err)
	}
	if closes {
		return &values.ParseError{
			Field: "supersedes_id", Code: "supersedes_cycle",
			Message: "that document already replaces this one, directly or through a chain",
		}
	}
	return nil
}
