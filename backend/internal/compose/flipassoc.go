// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The flip's association detangling (IEM-FORM-2): the estate's edges
// become native FKs and typed relationship rows. Activity links ride
// their row's insert (links are write-once with the activity), so they
// are resolved here at insert time; every other edge is applied after
// the row phase, once both endpoints exist. An edge that resolves to
// neither — an endpoint that was skipped, or a shape the native model
// has no target for — is DISCLOSED in the run report rather than
// vanishing.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// activityLinks resolves the activity's own edges to already-imported
// native targets (activities import last, so every endpoint exists).
//
// Resolution goes through lookup, NOT the in-memory cache: a resumed run
// skips whole already-processed classes without re-reading them, so the
// cache holds nothing for the endpoints an earlier attempt landed — and
// reading it alone would silently strip every link on the resume path.
// The engine-owned identity map remembers them across attempts.
//
// A target that genuinely never landed (a disclosed skip) yields no link
// and is reported by Associate, which sees the same unresolved edge.
func (w *flipWriters) activityLinks(ctx context.Context, activityExt string) ([]activities.ActivityLinkInput, error) {
	var links []activities.ActivityLinkInput
	for _, a := range w.assocs {
		if a.FromType != flipObjectActivity || a.FromID != activityExt {
			continue
		}
		switch a.ToType {
		case flipObjectPerson, flipObjectOrganization, flipObjectDeal:
			id, found, err := w.lookup(ctx, a.ToType, a.ToID)
			if err != nil {
				return nil, err
			}
			if found {
				links = append(links, activities.ActivityLinkInput{EntityType: a.ToType, EntityID: id})
			}
		}
	}
	return links, nil
}

// Associate applies one estate edge after the row phase. Activity edges
// were already applied at insert time (see activityLinks); person→org
// edges become employment relationship rows; deal→org edges set the
// deal's organization FK — IEM-FORM-2's detangling, on the edges the
// mirror actually holds. Every non-applied edge returns its reason, so
// the run report discloses it rather than counting it as applied.
func (w *flipWriters) Associate(ctx context.Context, a migration.Assoc) (migration.AssocResult, error) {
	if a.FromType == flipObjectActivity || a.ToType == flipObjectActivity {
		return w.activityEdgeResult(ctx, a)
	}
	fromID, fromOK, err := w.lookup(ctx, a.FromType, a.FromID)
	if err != nil {
		return migration.AssocResult{}, err
	}
	toID, toOK, err := w.lookup(ctx, a.ToType, a.ToID)
	if err != nil {
		return migration.AssocResult{}, err
	}
	if !fromOK || !toOK {
		return migration.AssocResult{Reason: "endpoint_not_imported"}, nil
	}
	switch {
	case a.FromType == flipObjectDeal && a.ToType == flipObjectOrganization:
		orgID := ids.From[ids.OrganizationKind](toID)
		if _, err := w.deals.UpdateDeal(ctx, ids.From[ids.DealKind](fromID), deals.UpdateDealInput{OrganizationID: &orgID}); err != nil {
			return migration.AssocResult{}, fmt.Errorf("flip import: linking deal %s to organization %s: %w", a.FromID, a.ToID, err)
		}
		return migration.AssocResult{Applied: true}, nil
	case a.FromType == flipObjectPerson && a.ToType == flipObjectOrganization:
		personID := ids.From[ids.PersonKind](fromID)
		orgID := ids.From[ids.OrganizationKind](toID)
		_, err := w.people.CreateRelationship(ctx, people.CreateRelationshipInput{
			Kind:             "employment",
			PersonID:         &personID,
			OrganizationID:   &orgID,
			IsCurrentPrimary: strings.EqualFold(a.Label, "primary"),
			Source:           w.provenance("relationship", a.FromID+"→"+a.ToID),
		})
		if err != nil {
			// The employment edge is unique per (person, organization):
			// a resumed run replaying its association phase re-offers an
			// edge that already landed, which is convergence, not a
			// failure — every other error still stops the run.
			if errors.Is(err, apperrors.ErrConflict) {
				return migration.AssocResult{Applied: true}, nil
			}
			return migration.AssocResult{}, fmt.Errorf("flip import: creating employment %s→%s: %w", a.FromID, a.ToID, err)
		}
		return migration.AssocResult{Applied: true}, nil
	default:
		return migration.AssocResult{Reason: "unmodelled_edge_shape"}, nil
	}
}

// activityEdgeResult reports an activity edge, which LogActivity already
// applied at insert time (links are write-once with the row). It is
// "applied" only if the target actually landed — otherwise it carries
// the same unresolved-endpoint reason every other edge shape gives,
// rather than a blanket claim the run report would count as real.
func (w *flipWriters) activityEdgeResult(ctx context.Context, a migration.Assoc) (migration.AssocResult, error) {
	if a.FromType != flipObjectActivity {
		return migration.AssocResult{Applied: true}, nil
	}
	if _, found, err := w.lookup(ctx, a.ToType, a.ToID); err != nil {
		return migration.AssocResult{}, err
	} else if !found {
		return migration.AssocResult{Reason: "endpoint_not_imported"}, nil
	}
	return migration.AssocResult{Applied: true}, nil
}
