// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"context"

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
	for _, teamID := range teams {
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
