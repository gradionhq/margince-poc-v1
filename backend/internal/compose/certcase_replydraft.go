// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for draft_reply/reply — the email the product drafts
// for a human to review, in the account owner's own voice where one has been
// built.
//
// It certifies the shipped path rather than a description of it, and here that
// means driving the drafter itself: Run calls completeVoiced, the method the
// transport calls, and lets it decide how many calls this draft takes. That
// decision is the site. A case that issued one request and graded the answer
// would certify the opening attempt of a path whose served draft routinely comes
// from the second or the third.
//
// The site is registered one_shot and issues up to three calls, and both are
// true: the kind names the conversation — no turn of this one is replayed from a
// human — while the retry and the fallback are the product talking to itself.
// Trace.Requests carries all of them, which is what the canary gate reads and
// what a record must show to be about the prompt the draft came from.
//
// ONE site, two system variants. A workspace with a built Voice DNA profile
// sends the voice prompt with the profile block inside the call's own boundary;
// a workspace without one sends the plain prompt. The fixture's voice artifact
// is what selects between them, exactly as the workspace's stored state selects
// between them in production — which is what the task contract's own doc: for
// draft_reply says, and why this is not two sites.
//
// What the expectation MEANS here is the REGISTER the product served the draft
// in: "voiced" or "plain". The draft is free prose and the reply schema offers
// no closed vocabulary to pin instead — pinning its sentences would fail every
// model that said the same thing differently, and that is what the rubric and
// the judge are for. What the product itself reads off a served draft is which
// variant produced it: DraftEmailWithProvenance stamps the voice profile version
// and a draft ref only for a voice-styled draft that cleared the deterministic
// anti-AI floor, and serves the plain fallback unstamped. That one bit is
// decided by the three calls this site makes, so it is the bit a scenario can
// assert and the bit worth asserting.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// replyDraftSite names this site in every refusal it writes, so a corpus author
// reading one knows which scenario to open.
const replyDraftSite = "draft_reply/reply"

// The two registers a served draft can be in — one per system variant, and a
// closed vocabulary, which is what lets an unreachable expectation be named at
// Prepare instead of measured as a zero.
const (
	replyDraftRegisterVoiced = "voiced"
	replyDraftRegisterPlain  = "plain"
)

// replyDraftFixture is ONE draft request in exactly what the drafting path is
// handed: the activity data the transport already bounded, and the workspace's
// Voice DNA state as the voice store returned it. No voice artifact is the
// plain variant, not a missing field.
//
// Both arrive assembled rather than as the rows they came from, because the
// certified thing is the prompt built from them, not the database reads that
// produced them. What the reads guarantee about them is enforced at Prepare
// instead: bounds and a built artifact are what the model is shown, so a fixture
// outside them describes a call the product cannot make.
type replyDraftFixture struct {
	Activity replyActivityData        `json:"activity"`
	Voice    *replyDraftVoiceArtifact `json:"voice"`
}

// replyDraftVoiceArtifact is the active profile pair a voiced call injects,
// carried in the shape the store holds it: the human-authored identity, the
// derived voice profile, the verbatim exemplars and the stylometric
// fingerprint.
//
// The whole fingerprint is carried even though the block renders two of its
// numbers, because which two the block renders is the drafting path's business
// and not the fixture's — a fixture that named the rendered sentences instead
// would let the two drift apart in silence.
type replyDraftVoiceArtifact struct {
	PersonalityMD  string             `json:"personality_md"`
	VoiceProfileMD string             `json:"voice_profile_md"`
	Exemplars      []ai.VoiceExemplar `json:"exemplars"`
	Stats          ai.VoiceStats      `json:"stats"`
	ProfileVersion int                `json:"profile_version"`
}

// replyDraftCases serves the one site that drafts a reply to an activity.
type replyDraftCases struct{}

func (replyDraftCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskDraftReply,
		Variant: "reply",
		Kind:    ai.SiteKindOneShot,
	}
}

// Prepare turns one draft request and the register the scenario expects into a
// runnable case, assembling the voice state through the same ai.VersionExemplars
// and ai.DecodeVersionStats the drafter assembles its block with.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (replyDraftCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f replyDraftFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("%s: the fixture is not the shape this site takes: %w", replyDraftSite, err)
	}
	if err := refuseUnsendableActivity(f.Activity); err != nil {
		return nil, err
	}
	voice, err := replyDraftVoiceState(f.Voice)
	if err != nil {
		return nil, err
	}
	// A correct draft differs from an incorrect one in the register alone, so
	// the expectation IS that token rather than a wrapper carrying it.
	var want string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf("%s: the expected answer is not a register: %w", replyDraftSite, err)
	}
	switch want {
	case replyDraftRegisterPlain:
	case replyDraftRegisterVoiced:
		if !voice.ok {
			return nil, fmt.Errorf(
				"%s: the scenario expects the %q register from a workspace with no Voice DNA state, "+
					"and the plain variant is the only prompt that call can send",
				replyDraftSite, replyDraftRegisterVoiced)
		}
	default:
		return nil, fmt.Errorf(
			"%s: the scenario expects the register %q, and this site serves %q or %q",
			replyDraftSite, want, replyDraftRegisterVoiced, replyDraftRegisterPlain)
	}
	// The anchor keys the learning signal a served draft records; it identifies
	// the activity, never reaches a prompt, and no model has ever seen one. The
	// case mints its own for the same reason the drafter reads production's from
	// the row: a fixture that supplied it would be describing something the
	// certified call does not carry.
	return &replyDraftCase{activity: f.Activity, voice: voice, anchor: ids.NewV7(), expected: want}, nil
}

// refuseUnsendableActivity names an activity the drafting path would never have
// been handed, and so a prompt the product never sends. DraftEmailWithProvenance
// bounds every one of these fields before the model call, so a longer one
// describes a call that cannot happen.
//
// The fields are read back out of the encoded activity — the same encoding the
// prompt carries — so a field added to the activity is bounded here on the day
// it is added rather than on the day someone remembers a list.
func refuseUnsendableActivity(activity replyActivityData) error {
	encoded, err := json.Marshal(activity)
	if err != nil {
		return fmt.Errorf("%s: the fixture's activity is not encodable: %w", replyDraftSite, err)
	}
	var fields map[string]string
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return fmt.Errorf(
			"%s: the fixture's activity is not the flat block the prompt carries: %w", replyDraftSite, err)
	}
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if n := len([]rune(fields[name])); n > replyActivityMaxRunes {
			return fmt.Errorf(
				"%s: the fixture's %s is %d characters, and the drafter is handed at most %d",
				replyDraftSite, name, n, replyActivityMaxRunes)
		}
	}
	return nil
}

// replyDraftVoiceState rebuilds the loaded profile a voiced call injects. A nil
// artifact is the plain variant — the state a workspace is in before its first
// voice build, and the one the drafter also degrades to when the profile read
// fails.
//
// The rebuilt version is read back through ai.VersionExemplars rather than
// trusted, because drafting shows at most two verbatim examples and drops the
// rest without saying so: a fixture whose examples do not survive that would
// certify a block the product never assembles.
func replyDraftVoiceState(artifact *replyDraftVoiceArtifact) (voiceContext, error) {
	if artifact == nil {
		return voiceContext{}, nil
	}
	if strings.TrimSpace(artifact.VoiceProfileMD) == "" {
		return voiceContext{}, fmt.Errorf(
			"%s: the fixture's profile carries no derived voice profile, and only a built one is ever active",
			replyDraftSite)
	}
	profileJSON, err := replyDraftStoredJSON(replyDraftProfileArtifact{Exemplars: artifact.Exemplars})
	if err != nil {
		return voiceContext{}, fmt.Errorf("%s: the fixture's verbatim examples are not storable: %w", replyDraftSite, err)
	}
	statsJSON, err := replyDraftStoredJSON(artifact.Stats)
	if err != nil {
		return voiceContext{}, fmt.Errorf("%s: the fixture's style fingerprint is not storable: %w", replyDraftSite, err)
	}
	version := ai.VoiceProfileVersion{
		ProfileVersion: artifact.ProfileVersion,
		VoiceProfileMD: artifact.VoiceProfileMD,
		ProfileJSON:    profileJSON,
		StatsJSON:      statsJSON,
	}
	if shown := ai.VersionExemplars(version); !slices.Equal(shown, artifact.Exemplars) {
		return voiceContext{}, fmt.Errorf(
			"%s: the fixture supplies %d verbatim examples and drafting shows %d",
			replyDraftSite, len(artifact.Exemplars), len(shown))
	}
	return voiceContext{
		// The profile id keys the learning signal, never a prompt, so it is
		// minted here for the same reason the anchor is.
		profile: ai.VoiceProfile{ID: ids.NewV7(), PersonalityMD: artifact.PersonalityMD},
		version: version,
		ok:      true,
	}, nil
}

// replyDraftProfileArtifact is the one key of a stored profile_json that
// drafting reads.
type replyDraftProfileArtifact struct {
	Exemplars []ai.VoiceExemplar `json:"exemplars"`
}

// replyDraftStoredJSON re-types one artifact field into the untyped map the
// voice store hands back, so the block this case sends is assembled by the same
// decoders the drafter assembles it with.
func replyDraftStoredJSON[T any](value T) (map[string]any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var stored map[string]any
	if err := json.Unmarshal(encoded, &stored); err != nil {
		return nil, err
	}
	return stored, nil
}

// replyDraftCase is one draft request ready to be answered, closed over the
// voice state that selects which variant it is answered in.
type replyDraftCase struct {
	activity replyActivityData
	voice    voiceContext
	anchor   ids.UUID
	expected string
}

// Run drives completeVoiced end to end and records every request it issued.
//
// The drafter is built with a brain and nothing else because the draft path
// itself does no I/O: the activity read happens above it, and the voice store is
// only where a served draft's learning signal lands — a sink a nil store skips,
// which changes what is written after the draft and never the draft.
//
// What completeVoiced returns is its own reading of the reply, and the case does
// not inherit it: Evaluate re-reads the reply with the same validators, so the
// record's verdict is measured rather than inferred from the absence of an
// error. So Run carries the raw text the drafter last read, and returns an error
// only when there was no reply to measure.
func (c *replyDraftCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	recorder := &replyDraftRecorder{completer: completer}
	_, _, _, err := replyDrafter{brain: recorder}.completeVoiced(ctx, c.anchor, c.activity, c.voice)
	trace := aitasks.Trace{Requests: recorder.requests}
	if recorder.failed != nil {
		return trace, fmt.Errorf("%s: the model call did not complete: %w", replyDraftSite, recorder.failed)
	}
	if err != nil && recorder.reply == "" {
		return trace, fmt.Errorf("%s: no reply reached the drafter to be measured: %w", replyDraftSite, err)
	}
	trace.Output = recorder.reply
	return trace, nil
}

// replyDraftRecorder is the brain the drafter drafts through: it records every
// request the drafter issues and the last reply it read, and it deliberately
// does NOT implement validatedBrain, so each call is sent bare. Production wraps
// the same request in the shape-retry when the brain supports one, and a case
// that retried would certify the answer a model gives after being told to try
// again rather than the answer it gives.
type replyDraftRecorder struct {
	completer aitasks.Completer
	requests  []model.Request
	reply     string
	// failed is the completer's own failure. It is kept apart from the
	// drafter's refusal because a call that never completed is the lane's
	// problem, not a measurement of the reply.
	failed error
}

func (r *replyDraftRecorder) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	r.requests = append(r.requests, req)
	resp, err := r.completer.Complete(ctx, req)
	if err != nil {
		r.failed = err
		return model.Response{}, err
	}
	r.reply = resp.Text
	return resp, nil
}

// Evaluate applies the drafter's own checks in the drafter's own order — parse,
// then validateReplyDraft — and only then asks whether the draft was served in
// the register the scenario expects. The order is the meaning: a reply the
// drafter refuses has no register to disagree with.
func (c *replyDraftCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	draft, err := parseReplyDraft(trace.Output)
	if err != nil {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: err.Error()}
	}
	if err := validateReplyDraft(draft); err != nil {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: err.Error()}
	}
	// The floor is the standard a voiced call is made under: completeVoiced will
	// not serve a voice-styled draft that trips it, and when it cannot get a
	// clean one it serves the plain draft instead. A plain draft that trips the
	// floor as well is a voiced call with no clean answer anywhere in it, which
	// is the one path this check can fire on — and unusable is what it is, for a
	// workspace that asked to be written for in its own voice.
	//
	// It runs on the SANITIZED text because the sanitizer edits what is served,
	// and a case that judged the raw reply would judge a draft nobody is shown.
	// A workspace with no Voice DNA state is never held to the floor by the
	// product, so it is not held to it here either.
	if c.voice.ok {
		sanitized := replyDraft{
			Subject: ai.SanitizeAIPatterns(draft.Subject),
			Body:    ai.SanitizeAIPatterns(draft.Body),
		}
		if violations := voiceDraftViolations(sanitized); len(violations) > 0 {
			return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: replyDraftFloorRefusal(violations)}
		}
	}
	register, err := replyDraftServedRegister(trace)
	if err != nil {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: err.Error()}
	}
	if register != c.expected {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: fmt.Sprintf("the drafter served the %s draft where the scenario expects %q",
				register, c.expected),
		}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}

// replyDraftServedRegister names which of the site's two system variants
// produced the served draft. The drafter chooses per call and the LAST call it
// made is that choice — a voiced attempt that could not be cleaned ends in a
// plain one — which is why the trace carries every request and not the first.
func replyDraftServedRegister(trace aitasks.Trace) (string, error) {
	if len(trace.Requests) == 0 {
		return "", errors.New("compose: the drafter issued no request, so no draft was served")
	}
	system := trace.Requests[len(trace.Requests)-1].System
	switch {
	case strings.HasPrefix(system, replyDraftVoiceSystem):
		return replyDraftRegisterVoiced, nil
	case strings.HasPrefix(system, replyDraftSystem):
		return replyDraftRegisterPlain, nil
	default:
		return "", errors.New("compose: the last request is in neither system variant this site sends")
	}
}

// replyDraftFloorRefusal renders the floor's drops in the floor's own words,
// all of them: a draft that broke three rules is not the near miss one line
// would read as.
func replyDraftFloorRefusal(violations []ai.VoiceViolation) string {
	broken := make([]string, 0, len(violations))
	for _, violation := range violations {
		broken = append(broken, violation.Code+" ("+violation.Detail+")")
	}
	return "compose: the served draft still trips the anti-AI floor: " + strings.Join(broken, "; ")
}
