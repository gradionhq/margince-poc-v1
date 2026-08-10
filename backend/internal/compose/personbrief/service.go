// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package personbrief

// The cache and the read around it.
//
// A cached brief is served only while its fingerprint still matches the
// relationship; the moment it does not, the brief is REWRITTEN before the
// request answers. So a reader is never handed text describing a state of play
// the relationship has moved on from.
//
// The alternative — hand back the old brief immediately, refresh behind the
// request — trades that guarantee for a faster first paint, and needs a
// regeneration that outlives the request to do it. It is not what this does,
// and nothing in the contract claims it: a brief that arrives is current.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Sentence and Evidence are the claim vocabulary both briefs share, so a
// grounding rule proved on one holds on the other.
type (
	Sentence = claims.Sentence
	Evidence = claims.Evidence
)

// Assembler reads the person exactly as their reader would see them. The
// composite read is injected rather than imported so this package composes one
// seam instead of re-deriving a dozen gated reads.
type Assembler interface {
	Assemble(ctx context.Context, personID ids.PersonID) (crmcontracts.Person360, error)
}

// Service writes and caches the brief.
type Service struct {
	pool           *pgxpool.Pool
	view           Assembler
	now            func() time.Time
	routingVersion string
}

// NewService binds the brief to the composite read it is written from.
func NewService(pool *pgxpool.Pool, view Assembler, routingVersion string, now func() time.Time) *Service {
	return &Service{pool: pool, view: view, routingVersion: routingVersion, now: now}
}

// storedVersion is the cached payload's shape version. A row written by an
// older build unmarshals into an envelope this one cannot trust, which reads
// as a cache MISS and rewrites — rather than as a brief with no sentences,
// which would render as a person nobody could say anything about.
const storedVersion = 1

// stored is the cached payload's shape.
type stored struct {
	Fingerprint string                 `json:"-"`
	Version     int                    `json:"version"`
	GeneratedAt time.Time              `json:"generated_at"`
	GeneratedBy crmcontracts.WrittenBy `json:"generated_by"`
	Sentences   []Sentence             `json:"sentences"`
}

// Get serves the brief, regenerating when the cache no longer matches.
// force skips the cache entirely — the explicit refresh.
func (s *Service) Get(ctx context.Context, personID ids.PersonID, force bool) (crmcontracts.PersonBrief, error) {
	// A brief is a reading aid for a person; an agent reading records through
	// a passport has the records themselves.
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.PersonBrief{}, err
	}
	userID, err := actingUser(ctx)
	if err != nil {
		return crmcontracts.PersonBrief{}, err
	}
	// The gates that matter run HERE, in the caller's own composite read: a
	// brief can only be written from what this caller may see, and a person
	// they cannot read refuses before any cache is consulted.
	view, err := s.view.Assemble(ctx, personID)
	if err != nil {
		return crmcontracts.PersonBrief{}, err
	}
	in := FromView(view)
	fingerprint, err := Fingerprint(in, s.routingVersion)
	if err != nil {
		return crmcontracts.PersonBrief{}, err
	}

	cached, found, err := s.cached(ctx, userID, personID)
	if err != nil {
		return crmcontracts.PersonBrief{}, err
	}
	if found && !force && cached.Version == storedVersion && cached.Fingerprint == fingerprint {
		return cached.wire(personID), nil
	}

	written := stored{
		Fingerprint: fingerprint,
		Version:     storedVersion,
		GeneratedAt: s.now().UTC(),
		GeneratedBy: crmcontracts.Deterministic,
		Sentences:   Deterministic(personID.String(), in),
	}
	if err := s.save(ctx, userID, personID, written); err != nil {
		return crmcontracts.PersonBrief{}, err
	}
	return written.wire(personID), nil
}

func (b stored) wire(personID ids.PersonID) crmcontracts.PersonBrief {
	return crmcontracts.PersonBrief{
		PersonId:    openapi_types.UUID(personID.UUID),
		GeneratedAt: b.GeneratedAt,
		GeneratedBy: b.GeneratedBy,
		Sentences:   wireSentences(b.Sentences),
	}
}

// wireSentences renders the brief, dropping the WHOLE sentence when any
// citation is not an id.
//
// Dropping only the bad citation would leave a readable claim standing on
// partial or empty evidence — the reader sees an assertion about a person with
// nothing to check it against, which is the one thing the grounding rule exists
// to prevent. A sentence is kept only when it cited something and every
// citation parsed.
func wireSentences(in []Sentence) []crmcontracts.OrganizationBriefSentence {
	out := make([]crmcontracts.OrganizationBriefSentence, 0, len(in))
	for _, sentence := range in {
		if len(sentence.Evidence) == 0 {
			continue
		}
		evidence := make([]crmcontracts.OrganizationBriefEvidence, 0, len(sentence.Evidence))
		malformed := false
		for _, cited := range sentence.Evidence {
			parsed, err := ids.Parse(cited.EntityID)
			if err != nil {
				malformed = true
				break
			}
			evidence = append(evidence, crmcontracts.OrganizationBriefEvidence{
				EntityId:   openapi_types.UUID(parsed),
				EntityType: crmcontracts.OrganizationBriefEvidenceEntityType(cited.EntityType),
			})
		}
		if malformed {
			continue
		}
		wired := crmcontracts.OrganizationBriefSentence{Text: sentence.Text, Evidence: evidence}
		if sentence.Nature != "" {
			nature := crmcontracts.OrganizationBriefSentenceNature(sentence.Nature)
			wired.Nature = &nature
		}
		out = append(out, wired)
	}
	return out
}

// cached reads this user's brief for this person. The user_id predicate is
// explicit in SQL: row-level security binds the workspace, so without it one
// rep would read another's brief — written from records they may not share.
func (s *Service) cached(ctx context.Context, userID ids.UserID, personID ids.PersonID) (stored, bool, error) {
	var out stored
	var payload []byte
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT fingerprint, generated_at, generated_by, payload FROM person_brief
			WHERE user_id = $1 AND person_id = $2`,
			userID, personID).Scan(&out.Fingerprint, &out.GeneratedAt, &out.GeneratedBy, &payload)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return stored{}, false, nil
	}
	if err != nil {
		return stored{}, false, err
	}
	// The WHOLE envelope, version included: unmarshalling into the sentence
	// list alone leaves Version at zero, the version check can never pass, and
	// the cache never once answers — a rewrite on every page load, invisible to
	// a build gate because nothing about the served brief looks wrong.
	if err := json.Unmarshal(payload, &out); err != nil {
		// A payload this build cannot read is a cache MISS, not a failure: the
		// brief is derived content, regenerating it is cheap, and the new row
		// replaces the unreadable one.
		//nolint:nilerr // an unreadable cache entry is a miss by design; the caller regenerates
		return stored{}, false, nil
	}
	return out, true, nil
}

func (s *Service) save(ctx context.Context, userID ids.UserID, personID ids.PersonID, brief stored) error {
	payload, err := json.Marshal(brief)
	if err != nil {
		return fmt.Errorf("encode the brief payload: %w", err)
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO person_brief (workspace_id, user_id, person_id, fingerprint,
			                          generated_at, generated_by, payload)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (workspace_id, user_id, person_id) DO UPDATE
			SET fingerprint = EXCLUDED.fingerprint,
			    generated_at = EXCLUDED.generated_at,
			    generated_by = EXCLUDED.generated_by,
			    payload = EXCLUDED.payload`,
			storekit.MustWorkspace(ctx), userID, personID, brief.Fingerprint,
			brief.GeneratedAt, brief.GeneratedBy, payload)
		return err
	})
}

// actingUser resolves the human this brief belongs to. That the brief is
// per-user IS the security posture, so a principal with no user id has no
// brief rather than a shared one.
func actingUser(ctx context.Context) (ids.UserID, error) {
	p, ok := principal.Actor(ctx)
	if !ok || p.UserID == (ids.UUID{}) {
		return ids.UserID{}, fmt.Errorf("the relationship brief is per-user and this call carries no user: %w",
			apperrors.ErrPermissionDenied)
	}
	return ids.From[ids.UserKind](p.UserID), nil
}
