// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// Serving one reader's growth fit: read the company's facts as the caller, ask
// whether this workspace has confirmed its own offering, and run both through
// DOSS-FORM-2.
//
// The two reads are deliberately different in kind. The company's facts are
// row-scoped and become citable evidence; our own offering is a single boolean
// that never leaves this file, because a fit derived from what WE sell is an
// assessment about them and must still cite THEIR records (DOSS-AC-6).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// growthFitPromptVersion identifies the assembly RULES in the fingerprint.
// Bumping it invalidates every cached assessment, which is the point: a change
// to the required inputs or the abstention floor must not leave yesterday's
// bands being served beside today's (DOSS-AC-14).
const growthFitPromptVersion = "growth-fit-v1"

// growthFitStoredVersion is the payload SHAPE this build writes and can read.
const growthFitStoredVersion = 1

// GrowthFitService assembles and caches one company's growth fit per reader.
type GrowthFitService struct {
	pool  *pgxpool.Pool
	facts Facts
	self  SelfOffering
	now   func() time.Time
	// routingVersion identifies the model binding in the fingerprint, so a
	// re-pointed lane invalidates rather than serving assessments written
	// against a model that is no longer wired.
	routingVersion string
}

// NewGrowthFitService binds the assessment to its reads; compose constructs it
// once per process role.
func NewGrowthFitService(pool *pgxpool.Pool, facts Facts, self SelfOffering,
	routingVersion string, now func() time.Time,
) *GrowthFitService {
	if now == nil {
		now = time.Now
	}
	return &GrowthFitService{pool: pool, facts: facts, self: self, now: now, routingVersion: routingVersion}
}

// storedGrowthFit is the cached envelope.
//
// It carries no factors, whitespace or objections yet, and the wire fields for
// them stay absent rather than empty. The deterministic floor abstains, and an
// abstention with an empty "what argues for this company" list would read as a
// finding that nothing does — the opposite of what it means.
type storedGrowthFit struct {
	Fingerprint  string                        `json:"fingerprint"`
	Version      int                           `json:"version"`
	GeneratedAt  time.Time                     `json:"generated_at"`
	GeneratedBy  string                        `json:"generated_by"`
	Band         string                        `json:"band"`
	CappedReason string                        `json:"capped_reason"`
	NextStep     string                        `json:"next_step"`
	Completeness crmcontracts.DataCompleteness `json:"completeness"`
}

// Get serves the growth fit, re-assessing when the cache no longer describes
// the company's current facts or our own confirmation state.
func (s *GrowthFitService) Get(ctx context.Context, orgID ids.OrganizationID, force bool) (crmcontracts.OrganizationGrowthFit, error) {
	var zero crmcontracts.OrganizationGrowthFit
	// A growth fit is a reading aid for a person; an agent reading records
	// through a passport has the records themselves.
	if err := auth.RequireHuman(ctx); err != nil {
		return zero, err
	}
	userID, err := actingUser(ctx)
	if err != nil {
		return zero, err
	}
	// The gates that matter run HERE, in the caller's own reads: a company this
	// caller cannot read refuses before any cache is consulted, and a field
	// they cannot see never counts toward the completeness figure.
	in, err := BuildInput(ctx, s.facts, orgID)
	if err != nil {
		return zero, err
	}
	if s.self == nil {
		// An unwired self-offering check is a deployment defect on a surface
		// whose whole premise is measuring them against us. It refuses — and
		// carries no sentinel, so it surfaces as a server fault — because
		// assuming confirmed would silently lift the DOSS-AC-13 cap and hand
		// every company a stronger band than the evidence supports.
		return zero, errors.New("the growth fit has no way to read this workspace's own offering")
	}
	confirmed, err := s.self(ctx)
	if err != nil {
		return zero, err
	}
	fingerprint, err := growthFitFingerprint(in, s.routingVersion, confirmed)
	if err != nil {
		return zero, err
	}

	cached, found, err := s.cached(ctx, userID, orgID)
	if err != nil {
		return zero, err
	}
	if found && !force && cached.Version == growthFitStoredVersion && cached.Fingerprint == fingerprint {
		return cached.wire(orgID), nil
	}

	// No model lane is wired yet, so every assessment is the deterministic
	// floor — which, for growth fit, ABSTAINS (DOSS-PARAM-7). The floor
	// restates recorded values and grading is not a restatement, so it proposes
	// `unknown` and lets the formula's own floor confirm it. What the reader
	// gets is the completeness figure and the named gap, which is the honest
	// answer until a model is configured.
	assessed := Assess(in, crmcontracts.GrowthFitBandUnknown, confirmed, s.now().UTC())
	written := storedGrowthFit{
		Fingerprint:  fingerprint,
		Version:      growthFitStoredVersion,
		GeneratedAt:  s.now().UTC(),
		GeneratedBy:  string(crmcontracts.Deterministic),
		Band:         string(assessed.Band),
		CappedReason: assessed.CappedReason,
		NextStep:     assessed.NextStep,
		Completeness: assessed.Completeness,
	}
	if err := s.save(ctx, userID, orgID, written); err != nil {
		return zero, err
	}
	return written.wire(orgID), nil
}

// growthFitFingerprint covers everything that could change the assessment: the
// company's facts, the assembly rules, the model routing version, and whether
// this workspace has confirmed its own offering.
//
// That last one is the difference from the dossier's fingerprint, and it is not
// optional: confirming our own profile changes every company's band cap without
// touching a single company record, so a key blind to it would keep serving
// capped bands to a workspace that has since described itself.
func growthFitFingerprint(in Input, routingVersion string, selfConfirmed bool) (string, error) {
	encoded, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("fingerprint the growth-fit input: %w", err)
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%t\x00%s",
		growthFitPromptVersion, routingVersion, selfConfirmed, encoded))
	return hex.EncodeToString(sum[:]), nil
}

func (g storedGrowthFit) wire(orgID ids.OrganizationID) crmcontracts.OrganizationGrowthFit {
	out := crmcontracts.OrganizationGrowthFit{
		OrganizationId:   openapi_types.UUID(orgID.UUID),
		Band:             crmcontracts.GrowthFitBand(g.Band),
		DataCompleteness: g.Completeness,
		GeneratedAt:      g.GeneratedAt,
		GeneratedBy:      crmcontracts.WrittenBy(g.GeneratedBy),
	}
	// Both stay ABSENT rather than empty when nothing capped the band and
	// nothing is outstanding: an empty string would render as a reason and a
	// next step that say nothing, which reads as a finding rather than as none.
	if g.CappedReason != "" {
		out.BandCappedReason = &g.CappedReason
	}
	if g.NextStep != "" {
		out.NextStep = &g.NextStep
	}
	return out
}

func (s *GrowthFitService) cached(ctx context.Context, userID ids.UserID, orgID ids.OrganizationID) (storedGrowthFit, bool, error) {
	row, found, err := growthFitCache.load(ctx, s.pool, userID, orgID)
	if err != nil || !found {
		return storedGrowthFit{}, false, err
	}
	var out storedGrowthFit
	if err := json.Unmarshal(row.Payload, &out); err != nil {
		// A payload this build cannot read is a cache MISS, not a failure: the
		// assessment is derived content and re-running it costs one pass over
		// facts we already hold.
		//nolint:nilerr // an unreadable cache entry is a miss by design; the caller re-assesses
		return storedGrowthFit{}, false, nil
	}
	return out, true, nil
}

func (s *GrowthFitService) save(ctx context.Context, userID ids.UserID, orgID ids.OrganizationID, fit storedGrowthFit) error {
	payload, err := json.Marshal(fit)
	if err != nil {
		return fmt.Errorf("encode the growth-fit payload: %w", err)
	}
	return growthFitCache.save(ctx, s.pool, userID, orgID, entry{
		Fingerprint: fit.Fingerprint,
		GeneratedAt: fit.GeneratedAt,
		GeneratedBy: fit.GeneratedBy,
		Payload:     payload,
	})
}
