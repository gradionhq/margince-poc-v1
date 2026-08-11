// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package meetingbrief

// The read. There is nothing else in this package that touches storage, because
// the brief is never stored — see doc.go for why this one has no cache when its
// personbrief sibling does.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The brief shares the account brief's claim vocabulary, so a grounding rule
// proved on that side holds here unchanged.
type (
	// Sentence is one written line with the records it was written from.
	Sentence = claims.Sentence
	// Evidence is one record a sentence cites.
	Evidence = claims.Evidence
)

// Assembler reads a person exactly as their reader would see them. Injected
// rather than imported so this package composes one seam instead of
// re-deriving a dozen gated reads that could disagree with the page's own.
type Assembler interface {
	Assemble(ctx context.Context, personID ids.PersonID) (crmcontracts.Person360, error)
}

// ClaimReader reads one person's live conversation claims inside a caller's
// transaction. It is the people store's own read, injected because a module is
// never imported across a compose seam by another module.
type ClaimReader interface {
	ClaimsForPerson(ctx context.Context, tx pgx.Tx, personID ids.PersonID, limit int) ([]crmcontracts.ConversationClaim, error)
}

// briefClaims bounds the commitments the brief reads per attendee. The person
// page's commitments card renders them all; this is prep, and past a handful the
// reader stops reading before the deal sections they came for.
const briefClaims = 8

// Service assembles one pre-meeting brief.
type Service struct {
	pool   *pgxpool.Pool
	view   Assembler
	claims ClaimReader
	now    func() time.Time
}

// NewService binds the brief to the reads it is written from.
func NewService(pool *pgxpool.Pool, view Assembler, claimReader ClaimReader, now func() time.Time) *Service {
	return &Service{pool: pool, view: view, claims: claimReader, now: now}
}

// Get assembles the brief for one meeting, fresh.
//
// The gates run in two places and both are load-bearing. The activity probe
// here decides whether this caller may know the meeting exists at all;
// everything the brief then says about the relationship arrives through the
// caller's own composite read, which carries its own row scope. A brief can
// therefore only describe records this caller could open themselves.
func (s *Service) Get(ctx context.Context, activityID ids.UUID) (crmcontracts.MeetingBrief, error) {
	// A brief is a reading aid for a person walking into a room; an agent
	// reading records through a passport has the records themselves.
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.MeetingBrief{}, err
	}
	// The OBJECT grant, before any row is read. Row scope decides WHICH
	// meetings a caller may see; it does not decide whether they may see
	// meetings at all, and a reader with no activity grant would otherwise
	// reach the brief through a path every sibling read refuses.
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return crmcontracts.MeetingBrief{}, err
	}
	// The brief names the people in the room and what they promised, so it is
	// also a person read — and the caller must hold that grant for the same
	// reason the person page does.
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return crmcontracts.MeetingBrief{}, err
	}
	in, err := s.assembleInput(ctx, activityID)
	if err != nil {
		return crmcontracts.MeetingBrief{}, err
	}
	return crmcontracts.MeetingBrief{
		ActivityId: openapi_types.UUID(activityID),
		// Always the instant of the read. Nothing is stored, so there is no
		// older instant this could honestly report.
		GeneratedAt: s.now().UTC(),
		// No model lane is wired: the sections are the deterministic floor and
		// say so, rather than passing a composition off as a written brief.
		GeneratedBy: crmcontracts.Deterministic,
		Sections:    wireSections(Deterministic(in)),
	}, nil
}

// assembleInput gathers everything the brief is written from.
//
// The meeting and its room come from ONE transaction, because they are one
// consistent answer to "who is in this room". The lead attendee's 360 is
// assembled after it, in its own transaction, on purpose: it is the person
// page's own read and reusing it whole is what keeps the brief and the page
// from disagreeing about what this caller may see.
func (s *Service) assembleInput(ctx context.Context, activityID ids.UUID) (Input, error) {
	var room meeting
	var perAttendee map[ids.UUID][]crmcontracts.ConversationClaim
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		loaded, err := s.readMeeting(ctx, tx, activityID)
		if err != nil {
			return err
		}
		room = loaded
		perAttendee, err = s.claimsPerAttendee(ctx, tx, loaded)
		return err
	})
	if err != nil {
		return Input{}, err
	}

	in := FromMeeting(room, perAttendee, s.now().UTC())
	if len(room.Attendees) == 0 {
		// Nobody in the room this caller may see. The header still stands, and
		// assembling a 360 for a person nobody named would be a read of a
		// record this brief has no reason to touch.
		return in, nil
	}
	view, err := s.view.Assemble(ctx, ids.From[ids.PersonKind](room.Attendees[0].PersonID))
	if err != nil {
		return Input{}, err
	}
	WithCounterpart(&in, view)
	return in, nil
}

// claimsPerAttendee reads what each person in the room has promised, asked and
// decided. It runs inside the meeting's own transaction so the commitments a
// reader is shown are the ones that were true when the room was read.
func (s *Service) claimsPerAttendee(ctx context.Context, tx pgx.Tx, room meeting) (map[ids.UUID][]crmcontracts.ConversationClaim, error) {
	out := make(map[ids.UUID][]crmcontracts.ConversationClaim, len(room.Attendees))
	for _, attendee := range room.Attendees {
		found, err := s.claims.ClaimsForPerson(ctx, tx, ids.From[ids.PersonKind](attendee.PersonID), briefClaims)
		if err != nil {
			return nil, err
		}
		out[attendee.PersonID] = found
	}
	return out, nil
}

// wireSections renders the assembled sections, dropping every sentence whose
// citations are not resolvable ids and then dropping any section that has
// nothing left.
//
// A sentence is dropped WHOLE rather than trimmed: one citing a real record and
// one malformed id is a sentence whose claim may rest on the malformed half, so
// keeping it with the good citation attached would present it as checked when
// it is not. A section reduced to nothing is omitted rather than emitted empty,
// which is what the contract's minItems promises a renderer.
func wireSections(in []Section) []crmcontracts.MeetingBriefSection {
	out := make([]crmcontracts.MeetingBriefSection, 0, len(in))
	for _, section := range in {
		sentences := wireSentences(section.Sentences)
		if len(sentences) == 0 {
			continue
		}
		out = append(out, crmcontracts.MeetingBriefSection{
			Kind:      section.Kind,
			Sentences: sentences,
		})
	}
	return out
}

func wireSentences(in []Sentence) []crmcontracts.OrganizationBriefSentence {
	out := make([]crmcontracts.OrganizationBriefSentence, 0, len(in))
	for _, sentence := range in {
		evidence, ok := wireEvidence(sentence.Evidence)
		if !ok {
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

// wireEvidence parses one sentence's citations, refusing the whole set when any
// of them is not an id. An uncited sentence is refused for the same reason: it
// is a claim with nothing behind it.
func wireEvidence(cited []Evidence) ([]crmcontracts.OrganizationBriefEvidence, bool) {
	if len(cited) == 0 {
		return nil, false
	}
	out := make([]crmcontracts.OrganizationBriefEvidence, 0, len(cited))
	for _, one := range cited {
		parsed, err := ids.Parse(one.EntityID)
		if err != nil {
			return nil, false
		}
		out = append(out, crmcontracts.OrganizationBriefEvidence{
			EntityId:   openapi_types.UUID(parsed),
			EntityType: crmcontracts.OrganizationBriefEvidenceEntityType(one.EntityType),
		})
	}
	return out, true
}
