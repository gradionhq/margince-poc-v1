// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The HTTP transport for GET /people/{id}/strength and
// GET /organizations/{id}/strength (§4 relationship strength): binds the
// path id to the typed store call and maps its result onto the
// generated wire shape. The store methods gate themselves (auth.Require
// + auth.EnsureVisible), so this file is pure edge + shape translation —
// no re-gating here.
//
// One wrinkle: PersonStrength/OrganizationStrength compute their inputs
// with aggregate SQL (max/count), which always answers one row — even
// for an id that was never there — so EnsureVisible's existence check
// is the only thing standing between an unbounded (admin) caller and a
// row that doesn't exist, and EnsureVisible skips that probe entirely
// for unbounded callers (the same gap orgrollupread.go documents and
// works around). GetPerson/GetOrganization's own SELECT has no such
// gap — a missing row is a missing row in its result set — so this file
// calls them first, purely to inherit their existence-hiding 404; their
// own auth.Require/EnsureVisible calls are redundant with the strength
// call's but idempotent, never a second, different gate.
import (
	"math"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// strengthHandlers shadows the generated GetPersonStrength /
// GetOrganizationStrength stubs over people's §4 computation.
type strengthHandlers struct {
	people *people.Store
	// now is the read's clock (newServer defaults it to time.Now), matching
	// orgRollupHandlers' shape.
	now func() time.Time
}

// GetPersonStrength implements GET /people/{id}/strength.
func (h strengthHandlers) GetPersonStrength(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	personID := ids.From[ids.PersonKind](ids.UUID(id))
	if _, err := h.people.GetPerson(r.Context(), personID, storekit.IncludeArchived); err != nil {
		httperr.Write(w, r, err)
		return
	}
	now := h.now()
	rs, err := h.people.PersonStrength(r.Context(), personID, now)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, strengthToWire(rs, now))
}

// GetOrganizationStrength implements GET /organizations/{id}/strength.
func (h strengthHandlers) GetOrganizationStrength(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	orgID := ids.From[ids.OrganizationKind](ids.UUID(id))
	if _, err := h.people.GetOrganization(r.Context(), orgID, storekit.IncludeArchived); err != nil {
		httperr.Write(w, r, err)
		return
	}
	now := h.now()
	rs, err := h.people.OrganizationStrength(r.Context(), orgID, now)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, strengthToWire(rs, now))
}

// strengthBucketToWire maps the domain's display bucket onto the
// contract vocabulary. The domain only ever emits the four cases below;
// an unrecognized value defaults to dormant rather than surfacing a
// wire value the enum doesn't declare.
func strengthBucketToWire(bucket string) crmcontracts.RelationshipStrengthBucket {
	switch bucket {
	case "weak":
		return crmcontracts.RelationshipStrengthBucketWeak
	case "moderate":
		return crmcontracts.RelationshipStrengthBucketWarm
	case "strong":
		return crmcontracts.RelationshipStrengthBucketStrong
	default: // "none"
		return crmcontracts.RelationshipStrengthBucketDormant
	}
}

// strengthToWire renders the computed §4 result onto the contract's
// RelationshipStrength.
func strengthToWire(rs people.RelationshipStrength, now time.Time) crmcontracts.RelationshipStrength {
	inbound, outbound := rs.Inbound90d, rs.Outbound90d

	// The contract's factors.direction has no dedicated domain field; the
	// domain computes this exact balance term internally (strength.go
	// finish()) on the way to reciprocity — surfaced here faithfully
	// rather than invented: (inbound+outbound) > 0 ? 1 - |inbound-outbound|/(inbound+outbound) : 0.
	direction := 0.0
	if directed := inbound + outbound; directed > 0 {
		direction = 1 - math.Abs(float64(inbound-outbound))/float64(directed)
	}

	contributing := make([]openapi_types.UUID, len(rs.ContributingIDs))
	for i, activityID := range rs.ContributingIDs {
		contributing[i] = openapi_types.UUID(activityID.UUID)
	}

	computedAt := now
	wire := crmcontracts.RelationshipStrength{
		Score:                   rs.Strength,
		Bucket:                  strengthBucketToWire(rs.Bucket),
		LastInteraction:         rs.LastInteraction,
		ComputedAt:              &computedAt,
		Inbound90d:              &inbound,
		Outbound90d:             &outbound,
		ContributingActivityIds: &contributing,
	}
	wire.Factors.Recency = float32(rs.Recency)
	wire.Factors.Frequency = float32(rs.Frequency)
	wire.Factors.Reciprocity = float32(rs.Reciprocity)
	wire.Factors.Direction = float32(direction)
	return wire
}
