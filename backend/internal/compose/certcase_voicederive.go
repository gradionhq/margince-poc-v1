// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for voice_build/derive — the one model pass a voice
// build makes over the author's own corpus, and the profile every later voiced
// draft is written from.
//
// It certifies the shipped path in the strongest form this census has: Run calls
// ai.DeriveVoice, the exported entry point the build worker calls, through the
// brain seam that entry point already takes. Nothing here re-creates anything —
// the prompt, the per-call boundary, the response schema and the validator are
// the build's own, reached by running the build rather than by copying it. That
// certifies MORE than an exported request builder would: a case built around a
// lifted builder would still leave the validator, the schema and the order they
// run in outside the claim, and a copy stays green through the change that
// breaks the original.
//
// It is also why nothing in modules/ai is exported for it. The seam that runs
// the whole path already exists, so widening the module's surface to reach one
// piece of it would buy a weaker claim at the price of a wider module.
//
// What the expectation MEANS here is which of the author's own samples the
// derived profile grounds its signature moves in. The rest of a profile is
// prose — how the author thinks, what they avoid — and pinning sentences would
// fail every model that said the same thing differently, which is what the
// rubric and the judge are for. The grounding is not prose: the build demands
// that every signature move quote a verbatim fragment and name the sample it
// came from, so a scenario can say which sample carries the style it is about
// and a profile that invented one cannot satisfy it.
//
// The quote itself is deliberately NOT the expectation. Which fragment of a
// sample proves a move is the model's own reading, and a scenario naming one
// would fail a profile that found an equally verbatim fragment two lines down —
// while a scenario naming a fragment no sample contains could never be satisfied
// at all, because the build refuses a quote it cannot find in the cited sample.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// voiceDeriveSite names this site in every refusal it writes, so a corpus author
// reading one knows which scenario to open.
const voiceDeriveSite = "voice_build/derive"

// voiceDeriveFixture is ONE voice build in exactly what the builder is handed:
// the human-authored identity and the corpus sources the build reads, each in
// the shape the corpus store holds it.
//
// The samples arrive already split — the build reserves a held-out set before it
// derives anything, and what reaches this site is what survived that split — for
// the same reason the identity arrives as text: the certified thing is the
// prompt built from them, not the reads and the split that produced them. What
// those guarantee about them is enforced at Prepare instead.
type voiceDeriveFixture struct {
	Personality string           `json:"personality"`
	Samples     []ai.VoiceSample `json:"samples"`
}

// voiceDeriveCases serves the one site that derives a voice profile.
type voiceDeriveCases struct{}

func (voiceDeriveCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskVoiceBuild,
		Variant: "derive",
		Kind:    ai.SiteKindOneShot,
	}
}

// Prepare turns one corpus and the samples the scenario says the profile grounds
// itself in into a runnable case.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (voiceDeriveCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f voiceDeriveFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("%s: the fixture is not the shape this site takes: %w", voiceDeriveSite, err)
	}
	if err := refuseUnbuildableCorpus(f.Samples); err != nil {
		return nil, err
	}
	// A profile that found the author's style differs from one that invented it
	// in which samples it can point at, so the expectation IS those ids rather
	// than a wrapper carrying them.
	var want []string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf(
			"%s: the expected answer is not a list of corpus sample ids: %w", voiceDeriveSite, err)
	}
	if len(want) == 0 {
		return nil, fmt.Errorf(
			"%s: the scenario names no sample the profile must ground itself in, so it asserts nothing",
			voiceDeriveSite)
	}
	if err := refuseUngroundableVoiceExpectation(want, f.Samples); err != nil {
		return nil, err
	}
	hash, err := voiceDeriveSourceHash(f.Samples)
	if err != nil {
		return nil, fmt.Errorf("%s: the fixture's corpus is not hashable: %w", voiceDeriveSite, err)
	}
	return &voiceDeriveCase{
		personality: f.Personality,
		sourceHash:  hash,
		samples:     f.Samples,
		expected:    want,
	}, nil
}

// refuseUnbuildableCorpus names a corpus the builder could never have been
// handed, and so a prompt the product never sends. Every clause is a bound the
// voice feature already holds: a stored source carries an id and content, its
// word count is the word count of that content, and DeriveVoice refuses a corpus
// under the starter floor before it calls anything.
func refuseUnbuildableCorpus(samples []ai.VoiceSample) error {
	seen := make(map[string]bool, len(samples))
	for _, sample := range samples {
		switch {
		case strings.TrimSpace(sample.ID) == "":
			return fmt.Errorf(
				"%s: the fixture supplies a sample with no id, and the prompt's list of valid ids is the only "+
					"vocabulary a citation has", voiceDeriveSite)
		case seen[sample.ID]:
			return fmt.Errorf(
				"%s: the fixture supplies sample %q twice, and a citation names one source", voiceDeriveSite, sample.ID)
		case strings.TrimSpace(sample.Text) == "":
			return fmt.Errorf(
				"%s: sample %q carries no text, and a source with no content is never stored",
				voiceDeriveSite, sample.ID)
		case sample.WordCount != ai.WordCount(sample.Text):
			return fmt.Errorf(
				"%s: sample %q declares %d words and carries %d, and the selector budgets the prompt on the "+
					"declared count — the two disagreeing sends a corpus this one never would",
				voiceDeriveSite, sample.ID, sample.WordCount, ai.WordCount(sample.Text))
		}
		seen[sample.ID] = true
	}
	if words := ai.AnalyzeVoice(samples).WordCount; words < ai.StarterVoiceWords {
		return fmt.Errorf(
			"%s: the fixture's corpus is %d own-authored words, and a build needs at least %d before it calls a model",
			voiceDeriveSite, words, ai.StarterVoiceWords)
	}
	return nil
}

// refuseUngroundableVoiceExpectation names an expectation this site's own validator
// would never let a reply satisfy. The build closes its validator over the
// SELECTED samples — the ones the word cap left in the prompt — and refuses a
// signature move citing anything else as unknown, so an expectation naming a
// sample the selection drops could only ever be measured as invalid.
func refuseUngroundableVoiceExpectation(want []string, samples []ai.VoiceSample) error {
	supplied := make(map[string]bool, len(samples))
	for _, sample := range samples {
		supplied[sample.ID] = true
	}
	shown := make(map[string]bool, len(samples))
	for _, sample := range ai.SelectVoiceSamples(samples) {
		shown[sample.ID] = true
	}
	for _, id := range want {
		switch {
		case shown[id]:
		case supplied[id]:
			return fmt.Errorf(
				"%s: the scenario expects the profile to ground itself in sample %q, and the prompt's word cap "+
					"drops that sample from the corpus this call is shown", voiceDeriveSite, id)
		default:
			return fmt.Errorf(
				"%s: the scenario expects the profile to ground itself in sample %q, which the fixture never supplies",
				voiceDeriveSite, id)
		}
	}
	return nil
}

// voiceDeriveSourceHash mints the corpus snapshot hash the store hands the
// builder. It identifies the snapshot the samples were read from, reaches
// neither the prompt nor any check, and is stamped on the artifact for the build
// row — so a fixture that supplied it would be describing something the
// certified call does not carry. It is derived from the fixture's own samples
// for the same reason production derives it from the rows it read: one corpus is
// one snapshot.
func voiceDeriveSourceHash(samples []ai.VoiceSample) (string, error) {
	encoded, err := json.Marshal(samples)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// voiceDeriveCase is one corpus ready to be derived from, closed over the
// samples the answer is expected to be grounded in.
type voiceDeriveCase struct {
	personality string
	sourceHash  string
	samples     []ai.VoiceSample
	expected    []string
}

// Run drives ai.DeriveVoice and records the request it issued.
//
// What DeriveVoice returns is its own reading of the reply, and the case does
// not inherit it: Evaluate re-derives from the recorded reply with the same
// entry point, so the record's verdict is measured rather than inferred from the
// absence of an error. So Run carries the raw text the builder was answered
// with, and returns an error only when there was no reply to measure.
func (c *voiceDeriveCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	recorder := &voiceDeriveRecorder{completer: completer}
	_, err := ai.DeriveVoice(ctx, recorder, c.personality, c.sourceHash, c.samples)
	trace := aitasks.Trace{Requests: recorder.requests}
	if recorder.failed != nil {
		return trace, fmt.Errorf("%s: the model call did not complete: %w", voiceDeriveSite, recorder.failed)
	}
	if err != nil && recorder.reply == "" {
		return trace, fmt.Errorf("%s: no reply reached the builder to be measured: %w", voiceDeriveSite, err)
	}
	trace.Output = recorder.reply
	return trace, nil
}

// voiceDeriveRecorder is the brain the build derives through: it records the
// request the build issued and the reply it read, and it deliberately does NOT
// implement the validated-brain seam, so the call is sent bare. Production wraps
// the same request in the shape-retry when the brain supports one, and a case
// that retried would certify the answer a model gives after being told what it
// got wrong rather than the answer it gives.
type voiceDeriveRecorder struct {
	completer aitasks.Completer
	requests  []model.Request
	reply     string
	// failed is the completer's own failure. It is kept apart from the build's
	// refusal because a call that never completed is the lane's problem, not a
	// measurement of the reply.
	failed error
}

func (r *voiceDeriveRecorder) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	r.requests = append(r.requests, req)
	resp, err := r.completer.Complete(ctx, req)
	if err != nil {
		r.failed = err
		return model.Response{}, err
	}
	r.reply = resp.Text
	return resp, nil
}

// Evaluate re-derives the profile from the recorded reply, which runs the
// build's own validator over it in the build's own order: parse, then every
// citation against the samples this call actually showed. Only a profile the
// build would have kept is then asked whether it grounds itself where the
// scenario says the style lives — the order is the meaning, since a reply the
// build refuses has no grounding to disagree with.
//
// Re-deriving costs a rebuilt prompt that is thrown away, and buys a verdict
// that comes from the shipped path instead of from this file. The replay does no
// I/O, so it needs nothing from the run's context.
func (c *voiceDeriveCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	artifact, err := ai.DeriveVoice(
		context.Background(), voiceDeriveReplay{reply: trace.Output}, c.personality, c.sourceHash, c.samples)
	if err != nil {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: err.Error()}
	}
	grounded := make(map[string]bool, len(artifact.Inference.SignatureMoves))
	for _, move := range artifact.Inference.SignatureMoves {
		grounded[move.SampleID] = true
	}
	// All of the missing ones, not the first: a profile that found the style in
	// one sample and missed it in two others is not the near miss one line would
	// read as.
	var missing []string
	for _, id := range c.expected {
		if !grounded[id] {
			missing = append(missing, id)
		}
	}
	cited := slices.Sorted(maps.Keys(grounded))
	if len(missing) > 0 {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: fmt.Sprintf(
				"the derived profile grounds no signature move in %s, which the scenario expects it to draw on; "+
					"it cites %s", strings.Join(missing, ", "), voiceDeriveCitations(cited)),
		}
	}
	return aitasks.Outcome{
		Result: aitasks.OutcomeAccepted,
		Detail: "the derived profile grounds its signature moves in " + voiceDeriveCitations(cited),
	}
}

// voiceDeriveCitations renders what a profile pointed at. A profile with no
// signature move at all passes the build's validator — the citation rules have
// nothing to check — and reads as an empty list here rather than as a blank.
func voiceDeriveCitations(cited []string) string {
	if len(cited) == 0 {
		return "no sample"
	}
	return strings.Join(cited, ", ")
}

// voiceDeriveReplay answers with the reply the run recorded, so Evaluate reaches
// the build's validator the only way it is reachable: by running the build.
type voiceDeriveReplay struct{ reply string }

func (r voiceDeriveReplay) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Text: r.reply}, nil
}
