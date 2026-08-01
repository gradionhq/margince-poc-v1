// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgbrief

// The cache and the read around it.
//
// A cached brief is served only while its fingerprint still matches the
// account; the moment it does not, the brief is REWRITTEN before the request
// answers. So a reader is never handed text that describes a state of play
// the account has moved on from.
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

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Assembler reads the account exactly as its reader would see it. The
// composite read is injected rather than imported so this package composes
// one seam instead of re-deriving nine gated reads.
type Assembler interface {
	Assemble(ctx context.Context, orgID ids.OrganizationID) (crmcontracts.Organization360, error)
}

// ProfileReader is what the company IS, as opposed to how it stands with us:
// the curated statements a site read produced and a human accepted. The 360
// does not carry them — it is the working state of the relationship — so the
// brief reads them through their own gated store method, under the same
// caller's authority.
//
// Optional: a deployment that does not wire it gets a brief about the
// relationship alone, which is what the brief was before.
type ProfileReader interface {
	ListOrganizationProfileFields(ctx context.Context, orgID ids.OrganizationID) ([]crmcontracts.CompanyProfileField, error)
}

// Service writes and caches the brief.
type Service struct {
	pool    *pgxpool.Pool
	view    Assembler
	profile ProfileReader
	lane    Completer
	now     func() time.Time
	// routingVersion identifies the model binding in the fingerprint, so
	// re-pointing the lane rewrites briefs rather than leaving text
	// attributed to a model that no longer writes it.
	routingVersion string
}

// NewService binds the brief to the composite read it is written from and
// the model lane that writes it. lane may be nil: that is a deployment
// running no model, and the deterministic floor is the answer.
func NewService(pool *pgxpool.Pool, view Assembler, profile ProfileReader, lane Completer, routingVersion string, now func() time.Time) *Service {
	return &Service{pool: pool, view: view, profile: profile, lane: lane, routingVersion: routingVersion, now: now}
}

// assemble reads both halves the brief is written from: how the account
// stands with us, and what the company is. A profile read that fails is not
// an error — the relationship half is the part a rep opens the page for, and
// a brief without the company description is still worth its card.
func (s *Service) assemble(ctx context.Context, orgID ids.OrganizationID) (Input, error) {
	view, err := s.view.Assemble(ctx, orgID)
	if err != nil {
		return Input{}, err
	}
	in := FromView(view)
	if s.profile != nil {
		fields, profileErr := s.profile.ListOrganizationProfileFields(ctx, orgID)
		if profileErr == nil {
			in.foldProfile(fields)
		}
	}
	return in, nil
}

// Get serves the brief, regenerating when the cache no longer matches.
// force skips the cache entirely — the explicit refresh.
func (s *Service) Get(ctx context.Context, orgID ids.OrganizationID, force bool) (crmcontracts.OrganizationBrief, error) {
	// A brief is a reading aid for a person; an agent reading records
	// through a passport has the records themselves.
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.OrganizationBrief{}, err
	}
	userID, err := actingUser(ctx)
	if err != nil {
		return crmcontracts.OrganizationBrief{}, err
	}
	// The gates that matter run HERE, in the caller's own composite read: a
	// brief can only be written from what this caller may see, and an
	// account they cannot read refuses before any cache is consulted.
	in, err := s.assemble(ctx, orgID)
	if err != nil {
		return crmcontracts.OrganizationBrief{}, err
	}
	fingerprint, err := Fingerprint(in, s.routingVersion)
	if err != nil {
		return crmcontracts.OrganizationBrief{}, err
	}

	cached, found, err := s.cached(ctx, userID, orgID)
	if err != nil {
		return crmcontracts.OrganizationBrief{}, err
	}
	if found && !force && cached.Fingerprint == fingerprint {
		return cached.wire(orgID), nil
	}

	sentences, by, err := Write(ctx, s.lane, orgID.String(), in)
	if err != nil {
		return crmcontracts.OrganizationBrief{}, err
	}
	written := stored{
		Fingerprint: fingerprint,
		GeneratedAt: s.now().UTC(),
		GeneratedBy: by,
		Sentences:   sentences,
	}
	if err := s.save(ctx, userID, orgID, written); err != nil {
		return crmcontracts.OrganizationBrief{}, err
	}
	return written.wire(orgID), nil
}

// Ask answers one prepared question about the account.
//
// Nothing is cached. A brief is standing text every visit re-reads, so a cache
// earns its keep there; a question is asked once and read once, and a cached
// answer would only introduce the chance of answering from an account state
// the reader has already moved past.
func (s *Service) Ask(
	ctx context.Context, orgID ids.OrganizationID, raw crmcontracts.OrganizationQuestion,
) (crmcontracts.OrganizationAnswer, error) {
	// A prepared question is a reading aid for a person; an agent asking about
	// an account has the records themselves.
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.OrganizationAnswer{}, err
	}
	question, err := ParseQuestion(raw)
	if err != nil {
		return crmcontracts.OrganizationAnswer{}, err
	}
	// The gates that matter run HERE, in the caller's own composite read: an
	// answer can only be written from what this caller may see, and an account
	// they cannot read refuses before a single word is written.
	in, err := s.assemble(ctx, orgID)
	if err != nil {
		return crmcontracts.OrganizationAnswer{}, err
	}
	sentences, by, err := Answer(ctx, s.lane, question, orgID.String(), in)
	if err != nil {
		return crmcontracts.OrganizationAnswer{}, err
	}
	answer := stored{GeneratedAt: s.now().UTC(), GeneratedBy: by, Sentences: sentences}.wire(orgID)
	return crmcontracts.OrganizationAnswer{
		OrganizationId: answer.OrganizationId,
		Question:       question,
		GeneratedAt:    answer.GeneratedAt,
		GeneratedBy:    answer.GeneratedBy,
		Sentences:      answer.Sentences,
	}, nil
}

// stored is the cached payload's shape.
type stored struct {
	Fingerprint string                 `json:"-"`
	GeneratedAt time.Time              `json:"generated_at"`
	GeneratedBy crmcontracts.WrittenBy `json:"generated_by"`
	Sentences   []Sentence             `json:"sentences"`
}

func (b stored) wire(orgID ids.OrganizationID) crmcontracts.OrganizationBrief {
	sentences := make([]crmcontracts.OrganizationBriefSentence, 0, len(b.Sentences))
	for _, sentence := range b.Sentences {
		evidence := make([]crmcontracts.OrganizationBriefEvidence, 0, len(sentence.Evidence))
		for _, cited := range sentence.Evidence {
			parsed, err := ids.Parse(cited.EntityID)
			if err != nil {
				// A citation that is not an id cannot be opened, so it is
				// not evidence — dropped rather than rendered as a dead link.
				continue
			}
			evidence = append(evidence, crmcontracts.OrganizationBriefEvidence{
				EntityId:   openapi_types.UUID(parsed),
				EntityType: crmcontracts.OrganizationBriefEvidenceEntityType(cited.EntityType),
			})
		}
		sentences = append(sentences, crmcontracts.OrganizationBriefSentence{
			Text: sentence.Text, Evidence: evidence,
		})
	}
	return crmcontracts.OrganizationBrief{
		OrganizationId: openapi_types.UUID(orgID.UUID),
		GeneratedAt:    b.GeneratedAt,
		GeneratedBy:    b.GeneratedBy,
		Sentences:      sentences,
	}
}

// cached reads this user's brief for this account. The user_id predicate is
// explicit in SQL: RLS binds the workspace, so without it one rep would
// read another's brief — which was written from records they may not share.
func (s *Service) cached(ctx context.Context, userID ids.UserID, orgID ids.OrganizationID) (stored, bool, error) {
	var out stored
	var payload []byte
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT fingerprint, generated_at, generated_by, payload FROM org_brief
			WHERE user_id = $1 AND organization_id = $2`,
			userID, orgID).Scan(&out.Fingerprint, &out.GeneratedAt, &out.GeneratedBy, &payload)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return stored{}, false, nil
	}
	if err != nil {
		return stored{}, false, err
	}
	if err := json.Unmarshal(payload, &out.Sentences); err != nil {
		// A payload this build cannot read is a cache MISS, not a failure:
		// the brief is derived content, regenerating it costs one call, and
		// the new row replaces the unreadable one.
		//nolint:nilerr // an unreadable cache entry is a miss by design; the caller regenerates
		return stored{}, false, nil
	}
	return out, true, nil
}

func (s *Service) save(ctx context.Context, userID ids.UserID, orgID ids.OrganizationID, brief stored) error {
	payload, err := json.Marshal(brief.Sentences)
	if err != nil {
		return fmt.Errorf("encode the brief payload: %w", err)
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO org_brief (workspace_id, user_id, organization_id, fingerprint,
			                       generated_at, generated_by, payload)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (workspace_id, user_id, organization_id) DO UPDATE
			SET fingerprint = EXCLUDED.fingerprint,
			    generated_at = EXCLUDED.generated_at,
			    generated_by = EXCLUDED.generated_by,
			    payload = EXCLUDED.payload`,
			storekit.MustWorkspace(ctx), userID, orgID, brief.Fingerprint,
			brief.GeneratedAt, brief.GeneratedBy, payload)
		return err
	})
}

// actingUser resolves the human this brief belongs to. Acknowledging that
// the brief is per-user is the whole security posture, so a principal with
// no user id has no brief rather than a shared one.
func actingUser(ctx context.Context) (ids.UserID, error) {
	p, ok := principal.Actor(ctx)
	if !ok || p.UserID == (ids.UUID{}) {
		return ids.UserID{}, fmt.Errorf("the account brief is per-user and this call carries no user: %w",
			apperrors.ErrPermissionDenied)
	}
	return ids.From[ids.UserKind](p.UserID), nil
}
