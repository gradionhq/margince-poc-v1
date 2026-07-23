// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The voice build over a real Postgres: a queued build is claimed, derived,
// evaluated and lands as a real active version with a real evaluation; a
// corpus edit between queue and claim fails the build honestly; a budget
// stop parks the row deferred and the sweep re-offers it when due.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

var voiceBuildPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"voice_profile": {Create: true, Read: true, Update: true},
	},
	RowScope: principal.RowScopeTeam,
}

// voiceDraftPerms adds the activity grant the drafter needs on top of the
// voice posture.
var voiceDraftPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"voice_profile": {Create: true, Read: true, Update: true},
		"activity":      {Create: true, Read: true, Update: true},
	},
	RowScope: principal.RowScopeAll,
}

var sampleIDPattern = regexp.MustCompile(`<sample id="([^"]+)"`)

// scriptedBuildBrain serves all three call shapes of one build: the builder
// pass (echoes real sample ids and a verbatim quote), the evaluation drafts,
// and the judge.
type scriptedBuildBrain struct {
	judgeScore float64
	budgetOut  bool
	quote      string
}

func (s *scriptedBuildBrain) Complete(_ context.Context, req model.Request) (model.Response, error) {
	if s.budgetOut {
		return model.Response{}, ai.ErrBudgetDeferred
	}
	switch {
	case strings.Contains(req.System, "forensic writing-style analyst"):
		matches := sampleIDPattern.FindAllStringSubmatch(req.Messages[0].Content, -1)
		if len(matches) == 0 {
			return model.Response{}, errors.New("scripted brain saw no samples in the builder prompt")
		}
		evidence := make([]string, 0, len(matches))
		for _, match := range matches {
			evidence = append(evidence, match[1])
		}
		// The quote must cite the sample that actually carries it; the
		// held-out split decides which samples reach the builder, so find it.
		quoteSample := ""
		for _, block := range strings.Split(req.Messages[0].Content, `<sample id="`)[1:] {
			closing := strings.Index(block, `"`)
			if closing < 0 {
				continue
			}
			if strings.Contains(block, s.quote) {
				quoteSample = block[:closing]
				break
			}
		}
		quote := s.quote
		if quoteSample == "" {
			// The quote-bearing sample was held out: quote the first
			// sample's opening words instead, verbatim.
			first := strings.Split(req.Messages[0].Content, `<sample id="`)[1]
			body := first[strings.Index(first, ">")+1:]
			words := strings.Fields(body)
			quote = strings.Join(words[:5], " ")
			quoteSample = evidence[0]
		}
		payload, err := json.Marshal(map[string]any{
			"identity_summary": "Direct and operational.", "thinking_pattern": "Verdict first, then the operational why.",
			"observed_obsessions": []string{"shipping"}, "directness": "high", "structure": "short paragraphs",
			"openings": []string{"straight in"}, "closings": []string{"a next step"},
			"vocabulary": []string{"ship", "honest"}, "avoid": []string{"filler"},
			"signature_moves": []map[string]string{{"move": "verdict first", "quote": quote, "sample_id": quoteSample}},
			"register_notes":  []string{"spoken is blunter"}, "evidence": evidence,
		})
		if err != nil {
			return model.Response{}, err
		}
		return model.Response{Text: string(payload), ServedModel: "scripted-1"}, nil
	case strings.Contains(req.System, "compare drafts"):
		count := strings.Count(req.Messages[0].Content, "<draft ")
		scores := make([]float64, count)
		for i := range scores {
			scores[i] = s.judgeScore
		}
		payload, err := json.Marshal(map[string]any{"scores": scores})
		if err != nil {
			return model.Response{}, err
		}
		return model.Response{Text: string(payload)}, nil
	default:
		payload, err := json.Marshal(map[string]string{
			"subject": "Re: the plan",
			"body":    "The verdict is simple. We ship Monday and the plan holds because the work is done.",
		})
		if err != nil {
			return model.Response{}, err
		}
		return model.Response{Text: string(payload)}, nil
	}
}

type voiceBuildEnv struct {
	e       *integration.Env
	store   *ai.VoiceStore
	owner   context.Context
	profile ai.VoiceProfile
}

// seedVoiceBuild creates an owner profile with a buildable corpus of
// sourceCount register-alternating pieces and one queued build. Two sources
// stay just over the build floor (no held-out reserve possible); six give
// the evaluator room to reserve real held-out prompts.
func seedVoiceBuild(t *testing.T, quote string, sourceCount int) (*voiceBuildEnv, ai.VoiceBuild) {
	t.Helper()
	e := integration.Setup(t)
	store := ai.NewVoiceStore(e.Pool)
	owner := e.As(e.Rep1, []ids.UUID{e.Team1}, voiceBuildPerms)
	profile, err := store.CreateProfile(owner, ai.CreateVoiceProfileInput{})
	if err != nil {
		t.Fatal(err)
	}
	filler := strings.Repeat("plain honest sentence about the actual work we do. ", 60)
	for i := 0; i < sourceCount; i++ {
		register := "email"
		if i%2 == 1 {
			register = "spoken"
		}
		content := fmt.Sprintf("Piece %d of the corpus. %s", i+1, filler)
		if i == 0 {
			content = quote + " " + content
		}
		if _, _, _, err := store.IngestSource(owner, profile.ID, ai.IngestSourceInput{
			Kind: "email", Register: register, SourceLabel: fmt.Sprintf("source-%d", i+1), Content: content,
		}); err != nil {
			t.Fatal(err)
		}
	}
	build, err := store.CreateBuild(owner, profile.ID, ai.CreateVoiceBuildInput{Reason: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if build.Status != "queued" {
		t.Fatalf("fresh build status = %s, want queued", build.Status)
	}
	return &voiceBuildEnv{e: e, store: store, owner: owner, profile: profile}, build
}

func (v *voiceBuildEnv) workerCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, err := voiceBuildWorkerCtx(context.Background(), VoiceBuildArgs{
		Workspace: v.e.WS.String(), ProfileID: v.profile.ID.String(),
		BuildID: ids.NewV7().String(), RequestedBy: v.e.Rep1.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestVoiceBuildRunsToAnActiveVersion(t *testing.T) {
	quote := "We ship on Monday, no excuses."
	env, build := seedVoiceBuild(t, quote, 6)
	ctx := env.workerCtx(t)
	worker := newVoiceBuildWorker(env.e.Pool, &scriptedBuildBrain{judgeScore: 0.9, quote: quote}, slog.New(slog.DiscardHandler))

	input, claimed, err := env.store.ClaimBuild(ctx, env.profile.ID, build.ID, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim: %v claimed=%v", err, claimed)
	}
	if err := worker.run(ctx, build.ID, input); err != nil {
		t.Fatalf("run: %v", err)
	}

	finished, err := env.store.GetBuild(env.owner, env.profile.ID, build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "succeeded" || finished.CandidateAction != "auto_activated" || finished.ResultVersion == nil {
		t.Fatalf("finished build = %+v", finished)
	}
	profile, err := env.store.GetProfile(env.owner, env.profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Status != "ready" || profile.ProfileVersion != *finished.ResultVersion {
		t.Fatalf("profile after build = status %s version %d", profile.Status, profile.ProfileVersion)
	}
	if !strings.Contains(profile.VoiceProfileMD, "## How you think") || !strings.Contains(profile.VoiceProfileMD, "verdict first") {
		t.Fatalf("derived artifact incomplete:\n%s", profile.VoiceProfileMD)
	}
	active, ok, err := env.store.ActiveVersion(env.owner, env.profile.ID)
	if err != nil || !ok {
		t.Fatalf("active version: %v ok=%v", err, ok)
	}
	if passed, isBool := active.Evaluation["passed"].(bool); !isBool || !passed {
		t.Fatalf("evaluation = %v, want a real passed=true", active.Evaluation)
	}
	if score, isNum := active.Evaluation["candidate_median_voice_score"].(float64); !isNum || score <= 0 || score >= 1 {
		t.Fatalf("median score = %v, want a real measured value in (0,1)", active.Evaluation["candidate_median_voice_score"])
	}
	drafts, isList := active.ProfileJSON["sample_drafts"].([]any)
	if !isList || len(drafts) != 2 {
		t.Fatalf("sample_drafts = %v, want 2 cached drafts", active.ProfileJSON["sample_drafts"])
	}
	if _, hasGuidance := active.ProfileJSON["guidance"].(map[string]any); !hasGuidance {
		t.Fatal("profile_json must carry the what-to-add-next guidance")
	}
}

func TestVoiceBuildFailsWhenTheCorpusChangedSinceQueueing(t *testing.T) {
	env, build := seedVoiceBuild(t, "A quote that will survive.", 6)
	if _, _, _, err := env.store.IngestSource(env.owner, env.profile.ID, ai.IngestSourceInput{
		Kind: "other", SourceLabel: "late addition", Content: "text arriving after the snapshot was taken",
	}); err != nil {
		t.Fatal(err)
	}
	ctx := env.workerCtx(t)
	if _, claimed, err := env.store.ClaimBuild(ctx, env.profile.ID, build.ID, time.Minute); err != nil || claimed {
		t.Fatalf("claim after corpus edit: err=%v claimed=%v, want unclaimed fail", err, claimed)
	}
	finished, err := env.store.GetBuild(env.owner, env.profile.ID, build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "failed" || finished.StatusCode == nil || *finished.StatusCode != "internal" {
		t.Fatalf("build after corpus edit = %+v, want failed/internal", finished)
	}
	if finished.StatusDetail == nil || !strings.Contains(*finished.StatusDetail, "corpus changed") {
		t.Fatalf("status detail = %v, want an actionable corpus-changed message", finished.StatusDetail)
	}
}

func TestVoiceBuildDefersOnBudgetAndTheSweepFindsItWhenDue(t *testing.T) {
	env, build := seedVoiceBuild(t, "Deferred quote.", 6)
	ctx := env.workerCtx(t)
	worker := newVoiceBuildWorker(env.e.Pool, &scriptedBuildBrain{budgetOut: true}, slog.New(slog.DiscardHandler))

	input, claimed, err := env.store.ClaimBuild(ctx, env.profile.ID, build.ID, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim: %v claimed=%v", err, claimed)
	}
	runErr := worker.run(ctx, build.ID, input)
	if !errors.Is(runErr, ai.ErrBudgetDeferred) {
		t.Fatalf("run under budget exhaustion = %v, want the sentinel", runErr)
	}
	past := time.Now().Add(-time.Minute)
	if err := env.store.DeferBuild(ctx, build.ID, *input.Build.StartedAt, "budget window exhausted", past); err != nil {
		t.Fatal(err)
	}
	deferred, err := env.store.GetBuild(env.owner, env.profile.ID, build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deferred.Status != "deferred" || deferred.NextAttemptAt == nil {
		t.Fatalf("deferred build = %+v", deferred)
	}
	due, err := env.store.DueDeferredBuilds(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ref := range due {
		if ref.BuildID == build.ID && ref.RequestedBy != nil && *ref.RequestedBy == env.e.Rep1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("due sweep = %+v, must include the past-due build with its requester", due)
	}

	// The sweep's re-offer claims the deferred row directly and finishes it.
	healthy := newVoiceBuildWorker(env.e.Pool, &scriptedBuildBrain{judgeScore: 0.9, quote: "Deferred quote."}, slog.New(slog.DiscardHandler))
	input, claimed, err = env.store.ClaimBuild(ctx, env.profile.ID, build.ID, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("re-claim: %v claimed=%v", err, claimed)
	}
	if err := healthy.run(ctx, build.ID, input); err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	finished, err := env.store.GetBuild(env.owner, env.profile.ID, build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "succeeded" {
		t.Fatalf("resumed build = %+v, want succeeded", finished)
	}
}

// The terminal failure write mints its own detached deadline AT write time:
// a work context the model calls already spent must still record the
// outcome, or the build sits "running" until reclaim and retries forever
// while the UI polls a row that never concludes.
func TestVoiceBuildRecordsFailureEvenWhenTheWorkContextIsSpent(t *testing.T) {
	env, build := seedVoiceBuild(t, "Spent-context quote.", 6)
	ctx := env.workerCtx(t)
	worker := newVoiceBuildWorker(env.e.Pool, nil, slog.New(slog.DiscardHandler))

	input, claimed, err := env.store.ClaimBuild(ctx, env.profile.ID, build.ID, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim: %v claimed=%v", err, claimed)
	}
	spent, cancelSpent := context.WithDeadline(ctx, time.Unix(0, 0))
	defer cancelSpent()
	if err := worker.fail(spent, build.ID, *input.Build.StartedAt, "internal", "the run outlived its work context"); err != nil {
		t.Fatalf("fail on a spent work context: %v", err)
	}
	finished, err := env.store.GetBuild(env.owner, env.profile.ID, build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "failed" || finished.StatusCode == nil || *finished.StatusCode != "internal" {
		t.Fatalf("build after spent-context failure = %+v, want failed/internal", finished)
	}
}

func TestVoiceBuildStarterCorpusActivatesUnevaluated(t *testing.T) {
	quote := "Starter quote for a thin corpus."
	env, build := seedVoiceBuild(t, quote, 2)
	ctx := env.workerCtx(t)
	worker := newVoiceBuildWorker(env.e.Pool, &scriptedBuildBrain{judgeScore: 0.9, quote: quote}, slog.New(slog.DiscardHandler))

	input, claimed, err := env.store.ClaimBuild(ctx, env.profile.ID, build.ID, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim: %v claimed=%v", err, claimed)
	}
	if err := worker.run(ctx, build.ID, input); err != nil {
		t.Fatalf("run: %v", err)
	}
	finished, err := env.store.GetBuild(env.owner, env.profile.ID, build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "succeeded" || finished.CandidateAction != "auto_activated" {
		t.Fatalf("starter build = %+v, want an auto-activated first profile", finished)
	}
	active, ok, err := env.store.ActiveVersion(env.owner, env.profile.ID)
	if err != nil || !ok {
		t.Fatalf("active version: %v ok=%v", err, ok)
	}
	if active.Evaluation["candidate_median_voice_score"] != nil {
		t.Fatalf("median = %v, want null when evaluation could not run", active.Evaluation["candidate_median_voice_score"])
	}
	if len(active.ReviewReasons) == 0 || !strings.Contains(active.ReviewReasons[0], "too small") {
		t.Fatalf("review reasons = %v, must state that evaluation did not run", active.ReviewReasons)
	}
}

func voiceBuildJob(env *voiceBuildEnv, build ai.VoiceBuild) *river.Job[VoiceBuildArgs] {
	return &river.Job[VoiceBuildArgs]{
		JobRow: &rivertype.JobRow{},
		Args: VoiceBuildArgs{
			Workspace: env.e.WS.String(), ProfileID: env.profile.ID.String(),
			BuildID: build.ID.String(), RequestedBy: env.e.Rep1.String(),
		},
	}
}

func TestVoiceBuildWorkEndToEnd(t *testing.T) {
	quote := "Work-path quote."
	env, build := seedVoiceBuild(t, quote, 6)
	worker := newVoiceBuildWorker(env.e.Pool, &scriptedBuildBrain{judgeScore: 0.9, quote: quote}, slog.New(slog.DiscardHandler))

	if err := worker.Work(context.Background(), voiceBuildJob(env, build)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	finished, err := env.store.GetBuild(env.owner, env.profile.ID, build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "succeeded" {
		t.Fatalf("build after Work = %+v, want succeeded", finished)
	}

	// A redelivered job for the terminal row is a clean no-op: nothing on
	// the build row moves and no second version appears.
	versionsBefore, err := env.store.ListVersions(env.owner, env.profile.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(context.Background(), voiceBuildJob(env, build)); err != nil {
		t.Fatalf("redelivered Work on a terminal build: %v", err)
	}
	after, err := env.store.GetBuild(env.owner, env.profile.ID, build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != finished.Version || after.Status != finished.Status {
		t.Fatalf("redelivery moved the build row: %+v -> %+v", finished, after)
	}
	versionsAfter, err := env.store.ListVersions(env.owner, env.profile.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(versionsAfter.Items) != len(versionsBefore.Items) {
		t.Fatalf("redelivery created a version: %d -> %d", len(versionsBefore.Items), len(versionsAfter.Items))
	}
}

func TestVoiceBuildWorkSnoozesWhileAnotherClaimIsLive(t *testing.T) {
	env, build := seedVoiceBuild(t, "Contended quote.", 6)
	ctx := env.workerCtx(t)
	if _, claimed, err := env.store.ClaimBuild(ctx, env.profile.ID, build.ID, time.Minute); err != nil || !claimed {
		t.Fatalf("rival claim: %v claimed=%v", err, claimed)
	}
	worker := newVoiceBuildWorker(env.e.Pool, &scriptedBuildBrain{judgeScore: 0.9, quote: "Contended quote."}, slog.New(slog.DiscardHandler))
	err := worker.Work(context.Background(), voiceBuildJob(env, build))
	if err == nil {
		t.Fatal("a live rival claim must snooze the job, never succeed it — success would strand the build")
	}
	current, getErr := env.store.GetBuild(env.owner, env.profile.ID, build.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if current.Status != "running" {
		t.Fatalf("build = %s, the rival's claim must be untouched", current.Status)
	}
}

func TestVoiceBuildWorkWithoutABrainFailsClosed(t *testing.T) {
	env, build := seedVoiceBuild(t, "Brainless quote.", 6)
	worker := newVoiceBuildWorker(env.e.Pool, nil, slog.New(slog.DiscardHandler))
	if err := worker.Work(context.Background(), voiceBuildJob(env, build)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	finished, err := env.store.GetBuild(env.owner, env.profile.ID, build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "failed" || finished.StatusCode == nil || *finished.StatusCode != "model_unavailable" {
		t.Fatalf("brainless build = %+v, want failed/model_unavailable", finished)
	}
	if finished.StatusDetail == nil || !strings.Contains(*finished.StatusDetail, "AI provider") {
		t.Fatalf("detail = %v, must tell the operator what to configure", finished.StatusDetail)
	}
}

func TestVoiceDraftReadsAndLearningSignals(t *testing.T) {
	quote := "The drafting read quote."
	env, build := seedVoiceBuild(t, quote, 6)
	ctx := env.workerCtx(t)
	worker := newVoiceBuildWorker(env.e.Pool, &scriptedBuildBrain{judgeScore: 0.9, quote: quote}, slog.New(slog.DiscardHandler))
	input, claimed, err := env.store.ClaimBuild(ctx, env.profile.ID, build.ID, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim: %v claimed=%v", err, claimed)
	}
	if err := worker.run(ctx, build.ID, input); err != nil {
		t.Fatalf("run: %v", err)
	}

	profile, version, found, err := env.store.ActiveVoiceForActor(env.owner)
	if err != nil || !found {
		t.Fatalf("ActiveVoiceForActor: %v found=%v", err, found)
	}
	if profile.ID != env.profile.ID || version.Status != "active" {
		t.Fatalf("active voice = profile %s version status %s", profile.ID, version.Status)
	}
	if len(ai.VersionExemplars(version)) == 0 {
		t.Fatal("the active version must carry decodable exemplars for drafting")
	}
	if stats := ai.DecodeVersionStats(version); stats.WordCount == 0 {
		t.Fatalf("the active version must carry decodable stats, got %+v", stats)
	}

	// Another rep never reads someone else's voice: absence, not denial.
	outsider := env.e.As(env.e.Rep3, []ids.UUID{env.e.Team2}, voiceBuildPerms)
	if _, _, found, err := env.store.ActiveVoiceForActor(outsider); err != nil || found {
		t.Fatalf("outsider ActiveVoiceForActor: %v found=%v, want clean absence", err, found)
	}

	// A served draft records once; the same reference replayed is idempotent,
	// and a rejection lands on the recorded row.
	ref := "replydraft:test:abc"
	if err := env.store.RecordDraftedSignal(env.owner, profile.ID, version.ProfileVersion, ref, "draft body"); err != nil {
		t.Fatal(err)
	}
	if err := env.store.RecordDraftedSignal(env.owner, profile.ID, version.ProfileVersion, ref, "draft body"); err != nil {
		t.Fatalf("a replayed draft reference must be idempotent: %v", err)
	}
	if _, err := env.store.RejectDraft(env.owner, profile.ID, ref); err != nil {
		t.Fatalf("rejecting a recorded draft: %v", err)
	}
}

// voicedBrain scripts the drafting side: the build shapes are served by
// scriptedBuildBrain, and reply drafts come back violating or clean.
type voicedDraftBrain struct {
	scriptedBuildBrain
	alwaysViolate bool
	draftCalls    int
}

func (b *voicedDraftBrain) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	if strings.Contains(req.System, "email reply") {
		b.draftCalls++
		body := "The plan holds. We ship Monday and the numbers back it."
		if b.alwaysViolate {
			body = "Here's the thing: it's not about tools, but transformation. What do you think?"
		}
		payload, err := json.Marshal(map[string]string{"subject": "Re: plan", "body": body})
		if err != nil {
			return model.Response{}, err
		}
		return model.Response{Text: string(payload)}, nil
	}
	return b.scriptedBuildBrain.Complete(ctx, req)
}

func seedReplyActivity(t *testing.T, env *voiceBuildEnv) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	err := database.WithWorkspaceTx(env.owner, env.e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO activity (id, workspace_id, kind, subject, body, source_system, source_id, source, captured_by)
			VALUES ($1, $2, 'email', 'the plan', 'Can you confirm the plan?', 'gmail', $3, 'gmail:'||$3, 'connector:gmail')`,
			id, env.e.WS, "voice-"+id.String())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func builtVoiceEnv(t *testing.T, quote string) *voiceBuildEnv {
	t.Helper()
	env, build := seedVoiceBuild(t, quote, 6)
	worker := newVoiceBuildWorker(env.e.Pool, &scriptedBuildBrain{judgeScore: 0.9, quote: quote}, slog.New(slog.DiscardHandler))
	if err := worker.Work(context.Background(), voiceBuildJob(env, build)); err != nil {
		t.Fatalf("build Work: %v", err)
	}
	return env
}

func TestReplyDraftCarriesVoiceProvenanceEndToEnd(t *testing.T) {
	env := builtVoiceEnv(t, "Provenance quote.")
	activity := seedReplyActivity(t, env)
	drafter := newReplyDrafter(env.e.Pool, &voicedDraftBrain{scriptedBuildBrain: scriptedBuildBrain{judgeScore: 0.9}}, slog.New(slog.DiscardHandler))

	draftCtx := env.e.As(env.e.Rep1, []ids.UUID{env.e.Team1}, voiceDraftPerms)
	result, err := drafter.DraftEmailWithProvenance(draftCtx, activity, "confirm the plan")
	if err != nil {
		t.Fatal(err)
	}
	if !result.AIGenerated || result.AIDisclosure == nil {
		t.Fatalf("voiced draft must carry the Art. 50 stamp: %+v", result)
	}
	if result.VoiceProfileVersion == nil {
		t.Fatalf("voiced draft must name the profile version that styled it: %+v", result)
	}
	summary, err := env.store.LearningSummary(env.owner, env.profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Drafted != 1 {
		t.Fatalf("drafted signals = %d, want the served draft recorded once", summary.Drafted)
	}

	// The plain seam rides the same path minus the provenance.
	subject, body, err := drafter.DraftEmail(draftCtx, activity, "confirm the plan")
	if err != nil || subject == "" || body == "" {
		t.Fatalf("plain seam: %q %q %v", subject, body, err)
	}
}

func TestReplyDraftFallsBackAndRecordsRejectionWhenTheFloorHolds(t *testing.T) {
	env := builtVoiceEnv(t, "Fallback quote.")
	activity := seedReplyActivity(t, env)
	drafter := newReplyDrafter(env.e.Pool, &voicedDraftBrain{
		scriptedBuildBrain: scriptedBuildBrain{judgeScore: 0.9}, alwaysViolate: true,
	}, slog.New(slog.DiscardHandler))

	draftCtx := env.e.As(env.e.Rep1, []ids.UUID{env.e.Team1}, voiceDraftPerms)
	result, err := drafter.DraftEmailWithProvenance(draftCtx, activity, "confirm the plan")
	if err != nil {
		t.Fatal(err)
	}
	if result.VoiceProfileVersion != nil {
		t.Fatalf("a floor-failing voice draft must fall back without a voice stamp: %+v", result)
	}
	summary, err := env.store.LearningSummary(env.owner, env.profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Rejected != 1 {
		t.Fatalf("rejected signals = %d, want the floor failure recorded", summary.Rejected)
	}
}

func TestVoiceBuildWorkRecordsAnInvalidModelAnswerAsFailed(t *testing.T) {
	env, build := seedVoiceBuild(t, "A quote the brain will not honor.", 6)
	worker := newVoiceBuildWorker(env.e.Pool, brainFunc(func(context.Context, model.Request) (model.Response, error) {
		return model.Response{Text: "not the requested JSON object"}, nil
	}), slog.New(slog.DiscardHandler))

	if err := worker.Work(context.Background(), voiceBuildJob(env, build)); err != nil {
		t.Fatalf("Work must own the failure on the row, not error the job: %v", err)
	}
	finished, err := env.store.GetBuild(env.owner, env.profile.ID, build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "failed" || finished.StatusCode == nil || *finished.StatusCode != "invalid_output" {
		t.Fatalf("build = %+v, want failed/invalid_output", finished)
	}
}

// brainFunc adapts a function into the completer seam.
type brainFunc func(context.Context, model.Request) (model.Response, error)

func (f brainFunc) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	return f(ctx, req)
}
