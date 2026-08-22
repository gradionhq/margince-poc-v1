// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// A deal's Files area: the files uploaded on the deal itself and the files of
// every message linked to it. A captured email's attachment hangs off the
// ACTIVITY, so without the link walk here an emailed contract is unreachable
// from the deal it is about — which is the gap this read closes.
//
// The link is READ, never stamped. `attachment.deal_id` exists in the schema
// and nothing writes it: a message can be relinked, and a column copied at
// capture would go stale the moment it was. Reading through `activity_link`
// is always the current answer.
//
// Taking a captured file off a deal is a HIDE, never an archive. The file
// belongs to its message: it stays on the activity and in the company
// library, and only this deal stops listing it.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// DealDocumentFilters narrows a deal's Files area. Omitted means no filter.
type DealDocumentFilters struct {
	Category      *string
	IncludeHidden bool
	Cursor        *string
	Limit         *int
}

// inlineImageCeiling is the size under which an image part of a captured
// message is taken for a signature logo or an icon and left out of the Files
// area. Capture does not mark inline parts, so the rule is the honest proxy:
// a contract scan is never this small, and a logo is never larger.
const inlineImageCeiling = 65536

// fieldDealID names the deal in a hide's audit image.
const fieldDealID = "deal_id"

// dealDocumentMembership says which attachments a deal's Files area holds,
// before any caller-bound visibility: the deal's own uploads, and the files of
// an activity linked to the deal, minus inline mail images. `$deal` is the
// placeholder the caller bound the deal id to.
func dealDocumentMembership(deal int) string {
	return fmt.Sprintf(`at.archived_at IS NULL AND (
		(at.entity_type = '%s' AND at.entity_id = $%d)
		OR (at.entity_type = '%s' AND EXISTS (
			SELECT 1 FROM activity_link l
			 WHERE l.activity_id = at.entity_id AND l.entity_type = '%s' AND l.deal_id = $%d)
			AND NOT (at.content_type LIKE 'image/%%' AND COALESCE(at.byte_size, 0) < %d)))`,
		linkEntityDeal, deal, linkEntityActivity, linkEntityDeal, deal, inlineImageCeiling)
}

// ListDealDocuments returns the deal's Files area, newest first. The caller
// must read the deal; each row then passes its own parent's gate, so a
// captured file follows the message's audience and a colleague outside it sees
// a shorter list than the mailbox owner.
func (s *Store) ListDealDocuments(
	ctx context.Context, dealID ids.UUID, in DealDocumentFilters,
) ([]crmcontracts.DealDocument, storekit.Page, error) {
	if err := auth.Require(ctx, linkEntityDeal, principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	lim := storekit.ClampLimit(in.Limit)
	var (
		out  []crmcontracts.DealDocument
		page storekit.Page
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, linkEntityDeal, dealID); err != nil {
			return err
		}
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		deal := arg(dealID)
		where := []string{dealDocumentMembership(deal)}
		if !in.IncludeHidden {
			where = append(where, fmt.Sprintf("NOT EXISTS (SELECT 1 FROM deal_document_hide h WHERE h.deal_id = $%d AND h.attachment_id = at.id)", deal))
		}
		if in.Category != nil {
			where = append(where, fmt.Sprintf("at.category = $%d", arg(*in.Category)))
		}
		if in.Cursor != nil && *in.Cursor != "" {
			c, err := storekit.DecodeCursor(*in.Cursor)
			if err != nil {
				return err
			}
			where = append(where, fmt.Sprintf("(at.created_at, at.id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID)))
		}
		visible, err := visibleParentClause(ctx, arg)
		if err != nil {
			return err
		}
		where = append(where, visible)
		rows, err := tx.Query(ctx, fmt.Sprintf(`
			SELECT %s,
			       EXISTS (SELECT 1 FROM deal_document_hide h WHERE h.deal_id = $%d AND h.attachment_id = at.id) AS hidden,
			       a.id, a.kind, a.subject, a.occurred_at, a.counterparty_email
			  FROM attachment at
			  LEFT JOIN activity a ON at.entity_type = '%s' AND a.id = at.entity_id AND a.archived_at IS NULL
			 WHERE %s
			 ORDER BY at.created_at DESC, at.id DESC
			 LIMIT %d`,
			attachmentColumns, deal, linkEntityActivity, strings.Join(where, " AND "), lim+1), args...)
		if err != nil {
			return fmt.Errorf("activities: listing the deal's documents: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			doc, err := scanDealDocument(rows)
			if err != nil {
				return err
			}
			out = append(out, doc)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("activities: iterating the deal's documents: %w", err)
		}
		if len(out) > lim {
			out = out[:lim]
			last := out[len(out)-1].Attachment
			page = storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(last.CreatedAt, ids.UUID(last.Id))}
		}
		return nil
	})
	if out == nil {
		out = []crmcontracts.DealDocument{}
	}
	return out, page, err
}

// scanDealDocument reads the attachment columns plus the hide flag and the
// origin activity in one Scan. The origin join is on live activities only —
// a held row is archived by CHECK — and the file itself already passed the
// activity content clause through visibleParentClause, so a restricted
// message contributes neither a file nor an origin line.
func scanDealDocument(rows pgx.Rows) (crmcontracts.DealDocument, error) {
	var (
		cols         attachmentScan
		hidden       bool
		activityID   *ids.UUID
		kind         *string
		subject      *string
		occurredAt   *time.Time
		counterparty *string
	)
	dest := append(cols.targets(), &hidden, &activityID, &kind, &subject, &occurredAt, &counterparty)
	if err := rows.Scan(dest...); err != nil {
		return crmcontracts.DealDocument{}, fmt.Errorf("activities: scanning a deal document: %w", err)
	}
	doc := crmcontracts.DealDocument{Attachment: cols.attachment(), Hidden: hidden}
	if activityID != nil && kind != nil && occurredAt != nil {
		doc.Origin = &crmcontracts.DealDocumentOrigin{
			ActivityId: openapi_types.UUID(*activityID),
			Kind:       *kind,
			OccurredAt: *occurredAt,
			Subject:    subject,
		}
		if counterparty != nil {
			email := openapi_types.Email(*counterparty)
			doc.Origin.CounterpartyEmail = &email
		}
	}
	return doc, nil
}

// HideDealDocument takes a file off this deal's Files area. The caller needs
// update on the deal, and the file must be in the area for THIS caller; a miss
// of either reads as not-found, so the call is no oracle for files the caller
// cannot see. Idempotent.
func (s *Store) HideDealDocument(ctx context.Context, dealID, attachmentID ids.UUID) error {
	return s.setDealDocumentHidden(ctx, dealID, attachmentID, true)
}

// UnhideDealDocument lists the file on the deal again. Same gate as hiding.
func (s *Store) UnhideDealDocument(ctx context.Context, dealID, attachmentID ids.UUID) error {
	return s.setDealDocumentHidden(ctx, dealID, attachmentID, false)
}

func (s *Store) setDealDocumentHidden(ctx context.Context, dealID, attachmentID ids.UUID, hidden bool) error {
	if err := auth.Require(ctx, linkEntityDeal, principal.ActionUpdate); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureWritable(ctx, tx, linkEntityDeal, dealID); err != nil {
			return err
		}
		if err := ensureInDealDocuments(ctx, tx, dealID, attachmentID); err != nil {
			return err
		}
		by, err := storekit.CapturedBy(ctx)
		if err != nil {
			return err
		}
		if hidden {
			_, err = tx.Exec(ctx, `INSERT INTO deal_document_hide (deal_id, attachment_id, hidden_by)
			                       VALUES ($1, $2, $3) ON CONFLICT (deal_id, attachment_id) DO NOTHING`,
				dealID, attachmentID, by)
		} else {
			_, err = tx.Exec(ctx, `DELETE FROM deal_document_hide WHERE deal_id = $1 AND attachment_id = $2`,
				dealID, attachmentID)
		}
		if err != nil {
			return fmt.Errorf("activities: changing whether a deal lists a document: %w", err)
		}
		// The audit verb set is closed; a hide is an UPDATE to how the file is
		// listed, and the image says on which deal and which way.
		if _, err := storekit.Audit(ctx, tx, "update", "attachment", attachmentID,
			map[string]any{fieldDealID: dealID.String(), "hidden_from_deal": !hidden},
			map[string]any{fieldDealID: dealID.String(), "hidden_from_deal": hidden}); err != nil {
			return err
		}
		return nil
	})
}

// ensureInDealDocuments answers whether the file is in the deal's Files area
// for this caller, through the same membership and visibility the list uses —
// hidden or not, since unhiding has to reach a hidden row.
func ensureInDealDocuments(ctx context.Context, tx pgx.Tx, dealID, attachmentID ids.UUID) error {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	deal := arg(dealID)
	visible, err := visibleParentClause(ctx, arg)
	if err != nil {
		return err
	}
	var found bool
	err = tx.QueryRow(ctx, fmt.Sprintf(
		`SELECT EXISTS (SELECT 1 FROM attachment at WHERE at.id = $%d AND %s AND %s)`,
		arg(attachmentID), dealDocumentMembership(deal), visible), args...).Scan(&found)
	if err != nil {
		return fmt.Errorf("activities: checking a deal document: %w", err)
	}
	if !found {
		return apperrors.ErrNotFound
	}
	return nil
}
