// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// Who on our side can actually reach each contact.
//
// The V2 company page has to answer this per CONTACT rather than as a
// contact × every-colleague matrix. A forty-person sales team makes the matrix
// unreadable, and the reader's question is never "show me all the pairs" — it is
// "who should make this call". So each contact carries the few colleagues worth
// naming and a count of the rest.
//
// TWO STATES THAT LOOK ALIKE AND ARE NOT. A contact nobody has ever exchanged a
// message with is UNTRIED. A contact somebody wrote to repeatedly with no reply
// scores COLD. Rendering them the same tells a rep an account is unreachable
// when in fact nobody has tried, which is the opposite instruction.
//
// The colleagues named here are live members only, by the same pair the roster
// lists by (liveMemberWhere): recommending an intro from someone who left is
// advice that cannot be taken. Their historical messages still count on the
// timeline — the person is gone, what happened is not.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
)

// routeCap is how many colleagues a contact row names. Three is what fits a
// table row a reader scans rather than studies; the rest are a count, and the
// explorer is where somebody who wants all of them goes.
const routeCap = 3

// contactRoutes resolves the internal routes for every contact the card placed,
// keyed by person.
//
// ONE query for the whole contact set, not one per contact: the section already
// has a fixed query budget, and a per-contact read is how a composite starts
// costing more on the accounts that matter most.
//
// The contacts passed in have already passed the person row-scope gate — the
// card placed them — so the visibility question left here is about the COLLEAGUE
// being named, which liveMemberWhere and the roster's own readability settle.
func contactRoutes(
	ctx context.Context, tx pgx.Tx, contactIDs []ids.UUID, now time.Time,
) (map[ids.UUID]crmcontracts.Organization360ContactRoutes, error) {
	routes := map[ids.UUID]crmcontracts.Organization360ContactRoutes{}
	for _, id := range contactIDs {
		// Every contact gets an answer, including the ones no edge mentions.
		// Leaving them out of the map would make "untried" indistinguishable
		// from "this section did not run".
		routes[id] = crmcontracts.Organization360ContactRoutes{
			Top:       []crmcontracts.Organization360Route{},
			Remainder: 0,
			Untried:   true,
		}
	}
	if len(contactIDs) == 0 {
		return routes, nil
	}

	rows, err := tx.Query(ctx, `
		SELECT e.person_id, u.id, u.display_name, e.last_at,
		       e.count_90d, e.in_count_90d, e.out_count_90d
		  FROM graph_interaction_edge e
		  JOIN app_user u ON u.id = e.user_id AND `+liveMemberWhere+`
		 WHERE e.person_id = ANY($1)`, contactIDs)
	if err != nil {
		return nil, fmt.Errorf("org360: reading the contacts' internal routes: %w", err)
	}
	type scored struct {
		route crmcontracts.Organization360Route
		score int
	}
	byPerson := map[ids.UUID][]scored{}
	for rows.Next() {
		var (
			personID, userID ids.UUID
			displayName      string
			lastAt           time.Time
			in               relstrength.Inputs
		)
		if err := rows.Scan(&personID, &userID, &displayName, &lastAt,
			&in.Count90d, &in.Inbound90d, &in.Outbound90d); err != nil {
			return nil, fmt.Errorf("org360: scanning an internal route: %w", err)
		}
		// Scored in Go, like every other surface that reads these edges: the
		// decay is a pure function of the stored counts and the read instant,
		// and a second spelling in SQL would let two screens rank the same
		// colleague differently.
		in.LastInteraction = &lastAt
		strength := relstrength.Compute(in, now)
		at := lastAt
		byPerson[personID] = append(byPerson[personID], scored{
			route: crmcontracts.Organization360Route{
				UserId:            openapi_types.UUID(userID),
				DisplayName:       displayName,
				StrengthBucket:    crmcontracts.Organization360RouteStrengthBucket(strength.Bucket),
				LastInteractionAt: &at,
			},
			score: strength.Strength,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("org360: iterating internal routes: %w", err)
	}

	for personID, found := range byPerson {
		// Strongest first, then most recent, then by name: the first two are
		// the ranking a reader expects and the third only exists so two equal
		// colleagues come back in a stable order rather than the scan's.
		slices.SortFunc(found, func(a, b scored) int {
			if a.score != b.score {
				return b.score - a.score
			}
			if !a.route.LastInteractionAt.Equal(*b.route.LastInteractionAt) {
				if a.route.LastInteractionAt.After(*b.route.LastInteractionAt) {
					return -1
				}
				return 1
			}
			return strings.Compare(a.route.DisplayName, b.route.DisplayName)
		})
		top := make([]crmcontracts.Organization360Route, 0, routeCap)
		for _, entry := range found[:min(len(found), routeCap)] {
			top = append(top, entry.route)
		}
		routes[personID] = crmcontracts.Organization360ContactRoutes{
			Top:       top,
			Remainder: len(found) - len(top),
			// Somebody has exchanged messages with them, so the account is not
			// untried however weak the strongest route turns out to be.
			Untried: false,
		}
	}
	return routes, nil
}
