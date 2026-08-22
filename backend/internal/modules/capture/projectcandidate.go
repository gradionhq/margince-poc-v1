// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The uncertain rung of the project attribution ladder (PROJ-FORM-4, T2).
//
// The deterministic rungs in sinkproject.go either know or conclude nothing.
// This rung sits below them and may only ever SUGGEST: when the message reaches
// an account with exactly one live project, or with several of which one is
// clearly nearest by embedding, it writes a candidate and asks a human through
// the approvals engine. It never writes a link. The confirm does, through the
// activities module's relink path, so a human-decided filing and a
// human-typed one are the same write with the same audit row.
//
// Two seams, because both halves belong to other modules: which live projects
// the message reaches is a question about project and relationship rows, and
// staging is the approvals engine's. Compose implements both.

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// The two ways this rung arrives at a project, as project_link_candidate.method
// spells them.
const (
	// MethodSoleLiveProject: the account the message reaches has exactly one
	// live project, so the message is most likely about it.
	MethodSoleLiveProject = "sole_live_project"
	// MethodRankedSimilarity: the account has several live projects and this
	// one's embedding is nearest the message's, above the floor and ahead of
	// the runner-up.
	MethodRankedSimilarity = "ranked_similarity"
)

// Candidate statuses, as project_link_candidate.status spells them.
const (
	CandidateStatusPending   = "pending"
	CandidateStatusConfirmed = "confirmed"
	CandidateStatusRejected  = "rejected"
	// CandidateStatusExpired: the offer's window closed with nobody answering.
	// Not a refusal — the same pairing may be offered again.
	CandidateStatusExpired = "expired"
)

// LiveProject is one project the finder seam answers with: enough to rank it
// and to name it on the card.
type LiveProject struct {
	ID   ids.UUID
	Name string
	Key  string
}

// EvidenceSpan is the character-offset range (0-based, half-open, in runes)
// of the text that names the project, in the field named. Positions, never a
// copy: the candidate row must hold nothing of the correspondence itself.
type EvidenceSpan struct {
	Field string
	Start int
	End   int
}

// ProjectCandidate is what the rung concluded about one message: the project
// it proposes, how it got there, and where in the text a reviewer can check.
type ProjectCandidate struct {
	ActivityID ids.ActivityID
	Project    LiveProject
	Method     string
	Confidence float64
	Evidence   *EvidenceSpan
	// Subject is the message's subject, bounded, so the card can say which
	// message is being filed. It reaches the approval row (which erasure
	// redacts by citation) and never the candidate row.
	Subject string
}

// ProjectCandidateFinder answers which LIVE projects — not archived, not closed
// — the message could be about: those of every account the activity reaches,
// within the caller's project read scope.
type ProjectCandidateFinder interface {
	LiveProjectsReached(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID) ([]LiveProject, error)
}

// ProjectCandidateProposer puts one candidate in front of a human. It reports
// the staged approval's id and whether anything was staged — false when a
// human already refused this very pairing, which is an answer, not a fault.
type ProjectCandidateProposer interface {
	ProposeProjectCandidate(ctx context.Context, candidate ProjectCandidate) (ids.UUID, bool, error)
}

// similarityFloor is the cosine similarity below which the nearest project is
// not proposed at all. Nearest is relative; a message about nothing in
// particular is still nearest to something, and this is what keeps that from
// becoming a question in somebody's inbox.
const similarityFloor = 0.6

// candidateProject is the rung. It runs only after every deterministic rung
// concluded nothing, on the ladder's own read transaction, and answers false
// in every case where the honest answer is "ask nobody": no seam wired, no
// account reached, no live project, several with nothing to tell them apart,
// or a sibling in the same thread that a human already filed elsewhere by
// refusing this project.
func (s *Sink) candidateProject(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields ActivityFields, activityID ids.ActivityID) (ProjectCandidate, bool, error) {
	if s.projectCandidates == nil || s.proposeCandidate == nil {
		return ProjectCandidate{}, false, nil
	}
	live, err := s.projectCandidates.LiveProjectsReached(ctx, tx, activityID)
	if err != nil {
		return ProjectCandidate{}, false, fmt.Errorf("capture: reading the projects the message reaches: %w", err)
	}
	live, err = withoutThreadRefusals(ctx, tx, rec, fields, activityID, live)
	if err != nil || len(live) == 0 {
		return ProjectCandidate{}, false, err
	}
	candidate := ProjectCandidate{ActivityID: activityID, Subject: boundedSubject(fields.Subject)}
	switch {
	case len(live) == 1:
		candidate.Project, candidate.Method, candidate.Confidence = live[0], MethodSoleLiveProject, 1
	default:
		ranked, similarity, found, err := nearestProject(ctx, tx, activityID, live)
		if err != nil || !found {
			return ProjectCandidate{}, false, err
		}
		candidate.Project, candidate.Method, candidate.Confidence = ranked, MethodRankedSimilarity, similarity
	}
	candidate.Evidence = locateProjectMention(candidate.Project, fields)
	return candidate, true, nil
}

// boundedSubject trims the subject to what a card may carry. The bound is the
// ledger's own (MaxCapturedSubjectChars), for the reason it exists there: a
// Subject header is outsider-written and unbounded.
func boundedSubject(subject string) string {
	if utf8.RuneCountInString(subject) <= MaxCapturedSubjectChars {
		return subject
	}
	return string([]rune(subject)[:MaxCapturedSubjectChars])
}

// locateProjectMention finds where the text names the project — by key first,
// because a key is the deliberate reference, then by name — in the subject
// first, because that is where a human looks first. The offsets are in runes,
// which is what a reader counting characters would count; a byte offset into
// a subject with an umlaut points at the wrong character.
//
// nil when nothing names it: the account reach alone is then the evidence, and
// the card says so by carrying no span rather than inventing one.
func locateProjectMention(project LiveProject, fields ActivityFields) *EvidenceSpan {
	for _, field := range []struct{ name, text string }{{"subject", fields.Subject}, {"body", fields.Body}} {
		for _, needle := range []string{project.Key, project.Name} {
			if needle == "" {
				continue
			}
			// Counted in the LOWERED text: lowering maps rune to rune, so the
			// rune count is the original's, while the byte length may not be.
			lowered := strings.ToLower(field.text)
			byteAt := strings.Index(lowered, strings.ToLower(needle))
			if byteAt < 0 {
				continue
			}
			start := utf8.RuneCountInString(lowered[:byteAt])
			return &EvidenceSpan{Field: field.name, Start: start, End: start + utf8.RuneCountInString(needle)}
		}
	}
	return nil
}
