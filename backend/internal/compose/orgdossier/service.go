// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// Serving one dossier: read the sidecars as the caller, decide whether the
// cached assembly still describes them, and write one if it does not.
//
// A cached dossier is served only while its fingerprint still matches the
// inputs it was written from. Facts and profile fields move without touching
// the organization row, so a key derived from that row would serve a dossier
// describing a company that has since been re-read, indefinitely.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// promptVersion identifies the assembly RULES in the fingerprint. Bumping it
// invalidates every cached dossier, which is the point: a change to how a
// dossier is written must not leave yesterday's assemblies being served beside
// today's (DOSS-AC-14).
const promptVersion = "dossier-v1"

// storedVersion is the payload SHAPE this build writes and can read. A row
// written by an older shape unmarshals cleanly into a newer envelope with its
// new fields zeroed, and serving that would render a company nobody could say
// anything about — so the shape is checked, not assumed.
const storedVersion = 1

// Service assembles and caches one company's dossier per reader.
type Service struct {
	pool  *pgxpool.Pool
	facts Facts
	lane  Completer
	now   func() time.Time
	// routingVersion identifies the model binding in the fingerprint, so a
	// re-pointed lane invalidates rather than serving assemblies written
	// against a model that is no longer wired.
	routingVersion string
}

// NewService binds the dossier to its reads; compose constructs it once per
// process role.
// A nil lane is the no-model deployment, which serves the deterministic floor
// and says so.
func NewService(pool *pgxpool.Pool, facts Facts, lane Completer,
	routingVersion string, now func() time.Time,
) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{pool: pool, facts: facts, lane: lane, now: now, routingVersion: routingVersion}
}

// stored is the cached envelope: the payload plus what it takes to decide
// whether this build may serve it.
type stored struct {
	Fingerprint string    `json:"fingerprint"`
	Version     int       `json:"version"`
	GeneratedAt time.Time `json:"generated_at"`
	GeneratedBy string    `json:"generated_by"`
	Sections    []Section `json:"sections"`
	// NewestSourceAt is when the freshest contributing value was last read.
	// `needs_refresh` is computed from it at render rather than stored, because
	// staleness arrives with the clock and a stored verdict would keep
	// answering with whatever was true on the day the row was written.
	NewestSourceAt time.Time `json:"newest_source_at"`
}

// usable reports whether this build may still serve a cached dossier. Unlike
// the growth fit's, it has no expiry: a dossier restates recorded values, and
// those do not become untrue with the clock — they become OLD, which the
// surface says out loud beside them instead (DOSS-PARAM-6).
func (d stored) usable(fingerprint string) bool {
	return d.Version == storedVersion && d.Fingerprint == fingerprint
}

// newestSource is when the freshest value this dossier was written from was
// last read. A company with nothing dated — every value entered by a person —
// has no source age at all, and reports the zero time rather than "now", which
// would claim a read nobody performed.
func newestSource(in Input) time.Time {
	var newest time.Time
	consider := func(source string, retrievedAt *time.Time, updatedAt time.Time) {
		if source == string(crmcontracts.CompanyProfileFieldSourceHuman) {
			return
		}
		read := updatedAt
		if retrievedAt != nil {
			read = *retrievedAt
		}
		if read.After(newest) {
			newest = read
		}
	}
	for _, field := range in.ProfileFields {
		consider(string(field.Source), field.RetrievedAt, field.UpdatedAt)
	}
	for _, fact := range in.Facts {
		consider(string(fact.Source), fact.RetrievedAt, fact.UpdatedAt)
	}
	return newest
}

// Get serves the dossier, reassembling first when the cache no longer describes
// the company's current facts.
func (s *Service) Get(ctx context.Context, orgID ids.OrganizationID, force bool) (crmcontracts.OrganizationDossier, error) {
	var zero crmcontracts.OrganizationDossier
	// A dossier is a reading aid for a person; an agent reading records through
	// a passport has the records themselves.
	if err := auth.RequireHuman(ctx); err != nil {
		return zero, err
	}
	userID, err := actingUser(ctx)
	if err != nil {
		return zero, err
	}
	// The gates that matter run HERE, in the caller's own reads: a dossier can
	// only be written from records this caller could already fetch themselves,
	// and a company they cannot read refuses before any cache is consulted.
	in, err := BuildInput(ctx, s.facts, orgID)
	if err != nil {
		return zero, err
	}
	fingerprint, err := Fingerprint(in, s.routingVersion)
	if err != nil {
		return zero, err
	}

	cached, found, err := s.cached(ctx, userID, orgID)
	if err != nil {
		return zero, err
	}
	if found && !force && cached.usable(fingerprint) {
		return cached.wire(orgID, s.now().UTC()), nil
	}

	sections, by, laneFailed := WriteDossier(ctx, s.lane, in)
	if laneFailed && found && cached.usable(fingerprint) {
		// The floor is a real answer, but it is a plainer one than the model
		// already wrote for these same facts. A transient outage must not
		// replace what is cached — stored under the current fingerprint, that
		// downgrade would read as a cache hit indefinitely.
		//
		// The same gate the ordinary read applies: a lane being down is a
		// reason to keep a good answer, never to serve one written from facts
		// that have since moved or in a shape this build cannot read.
		return cached.wire(orgID, s.now().UTC()), nil
	}
	written := stored{
		Fingerprint:    fingerprint,
		Version:        storedVersion,
		GeneratedAt:    s.now().UTC(),
		GeneratedBy:    string(by),
		Sections:       sections,
		NewestSourceAt: newestSource(in),
	}
	if err := s.save(ctx, userID, orgID, written); err != nil {
		return zero, err
	}
	return written.wire(orgID, s.now().UTC()), nil
}

// keepGrounded runs every assembled sentence past the SHARED filter, whichever
// writer produced it.
//
// The floor is checked too, not just the model. A floor that could bypass the
// filter would be a second definition of "checkable" — and the day a floor bug
// cites a row it did not supply, the surface would render it as though it had
// been verified.
func keepGrounded(sections []Section, in Input) []Section {
	known := KnownRecords(in)
	out := make([]Section, 0, len(sections))
	for _, section := range sections {
		kept := claims.Keep(section.Sentences, known, knownNature, natureFact)
		if len(kept) == 0 {
			// A section whose sentences all fell out is omitted rather than
			// rendered empty (DOSS-FORM-1).
			continue
		}
		out = append(out, Section{Kind: section.Kind, Sentences: kept})
	}
	return out
}

// knownNature is every nature the contract declares, derived rather than
// re-spelled so a rename upstream fails to compile instead of laundering a
// hand-typed string past the filter.
var knownNature = map[string]bool{
	string(crmcontracts.Fact):           true,
	string(crmcontracts.Assessment):     true,
	string(crmcontracts.Recommendation): true,
}

// Fingerprint covers everything that could change the content: the assembled
// factual input, the prompt version and the model routing version
// (DOSS-PARAM-5).
func Fingerprint(in Input, routingVersion string) (string, error) {
	// json.Marshal orders struct fields by declaration, so the same input
	// hashes the same way across processes — a map would not.
	encoded, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("fingerprint the dossier input: %w", err)
	}
	sum := sha256.Sum256([]byte(promptVersion + "\x00" + routingVersion + "\x00" + string(encoded)))
	return hex.EncodeToString(sum[:]), nil
}

func (d stored) wire(orgID ids.OrganizationID, now time.Time) crmcontracts.OrganizationDossier {
	sections := make([]crmcontracts.OrganizationDossierSection, 0, len(d.Sections))
	for _, section := range d.Sections {
		sections = append(sections, crmcontracts.OrganizationDossierSection{
			Kind:      crmcontracts.OrganizationDossierSectionKind(section.Kind),
			Sentences: wireSentences(section.Sentences),
		})
	}
	out := crmcontracts.OrganizationDossier{
		OrganizationId: openapi_types.UUID(orgID.UUID),
		GeneratedAt:    d.GeneratedAt,
		GeneratedBy:    crmcontracts.WrittenBy(d.GeneratedBy),
		Sections:       sections,
	}
	// Said out loud BESIDE the content, never instead of it: a stale dossier is
	// more useful than none. A company with no dated source is not stale — it
	// is undated, which is a different claim and gets no badge.
	if !d.NewestSourceAt.IsZero() && now.Sub(d.NewestSourceAt) > freshness {
		stale := true
		out.NeedsRefresh = &stale
	}
	return out
}

func wireSentences(in []claims.Sentence) []crmcontracts.OrganizationBriefSentence {
	out := make([]crmcontracts.OrganizationBriefSentence, 0, len(in))
	for _, sentence := range in {
		evidence := make([]crmcontracts.OrganizationBriefEvidence, 0, len(sentence.Evidence))
		for _, cited := range sentence.Evidence {
			id, err := ids.Parse(cited.EntityID)
			if err != nil {
				// A citation whose id will not parse names no record, so the
				// chip would go nowhere. The sentence has already passed the
				// grounding filter against ids that came from our own rows, so
				// this is unreachable in practice and dropped rather than
				// rendered as a link to nothing.
				continue
			}
			evidence = append(evidence, crmcontracts.OrganizationBriefEvidence{
				EntityType: crmcontracts.OrganizationBriefEvidenceEntityType(cited.EntityType),
				EntityId:   openapi_types.UUID(id),
			})
		}
		nature := crmcontracts.OrganizationBriefSentenceNature(sentence.Nature)
		out = append(out, crmcontracts.OrganizationBriefSentence{
			Text:     sentence.Text,
			Nature:   &nature,
			Evidence: evidence,
		})
	}
	return out
}

// cached reads this READER's dossier out of the shared per-reader cache.
func (s *Service) cached(ctx context.Context, userID ids.UserID, orgID ids.OrganizationID) (stored, bool, error) {
	row, found, err := dossierCache.load(ctx, s.pool, userID, orgID)
	if err != nil || !found {
		return stored{}, false, err
	}
	var out stored
	if err := json.Unmarshal(row.Payload, &out); err != nil {
		// A payload this build cannot read is a cache MISS, not a failure: the
		// dossier is derived content, reassembling costs one pass over facts we
		// already hold, and the new row replaces the unreadable one.
		//nolint:nilerr // an unreadable cache entry is a miss by design; the caller reassembles
		return stored{}, false, nil
	}
	return out, true, nil
}

func (s *Service) save(ctx context.Context, userID ids.UserID, orgID ids.OrganizationID, dossier stored) error {
	// The whole envelope, so a later read can tell which shape it is holding.
	payload, err := json.Marshal(dossier)
	if err != nil {
		return fmt.Errorf("encode the dossier payload: %w", err)
	}
	return dossierCache.save(ctx, s.pool, userID, orgID, entry{
		Fingerprint: dossier.Fingerprint,
		GeneratedAt: dossier.GeneratedAt,
		GeneratedBy: dossier.GeneratedBy,
		Payload:     payload,
	})
}

// actingUser resolves the human an assembly belongs to. That these assemblies
// are per-reader IS the security posture, so a principal with no user id gets
// none rather than a shared one.
//
// Both surfaces on this package call it, so the message names neither: a reader
// sent to the dossier by an error the growth fit raised looks in the wrong place.
func actingUser(ctx context.Context) (ids.UserID, error) {
	p, ok := principal.Actor(ctx)
	if !ok || p.UserID == (ids.UUID{}) {
		return ids.UserID{}, fmt.Errorf(
			"this company assembly is written per reader and the call carries no user: %w",
			apperrors.ErrPermissionDenied)
	}
	return ids.From[ids.UserKind](p.UserID), nil
}
