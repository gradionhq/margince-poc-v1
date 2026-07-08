// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The warm/cold join (B-E08.3, features/07 §9 [MVP]) — the V1-WOW core:
// a signal resolved to an organization where we already hold a live
// contact edge is WARM and routes to the warm room; a resolved
// organization with no contact is COLD and routes to the cold queue. The
// answer is EVIDENCE — the source signal id, the resolved org id, and the
// specific contact id(s) in our own graph, each with its explainable §4
// strength — never a bare score. The join reads only company-level rows
// and our own relational core; it creates nothing (P11/P12).

package signals

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

// warmContact pairs the contract evidence row with its person id for the
// intro-path ranking.
type warmContact struct {
	PersonID ids.PersonID
	Contact  crmcontracts.SignalWarmContact
}

// Warmth computes the warm/cold branch for a resolved signal.
func (s *Store) Warmth(ctx context.Context, signalID ids.SignalID, now time.Time) (crmcontracts.SignalWarmth, error) {
	if err := auth.Require(ctx, "signal", principal.ActionRead); err != nil {
		return crmcontracts.SignalWarmth{}, err
	}
	var sig crmcontracts.Signal
	var contacts []warmContact
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureSignalVisible(ctx, tx, signalID.UUID); err != nil {
			return err
		}
		var err error
		if sig, err = readSignal(ctx, tx, signalID, storekit.LiveOnly); err != nil {
			return err
		}
		if sig.ResolutionState != "resolved" || sig.ResolvedOrgId == nil {
			return &NoWarmthError{Reason: fmt.Sprintf(
				"signal is %s: only a signal resolved to an organization has a warm/cold branch", sig.ResolutionState)}
		}
		contacts, err = contactEdges(ctx, tx, ids.From[ids.OrganizationKind](ids.UUID(*sig.ResolvedOrgId)))
		return err
	})
	if err != nil {
		return crmcontracts.SignalWarmth{}, err
	}

	// Strength rides the injected §4 seam (B-E13.16) — outside the row
	// transaction, exactly like the people module's own org roll-up. A
	// contact outside the caller's row scope was already excluded by the
	// edge query; a residual scope miss contributes nothing rather than
	// out-seeing the person list.
	scored := make([]warmContact, 0, len(contacts))
	for _, c := range contacts {
		strength, err := s.strength.PersonStrength(ctx, c.PersonID, now)
		switch {
		case errors.Is(err, apperrors.ErrNotFound):
			continue
		case err != nil:
			return crmcontracts.SignalWarmth{}, fmt.Errorf("relationship strength for contact: %w", err)
		}
		c.Contact.Strength = strength.Strength
		c.Contact.StrengthBucket = crmcontracts.SignalWarmContactStrengthBucket(strength.Bucket)
		scored = append(scored, c)
	}
	// Strongest relationship first: the warm room leads with the best
	// route in (deterministic tie-break on person id).
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Contact.Strength != scored[j].Contact.Strength {
			return scored[i].Contact.Strength > scored[j].Contact.Strength
		}
		return scored[i].PersonID.String() < scored[j].PersonID.String()
	})

	out := crmcontracts.SignalWarmth{
		SourceSignalId: sig.Id,
		ResolvedOrgId:  *sig.ResolvedOrgId,
		ContactIds:     []openapi_types.UUID{},
		Contacts:       []crmcontracts.SignalWarmContact{},
		Warm:           len(scored) > 0,
		Routing:        crmcontracts.SignalWarmthRouting("cold_queue"),
	}
	if out.Warm {
		out.Routing = crmcontracts.SignalWarmthRouting("warm_room")
	}
	for _, c := range scored {
		out.ContactIds = append(out.ContactIds, openapi_types.UUID(c.PersonID.UUID))
		out.Contacts = append(out.Contacts, c.Contact)
	}
	return out, nil
}

// contactEdges finds the live contact edges anchoring the org in OUR
// graph: current employment at the org, or a stakeholder seat on one of
// the org's live deals. Row-scoped — a contact the caller cannot see
// cannot be their evidence.
func contactEdges(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) ([]warmContact, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)

	scope, err := auth.ScopeClauseFor(ctx, "person", "p", arg)
	if err != nil {
		return nil, err
	}
	visible := ""
	if scope != "" {
		visible = " AND " + scope
	}

	rows, err := tx.Query(ctx, storekit.SQLf(`
		SELECT p.id, p.full_name, r.kind, r.role
		FROM relationship r
		JOIN person p ON p.id = r.person_id AND p.archived_at IS NULL
		WHERE r.archived_at IS NULL AND r.ended_at IS NULL AND r.person_id IS NOT NULL
		  AND ((r.kind = 'employment' AND r.organization_id = $%[1]d)
		    OR (r.kind = 'deal_stakeholder' AND r.deal_id IN (
		          SELECT d.id FROM deal d WHERE d.organization_id = $%[1]d AND d.archived_at IS NULL)))%s
		ORDER BY p.id, r.kind`, orgPos, visible), args...)
	if err != nil {
		return nil, fmt.Errorf("contact edges: %w", err)
	}
	defer rows.Close()

	// One evidence row per person: employment is the primary edge when a
	// person holds both (it is the durable "we know someone there" fact).
	byPerson := map[ids.PersonID]warmContact{}
	var order []ids.PersonID
	for rows.Next() {
		var personID ids.PersonID
		var fullName, role *string
		var kind string
		if err := rows.Scan(&personID, &fullName, &kind, &role); err != nil {
			return nil, err
		}
		if have, ok := byPerson[personID]; ok {
			if have.Contact.RelationshipKind == "employment" || kind != "employment" {
				continue
			}
		} else {
			order = append(order, personID)
		}
		byPerson[personID] = warmContact{
			PersonID: personID,
			Contact: crmcontracts.SignalWarmContact{
				PersonId:         openapi_types.UUID(personID.UUID),
				FullName:         fullName,
				RelationshipKind: crmcontracts.SignalWarmContactRelationshipKind(kind),
				RelationshipRole: role,
			},
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]warmContact, 0, len(order))
	for _, id := range order {
		out = append(out, byPerson[id])
	}
	return out, nil
}
