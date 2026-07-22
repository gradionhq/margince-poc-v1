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

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
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
		if _, _, err := store.IngestSource(owner, profile.ID, ai.IngestSourceInput{
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
	if err := worker.run(ctx, ctx, build.ID, input); err != nil {
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
	if _, _, err := env.store.IngestSource(env.owner, env.profile.ID, ai.IngestSourceInput{
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
	runErr := worker.run(ctx, ctx, build.ID, input)
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
	if err := healthy.run(ctx, ctx, build.ID, input); err != nil {
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

func TestVoiceBuildStarterCorpusActivatesUnevaluated(t *testing.T) {
	quote := "Starter quote for a thin corpus."
	env, build := seedVoiceBuild(t, quote, 2)
	ctx := env.workerCtx(t)
	worker := newVoiceBuildWorker(env.e.Pool, &scriptedBuildBrain{judgeScore: 0.9, quote: quote}, slog.New(slog.DiscardHandler))

	input, claimed, err := env.store.ClaimBuild(ctx, env.profile.ID, build.ID, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim: %v claimed=%v", err, claimed)
	}
	if err := worker.run(ctx, ctx, build.ID, input); err != nil {
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
