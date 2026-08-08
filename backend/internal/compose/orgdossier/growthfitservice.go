// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// Serving one reader's growth fit: read the company's facts as the caller, ask
// whether this workspace has confirmed its own offering, and run both through
// DOSS-FORM-2.
//
// The two reads are deliberately different in kind. The company's facts are
// row-scoped and become citable evidence; our own offering arrives as a
// confirmation flag and a digest, never as text, because a fit derived from
// what WE sell is an assessment about them and must still cite THEIR records
// (DOSS-AC-6).

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

	"github.com/gradionhq/margince/backend/internal/compose/claims"
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
	lane  Completer
	now   func() time.Time
	// routingVersion identifies the model binding in the fingerprint, so a
	// re-pointed lane invalidates rather than serving assessments written
	// against a model that is no longer wired.
	routingVersion string
}

// NewGrowthFitService binds the assessment to its reads; compose constructs it
// once per process role. A nil lane is the no-model deployment, which serves
// the deterministic floor and says so.
func NewGrowthFitService(pool *pgxpool.Pool, facts Facts, self SelfOffering,
	lane Completer, routingVersion string, now func() time.Time,
) *GrowthFitService {
	if now == nil {
		now = time.Now
	}
	return &GrowthFitService{
		pool: pool, facts: facts, self: self, lane: lane,
		now: now, routingVersion: routingVersion,
	}
}

// storedGrowthFit is the cached envelope.
//
// An abstention carries no claims, and the wire fields for them stay ABSENT
// rather than empty: an empty "what argues for this company" list beside
// "not enough evidence" would read as a finding that nothing does, which is the
// opposite of what an abstention means.
type storedGrowthFit struct {
	Fingerprint  string                        `json:"fingerprint"`
	Version      int                           `json:"version"`
	GeneratedAt  time.Time                     `json:"generated_at"`
	GeneratedBy  string                        `json:"generated_by"`
	Band         string                        `json:"band"`
	CappedReason string                        `json:"capped_reason"`
	NextStep     string                        `json:"next_step"`
	Completeness crmcontracts.DataCompleteness `json:"completeness"`
	Claims       GrowthFitClaims               `json:"claims"`
	// StaleAt is when the completeness figure above stops being true on the
	// clock alone. Zero means nothing it counted can age out.
	StaleAt time.Time `json:"stale_at"`
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
	// caller cannot read refuses before any cache is consulted, and the
	// completeness figure counts only records they could already fetch.
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
	offering, err := s.self(ctx)
	if err != nil {
		return zero, err
	}
	fingerprint, err := growthFitFingerprint(in, s.routingVersion, offering)
	if err != nil {
		return zero, err
	}

	utc := func() time.Time { return s.now().UTC() }
	cached, found, err := s.cached(ctx, userID, orgID)
	if err != nil {
		return zero, err
	}
	if found && !force && cached.usable(fingerprint, utc()) {
		return cached.wire(orgID), nil
	}

	assessed, by, laneFailed := WriteGrowthFit(ctx, s.lane, in, offering.Confirmed, utc)
	if laneFailed && cached.mayStandIn(found, fingerprint, utc()) {
		// A lane that failed must not be able to DESTROY an assessment. The
		// degraded answer is an abstention, and writing it over a real band
		// would let one unreachable model call — or one reader looping the
		// refresh until the token budget is gone — flatten every cached
		// assessment in the workspace to "not enough evidence".
		//
		// The contract says as much for this endpoint: a refresh the budget
		// refuses returns the cached assembly with its age rather than an
		// error. Its `generated_at` is that age.
		return cached.wire(orgID), nil
	}
	written := storedGrowthFit{
		Fingerprint:  fingerprint,
		Version:      growthFitStoredVersion,
		GeneratedAt:  utc(),
		GeneratedBy:  string(by),
		Band:         string(assessed.Band),
		CappedReason: assessed.CappedReason,
		NextStep:     assessed.NextStep,
		Completeness: assessed.Completeness,
		Claims:       assessed.Claims,
		StaleAt:      assessed.StaleAt,
	}
	// A degraded answer is NOT cached. With no usable entry to fall back on the
	// abstention is the honest response to this request, but storing it under
	// the current fingerprint would make every later read a cache hit — so the
	// reader is told "try again in a few minutes" by a row that guarantees the
	// retry never happens. For a company held on human-entered values there is
	// no expiry either, so that would be permanent.
	if laneFailed {
		return written.wire(orgID), nil
	}
	if err := s.save(ctx, userID, orgID, written); err != nil {
		return zero, err
	}
	return written.wire(orgID), nil
}

// usable reports whether this build may still serve a cached entry.
//
// The fingerprint catches every change that arrives as a WRITE — a fact moves,
// the prompt changes, the lane is re-pointed. Ageing out is the one change that
// arrives with no write at all, so a fingerprint-only check would serve a band
// resting on evidence that has since gone stale, indefinitely.
func (g storedGrowthFit) usable(fingerprint string, now time.Time) bool {
	if g.Version != growthFitStoredVersion || g.Fingerprint != fingerprint {
		return false
	}
	return g.StaleAt.IsZero() || now.Before(g.StaleAt)
}

// mayStandIn reports whether a cached entry may be served in PLACE of an
// assessment the lane failed to produce.
//
// It is the same test the ordinary read applies, and that is the whole point:
// "a lane failed" is a reason to keep a good answer, never a reason to serve
// one that is out of date, written from facts that have since changed, or
// stored in a shape this build cannot read. Without the check, one reader
// exhausting the workspace's token budget would freeze every cached band in
// place — including bands written from values since corrected or erased.
func (g storedGrowthFit) mayStandIn(found bool, fingerprint string, now time.Time) bool {
	return found && g.usable(fingerprint, now)
}

// growthFitFingerprint covers everything that could change the assessment: the
// company's facts, the assembly rules, the model routing version, and whether
// this workspace has confirmed its own offering.
//
// The last one is the difference from the dossier's fingerprint, and neither
// half of it is optional. Confirming our own profile changes every company's
// band cap without touching a single company record; EDITING it — a new
// product, a different ideal customer — changes every company's fit while
// `confirmed` stays true throughout. A key blind to either keeps serving bands
// measured against an offering this workspace no longer has.
func growthFitFingerprint(in Input, routingVersion string, offering Offering) (string, error) {
	encoded, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("fingerprint the growth-fit input: %w", err)
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%t\x00%s\x00%s",
		growthFitPromptVersion, routingVersion, offering.Confirmed, offering.Fingerprint, encoded))
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
	// Each list stays absent when it is empty, for the reason the envelope's
	// own comment gives: a rendered-but-empty "what argues against them" reads
	// as a finding of nothing rather than as nothing found.
	if factors := wireSentences(g.Claims.PositiveFactors); len(factors) > 0 {
		out.PositiveFactors = &factors
	}
	if factors := wireSentences(g.Claims.NegativeFactors); len(factors) > 0 {
		out.NegativeFactors = &factors
	}
	if space := wireSentences(g.Claims.Whitespace); len(space) > 0 {
		out.Whitespace = &space
	}
	if objections := wireSentences(g.Claims.Objections); len(objections) > 0 {
		out.Objections = &objections
	}
	if g.Claims.RecommendedAngle != nil {
		if angle := wireSentences([]claims.Sentence{*g.Claims.RecommendedAngle}); len(angle) == 1 {
			out.RecommendedAngle = &angle[0]
		}
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
