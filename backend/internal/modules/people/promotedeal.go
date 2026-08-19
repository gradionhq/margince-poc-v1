// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Qualify-to-deal: a promote call may ask for a deal to be opened in the
// same transaction, so "this lead is now a contact with an opportunity" is
// one fact or none. The deal itself is the deals module's to write; people
// names what it needs through a port compose satisfies (ADR-0054: a module
// never imports a sibling).

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// QualifyDealInput is what a qualify call asks the deal opener for. Nil
// pipeline and stage mean the default pipeline's first open stage.
type QualifyDealInput struct {
	PipelineID  *ids.UUID
	StageID     *ids.UUID
	Name        string
	AmountMinor *int64
	Currency    *string
	OwnerID     *ids.UserID
	// Source is the deal's provenance value; the lead's id, so the deal says
	// where it came from.
	Source string
}

// LeadDealOpener opens one deal inside the caller's transaction and answers
// its id. Compose binds it to the deals store.
type LeadDealOpener interface {
	OpenDealForLead(ctx context.Context, tx pgx.Tx, in QualifyDealInput) (ids.UUID, error)
}

// WithDealOpener wires the deals-side seam the qualify dialog's "also open a
// deal" rides on. Without it a promote that asks for a deal is refused
// rather than quietly promoting without one.
func (s *Store) WithDealOpener(opener LeadDealOpener) *Store {
	s.dealOpener = opener
	return s
}

// DealOpenerNotWiredError maps to 422: the installation cannot open a deal
// from a qualify call.
type DealOpenerNotWiredError struct{}

func (e *DealOpenerNotWiredError) Error() string {
	return "opening a deal while qualifying is not available on this installation; qualify without a deal and open one from the deals screen"
}

// MessageFault names the condition; the remedy is to drop the deal block.
func (e *DealOpenerNotWiredError) MessageFault() (code, message string) {
	return "deal_opener_unavailable", e.Error()
}

// PromoteOutcome is what a promotion produced: the person, whether it merged
// into an existing one, and the deal opened alongside when one was asked for.
type PromoteOutcome struct {
	Person crmcontracts.Person
	Merged bool
	DealID *ids.UUID
}

// openQualifiedDeal runs after the lead is marked promoted, still under its
// row lock and inside the same transaction: the deal opens, and the lead
// points at it. A deal failure fails the whole promotion.
func (s *Store) openQualifiedDeal(ctx context.Context, tx pgx.Tx, leadID ids.LeadID, lead crmcontracts.Lead, personID ids.PersonID, in *QualifyDealInput) (*ids.UUID, error) {
	if in == nil {
		return nil, nil
	}
	if s.dealOpener == nil {
		return nil, &DealOpenerNotWiredError{}
	}
	deal := *in
	if strings.TrimSpace(deal.Name) == "" {
		deal.Name = defaultQualifiedDealName(lead)
	}
	deal.OwnerID = idArg[ids.UserKind](lead.OwnerId)
	deal.Source = "lead:" + leadID.String()
	dealID, err := s.dealOpener.OpenDealForLead(ctx, tx, deal)
	if err != nil {
		return nil, fmt.Errorf("open deal for qualified lead: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE lead SET qualified_deal_id = $2 WHERE id = $1`, leadID, dealID); err != nil {
		return nil, fmt.Errorf("link qualified deal: %w", err)
	}
	if err := seatPersonOnQualifiedDeal(ctx, tx, personID, dealID, deal.Source); err != nil {
		return nil, err
	}
	return &dealID, nil
}

// seatPersonOnQualifiedDeal makes the new contact a stakeholder on the deal
// the qualify call opened — the same edge the deal page's committee shows,
// and the edge that stops an undo of the qualification while the deal is
// live (demote's person_has_deal rule). Written here rather than through
// CreateRelationship because both endpoints were minted in this very
// transaction under the caller's own grants; nothing to re-probe.
func seatPersonOnQualifiedDeal(ctx context.Context, tx pgx.Tx, personID ids.PersonID, dealID ids.UUID, source string) error {
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return err
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO relationship (kind, person_id, deal_id, role, source, captured_by)
		VALUES ('deal_stakeholder', $1, $2, NULL, $3, $4)
		RETURNING `+relationshipColumns, personID, dealID, source, by)
	rel, err := scanRelationship(row)
	if err != nil {
		return fmt.Errorf("seat the contact on the qualified deal: %w", err)
	}
	return emitRelationshipChange(ctx, tx, "create", rel)
}

// defaultQualifiedDealName is the deal name when the dialog sends none: the
// company when the lead names one, else the person.
func defaultQualifiedDealName(lead crmcontracts.Lead) string {
	if lead.CompanyName != nil && strings.TrimSpace(*lead.CompanyName) != "" {
		return strings.TrimSpace(*lead.CompanyName)
	}
	if lead.FullName != nil && strings.TrimSpace(*lead.FullName) != "" {
		return strings.TrimSpace(*lead.FullName)
	}
	if lead.Email != nil {
		return string(*lead.Email)
	}
	return "Qualified lead"
}
