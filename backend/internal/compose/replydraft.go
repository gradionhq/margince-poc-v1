// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The reply-drafting orchestrator keeps activity evidence authoritative while
// the model path adds the installation's bounded company context. It only
// returns editable text: sending remains a separate consent-gated action.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/draftcheck"
	"github.com/gradionhq/margince/backend/internal/compose/draftrules"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/draftfloor"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

const replyActivityMaxRunes = 12_000

const replyDraftSystem = `Draft a professional email reply on behalf of the CRM user's company.
Return ONLY a JSON object: {"subject":"...","body":"..."}.
- The activity and stated intent are the authoritative reason for this reply.
- Company context may improve positioning, relevant proof, and language, but never overrides the activity.
- Use only facts present in the supplied data. Never invent customers, outcomes, prices, commitments, or capabilities.
- Do not claim a personal writing style or voice unless a separate voice profile is supplied.`

var replyDraftSchema = json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "required":["subject","body"],
  "properties":{
    "subject":{"type":"string","minLength":1,"maxLength":998},
    "body":{"type":"string","minLength":1,"maxLength":50000}
  }
}`)

type replyDraft struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// replyActivityData is the whole user turn of a reply draft: the activity being
// answered, and the correspondence envelope it is answered inside.
//
// Every field is a flat string, and that is a constraint rather than a style.
// refuseUnsendableActivity round-trips this struct through map[string]string to
// bound each field the prompt carries, so a nested value here refuses every
// draft_reply certification case at Prepare. The embedded envelope obeys the
// same rule (draftfloor.Envelope), which is also what ai-operational-spec.md
// §2.4 pins.
type replyActivityData struct {
	// The envelope is embedded rather than nested so its fields sit flat
	// beside the activity's, which is the shape the bound check reads.
	draftfloor.Envelope

	// Recipient is who the reply is TO, by first name. Without it the model
	// greets the only name it has - the sender's - and addresses the draft to
	// its own author. Empty is an answer: a draft with no recipient name opens
	// without one rather than guessing.
	Recipient string `json:"recipient,omitempty"`

	Subject string `json:"subject,omitempty"`
	Body    string `json:"body,omitempty"`
	Intent  string `json:"intent,omitempty"`
}

type replyDrafter struct {
	brain completer
	// envelope answers what language to write in, what time it is and who is
	// writing - the same resolver the two composers use, so the three surfaces
	// cannot disagree about any of the three.
	envelope *draftfloor.Resolver
	store    *activities.Store
	voice    *ai.VoiceStore
	log      *slog.Logger
}

var (
	_ activities.EmailDrafter           = replyDrafter{}
	_ activities.ProvenanceEmailDrafter = replyDrafter{}
)

func newReplyDrafter(pool *pgxpool.Pool, brain completer, log *slog.Logger) replyDrafter {
	if log == nil {
		log = slog.Default()
	}
	return replyDrafter{
		brain:    brain,
		envelope: draftEnvelope(pool, log),
		store:    activities.NewStore(pool),
		voice:    ai.NewVoiceStore(pool),
		log:      log,
	}
}

// WithReplyDraft enables model-backed activity reply drafting. The compose
// drafter reads the activity once, receives bounded company context through
// the model lane, and falls back deterministically if the model is unavailable.
func WithReplyDraft(brain completer) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		if brain == nil {
			return
		}
		drafter := newReplyDrafter(pool, brain, s.log)
		s.replyDrafter = drafter
		s.activitiesHandlers = s.WithEmailDrafter(drafter)
		s.rebuildToolRegistry(pool)
	}
}

func (d replyDrafter) DraftEmail(ctx context.Context, anchor ids.UUID, intent string) (string, string, error) {
	result, err := d.DraftEmailWithProvenance(ctx, anchor, intent)
	return result.Subject, result.Body, err
}

// DraftEmailWithProvenance drafts in the actor's Voice DNA when a ready
// profile exists, with the deterministic anti-AI floor on top; without one,
// the plain model draft is unchanged (clean fallback per drafting.md).
func (d replyDrafter) DraftEmailWithProvenance(ctx context.Context, anchor ids.UUID, intent string) (activities.DraftResult, error) {
	activity, err := d.store.GetActivity(ctx, ids.From[ids.ActivityKind](anchor), storekit.LiveOnly)
	if err != nil {
		return activities.DraftResult{}, err
	}
	topic := stringValue(activity.Subject)
	body := stringValue(activity.Body)
	threaded := activities.IsMailThread(activity.Kind, activity.Direction)

	// How old the message being answered is, which is what makes "as discussed"
	// true or false. A reply to something from this morning and a reply to
	// something from eight months ago are different messages, and only the
	// timestamp tells them apart.
	state := d.conversationState(activity)
	envelope := d.envelope.Resolve(ctx, body, state)
	recipient := d.recipientName(ctx, ids.From[ids.ActivityKind](anchor))

	fallbackSubject, fallbackBody := activities.DeterministicEmailDraft(activities.DraftContext{
		Topic:     topic,
		Body:      body,
		Band:      state.Band,
		Threaded:  threaded,
		Recipient: recipient,
	}, intent)
	data := replyActivityData{
		// Already bounded: NewEnvelope caps the two identity fields, which are
		// the only ones that come from a text column rather than being
		// server-derived and fixed-shape.
		Envelope:  envelope,
		Recipient: boundedRunes(recipient, recipientMaxRunes),
		Subject:   boundedRunes(topic, replyActivityMaxRunes),
		Body:      boundedRunes(body, replyActivityMaxRunes),
		Intent:    boundedRunes(strings.TrimSpace(intent), replyActivityMaxRunes),
	}

	voice := d.loadVoice(ctx)
	draft, voiceVersion, draftRef, err := d.completeVoiced(ctx, anchor, data, voice)
	if err != nil {
		// Drafting is an assistive read, not the authority to send. Preserve
		// the deterministic floor and leave the routed ai_call failure visible.
		d.logger().WarnContext(ctx, "model reply draft unavailable; using deterministic draft", "err", err)
		return activities.DraftResult{Subject: fallbackSubject, Body: fallbackBody}, nil
	}
	disclosure := signals.Art50Disclosure
	return activities.DraftResult{
		Subject:             draft.Subject,
		Body:                draft.Body,
		AIGenerated:         true,
		AIDisclosure:        &disclosure,
		VoiceProfileVersion: voiceVersion,
		DraftRef:            draftRef,
	}, nil
}

// logger is the drafter's log, defaulting rather than being required: the
// certification case constructs a drafter with a brain and nothing else,
// because the draft path itself does no I/O — and a degrade path that panicked
// on the nil logger would fail the run for a reason that has nothing to do with
// the draft being measured.
func (d replyDrafter) logger() *slog.Logger {
	if d.log == nil {
		return slog.Default()
	}
	return d.log
}

// recipientMaxRunes bounds the greeting name in the prompt. A first name is a
// first name; this is generous for one, and it keeps the payload's every field
// bounded the way the certification harness assumes.
const recipientMaxRunes = 200

// recipientName is who this reply is written to, or nothing.
//
// A failure to resolve the name degrades to no name rather than failing the
// draft: the person may be outside this caller's scope, the activity may be
// linked to nobody, and in both cases an unnamed greeting is the honest answer.
// The reason is logged, so a lookup that breaks for some other cause is visible
// rather than silently reading as "no recipient".
func (d replyDrafter) recipientName(ctx context.Context, anchor ids.ActivityID) string {
	if d.store == nil {
		return ""
	}
	recipient, err := d.store.ReplyRecipientFor(ctx, anchor)
	if err != nil {
		d.logger().WarnContext(ctx, "reply recipient unavailable; drafting without a greeting name", "err", err)
		return ""
	}
	if recipient.FirstName != "" {
		return recipient.FirstName
	}
	return recipient.FullName
}

// conversationState places the message being answered on the silence axis.
//
// A reply reads its own anchor rather than the person's whole history, which is
// the honest scope for this surface: the drafter was pointed at one activity
// and asked to answer it. Which direction that message went decides what the
// reply owes — an inbound message is a question waiting, an outbound one is our
// own approach nobody answered.
// An activity with no direction at all — a note, a task — is neither, and
// counting it as inbound would claim the counterparty wrote something they did
// not. It carries a real timestamp, so the silence it produces is honest even
// though nobody spoke: the anchor is treated as our own side's, which is the
// reading that assumes least about them.
func (d replyDrafter) conversationState(activity crmcontracts.Activity) convstate.State {
	occurred := activity.OccurredAt
	inbound := activity.Direction != nil &&
		*activity.Direction == crmcontracts.ActivityDirectionInbound
	if inbound {
		return convstate.Classify(d.envelope.Now(), occurred, time.Time{})
	}
	return convstate.Classify(d.envelope.Now(), time.Time{}, occurred)
}

// voiceContext is the loaded active profile a voiced draft injects.
type voiceContext struct {
	profile ai.VoiceProfile
	version ai.VoiceProfileVersion
	ok      bool
}

// loadVoice resolves the actor's active voice; any lookup failure degrades
// to the plain draft with the failure visible in the log — a broken voice
// read must never take reply drafting down with it.
func (d replyDrafter) loadVoice(ctx context.Context) voiceContext {
	if d.voice == nil {
		return voiceContext{}
	}
	profile, version, ok, err := d.voice.ActiveVoiceForActor(ctx)
	if err != nil {
		d.logger().WarnContext(ctx, "voice profile lookup failed; drafting without voice", "err", err)
		return voiceContext{}
	}
	return voiceContext{profile: profile, version: version, ok: ok}
}

// completeVoiced drafts with the voice block when one is loaded, enforcing
// the deterministic anti-AI floor: detect → one critic retry → sanitize →
// on surviving violations fall back to the plain draft and record the
// failure as a rejected learning signal.
func (d replyDrafter) completeVoiced(ctx context.Context, anchor ids.UUID, data replyActivityData, voice voiceContext) (replyDraft, *int, *string, error) {
	if !voice.ok {
		draft, err := d.completeChecked(ctx, data, nil)
		return draft, nil, nil, err
	}
	block := func(fence promptfence.Fence) string {
		return voiceDraftPromptBlock(voice.profile.PersonalityMD, voice.version.VoiceProfileMD,
			ai.VersionExemplars(voice.version), ai.DecodeVersionStats(voice.version), fence)
	}
	draft, err := d.completeChecked(ctx, data, block)
	if err != nil {
		return replyDraft{}, nil, nil, err
	}
	// Detect on the RAW draft (subject and body separately — the
	// canned-opener rule anchors at text start): a violation the sanitizer
	// could mechanically remove still earns the critic retry, because the
	// retry fixes the sentence, not just the punctuation.
	if violations := voiceDraftViolations(draft); len(violations) > 0 {
		withFeedback := func(fence promptfence.Fence) string {
			return block(fence) + voiceViolationFeedback(violations)
		}
		retried, retryErr := d.complete(ctx, data, withFeedback)
		if retryErr == nil {
			draft = retried
		}
	}
	draft.Subject = ai.SanitizeAIPatterns(draft.Subject)
	draft.Body = ai.SanitizeAIPatterns(draft.Body)
	version := voice.version.ProfileVersion
	// The sanitizer edits text, so the floor AND the shape are re-checked on
	// what would actually be served.
	if len(voiceDraftViolations(draft)) > 0 || validateReplyDraft(draft) != nil {
		// The voice-styled draft kept tripping the floor: serve the plain
		// draft instead and let the failure feed the learning panel.
		d.recordVoiceRejection(ctx, voice, anchor, draft)
		plain, plainErr := d.completeChecked(ctx, data, nil)
		return plain, nil, nil, plainErr
	}
	d.recordVoiceDraft(ctx, voice, anchor, draft)
	ref := voiceDraftRef(voice, anchor, draft)
	return draft, &version, &ref, nil
}

// voiceDraftViolations runs the deterministic floor over subject and body
// independently; concatenation would hide a canned opener inside the body.
func voiceDraftViolations(draft replyDraft) []ai.VoiceViolation {
	return append(ai.DetectAIPatterns(draft.Subject), ai.DetectAIPatterns(draft.Body)...)
}

func voiceViolationFeedback(violations []ai.VoiceViolation) string {
	var b strings.Builder
	b.WriteString("\n\nThe previous attempt violated these hard rules; rewrite without them:\n")
	for _, violation := range violations {
		b.WriteString("- " + violation.Detail + "\n")
	}
	return b.String()
}

// voiceDraftRef keys one served draft for learning-signal feedback. It
// covers profile, version, anchor, and the full draft: two drafts for the
// same activity with the same body but different subjects — or from
// different profile versions — never collide.
func voiceDraftRef(voice voiceContext, anchor ids.UUID, draft replyDraft) string {
	sum := sha256.Sum256([]byte(draft.Subject + "\n" + draft.Body))
	return fmt.Sprintf("replydraft:%s:%s:v%d:%s",
		voice.profile.ID, anchor, voice.version.ProfileVersion, hex.EncodeToString(sum[:8]))
}

func (d replyDrafter) recordVoiceDraft(ctx context.Context, voice voiceContext, anchor ids.UUID, draft replyDraft) {
	if d.voice == nil {
		return
	}
	if err := d.voice.RecordDraftedSignal(ctx, voice.profile.ID, voice.version.ProfileVersion,
		voiceDraftRef(voice, anchor, draft), draft.Body); err != nil {
		d.logger().WarnContext(ctx, "voice draft signal not recorded", "err", err)
	}
}

func (d replyDrafter) recordVoiceRejection(ctx context.Context, voice voiceContext, anchor ids.UUID, draft replyDraft) {
	if d.voice == nil {
		return
	}
	ref := voiceDraftRef(voice, anchor, draft)
	if err := d.voice.RecordDraftedSignal(ctx, voice.profile.ID, voice.version.ProfileVersion, ref, draft.Body); err != nil {
		d.logger().WarnContext(ctx, "voice rejection signal not recorded", "err", err)
		return
	}
	if _, err := d.voice.RejectDraft(ctx, voice.profile.ID, ref); err != nil {
		d.logger().WarnContext(ctx, "voice rejection signal not recorded", "err", err)
	}
}

// replyDraftVoiceSystem replaces the no-voice guard when a profile block is
// supplied: the profile controls expression, never facts.
const replyDraftVoiceSystem = `Draft a professional email reply on behalf of the CRM user's company, written in the user's own voice.
Return ONLY a JSON object: {"subject":"...","body":"..."}.
- The activity and stated intent are the authoritative reason for this reply.
- The supplied voice profile controls expression — rhythm, vocabulary, directness, structure — never facts.
- Use only facts present in the supplied data. Never invent customers, outcomes, prices, commitments, or capabilities.
- Obey the profile's avoid rules and the universal anti-AI rules; treat its style metrics as limits, not targets.`

// voiceBlockFor renders the voice profile block under the CALLING call's fence.
// The block is prepended to that call's user turn, so it must be bounded by the
// marker that call's system prompt declares — not one of its own.
type voiceBlockFor func(promptfence.Fence) string

// replyDraftSystemFor assembles this call's system turn: what this surface is
// for, the rules every drafting surface shares, and THIS call's data boundary
// (see promptfence.Fence.Rule).
func replyDraftSystemFor(system string, fence promptfence.Fence) string {
	return system + "\n\n" + draftrules.Shared + "\n" + fence.Rule("activity")
}

// replyDraftRequest builds the one request a draft call sends, in whichever of
// this site's two system variants the call is made under. The workspace's Voice
// DNA state selects the variant per call — a loaded profile supplies a block and
// takes the voice prompt, no profile takes the plain one — and both remain the
// same invocation site: same schema, same bounds, same data boundary.
func replyDraftRequest(activity replyActivityData, voiceBlock voiceBlockFor, correction string) (model.Request, error) {
	payload, err := json.Marshal(activity)
	if err != nil {
		return model.Request{}, fmt.Errorf("compose: encode reply activity context: %w", err)
	}
	// The activity is the counterparty's own text. It was safe here only by
	// accident — json.Marshal escapes "<" to \u003c, so a forged block marker
	// could not be spelled — and an accident of the encoder is not a boundary:
	// it goes the moment this block is rendered as text rather than JSON.
	fence := promptfence.New()
	system := replyDraftSystem
	content := fence.Wrap(string(payload))
	if voiceBlock != nil {
		system = replyDraftVoiceSystem
		content = voiceBlock(fence) + "\n\n" + content
	}
	// The correction rides the USER turn and never the variant choice: it is
	// feedback about one attempt, and a plain draft told to fix a phrase must
	// stay a plain draft rather than silently becoming a voiced one.
	content += correction
	return model.Request{
		System: replyDraftSystemFor(system, fence),
		Messages: []model.Message{{
			Role:    chatRoleUser,
			Content: content,
		}},
		MaxTokens:      ai.ReasoningOutputMaxTokens,
		ResponseSchema: replyDraftSchema,
		SecretStripper: ai.NewSecretStripper(),
	}, nil
}

func (d replyDrafter) complete(ctx context.Context, activity replyActivityData, voiceBlock voiceBlockFor) (replyDraft, error) {
	return d.completeWith(ctx, activity, voiceBlock, "")
}

// completeWith is complete plus the correction a retry carries.
func (d replyDrafter) completeWith(ctx context.Context, activity replyActivityData, voiceBlock voiceBlockFor, correction string) (replyDraft, error) {
	req, err := replyDraftRequest(activity, voiceBlock, correction)
	if err != nil {
		return replyDraft{}, err
	}

	var resp model.Response
	if structured, ok := d.brain.(validatedBrain); ok {
		resp, err = structured.CompleteValidated(ctx, req, replyDraftShapeValid)
	} else {
		resp, err = d.brain.Complete(ctx, req)
	}
	if err != nil {
		return replyDraft{}, err
	}
	draft, err := parseReplyDraft(resp.Text)
	if err != nil {
		return replyDraft{}, err
	}
	if err := validateReplyDraft(draft); err != nil {
		return replyDraft{}, err
	}
	return draft, nil
}

// completeChecked drafts, then reads what came back against the envelope it was
// written into, and gives the model ONE chance to fix what it got wrong.
//
// The retry exists because the prompt rules keep losing to reflexes: "checking
// in" and "we discussed" survive an explicit ban when the model has a gap to
// paper over. Naming the exact phrase back to it is what a prompt sentence
// cannot do, and one retry is the limit — a second is a model that will not
// comply, and the caller has a deterministic floor for that.
//
// A retry that fails leaves the first draft standing. It has the defect, and it
// is still a real draft a human can edit; refusing to answer would be worse.
func (d replyDrafter) completeChecked(ctx context.Context, data replyActivityData, voiceBlock voiceBlockFor) (replyDraft, error) {
	draft, err := d.complete(ctx, data, voiceBlock)
	if err != nil {
		return replyDraft{}, err
	}
	findings := draftcheck.Body(draft.Body, data.Lang(), data.Band())
	if len(findings) == 0 {
		return draft, nil
	}

	retried, retryErr := d.completeWith(ctx, data, voiceBlock, draftcheck.Feedback(findings))
	if retryErr != nil {
		d.logger().WarnContext(ctx, "draft correction retry failed; serving the first draft",
			"findings", len(findings), "err", retryErr)
		return draft, nil
	}
	remaining := draftcheck.Body(retried.Body, data.Lang(), data.Band())
	if len(remaining) == 0 {
		return retried, nil
	}
	// The retry did not clear it. Serve whichever attempt carries LESS of the
	// rejected phrasing rather than the later one by default: a second attempt
	// is not automatically better, and the count is the only evidence available
	// without asking a model to judge its own output.
	d.logger().WarnContext(ctx, "draft still carries rejected phrasing after one retry",
		"phrase", remaining[0].Phrase, "rule", remaining[0].Rule, "remaining", len(remaining))
	if len(remaining) < len(findings) {
		return retried, nil
	}
	return draft, nil
}

// parseReplyDraft reads one model reply as the draft it claims to be. The
// provider's own envelope comes off first: a reply is not malformed for having
// been returned inside one.
func parseReplyDraft(text string) (replyDraft, error) {
	var draft replyDraft
	if err := json.Unmarshal([]byte(ai.Unfence(text)), &draft); err != nil {
		return replyDraft{}, fmt.Errorf("compose: reply draft response is not valid JSON: %w", err)
	}
	return draft, nil
}

func replyDraftShapeValid(text string) error {
	var draft replyDraft
	if err := json.Unmarshal([]byte(ai.Unfence(text)), &draft); err != nil {
		return fmt.Errorf(`output must be {"subject":"...","body":"..."}: %w`, err)
	}
	return validateReplyDraft(draft)
}

func validateReplyDraft(draft replyDraft) error {
	if strings.TrimSpace(draft.Subject) == "" {
		return fmt.Errorf("compose: reply draft subject is empty")
	}
	if strings.ContainsAny(draft.Subject, "\r\n") {
		return fmt.Errorf("compose: reply draft subject contains a line break")
	}
	if strings.TrimSpace(draft.Body) == "" {
		return fmt.Errorf("compose: reply draft body is empty")
	}
	if len([]rune(draft.Subject)) > 998 || len([]rune(draft.Body)) > 50_000 {
		return fmt.Errorf("compose: reply draft exceeds the supported length")
	}
	return nil
}

func boundedRunes(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
