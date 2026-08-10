// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Putting a deal's coverage risks into the assembled context (ADR-0078).
//
// The risk rules live in compose/network because every one of them joins deals,
// people and the interaction projection, and a module never imports a sibling.
// The retriever lives in search. So the assistant summarising a deal could see
// its timeline and its related records, and could not see that the account has
// been silent for six weeks or that the champion has left — the two facts a
// human would lead with.
//
// This is the join, made where every cross-module edge in this codebase is
// made. It decorates the retriever rather than widening the seam: the
// retrieval port stays a port, and search keeps knowing nothing about deals.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/network"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// riskAwareRetriever appends a deal anchor's coverage findings to the context
// the inner retriever assembled.
type riskAwareRetriever struct {
	pool  *pgxpool.Pool
	inner retrieval.Retriever
}

// Search is untouched — risks are a property of an anchor, and a search result
// set has none.
func (r riskAwareRetriever) Search(ctx context.Context, q retrieval.Query) (retrieval.Result, error) {
	return r.inner.Search(ctx, q)
}

// AssembleContext runs the inner walk, then adds `network_risks` on a deal.
//
// The risks are read in their OWN transaction: the retrieval port hands out
// none, and threading one through it would make every implementer take a
// database. So the two reads can be milliseconds apart and a risk can describe
// a deal a concurrent write just changed — the staleness any two-request client
// already sees. GET /deals/{id}/coverage is the single-snapshot answer.
func (r riskAwareRetriever) AssembleContext(ctx context.Context, anchor datasource.EntityRef, opts retrieval.AssembleOptions) (retrieval.Context, error) {
	out, err := r.inner.AssembleContext(ctx, anchor, opts)
	if err != nil {
		return out, err
	}
	if anchor.Type != datasource.EntityDeal {
		return out, nil
	}
	section, err := r.riskSection(ctx, anchor.ID)
	if err != nil {
		// A grant that vanished between the walk and this read drops the
		// SECTION, not the answer. The same policy the at-risk sweep takes: a
		// coverage summary is advisory, and throwing away a fully assembled
		// timeline because one advisory read was refused turns a revoked grant
		// into a broken assistant. Anything else still fails loudly.
		if errors.Is(err, apperrors.ErrNotFound) || errors.Is(err, apperrors.ErrPermissionDenied) {
			return out, nil
		}
		return retrieval.Context{}, err
	}
	if len(section.Items) > 0 {
		out.Sections = append(out.Sections, section)
	}
	return out, nil
}

func (r riskAwareRetriever) riskSection(ctx context.Context, dealID ids.UUID) (retrieval.Section, error) {
	section := retrieval.Section{Name: "network_risks"}
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		// The same deal gate the coverage endpoint and the coverage tool take.
		// The inner walk already proved the caller can read this deal, but the
		// gate is re-taken rather than assumed: a context assembly that trusted
		// a previous read for its authority would be the one place in this
		// codebase where authority travels between reads.
		if err := requireVisibleDeal(ctx, tx, dealID); err != nil {
			return err
		}
		coverage, err := network.CoverageFor(ctx, tx, ids.From[ids.DealKind](dealID), clockNow())
		if err != nil {
			return err
		}
		for _, risk := range coverage.Risks {
			section.Items = append(section.Items, retrieval.Item{
				Ref:     datasource.EntityRef{Type: datasource.EntityDeal, ID: dealID},
				Summary: risk.Summary,
				// The evidence source names the RULE, not the deal row, so a
				// model quoting the finding cites the rule that produced it and
				// a reader can go look the rule up.
				Evidence: []retrieval.Evidence{{
					Source:  "deal_coverage_risk:" + risk.Kind,
					Snippet: fmt.Sprintf("%s: %s", risk.Kind, risk.Summary),
				}},
			})
		}
		return nil
	})
	if err != nil {
		return retrieval.Section{}, err
	}
	return section, nil
}
