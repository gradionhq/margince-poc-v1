// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The pre-meeting brief, as the agent surface reads it.
//
// meetingbrief is a compose subpackage and agents is a module, so the edge
// between them is wired here like every other cross-module edge (ADR-0054 §9).
// What crosses is one function: assemble the brief for one meeting.
//
// It exists because there were TWO answers to "prepare me for this meeting".
// A person read eight cited sections; an agent asking the same question got a
// separate context walk with its open tasks pulled forward, sharing no code
// with the brief. Both were individually reasonable, which is why the drift
// went unnoticed. One engine, two surfaces.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/meetingbrief"
	"github.com/gradionhq/margince/backend/internal/compose/person360"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// meetingBriefReader binds the tool to the same service the person page calls.
//
// Nothing about the read changes here — the service applies the caller's own
// object grants and row scope, refuses a non-meeting with not-found, and
// assembles fresh on every call because a cached brief is the one thing a
// pre-meeting read must not be. The only difference between the two surfaces
// is who is asking.
func meetingBriefReader(pool *pgxpool.Pool) agents.MeetingBriefReader {
	peopleStore := people.NewStore(InstallationDB(pool))
	view := person360.NewService(pool, peopleStore, deals.NewStore(InstallationDB(pool), DealsInstallation()),
		consent.NewStore(InstallationDB(pool)),
		ai.NewFeedbackStore(InstallationDB(pool)), time.Now)
	service := meetingbrief.NewService(pool, view, peopleStore, time.Now)
	return func(ctx context.Context, activityID ids.UUID) (agents.MeetingBriefResult, error) {
		brief, filed, err := service.GetFiled(ctx, activityID)
		if err != nil {
			return agents.MeetingBriefResult{}, err
		}
		out := agentMeetingBrief(brief)
		out.ProjectID = filed
		return out, nil
	}
}

// agentMeetingBrief maps the wire brief into the tool surface's own shape.
//
// The citations survive the crossing, and that is the point of mapping rather
// than flattening: the brief drops any sentence whose citations do not
// resolve, so what arrives here is a set of claims each naming records the
// caller can open. Prose alone would hand an agent something it cannot act on.
func agentMeetingBrief(brief crmcontracts.MeetingBrief) agents.MeetingBriefResult {
	out := agents.MeetingBriefResult{
		ActivityID:  ids.UUID(brief.ActivityId),
		GeneratedAt: brief.GeneratedAt.UTC().Format(time.RFC3339),
		GeneratedBy: string(brief.GeneratedBy),
	}
	for _, section := range brief.Sections {
		part := agents.MeetingBriefPart{Kind: string(section.Kind)}
		for _, sentence := range section.Sentences {
			part.Sentences = append(part.Sentences, agentBriefLine(sentence))
		}
		out.Sections = append(out.Sections, part)
	}
	return out
}

func agentBriefLine(sentence crmcontracts.OrganizationBriefSentence) agents.MeetingBriefLine {
	line := agents.MeetingBriefLine{Text: sentence.Text}
	if sentence.Nature != nil {
		line.Nature = string(*sentence.Nature)
	}
	for _, cited := range sentence.Evidence {
		line.Evidence = append(line.Evidence, agents.MeetingBriefCite{
			RecordType: string(cited.EntityType),
			RecordID:   ids.UUID(cited.EntityId),
		})
	}
	return line
}
