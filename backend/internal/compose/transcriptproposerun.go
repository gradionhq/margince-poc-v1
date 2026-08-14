// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What one reading of one transcript does, from claiming the run record to
// closing it.
//
// The division of labour with transcriptpropose.go is: that file owns the
// question put to the model and what may come back, this one owns the run — the
// claim, the staging, and the three outcomes a rep can be shown. Keeping the
// outcomes here is deliberate, because the difference between them is the
// product: "still reading", "read it and it stated nothing", and "could not
// read it" must never collapse into one another.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/diffhash"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TranscriptProposalKind is the staging kind a transcript reading produces. It
// is registered with an executor in coldstartaccept.go and with a decision
// grant in the approvals module; a kind missing either is one no human can
// decide, which the composition root's fitness tests refuse.
const TranscriptProposalKind = "transcript_proposal"

// transcriptTargetType is what the proposal is filed against: the transcript it
// was read from. The proposal does not MODIFY that activity — it proposes a new
// one — which is why the kind is a context target in approvals rather than a
// version-pinned one.
const transcriptTargetType = "activity"

// TranscriptStepProposal is the staged payload: what was promised, by whom, and
// where in the transcript it was said.
//
// The links are frozen at staging time rather than re-read on accept. A rep
// confirms the proposal they were shown, and re-reading the transcript's links
// at accept would let a relink between the two moments silently move where the
// task lands.
type TranscriptStepProposal struct {
	ActivityID  ids.UUID                       `json:"activity_id"`
	Summary     string                         `json:"summary"`
	Owner       string                         `json:"owner"`
	SourceLines []int                          `json:"source_lines"`
	Links       []activities.ActivityLinkInput `json:"links"`
}

// UnmarshalTranscriptStepProposal reads back what was staged.
func UnmarshalTranscriptStepProposal(raw json.RawMessage) (TranscriptStepProposal, error) {
	var out TranscriptStepProposal
	if err := json.Unmarshal(raw, &out); err != nil {
		return TranscriptStepProposal{}, fmt.Errorf("compose: unmarshal transcript step proposal: %w", err)
	}
	return out, nil
}

// Read performs one reading: claim the run, put the transcript to the model,
// stage what it says was promised, and close the run with what happened.
//
// It returns an error only for a fault the JOB should retry — the model lane
// being down, the database being unreachable. A reading that legitimately
// could not be used closes the run as failed and returns nil, because retrying
// would ask the same question of the same text and get the same answer.
func (p *TranscriptProposer) Read(ctx context.Context, store transcriptReadStore, readID ids.UUID, activityID ids.ActivityID) error {
	if _, err := store.BeginTranscriptRead(ctx, readID); err != nil {
		return err
	}
	reading, err := store.ReadTranscript(ctx, activityID)
	if err != nil {
		return p.fail(ctx, store, readID, err.Error())
	}
	if err := withinReadingBounds(reading.Lines); err != nil {
		return p.fail(ctx, store, readID, err.Error())
	}
	steps, err := p.ask(ctx, reading.Lines)
	if err != nil {
		if errors.Is(err, errRefusedTranscript) {
			p.log.WarnContext(ctx, "transcript reading refused",
				"transcript_read_id", readID, "activity_id", activityID, "reason", err)
			return p.fail(ctx, store, readID,
				"the model's reading of this transcript could not be used; the transcript is unchanged and can be read again")
		}
		return err
	}
	kept := aboveFloor(steps)
	if len(kept) == 0 {
		return store.FinishTranscriptRead(ctx, readID, activities.TranscriptReadOutcome{
			Status: activities.TranscriptReadDone,
			Detail: "this transcript states no next steps clearly enough to propose one",
		})
	}
	staged, err := p.stage(ctx, kept, reading, activityID)
	if err != nil {
		return err
	}
	return store.FinishTranscriptRead(ctx, readID, activities.TranscriptReadOutcome{
		Status:      activities.TranscriptReadDone,
		ProposalIDs: staged,
	})
}

// fail closes the run with a reason a rep can act on. A failure to record the
// failure is returned, so a run cannot be left claimed and silent.
func (p *TranscriptProposer) fail(ctx context.Context, store transcriptReadStore, readID ids.UUID, detail string) error {
	return store.FinishTranscriptRead(ctx, readID, activities.TranscriptReadOutcome{
		Status: activities.TranscriptReadFailed,
		Detail: detail,
	})
}

// stage files each next step as its own question, under one bundle id.
//
// One bundle because they were asked together: a meeting that produced three
// commitments is one act of reading, and an inbox showing them as three
// unrelated questions makes the rep reconstruct that themselves (0200). Each
// still keeps its own diff hash, expiry and verdict — accepting two and
// rejecting one is the whole point.
func (p *TranscriptProposer) stage(
	ctx context.Context, steps []proposedStep, reading activities.TranscriptReading, activityID ids.ActivityID,
) ([]ids.UUID, error) {
	bundleID := ids.NewV7()
	staged := make([]ids.UUID, 0, len(steps))
	for _, step := range steps {
		proposal := TranscriptStepProposal{
			ActivityID:  activityID.UUID,
			Summary:     step.Summary,
			Owner:       step.Owner,
			SourceLines: step.SourceLines,
			Links:       reading.Links,
		}
		raw, err := json.Marshal(proposal)
		if err != nil {
			return nil, fmt.Errorf("compose: marshal transcript step proposal: %w", err)
		}
		canonical, hash, err := diffhash.Canonical(raw)
		if err != nil {
			return nil, fmt.Errorf("compose: canonicalize transcript step proposal: %w", err)
		}
		approvalID, err := p.approval.Stage(ctx, approvals.StageInput{
			Kind:           TranscriptProposalKind,
			ProposedChange: canonical,
			DiffHash:       hash,
			TargetType:     transcriptTargetType,
			TargetID:       activityID.UUID,
			Summary:        step.Summary,
			Evidence:       []approvals.Evidence{stepEvidence(step, reading.Lines, activityID)},
			BundleID:       bundleID,
		})
		if err != nil {
			return nil, err
		}
		staged = append(staged, approvalID.UUID)
	}
	return staged, nil
}
