// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// userInvitedPayload builds user.invited's typed payload.
func userInvitedPayload(userID ids.UserID, role string, by ids.UserID, teams []ids.UUID) crmcontracts.PublicEventUserInvited {
	out := crmcontracts.PublicEventUserInvited{
		UserId: openapi_types.UUID(userID.UUID),
		Role:   role,
		By:     openapi_types.UUID(by.UUID),
	}
	if len(teams) > 0 {
		wire := make([]openapi_types.UUID, 0, len(teams))
		for _, t := range teams {
			wire = append(wire, openapi_types.UUID(t))
		}
		out.TeamIds = &wire
	}
	return out
}

// joinTeamsTx puts a new member on the teams the invite named. Every team
// must exist and be live — an invite naming a team that is not there is a
// mistake to surface, not a membership to drop silently.
func joinTeamsTx(ctx context.Context, tx pgx.Tx, userID ids.UUID, teams []ids.UUID) error {
	for _, teamID := range uniqueTeams(teams) {
		tag, err := tx.Exec(ctx, `
			INSERT INTO team_membership (team_id, user_id)
			SELECT id, $2 FROM team WHERE id = $1 AND archived_at IS NULL
			ON CONFLICT (team_id, user_id) DO NOTHING`, teamID, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return &values.ParseError{Field: "team_ids", Code: "unknown_team",
				Message: "team " + teamID.String() + " does not exist or is archived"}
		}
	}
	return nil
}

// maxInviteTeams mirrors the contract's maxItems on team_ids.
const maxInviteTeams = 20

// uniqueTeams drops repeats so a team named twice is joined once and the
// second mention is not mistaken for a team that is not there.
func uniqueTeams(teams []ids.UUID) []ids.UUID {
	seen := make(map[ids.UUID]bool, len(teams))
	out := make([]ids.UUID, 0, len(teams))
	for _, t := range teams {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// validTeamIDs bounds a client-supplied team list to the contract's ceiling.
func validTeamIDs(teams []ids.UUID) ([]ids.UUID, error) {
	teams = uniqueTeams(teams)
	if len(teams) > maxInviteTeams {
		return nil, &values.ParseError{Field: "team_ids", Code: "too_many_teams",
			Message: fmt.Sprintf("at most %d teams", maxInviteTeams)}
	}
	return teams, nil
}
