// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

// What is commercially at stake with this person, and who else is in the room.
//
// The deal-roles section beside this one lists every seat this person holds;
// this answers the different question the page opens with — which ONE deal
// matters now, what money is on it, and who else has to be convinced.
//
// The role is always what the relationship edge RECORDS. It is never inferred
// from a job title: a CFO is not automatically the economic buyer, and a page
// that guessed would be asserting a fact about someone's authority that nobody
// wrote down.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// committeeCap bounds who else is shown. Past a handful this is an org chart,
// and the question the card answers is "who else do I have to convince".
const committeeCap = 8

// scopeAll is the permissive predicate a row-scope clause reduces to when the
// caller's grants add no restriction of their own. Spelled once so a reader
// grepping for the scope seam finds every site.
const scopeAll = "true"

// commercialSection reads the one open deal that matters and the committee
// around it.
//
// Absent and empty are different answers here, and the section is careful to
// produce the right one: a caller with no deal grant gets the section OMITTED
// and named, while a caller who may see deals and has none gets the section
// present with a null deal. "No open deal" and "you may not see deals" would
// otherwise render identically, which is the disclosure the omit-and-name
// discipline exists to prevent.
func (s *Service) commercialSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, out *crmcontracts.Person360) error {
	if err := requireRead(ctx, "relationship"); err != nil {
		return err
	}
	if err := requireRead(ctx, "deal"); err != nil {
		return err
	}
	commercial := crmcontracts.Person360Commercial{Committee: []crmcontracts.Person360CommitteeMember{}}
	seat, found, err := s.leadingDealSeat(ctx, tx, personID)
	if err != nil {
		return err
	}
	if !found {
		out.Commercial = &commercial
		return nil
	}
	commercial.Deal = &seat.deal
	commercial.Role = &seat.role
	committee, err := s.committeeFor(ctx, tx, personID, seat.dealID)
	if err != nil {
		return err
	}
	commercial.Committee = committee
	out.Commercial = &commercial
	return nil
}

// dealSeat is one person's seat on one deal, with the deal's own figures.
type dealSeat struct {
	dealID ids.UUID
	role   string
	deal   crmcontracts.Person360CommercialDeal
}

// leadingDealSeat picks the open deal this person sits on that the page should
// lead with: the one closing soonest, because a date is the only ordering the
// data supports without inventing a relevance score.
//
// "Open" is spelled the way every other surface spells it — status open, not
// archived — so a deal this card names can never be one the deals page would
// refuse to show.
func (s *Service) leadingDealSeat(ctx context.Context, tx pgx.Tx, personID ids.PersonID) (dealSeat, bool, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	personPos := arg(personID)
	dealScope, err := auth.ScopeClauseFor(ctx, "deal", "d", arg)
	if err != nil {
		return dealSeat{}, false, err
	}
	if dealScope == "" {
		dealScope = scopeAll
	}
	var seat dealSeat
	var stage, currency *string
	var amount *int64
	var closeDate *openapi_types.Date
	err = tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT d.id, r.role, d.name, s.name, d.amount_minor, d.currency, d.expected_close_date
		FROM relationship r
		JOIN deal d ON d.id = r.deal_id AND d.status = 'open' AND d.archived_at IS NULL
		LEFT JOIN stage s ON s.id = d.stage_id
		WHERE r.kind = 'deal_stakeholder' AND r.person_id = $%d
		  AND r.archived_at IS NULL AND (%s)
		ORDER BY d.expected_close_date NULLS LAST, d.id
		LIMIT 1`, personPos, dealScope), args...).
		Scan(&seat.dealID, &seat.role, &seat.deal.Title, &stage, &amount, &currency, &closeDate)
	if errors.Is(err, pgx.ErrNoRows) {
		return dealSeat{}, false, nil
	}
	if err != nil {
		return dealSeat{}, false, fmt.Errorf("read the leading deal seat: %w", err)
	}
	seat.deal.DealId = openapi_types.UUID(seat.dealID)
	seat.deal.Stage = stage
	seat.deal.AmountMinor = amount
	seat.deal.Currency = currency
	seat.deal.CloseDate = closeDate
	return seat, true, nil
}

// committeeFor reads the other stakeholders on the same deal.
//
// Each row carries the person row-scope predicate: a colleague the caller may
// not see is not disclosed by being listed here, and an empty committee is
// then honestly "nobody else I can show you" rather than "single-threaded".
func (s *Service) committeeFor(ctx context.Context, tx pgx.Tx, personID ids.PersonID, dealID ids.UUID) ([]crmcontracts.Person360CommitteeMember, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	dealPos := arg(dealID)
	personPos := arg(personID)
	personScope, err := auth.ScopeClauseFor(ctx, "person", "p", arg)
	if err != nil {
		return nil, err
	}
	if personScope == "" {
		personScope = scopeAll
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT p.id, p.full_name, r.role, p.photo_object_key
		FROM relationship r
		JOIN person p ON p.id = r.person_id AND p.archived_at IS NULL
		WHERE r.kind = 'deal_stakeholder' AND r.deal_id = $%d
		  AND r.person_id <> $%d AND r.archived_at IS NULL AND (%s)
		ORDER BY p.full_name, p.id
		LIMIT %d`, dealPos, personPos, personScope, committeeCap), args...)
	if err != nil {
		return nil, fmt.Errorf("read the buying committee: %w", err)
	}
	defer rows.Close()

	committee := make([]crmcontracts.Person360CommitteeMember, 0, committeeCap)
	for rows.Next() {
		var member crmcontracts.Person360CommitteeMember
		var id ids.UUID
		var photoKey *string
		if err := rows.Scan(&id, &member.FullName, &member.Role, &photoKey); err != nil {
			return nil, fmt.Errorf("scan a committee member: %w", err)
		}
		member.PersonId = openapi_types.UUID(id)
		member.PhotoUrl = personPhotoURL(id, photoKey)
		committee = append(committee, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the buying committee: %w", err)
	}
	return committee, nil
}

// personPhotoURL points at the stream endpoint when a photo was uploaded, and
// nowhere when one was not — the client draws its deterministic monogram,
// which is the no-photo face rather than a broken image.
func personPhotoURL(personID ids.UUID, objectKey *string) *string {
	if objectKey == nil || *objectKey == "" {
		return nil
	}
	url := fmt.Sprintf("/v1/people/%s/photo", personID)
	return &url
}
