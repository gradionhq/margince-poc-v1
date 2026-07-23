// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Store-level Voice DNA gates (ADR-0066): the admitted-owner predicate
// over profiles and corpus — another human in the same team or an admin
// reads existence-hiding absence — and the derived-rebuild write path: SetDerivedProfile
// persists the §B0.2 artifact from an ingested fixture corpus WITH a
// bumped profile_version while the human-authored personality_md
// survives untouched (the split that makes rebuilds safe).

import (
	"context"
	"crypto/sha256"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// voiceRepPerms mirrors the seeded rep grant for the voice_profile
// object (posture: create/read/update, no delete,
// team scope).
var voiceRepPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"voice_profile": {Create: true, Read: true, Update: true},
	},
	RowScope: principal.RowScopeTeam,
}

func seedVoiceCandidate(t *testing.T, pool *pgxpool.Pool, workspaceID, profileID ids.UUID, profileVersion, predecessor int, classification string) {
	t.Helper()
	ctx := context.Background()
	evaluation := `{"held_out_prompts":5,"repeats_per_prompt":3,"active_median_voice_score":1,"candidate_median_voice_score":1,"anti_ai_hard_failures":0,"structured_output_valid":true,"corpus_citations_valid":true,"identity_word_jaccard":1,"signature_set_jaccard":1,"removed_avoid_rules":0,"removed_register_rules":0,"classification":"` + classification + `","passed":true}`
	if _, err := pool.Exec(ctx, `
		INSERT INTO voice_profile_version
		  (workspace_id, voice_profile_id, profile_version, status, voice_profile_md,
		   profile_json, stats_json, source_hash, source_count, reason, predecessor_version,
		   model_provider, model_name, builder_version, activation_policy_version,
		   evaluation_json, review_reasons, source, captured_by, updated_at)
		VALUES ($1, $2, $3, 'candidate', $4::text, jsonb_build_object('document', $4::text), '{}'::jsonb,
		        'candidate-hash', 1, 'automatic', $5, 'test', 'test', 'test', 'test',
		        $6::jsonb, ARRAY['owner review'], 'system', 'system', now())`,
		workspaceID, profileID, profileVersion, "candidate version "+strconv.Itoa(profileVersion), predecessor, evaluation); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO voice_profile_delta
		  (workspace_id, voice_profile_id, from_version, to_version, classification,
		   activation_outcome, delta_json)
		VALUES ($1, $2, $3, $4, $5, 'review_required',
		        '{"words_added":1,"sources_added":1,"sources_excluded":0,"identity_word_jaccard":1,"signature_set_jaccard":1,"avoid_rules_added":0,"avoid_rules_removed":0,"register_rules_removed":0}')`,
		workspaceID, profileID, predecessor, profileVersion, classification); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceProfileAndCorpusAreOwnerPrivate(t *testing.T) {
	e := Setup(t)
	voice := ai.NewVoiceStore(e.Pool)

	owner := e.As(e.Rep1, []ids.UUID{e.Team1}, voiceRepPerms)
	created, err := voice.CreateProfile(owner, ai.CreateVoiceProfileInput{})
	if err != nil {
		t.Fatal(err)
	}

	// A teammate and a rep in another team both read absence. Voice ownership
	// is stricter than tenant/team record visibility.
	teammate := e.As(e.Rep2, []ids.UUID{e.Team1}, voiceRepPerms)
	if _, err := voice.GetProfile(teammate, created.ID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("teammate read: %v, want ErrNotFound", err)
	}
	outsider := e.As(e.Rep3, []ids.UUID{e.Team2}, voiceRepPerms)
	if _, err := voice.GetProfile(outsider, created.ID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("outsider read → %v, want ErrNotFound", err)
	}
	page, err := voice.ListProfiles(outsider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("outsider lists %d profiles, want 0", len(page.Items))
	}

	// The corpus rides the same gate: an outsider cannot ingest into or
	// enumerate a foreign profile.
	if _, _, err := voice.ListSources(outsider, created.ID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("outsider manifest → %v, want ErrNotFound", err)
	}
	if _, _, _, err := voice.IngestSource(outsider, created.ID, ai.IngestSourceInput{
		Kind: "linkedin", SourceLabel: "poison", Content: "not my voice",
	}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("outsider ingest → %v, want ErrNotFound", err)
	}

	// A teammate — and even an unbounded admin — sees owner-private absence
	// on every profile/corpus path.
	admin := e.As(ids.NewV7(), nil, principal.Permissions{
		RoleKeys: []string{"admin"},
		Objects: map[string]principal.ObjectGrant{
			"voice_profile": {Create: true, Read: true, Update: true, Delete: true},
		},
		RowScope: principal.RowScopeAll,
	})
	for who, ctx := range map[string]context.Context{"teammate": teammate, "admin": admin} {
		if _, _, _, err := voice.IngestSource(ctx, created.ID, ai.IngestSourceInput{
			Kind: "linkedin", SourceLabel: "poison", Content: "words in another's mouth",
		}); !errors.Is(err, apperrors.ErrNotFound) {
			t.Fatalf("%s ingest into a personal profile → %v, want ErrNotFound", who, err)
		}
		personality := "not their identity"
		if _, err := voice.UpdateProfile(ctx, created.ID, ai.UpdateVoiceProfileInput{PersonalityMD: &personality}); !errors.Is(err, apperrors.ErrNotFound) {
			t.Fatalf("%s personality edit → %v, want ErrNotFound", who, err)
		}
		if _, _, err := voice.ListSources(ctx, created.ID); !errors.Is(err, apperrors.ErrNotFound) {
			t.Fatalf("%s manifest browse → %v, want ErrNotFound", who, err)
		}
	}
	// The owner's own path is untouched.
	if _, _, _, err := voice.IngestSource(owner, created.ID, ai.IngestSourceInput{
		Kind: "linkedin", SourceLabel: "mine", Content: "my own words, my own voice",
	}); err != nil {
		t.Fatalf("owner ingest: %v", err)
	}
}

// The fixture corpus the rebuild regression loads (the B-E07.4
// reusable-artifact DoD): one written post + one both-sided spoken
// transcript whose other party must contribute nothing.
const voiceFixturePost = `Most CRMs make reps type. Ours reads the meeting and drafts the follow-up; the rep just decides.`

const voiceFixtureVTT = `WEBVTT

1
00:00:01.000 --> 00:00:05.000
<v Ada Admin>I always open with the customer's number, not ours.</v>

2
00:00:05.500 --> 00:00:08.000
<v Klaus Kunde>That is unusual but it works on our board.</v>
`

// voiceFixtureArtifact stands in for the ported builder's output: the
// §B0.2 fixed-schema markdown the rebuild persists. The builder itself
// is B-E07.6; the schema contract is pinned here.
const voiceFixtureArtifact = `# Voice Profile — Ada Admin

## Identity
Direct operator; outcome-first.

## Stats snapshot
avg sentence 11 words · contractions high

## Signature moves
Opens with the customer's number.

## Structural patterns
Short paragraphs; one ask per message.

## Punctuation / emoji / POV
No emoji; first person singular.

## Vocabulary
ship, land, decide

## Anti-patterns (forbidden)
No "circling back", no exclamation marks.

## Register notes
Spoken register is looser than posts.

## Few-shot examples
### email
Kurz: das Angebot steht, Freitag entscheiden wir.
`

func TestVoiceDerivedRebuildVersionsArtifactAndPreservesIdentity(t *testing.T) {
	e := Setup(t)
	voice := ai.NewVoiceStore(e.Pool)
	owner := e.As(e.Rep1, []ids.UUID{e.Team1}, voiceRepPerms)

	created, err := voice.CreateProfile(owner, ai.CreateVoiceProfileInput{
		PersonalityMD: "Hand-written: dry humor stays.",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The fixture corpus ingests deterministically: registers tagged per
	// kind, the transcript speaker-filtered to the owner.
	post, _, _, err := voice.IngestSource(owner, created.ID, ai.IngestSourceInput{
		Kind: "linkedin", SourceLabel: "post", SourceRef: "fixture-post", Content: voiceFixturePost,
	})
	if err != nil {
		t.Fatal(err)
	}
	call, summary, _, err := voice.IngestSource(owner, created.ID, ai.IngestSourceInput{
		Kind: "transcript", SourceLabel: "call", SourceRef: "fixture-call",
		Format: "vtt", SpeakerLabel: "Ada Admin", Content: voiceFixtureVTT,
	})
	if err != nil {
		t.Fatal(err)
	}
	if post.Register != "social" || call.Register != "spoken" {
		t.Fatalf("registers = %s/%s, want social/spoken", post.Register, call.Register)
	}
	if call.WordCount != 9 {
		t.Fatalf("transcript word_count = %d, want the owner's 9 words only", call.WordCount)
	}
	if summary.TotalWords != post.WordCount+call.WordCount {
		t.Fatalf("meter %d ≠ %d + %d", summary.TotalWords, post.WordCount, call.WordCount)
	}

	// An empty rebuild is refused — a versioned nothing helps no one.
	if _, err := voice.SetDerivedProfile(owner, created.ID, "  ", nil); err == nil {
		t.Fatal("empty derived artifact accepted")
	}

	// First rebuild: artifact persisted, versioned, status ready — and
	// the human-authored half is byte-identical.
	modelRef := "style:v1"
	built, err := voice.SetDerivedProfile(owner, created.ID, voiceFixtureArtifact, &modelRef)
	if err != nil {
		t.Fatal(err)
	}
	if built.ProfileVersion != 1 || built.Status != "ready" {
		t.Fatalf("first rebuild → version %d status %s, want 1/ready", built.ProfileVersion, built.Status)
	}
	if !strings.Contains(built.VoiceProfileMD, "Anti-patterns (forbidden)") {
		t.Fatal("persisted artifact lost the §B0.2 anti-patterns section")
	}
	if built.PersonalityMD != "Hand-written: dry humor stays." {
		t.Fatalf("rebuild touched personality_md: %q", built.PersonalityMD)
	}
	versions, err := voice.ListVersions(owner, created.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions.Items) != 1 || versions.Items[0].Status != "active" || versions.Items[0].ProfileVersion != 1 {
		t.Fatalf("first version history = %+v", versions.Items)
	}

	// New eligible corpus never destroys the known-good artifact. It marks
	// the control row stale until a later build succeeds.
	if _, _, _, err := voice.IngestSource(owner, created.ID, ai.IngestSourceInput{
		Kind: "email", SourceLabel: "new note", SourceRef: "fixture-note",
		Content: "A new note changes the evidence without changing the active artifact.",
	}); err != nil {
		t.Fatal(err)
	}
	stale, err := voice.GetProfile(owner, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != "stale" || stale.ProfileVersion != 1 || stale.VoiceProfileMD != voiceFixtureArtifact {
		t.Fatalf("new corpus changed known-good state: %+v", stale)
	}

	// A second rebuild bumps the artifact version again.
	rebuilt, err := voice.SetDerivedProfile(owner, created.ID, voiceFixtureArtifact+"\nrev2\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.ProfileVersion != 2 || rebuilt.PersonalityMD != built.PersonalityMD {
		t.Fatalf("second rebuild → version %d personality %q", rebuilt.ProfileVersion, rebuilt.PersonalityMD)
	}
	versions, err = voice.ListVersions(owner, created.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions.Items) != 2 || versions.Items[0].Status != "active" || versions.Items[1].Status != "superseded" {
		t.Fatalf("second version history = %+v", versions.Items)
	}
	deltas, err := voice.ListDeltas(owner, created.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas.Items) != 2 || deltas.Items[0].ToVersion != 2 || deltas.Items[1].ToVersion != 1 {
		t.Fatalf("delta history = %+v", deltas.Items)
	}

	// Rollback copies an immutable earlier artifact forward; version numbers
	// never move backwards and the human-authored preferences remain intact.
	rolledBack, err := voice.RollbackVersion(owner, created.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.ProfileVersion != 3 || rolledBack.Status != "active" || rolledBack.VoiceProfileMD != voiceFixtureArtifact {
		t.Fatalf("rollback result = %+v", rolledBack)
	}
	profile, err := voice.GetProfile(owner, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ProfileVersion != 3 || profile.PersonalityMD != built.PersonalityMD {
		t.Fatalf("profile after rollback = %+v", profile)
	}

	autoLearning := true
	profile, err = voice.UpdateProfile(owner, created.ID, ai.UpdateVoiceProfileInput{
		AutoLearningEnabled: &autoLearning, IfVersion: &profile.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := voice.ClearCorpus(owner, created.ID, &profile.Version)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Status != "collecting" || cleared.ProfileVersion != 0 || cleared.VoiceProfileMD != "" || cleared.AutoLearningEnabled {
		t.Fatalf("cleared profile = %+v", cleared)
	}
	if cleared.PersonalityMD != "Hand-written: dry humor stays." {
		t.Fatalf("corpus clear touched personality: %q", cleared.PersonalityMD)
	}
	sources, summary, err := voice.ListSources(owner, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 || summary.TotalWords != 0 {
		t.Fatalf("corpus after clear: sources=%d summary=%+v", len(sources), summary)
	}
	versions, err = voice.ListVersions(owner, created.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	deltas, err = voice.ListDeltas(owner, created.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions.Items) != 0 || len(deltas.Items) != 0 {
		t.Fatalf("derived history survived clear: versions=%d deltas=%d", len(versions.Items), len(deltas.Items))
	}
}

func TestVoiceCandidateTransitionsUseCandidateConcurrencyAndForwardRollback(t *testing.T) {
	e := Setup(t)
	ownerDB := SchemaPool(t)
	voice := ai.NewVoiceStore(e.Pool)
	owner := e.As(e.Rep1, []ids.UUID{e.Team1}, voiceRepPerms)
	profile, err := voice.CreateProfile(owner, ai.CreateVoiceProfileInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := voice.IngestSource(owner, profile.ID, ai.IngestSourceInput{
		Kind: "document", SourceLabel: "seed", SourceRef: "seed", Content: strings.Repeat("word ", 800),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := voice.SetDerivedProfile(owner, profile.ID, voiceFixtureArtifact, nil); err != nil {
		t.Fatal(err)
	}

	seedVoiceCandidate(t, ownerDB, e.WS, profile.ID, 2, 1, "material")
	candidateVersion := int64(1)
	applied, err := voice.ApplyVersion(owner, profile.ID, 2, &candidateVersion)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != "active" || applied.ProfileVersion != 2 {
		t.Fatalf("applied candidate = %+v", applied)
	}
	active, err := voice.GetProfile(owner, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.ProfileVersion != 2 || active.VoiceProfileMD != applied.VoiceProfileMD {
		t.Fatalf("profile after apply = %+v", active)
	}

	seedVoiceCandidate(t, ownerDB, e.WS, profile.ID, 3, 2, "material")
	rejected, err := voice.RejectVersion(owner, profile.ID, 3, &candidateVersion)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != "rejected" {
		t.Fatalf("rejected candidate = %+v", rejected)
	}
	active, err = voice.GetProfile(owner, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.ProfileVersion != 2 {
		t.Fatalf("reject replaced known-good profile: %+v", active)
	}

	rolledBack, err := voice.RollbackVersion(owner, profile.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.ProfileVersion != 4 || rolledBack.Status != "active" || rolledBack.VoiceProfileMD != voiceFixtureArtifact {
		t.Fatalf("forward rollback = %+v, want new active version 4", rolledBack)
	}
}

func TestVoiceConcurrentBuildRequestsConvergeOnOneDurableRow(t *testing.T) {
	e := Setup(t)
	ownerDB := SchemaPool(t)
	voice := ai.NewVoiceStore(e.Pool)
	owner := e.As(e.Rep1, []ids.UUID{e.Team1}, voiceRepPerms)
	profile, err := voice.CreateProfile(owner, ai.CreateVoiceProfileInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := voice.IngestSource(owner, profile.ID, ai.IngestSourceInput{
		Kind: "document", SourceLabel: "seed", SourceRef: "seed", Content: strings.Repeat("word ", 800),
	}); err != nil {
		t.Fatal(err)
	}

	type result struct {
		build ai.VoiceBuild
		err   error
	}
	const callers = 8
	start := make(chan struct{})
	results := make(chan result, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			build, err := voice.CreateBuild(owner, profile.ID, ai.CreateVoiceBuildInput{Reason: "manual"})
			results <- result{build: build, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var buildID ids.UUID
	for got := range results {
		if got.err != nil {
			t.Fatalf("concurrent build: %v", got.err)
		}
		if buildID.IsZero() {
			buildID = got.build.ID
		}
		if got.build.ID != buildID {
			t.Fatalf("concurrent requests returned %s and %s", buildID, got.build.ID)
		}
	}
	var count int
	if err := ownerDB.QueryRow(context.Background(), `
		SELECT count(*)::int FROM voice_build WHERE voice_profile_id = $1`, profile.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("durable build rows = %d, want 1", count)
	}
}

func TestVoiceDraftRejectionIsIdempotentAndTerminalSafe(t *testing.T) {
	e := Setup(t)
	ownerDB := SchemaPool(t)
	voice := ai.NewVoiceStore(e.Pool)
	owner := e.As(e.Rep1, []ids.UUID{e.Team1}, voiceRepPerms)
	profile, err := voice.CreateProfile(owner, ai.CreateVoiceProfileInput{})
	if err != nil {
		t.Fatal(err)
	}

	draftRef := "reply:draft-1"
	draftHash := sha256.Sum256([]byte(draftRef))
	var signalID ids.UUID
	if err := ownerDB.QueryRow(context.Background(), `
		INSERT INTO voice_learning_signal
		  (workspace_id, voice_profile_id, draft_ref_hash, outcome, generated_original,
		   retention_until, source, captured_by)
		VALUES ($1, $2, $3, 'drafted', 'private generated text', $4, 'system', 'system')
		RETURNING id`, e.WS, profile.ID, draftHash[:], time.Now().Add(24*time.Hour)).Scan(&signalID); err != nil {
		t.Fatal(err)
	}
	summary, err := voice.RejectDraft(owner, profile.ID, draftRef)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Rejected != 1 || summary.Drafted != 0 {
		t.Fatalf("summary after rejection = %+v", summary)
	}
	summary, err = voice.RejectDraft(owner, profile.ID, draftRef)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Rejected != 1 {
		t.Fatalf("repeated rejection changed aggregate = %+v", summary)
	}
	var auditCount, eventCount int
	if err := ownerDB.QueryRow(context.Background(), `
		SELECT count(*)::int FROM audit_log
		WHERE entity_type = 'voice_learning_signal' AND entity_id = $1`, signalID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := ownerDB.QueryRow(context.Background(), `
		SELECT count(*)::int FROM event_outbox
		WHERE envelope->>'type' = 'voice.draft_outcome_recorded'
		  AND envelope->'entity'->>'id' = $1`, profile.ID.String()).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 || eventCount != 1 {
		t.Fatalf("idempotent rejection wrote audits/events %d/%d, want 1/1", auditCount, eventCount)
	}

	acceptedRef := "reply:draft-accepted"
	acceptedHash := sha256.Sum256([]byte(acceptedRef))
	if _, err := ownerDB.Exec(context.Background(), `
		INSERT INTO voice_learning_signal
		  (workspace_id, voice_profile_id, draft_ref_hash, outcome, retention_until, source, captured_by)
		VALUES ($1, $2, $3, 'accepted', $4, 'system', 'system')`,
		e.WS, profile.ID, acceptedHash[:], time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := voice.RejectDraft(owner, profile.ID, acceptedRef); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("accepted draft rejection = %v, want ErrConflict", err)
	}
}

// Clearing the corpus must erase a qualifying learning signal (edited_sent with
// human-authored final text) without tripping voice_learning_signal_qualifies_check.
// The scrub nulls final_text, and the CHECK requires final_text NOT NULL while
// qualifies_as_source stays true — so the clear has to drop the qualifying flag
// in the same UPDATE, or the whole erase fails the constraint.
func TestVoiceClearCorpusScrubsQualifyingLearningSignal(t *testing.T) {
	e := Setup(t)
	ownerDB := SchemaPool(t)
	voice := ai.NewVoiceStore(e.Pool)
	owner := e.As(e.Rep1, []ids.UUID{e.Team1}, voiceRepPerms)
	profile, err := voice.CreateProfile(owner, ai.CreateVoiceProfileInput{})
	if err != nil {
		t.Fatal(err)
	}

	qualHash := sha256.Sum256([]byte("reply:edited-sent-1"))
	if _, err := ownerDB.Exec(context.Background(), `
		INSERT INTO voice_learning_signal
		  (workspace_id, voice_profile_id, draft_ref_hash, outcome, final_text,
		   final_captured_by, qualifies_as_source, retention_until, source, captured_by)
		VALUES ($1, $2, $3, 'edited_sent', 'the human-authored final text',
		        'human:ada', true, $4, 'system', 'system')`,
		e.WS, profile.ID, qualHash[:], time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	if _, err := voice.ClearCorpus(owner, profile.ID, &profile.Version); err != nil {
		t.Fatalf("clearing a corpus that has a qualifying learning signal: %v", err)
	}

	var qualifies bool
	var finalText *string
	if err := ownerDB.QueryRow(context.Background(), `
		SELECT qualifies_as_source, final_text FROM voice_learning_signal
		WHERE workspace_id = $1 AND voice_profile_id = $2`,
		e.WS, profile.ID).Scan(&qualifies, &finalText); err != nil {
		t.Fatal(err)
	}
	if qualifies || finalText != nil {
		t.Fatalf("clear left the signal qualifying/text intact: qualifies=%v final_text=%v", qualifies, finalText)
	}
}
