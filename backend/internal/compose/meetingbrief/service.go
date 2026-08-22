// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package meetingbrief

// The read. There is nothing else in this package that touches storage, because
// the brief is never stored — see doc.go for why this one has no cache when its
// personbrief sibling does.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	"github.com/gradionhq/margince/backend/internal/compose/person360"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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
	AssembleScoped(ctx context.Context, personID ids.PersonID, opts person360.AssembleOptions) (crmcontracts.Person360, error)
}

// ClaimReader reads one person's live conversation claims inside a caller's
// transaction. It is the people store's own read, injected because a module is
// never imported across a compose seam by another module.
type ClaimReader interface {
	ClaimsForPerson(ctx context.Context, tx pgx.Tx, personID ids.PersonID, within *ids.ProjectID, limit int) ([]crmcontracts.ConversationClaim, error)
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
	brief, _, err := s.GetFiled(ctx, activityID)
	return brief, err
}

// GetScoped is Get with the project the caller wants to prepare for.
//
// The meeting's own filing wins. A meeting filed under a project is about
// that project, and the brief scopes itself by it with or without a request;
// asking for the same project is agreement, asking for a different one is a
// brief this meeting cannot honestly be, and it answers not-found — the same
// existence-hiding answer the tool surface gives a scope naming a project
// the caller may not see. Only a meeting filed under NO project takes the
// requested scope as its own.
func (s *Service) GetScoped(ctx context.Context, activityID ids.UUID, requested *ids.ProjectID) (crmcontracts.MeetingBrief, error) {
	brief, _, err := s.assembleFiled(ctx, activityID, requested)
	return brief, err
}

// GetFiled is Get plus the project the meeting is filed under, nil when it
// is filed under none. The brief scopes itself by that project without
// saying so on the wire; a surface that lets a caller ask for a project
// needs it to tell an agreeing request from a disagreeing one.
func (s *Service) GetFiled(ctx context.Context, activityID ids.UUID) (crmcontracts.MeetingBrief, *ids.UUID, error) {
	return s.assembleFiled(ctx, activityID, nil)
}

func (s *Service) assembleFiled(ctx context.Context, activityID ids.UUID, requested *ids.ProjectID) (crmcontracts.MeetingBrief, *ids.UUID, error) {
	// NO human gate. It used to read "an agent reading records through a
	// passport has the records themselves", and that argument is what produced
	// two answers to one question: agents could not reach this, so a second
	// prep tool grew beside it with different grounding rules, and the two
	// disagreed about the same meeting. Having the records is not having the
	// brief — the eight sections, the first-time flags, the cited claims.
	//
	// Nothing is widened by admitting an agent. Every gate below is the
	// caller's own: the object grants, the activity probe, and the composite
	// read's row scope. A passport is already capped by the granting human's
	// live seat, so an agent reads exactly the brief that human would.
	// The OBJECT grant, before any row is read. Row scope decides WHICH
	// meetings a caller may see; it does not decide whether they may see
	// meetings at all, and a reader with no activity grant would otherwise
	// reach the brief through a path every sibling read refuses.
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return crmcontracts.MeetingBrief{}, nil, err
	}
	// The brief names the people in the room and what they promised, so it is
	// also a person read — and the caller must hold that grant for the same
	// reason the person page does.
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return crmcontracts.MeetingBrief{}, nil, err
	}
	in, scope, err := s.assembleInput(ctx, activityID, requested)
	if err != nil {
		return crmcontracts.MeetingBrief{}, nil, err
	}
	var filed *ids.UUID
	if in.Project != nil {
		id, err := ids.Parse(in.Project.ID)
		if err != nil {
			// The id travels through the brief's input as the string the
			// header line prints, and is parsed back here.
			return crmcontracts.MeetingBrief{}, nil, fmt.Errorf("meeting brief: project id %q read off the meeting is not an id: %w", in.Project.ID, err)
		}
		filed = &id
	}
	return crmcontracts.MeetingBrief{
		ActivityId: openapi_types.UUID(activityID),
		// Always the instant of the read. Nothing is stored, so there is no
		// older instant this could honestly report.
		GeneratedAt: s.now().UTC(),
		// No model lane is wired: the sections are the deterministic floor and
		// say so, rather than passing a composition off as a written brief.
		GeneratedBy: crmcontracts.Deterministic,
		Scope:       scope,
		Sections:    wireSections(Deterministic(in)),
	}, filed, nil
}

// assembleInput gathers everything the brief is written from.
//
// The meeting and its room come from ONE transaction, because they are one
// consistent answer to "who is in this room". The lead attendee's 360 is
// assembled after it, in its own transaction, on purpose: it is the person
// page's own read and reusing it whole is what keeps the brief and the page
// from disagreeing about what this caller may see.
//
// It answers the scope report beside the input: the lead attendee's page,
// read under the scope, counts what the narrowing kept of their history.
// Nil when the read ran unscoped, and nil too for a scoped room nobody in
// may be seen — there is no attendee whose history the count would be of.
func (s *Service) assembleInput(ctx context.Context, activityID ids.UUID, requested *ids.ProjectID) (Input, *crmcontracts.ProjectScope, error) {
	var room meeting
	var scope *ids.ProjectID
	var perAttendee map[ids.UUID][]crmcontracts.ConversationClaim
	var earlier []priorMeeting
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		loaded, err := s.readMeeting(ctx, tx, activityID)
		if err != nil {
			return err
		}
		room = loaded
		chosen, err := resolveScope(ctx, tx, loaded, requested)
		if err != nil {
			return err
		}
		scope = chosen.project
		perAttendee, err = s.claimsPerAttendee(ctx, tx, loaded, scope)
		if err != nil {
			return err
		}
		earlier, err = s.readPriorMeetings(ctx, tx, loaded, s.now().UTC())
		return err
	})
	if err != nil {
		return Input{}, nil, err
	}

	in := FromMeeting(room, perAttendee, s.now().UTC())
	in.PriorMeetings = foldPriorMeetings(earlier)
	if len(room.Attendees) == 0 {
		// Nobody in the room this caller may see. The header still stands, and
		// assembling a 360 for a person nobody named would be a read of a
		// record this brief has no reason to touch.
		return in, nil, nil
	}
	// Scoped like the claims above: the lead attendee's page read for a
	// meeting on one engagement must not describe the account's other
	// engagement as this room's recent history.
	view, err := s.view.AssembleScoped(ctx, ids.From[ids.PersonKind](room.Attendees[0].PersonID),
		person360.AssembleOptions{ProjectID: scope})
	if err != nil {
		return Input{}, nil, err
	}
	WithCounterpart(&in, view)
	return in, view.Scope, nil
}

// resolveScope resolves the project the brief's reads are narrowed by.
//
// The meeting's own filing comes first and needs no gate beyond the
// meeting's: a caller who may read the meeting may read which project it is
// filed under. A request that names the same project agrees; one that names
// another is refused as not-found, because the brief for a meeting about one
// engagement is not available as a brief about another and the refusal must
// not confirm which project the meeting IS filed under.
//
// A meeting filed under none takes the request as its scope, gated HERE as a
// read of the project (activities.RequireProjectScope) rather than left to
// the lead attendee's 360 — a room with no visible attendee never assembles
// one, and a scope nobody checked would still narrow the claims read above.
//
// The answer is an option rather than a pointer: "narrows nothing" is one of
// its three honest outcomes, not an absence.
func resolveScope(ctx context.Context, tx pgx.Tx, room meeting, requested *ids.ProjectID) (scopeChoice, error) {
	if room.Project != nil {
		filed := ids.From[ids.ProjectKind](room.Project.ID)
		if requested != nil && requested.UUID != filed.UUID {
			return scopeChoice{}, apperrors.ErrNotFound
		}
		return scopeChoice{project: &filed}, nil
	}
	if requested == nil {
		return scopeChoice{}, nil
	}
	if err := activities.RequireProjectScope(ctx, tx, *requested); err != nil {
		return scopeChoice{}, err
	}
	return scopeChoice{project: requested}, nil
}

// scopeChoice is the resolved narrowing: the project the brief's reads are
// scoped by, or none.
type scopeChoice struct {
	project *ids.ProjectID
}

// claimsPerAttendee reads what each person in the room has promised, asked and
// decided. It runs inside the meeting's own transaction so the commitments a
// reader is shown are the ones that were true when the room was read.
func (s *Service) claimsPerAttendee(ctx context.Context, tx pgx.Tx, room meeting, scope *ids.ProjectID) (map[ids.UUID][]crmcontracts.ConversationClaim, error) {
	out := make(map[ids.UUID][]crmcontracts.ConversationClaim, len(room.Attendees))
	for _, attendee := range room.Attendees {
		found, err := s.claims.ClaimsForPerson(ctx, tx, ids.From[ids.PersonKind](attendee.PersonID), scope, briefClaims)
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
