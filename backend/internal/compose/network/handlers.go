// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package network

// The HTTP surface: who knows this contact, and how is this deal covered.

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
)

// Reads serves the network HTTP surface.
//
// Named Reads rather than Handlers because compose embeds it alongside the
// briefs handlers, and two embedded types called Handlers collide.
type Reads struct {
	pool *pgxpool.Pool
	// now is injected so the decayed scores are testable against a fixed
	// clock. A score that reads time.Now() inside the handler cannot be
	// asserted on without sleeping.
	now func() time.Time
}

// NewReads builds the network surface over the pool.
func NewReads(pool *pgxpool.Pool) Reads {
	return Reads{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

// GetPersonNetwork implements GET /people/{id}/network.
func (h Reads) GetPersonNetwork(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	personID := ids.From[ids.PersonKind](ids.UUID(id))
	now := h.now()
	var out crmcontracts.PersonNetwork
	out.PersonId = id
	out.Colleagues = []crmcontracts.PersonNetworkColleague{}

	ctx := r.Context()
	err := database.WithWorkspaceTx(ctx, h.pool, func(tx pgx.Tx) error {
		// EdgesForPerson carries both gates: the person grant, and the row
		// probe that 404s a contact this caller cannot read. Capture privacy
		// rides that probe, so an unpromoted contact discloses neither its
		// existence nor who talks to it.
		edges, err := search.EdgesForPerson(ctx, tx, personID.UUID, personNetworkCap)
		if err != nil {
			return err
		}
		names, err := userNames(ctx, tx, edgeUsers(edges))
		if err != nil {
			return err
		}
		for _, e := range edges {
			out.Colleagues = append(out.Colleagues, wireColleague(e, names[e.UserID], now))
		}
		return nil
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}

// personNetworkCap bounds the answer. The question is "who should I ask", and
// nobody reads past the tenth name; an uncapped list would also make the
// payload grow with a contact's history rather than with its relevance.
const personNetworkCap = 10

// GetDealCoverage implements GET /deals/{id}/coverage.
func (h Reads) GetDealCoverage(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	dealID := ids.From[ids.DealKind](ids.UUID(id))
	now := h.now()
	var out crmcontracts.DealCoverage
	out.DealId = id

	ctx := r.Context()
	err := database.WithWorkspaceTx(ctx, h.pool, func(tx pgx.Tx) error {
		// The deal gate first: a coverage payload names the deal's people, so
		// a caller who cannot read the deal must not learn who sits on it.
		if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
			return err
		}
		if err := auth.EnsureVisible(ctx, tx, "deal", dealID.UUID); err != nil {
			return err
		}
		coverage, err := CoverageFor(ctx, tx, dealID, now)
		if err != nil {
			return err
		}
		names, err := userNames(ctx, tx, coverageUsers(coverage))
		if err != nil {
			return err
		}
		out = wireCoverage(coverage, names)
		return nil
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}

// wireColleague renders one edge. A `none` band carries NO number: "we have
// never spoken" and "we spoke and it went cold" are different facts, and a
// zero renders them identically.
func wireColleague(e search.InteractionEdge, name string, now time.Time) crmcontracts.PersonNetworkColleague {
	score := e.StrengthOf(now)
	out := crmcontracts.PersonNetworkColleague{
		UserId:          openapi_types.UUID(e.UserID),
		DisplayName:     name,
		StrengthBucket:  crmcontracts.PersonNetworkColleagueStrengthBucket(score.Bucket),
		Interactions90d: e.Count90d,
	}
	if score.Bucket != relstrength.BucketNone {
		strength := score.Strength
		out.Strength = &strength
	}
	last := e.LastAt
	out.LastAt = &last
	return out
}

func wireCoverage(c DealCoverage, names map[ids.UUID]string) crmcontracts.DealCoverage {
	out := crmcontracts.DealCoverage{
		DealId:       openapi_types.UUID(c.DealID),
		Stakeholders: []crmcontracts.DealCoverageSeat{},
		OurSide:      []crmcontracts.PersonNetworkColleague{},
		Risks:        []crmcontracts.DealCoverageRisk{},
	}
	for _, s := range c.Stakeholders {
		out.Stakeholders = append(out.Stakeholders, crmcontracts.DealCoverageSeat{
			PersonId: openapi_types.UUID(s.PersonID), Role: s.Role, Engaged: s.Engaged,
		})
	}
	for _, e := range c.OurSide {
		colleague := crmcontracts.PersonNetworkColleague{
			UserId:          openapi_types.UUID(e.UserID),
			DisplayName:     names[e.UserID],
			StrengthBucket:  crmcontracts.PersonNetworkColleagueStrengthBucket(e.Strength.Bucket),
			Interactions90d: e.Count90d,
		}
		if e.Strength.Bucket != relstrength.BucketNone {
			strength := e.Strength.Strength
			colleague.Strength = &strength
		}
		out.OurSide = append(out.OurSide, colleague)
	}
	for _, r := range c.Risks {
		out.Risks = append(out.Risks, crmcontracts.DealCoverageRisk{
			Kind:      crmcontracts.DealCoverageRiskKind(r.Kind),
			Summary:   r.Summary,
			PersonIds: wireIDs(r.PersonIDs),
			UserIds:   wireIDs(r.UserIDs),
		})
	}
	return out
}

func wireIDs(in []ids.UUID) *[]openapi_types.UUID {
	if len(in) == 0 {
		return nil
	}
	out := make([]openapi_types.UUID, 0, len(in))
	for _, id := range in {
		out = append(out, openapi_types.UUID(id))
	}
	return &out
}

func edgeUsers(edges []search.InteractionEdge) []ids.UUID {
	out := make([]ids.UUID, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.UserID)
	}
	return out
}

func coverageUsers(c DealCoverage) []ids.UUID {
	out := make([]ids.UUID, 0, len(c.OurSide))
	for _, e := range c.OurSide {
		out = append(out, e.UserID)
	}
	return out
}

// userNames resolves the colleagues' display names in one read. The roster is
// readable by any authenticated member, so naming a colleague on a record the
// caller can already open discloses nothing new.
func userNames(ctx context.Context, tx pgx.Tx, users []ids.UUID) (map[ids.UUID]string, error) {
	out := map[ids.UUID]string{}
	if len(users) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx,
		`SELECT id, display_name FROM app_user WHERE id = ANY($1)`, users)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id ids.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}
