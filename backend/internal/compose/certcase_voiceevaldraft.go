// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for voice_build/eval_draft — the held-out drafting a
// voice build grades its own candidate on before the profile is allowed to
// write anything for a human.
//
// It certifies the shipped path rather than a description of it: the request
// comes from voiceEvalDraftRequest, the same builder the evaluation loop calls,
// and the reply is read by readVoiceEvalDraft, the evaluation's own reading of a
// draft. A case that rebuilt either would measure a copy, and a copy stays green
// through the change that breaks the original.
//
// ONE call of this site is one held-out sample at one REPEAT, and the repeat is
// in the fixture because it is in the prompt: the evaluation asks the same
// question voiceEvalRepeatsPerPrompt times and the variation suffix is the only
// thing that differs between those calls. A fixture without it could only
// certify one of the three prompts the product sends.
//
// What the expectation MEANS here is the stylometric floor the scenario claims a
// draft written to this profile reaches: how close the draft's own deterministic
// fingerprint — sentence rhythm and punctuation rates — sits to the corpus
// fingerprint, on the product's own [0,1] scale. That number is half of the
// score this site's draft is folded into, and it is the half that is decided
// here rather than by the judge: the other half is a model's opinion, and the
// rubric and the bands are where an opinion belongs. It is a FLOOR and not a
// value because a draft may legitimately land anywhere above it — pinning a
// number would fail a model for writing closer to the author than the scenario
// dared to claim.
//
// A tell is neither a wrong answer nor an unusable draft: it is a cost the
// record carries. The evaluation counts every tell the sanitizer could not
// remove, KEEPS the draft, and scores it — the count is spent later, on whether
// the whole candidate may activate, once every held-out prompt is folded
// together. So this case reports the tells beside its measurement rather than
// refusing the draft over them, because a draft the evaluation went on to grade
// is not one this record may call unmeasurable.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// voiceEvalDraftSite names this site in every refusal it writes, so a corpus
// author reading one knows which scenario to open.
const voiceEvalDraftSite = "voice_build/eval_draft"

// voiceEvalDraftFixture is ONE held-out drafting call in exactly what the
// evaluation hands it: the human-authored identity, the candidate's derived
// profile with its verbatim examples and its style fingerprint, the reserved
// sample being replied to, and which repeat of that sample this call is.
//
// The candidate arrives assembled rather than as the corpus it was built from,
// because the certified thing is the prompt built from it, not the build that
// produced it. What the build guarantees about it is enforced at Prepare
// instead.
type voiceEvalDraftFixture struct {
	Personality    string                 `json:"personality"`
	VoiceProfileMD string                 `json:"voice_profile_md"`
	Exemplars      []ai.VoiceExemplar     `json:"exemplars"`
	Stats          ai.VoiceStats          `json:"stats"`
	HeldOut        voiceEvalHeldOutSample `json:"held_out"`
	Repeat         int                    `json:"repeat"`
}

// voiceEvalHeldOutSample is the reserved sample the draft replies to, in the two
// fields that reach the prompt. The sample's id, kind, weight and word count
// select which sample is reserved and never leave the split, so a fixture
// carrying them would describe something the certified call does not send.
type voiceEvalHeldOutSample struct {
	Register string `json:"register"`
	Text     string `json:"text"`
}

// voiceEvalDraftCases serves the one site that drafts against a held-out sample.
type voiceEvalDraftCases struct{}

func (voiceEvalDraftCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskVoiceBuild,
		Variant: "eval_draft",
		Kind:    ai.SiteKindOneShot,
	}
}

// Prepare turns one held-out drafting call and the floor the scenario claims it
// clears into a runnable case.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (voiceEvalDraftCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f voiceEvalDraftFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("%s: the fixture is not the shape this site takes: %w", voiceEvalDraftSite, err)
	}
	if err := refuseUnevaluableCandidate(f); err != nil {
		return nil, err
	}
	// A correct draft differs from an incorrect one in how close it sits to the
	// corpus, so the expectation IS that number rather than a wrapper carrying
	// it.
	var floor float64
	if err := json.Unmarshal(expected, &floor); err != nil {
		return nil, fmt.Errorf(
			"%s: the expected answer is not a stylometric floor in [0,1]: %w", voiceEvalDraftSite, err)
	}
	switch {
	case floor <= 0:
		return nil, fmt.Errorf(
			"%s: the scenario expects a floor of %g, which every draft clears — including one with nothing in "+
				"common with the corpus — so it asserts nothing", voiceEvalDraftSite, floor)
	case floor > 1:
		return nil, fmt.Errorf(
			"%s: the scenario expects a floor of %g, and stylometric proximity is at most 1",
			voiceEvalDraftSite, floor)
	}
	return &voiceEvalDraftCase{
		personality: f.Personality,
		artifact: ai.VoiceArtifact{
			Markdown:  f.VoiceProfileMD,
			Stats:     f.Stats,
			Exemplars: f.Exemplars,
		},
		sample: ai.VoiceSample{Register: f.HeldOut.Register, Text: f.HeldOut.Text},
		repeat: f.Repeat,
		floor:  floor,
	}, nil
}

// refuseUnevaluableCandidate names a fixture the evaluation could never have
// been handed, and so a prompt the product never sends. Every clause below is a
// bound the build itself already holds: DeriveVoice refuses a corpus under the
// starter floor and returns a compiled profile, SelectExemplars keeps at most
// two non-empty excerpts, every ingested sample carries a register, and the loop
// runs a fixed number of repeats over a sample that has something in it.
func refuseUnevaluableCandidate(f voiceEvalDraftFixture) error {
	switch {
	case strings.TrimSpace(f.VoiceProfileMD) == "":
		return fmt.Errorf(
			"%s: the fixture's candidate carries no derived voice profile, and only a built one is ever evaluated",
			voiceEvalDraftSite)
	case len(f.Exemplars) > 2:
		return fmt.Errorf(
			"%s: the fixture supplies %d verbatim examples, and a build keeps at most 2",
			voiceEvalDraftSite, len(f.Exemplars))
	case f.Stats.WordCount < ai.StarterVoiceWords:
		return fmt.Errorf(
			"%s: the fixture's corpus is %d own-authored words, and a build needs at least %d",
			voiceEvalDraftSite, f.Stats.WordCount, ai.StarterVoiceWords)
	case strings.TrimSpace(f.HeldOut.Register) == "":
		return fmt.Errorf(
			"%s: the fixture's held-out sample names no register, and the drafting task is asked in one",
			voiceEvalDraftSite)
	case evalSampleOpening(ai.VoiceSample{Text: f.HeldOut.Text}) == "":
		return fmt.Errorf(
			"%s: the fixture's held-out sample is empty, so the draft has nothing to reply to", voiceEvalDraftSite)
	case f.Repeat < 0 || f.Repeat >= voiceEvalRepeatsPerPrompt:
		return fmt.Errorf(
			"%s: the fixture is repeat %d, and the evaluation repeats each held-out prompt %d times",
			voiceEvalDraftSite, f.Repeat, voiceEvalRepeatsPerPrompt)
	}
	for _, exemplar := range f.Exemplars {
		if strings.TrimSpace(exemplar.Text) == "" {
			return fmt.Errorf(
				"%s: the fixture supplies a verbatim example with no text, which the selector never keeps",
				voiceEvalDraftSite)
		}
	}
	return nil
}

// voiceEvalDraftCase is one held-out drafting call ready to be answered, closed
// over the candidate whose fingerprint the answer is measured against.
type voiceEvalDraftCase struct {
	personality string
	artifact    ai.VoiceArtifact
	sample      ai.VoiceSample
	repeat      int
	floor       float64
}

// Run issues the one request this site sends, bare — and so does the
// evaluation: it drafts over the plain completer seam, never asking for the
// shape-retry, so one draft is one call there and here alike.
func (c *voiceEvalDraftCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := voiceEvalDraftRequest(c.personality, c.artifact, c.sample, c.repeat)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("%s: %w", voiceEvalDraftSite, err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the evaluation's own steps in the evaluation's own order —
// read the draft, then measure what it reads like — and only then asks whether
// it sits as close to the author as the scenario claims. The order is the
// meaning: a draft the evaluation cannot read has no fingerprint to disagree
// with.
//
// The proximity is measured on the SANITIZED body, because sanitized is the text
// the evaluation scores and caches; a case that measured the raw reply would
// measure a draft nobody keeps.
//
// The anti-AI floor is not one of those steps. The evaluation does not spend it
// here: it counts the tells, keeps the draft and scores it, and charges the
// count against the whole candidate's activation once every held-out prompt is
// in. A case that refused the draft over a tell would call a run unmeasurable
// that the build measured, and would lose the one measurement this site takes —
// so the tells go into the Detail, where a corpus author reading an accepted run
// still learns the draft earned the build a review.
func (c *voiceEvalDraftCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	reply, err := readVoiceEvalDraft(trace.Output)
	if err != nil {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: err.Error()}
	}
	proximity := stylometricProximity(c.artifact.Stats, reply.body)
	result := aitasks.OutcomeAccepted
	detail := fmt.Sprintf("the draft sits at %.4f of the corpus fingerprint", proximity)
	if proximity < c.floor {
		result = aitasks.OutcomeWrongAnswer
		detail += fmt.Sprintf(", and the scenario expects at least %.4f", c.floor)
	}
	if len(reply.tells) > 0 {
		detail += "; " + voiceEvalTellNote(reply.tells)
	}
	return aitasks.Outcome{Result: result, Detail: detail}
}

// voiceEvalTellNote renders the floor's finds in the floor's own words, all of
// them: a draft that broke three rules is not the near miss one line would read
// as.
func voiceEvalTellNote(tells []ai.VoiceViolation) string {
	found := make([]string, 0, len(tells))
	for _, tell := range tells {
		found = append(found, tell.Code+" ("+tell.Detail+")")
	}
	return "the evaluation counts these anti-AI hard failures against the candidate: " + strings.Join(found, "; ")
}
