// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import "github.com/gradionhq/margince/backend/internal/shared/kernel/values"

// DealStatus and StageSemantic are the deal lifecycle vocabulary — the
// Go spelling of the deal_status and stage semantic CHECKs (0006), kept
// in sync by the enumsync fitness gate. Domain logic branches on these
// constants, never on raw literals. The two sets share values by design
// (a stage's semantic derives the deal's status), but they are distinct
// vocabularies: a stage is never "open-ish", a deal never "a column".
type DealStatus string

const (
	DealOpen DealStatus = "open"
	DealWon  DealStatus = "won"
	DealLost DealStatus = "lost"
)

type StageSemantic string

const (
	SemanticOpen StageSemantic = "open"
	SemanticWon  StageSemantic = "won"
	SemanticLost StageSemantic = "lost"
)

// Terminal reports whether a stage closes the deal.
func (s StageSemantic) Terminal() bool { return s == SemanticWon || s == SemanticLost }

// ParseStageSemantic is the config seam's membership check (pipeline
// and stage editing take the semantic from the client).
func ParseStageSemantic(raw string) (StageSemantic, error) {
	switch s := StageSemantic(raw); s {
	case SemanticOpen, SemanticWon, SemanticLost:
		return s, nil
	}
	return "", &values.ParseError{Field: "semantic", Code: "invalid_stage_semantic",
		Message: "semantic is one of open, won, lost"}
}

// Offer status needs no local vocabulary: the generated contract enum
// (crmcontracts.OfferStatus + its constants) is the source of truth the
// stores compare against.

// ProposalState is the offer line's approval vocabulary — the Go
// spelling of the offer_line_item.proposal_state CHECK (0059). A staged
// line is an AI-drafted proposal awaiting human acceptance (E03.21a):
// it never contributes to the server-computed offer totals, so a draft
// can never move a number the buyer sees. Not exposed on the contract —
// the accept transition is store-internal until a drafting surface ships.
type ProposalState string

const (
	ProposalStaged   ProposalState = "staged"
	ProposalAccepted ProposalState = "accepted"
)
