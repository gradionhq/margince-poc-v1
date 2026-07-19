// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type voiceVersionWire struct {
	ProfileVersion int    `json:"profile_version"`
	Status         string `json:"status"`
	VoiceProfileMD string `json:"voice_profile_md"`
	Version        int    `json:"version"`
}

type voiceVersionPageWire struct {
	Data []voiceVersionWire `json:"data"`
	Page struct {
		NextCursor *string `json:"next_cursor"`
		HasMore    bool    `json:"has_more"`
	} `json:"page"`
}

type voiceDeltaPageWire struct {
	Data []struct {
		FromVersion       *int   `json:"from_version"`
		ToVersion         int    `json:"to_version"`
		Classification    string `json:"classification"`
		ActivationOutcome string `json:"activation_outcome"`
	} `json:"data"`
	Page struct {
		NextCursor *string `json:"next_cursor"`
		HasMore    bool    `json:"has_more"`
	} `json:"page"`
}

type voiceLearningSummaryWire struct {
	Drafted  int `json:"drafted"`
	Rejected int `json:"rejected"`
}

const voiceLifecycleEvaluation = `{
  "held_out_prompts": 5,
  "repeats_per_prompt": 3,
  "active_median_voice_score": 1,
  "candidate_median_voice_score": 1,
  "anti_ai_hard_failures": 0,
  "structured_output_valid": true,
  "corpus_citations_valid": true,
  "identity_word_jaccard": 1,
  "signature_set_jaccard": 1,
  "removed_avoid_rules": 0,
  "removed_register_rules": 0,
  "classification": "routine",
  "passed": true
}`

func seedVoiceLifecycleHistory(t *testing.T, e *env, profileID string) (ids.UUID, ids.UUID) {
	t.Helper()
	ctx := context.Background()
	var workspaceID, ownerID ids.UUID
	if err := e.owner.QueryRow(ctx,
		`SELECT workspace_id, owner_id FROM voice_profile WHERE id = $1`, profileID,
	).Scan(&workspaceID, &ownerID); err != nil {
		t.Fatal(err)
	}
	capturedBy := "human:" + ownerID.String()
	if _, err := e.owner.Exec(ctx, `
		UPDATE voice_profile
		SET status = 'ready', voice_profile_md = 'active version 1', profile_version = 1,
		    active_source_hash = 'active-hash', last_built_at = now(), updated_at = now()
		WHERE id = $1`, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO voice_profile_version
		  (workspace_id, voice_profile_id, profile_version, status, voice_profile_md,
		   profile_json, stats_json, source_hash, source_count, reason,
		   model_provider, model_name, builder_version, activation_policy_version,
		   evaluation_json, review_reasons, activated_at, source, captured_by, updated_at)
		VALUES ($1, $2, 1, 'active', 'active version 1',
		        '{"document":"active version 1"}', '{"avg_sentence_words":8}',
		        'active-hash', 1, 'manual', 'test', 'test-model', 'test-builder', '1',
		        $3::jsonb, '{}', now(), 'ui', $4, now())`,
		workspaceID, profileID, voiceLifecycleEvaluation, capturedBy); err != nil {
		t.Fatal(err)
	}
	seedVoiceLifecycleCandidate(t, e, workspaceID, profileID, 2, 1, capturedBy)
	return workspaceID, ownerID
}

func seedVoiceLifecycleCandidate(
	t *testing.T,
	e *env,
	workspaceID ids.UUID,
	profileID string,
	profileVersion int,
	predecessor int,
	capturedBy string,
) {
	t.Helper()
	ctx := context.Background()
	artifact := "candidate version " + strconv.Itoa(profileVersion)
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO voice_profile_version
		  (workspace_id, voice_profile_id, profile_version, status, voice_profile_md,
		   profile_json, stats_json, source_hash, source_count, reason, predecessor_version,
		   model_provider, model_name, builder_version, activation_policy_version,
		   evaluation_json, review_reasons, source, captured_by, updated_at)
		VALUES ($1, $2, $3, 'candidate', $4,
		        jsonb_build_object('document', $4::text), '{"avg_sentence_words":9}',
		        $5, 2, 'automatic', $6, 'test', 'test-model', 'test-builder', '1',
		        $7::jsonb, ARRAY['owner review'], 'system', $8, now())`,
		workspaceID, profileID, profileVersion, artifact,
		"candidate-hash-"+strconv.Itoa(profileVersion), predecessor,
		voiceLifecycleEvaluation, capturedBy); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO voice_profile_delta
		  (workspace_id, voice_profile_id, from_version, to_version, classification,
		   activation_outcome, delta_json)
		VALUES ($1, $2, $3, $4, 'routine', 'review_required',
		        '{"words_added":10,"sources_added":1,"sources_excluded":0,"identity_word_jaccard":1,"signature_set_jaccard":1,"avoid_rules_added":0,"avoid_rules_removed":0,"register_rules_removed":0}')`,
		workspaceID, profileID, predecessor, profileVersion); err != nil {
		t.Fatal(err)
	}
}

func seedVoiceDraftSignal(t *testing.T, e *env, workspaceID ids.UUID, profileID string, ownerID ids.UUID, draftRef string) {
	t.Helper()
	hash := sha256.Sum256([]byte(draftRef))
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO voice_learning_signal
		  (workspace_id, voice_profile_id, profile_version, draft_ref_hash, outcome,
		   generated_original, transformations, retention_until, source, captured_by)
		VALUES ($1, $2, 2, $3, 'drafted', 'generated draft', '[]', $4, 'system', $5)`,
		workspaceID, profileID, hash[:], time.Now().UTC().Add(30*24*time.Hour),
		"human:"+ownerID.String()); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceLifecycleHTTPRoundTrip(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	created := createVoiceProfile(t, e)
	base := "/v1/voice-profiles/" + created.ID
	workspaceID, ownerID := seedVoiceLifecycleHistory(t, e, created.ID)

	var firstPage voiceVersionPageWire
	if status := e.call(t, "GET", base+"/versions?limit=1", nil, nil, &firstPage); status != http.StatusOK {
		t.Fatalf("list versions → %d", status)
	}
	if len(firstPage.Data) != 1 || !firstPage.Page.HasMore || firstPage.Page.NextCursor == nil {
		t.Fatalf("first version page = %+v, want one row and a cursor", firstPage)
	}
	var secondPage voiceVersionPageWire
	if status := e.call(t, "GET", base+"/versions?limit=1&cursor="+*firstPage.Page.NextCursor, nil, nil, &secondPage); status != http.StatusOK {
		t.Fatalf("second version page → %d", status)
	}
	if len(secondPage.Data) != 1 || secondPage.Page.HasMore {
		t.Fatalf("second version page = %+v, want the final row", secondPage)
	}

	var applied voiceVersionWire
	if status := e.call(t, "POST", base+"/versions/2/apply", nil,
		map[string]string{"If-Match": "1"}, &applied); status != http.StatusOK {
		t.Fatalf("apply candidate → %d", status)
	}
	if applied.ProfileVersion != 2 || applied.Status != "active" || applied.VoiceProfileMD != "candidate version 2" {
		t.Fatalf("applied version = %+v", applied)
	}

	seedVoiceLifecycleCandidate(t, e, workspaceID, created.ID, 3, 2, "human:"+ownerID.String())
	var rejected voiceVersionWire
	if status := e.call(t, "POST", base+"/versions/3/reject", nil,
		map[string]string{"If-Match": "1"}, &rejected); status != http.StatusOK {
		t.Fatalf("reject candidate → %d", status)
	}
	if rejected.ProfileVersion != 3 || rejected.Status != "rejected" {
		t.Fatalf("rejected version = %+v", rejected)
	}

	var deltas voiceDeltaPageWire
	if status := e.call(t, "GET", base+"/deltas?limit=1", nil, nil, &deltas); status != http.StatusOK {
		t.Fatalf("list deltas → %d", status)
	}
	if len(deltas.Data) != 1 || !deltas.Page.HasMore || deltas.Page.NextCursor == nil {
		t.Fatalf("delta page = %+v, want one row and a cursor", deltas)
	}
	var remainingDeltas voiceDeltaPageWire
	if status := e.call(t, "GET", base+"/deltas?limit=10&cursor="+*deltas.Page.NextCursor, nil, nil, &remainingDeltas); status != http.StatusOK {
		t.Fatalf("remaining deltas → %d", status)
	}
	if len(remainingDeltas.Data) != 1 {
		t.Fatalf("remaining deltas = %+v, want one row", remainingDeltas)
	}

	draftRef := "draft:lifecycle-http"
	seedVoiceDraftSignal(t, e, workspaceID, created.ID, ownerID, draftRef)
	var beforeReject voiceLearningSummaryWire
	if status := e.call(t, "GET", base+"/learning", nil, nil, &beforeReject); status != http.StatusOK {
		t.Fatalf("learning summary → %d", status)
	}
	if beforeReject.Drafted != 1 || beforeReject.Rejected != 0 {
		t.Fatalf("learning summary before rejection = %+v", beforeReject)
	}
	var afterReject voiceLearningSummaryWire
	if status := e.call(t, "POST", base+"/draft-rejections", anyMap{"draft_ref": draftRef}, nil, &afterReject); status != http.StatusOK {
		t.Fatalf("reject draft → %d", status)
	}
	if afterReject.Drafted != 0 || afterReject.Rejected != 1 {
		t.Fatalf("learning summary after rejection = %+v", afterReject)
	}

	var rolledBack voiceVersionWire
	if status := e.call(t, "POST", base+"/versions/1/rollback", nil, nil, &rolledBack); status != http.StatusCreated {
		t.Fatalf("rollback → %d", status)
	}
	if rolledBack.ProfileVersion != 4 || rolledBack.Status != "active" || rolledBack.VoiceProfileMD != "active version 1" {
		t.Fatalf("rollback version = %+v", rolledBack)
	}

	var profile voiceProfileWire
	if status := e.call(t, "GET", base, nil, nil, &profile); status != http.StatusOK {
		t.Fatalf("profile after rollback → %d", status)
	}
	var cleared voiceProfileWire
	if status := e.call(t, "POST", base+"/corpus/clear", nil,
		map[string]string{"If-Match": strconv.Itoa(profile.Version)}, &cleared); status != http.StatusOK {
		t.Fatalf("clear corpus → %d", status)
	}
	if cleared.Status != "collecting" || cleared.ProfileVersion != 0 || cleared.VoiceProfileMD != "" {
		t.Fatalf("cleared profile = %+v", cleared)
	}
}
