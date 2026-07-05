// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose_test

// The Voice DNA surface end to end (B-E07.4/.5a): profile lifecycle with
// the one-live-profile-per-user rule, multi-source corpus ingest with
// register tagging + the §B1.2 speaker filter over the wire, idempotency
// on source_ref, the manifest meter with its quality bands, the
// human/derived split under If-Match — and the boundaries: agents are
// shut out of mutations, a second tenant sees nothing.

import (
	"net/http"
	"strings"
	"testing"
)

type voiceProfileWire struct {
	ID             string  `json:"id"`
	OwnerID        *string `json:"owner_id"`
	Scope          string  `json:"scope"`
	Status         string  `json:"status"`
	VoiceProfileMD string  `json:"voice_profile_md"`
	ProfileVersion int     `json:"profile_version"`
	PersonalityMD  string  `json:"personality_md"`
	Version        int     `json:"version"`
}

type voiceSourceWire struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Register  string  `json:"register"`
	Weight    float64 `json:"weight"`
	SourceRef string  `json:"source_ref"`
	WordCount int     `json:"word_count"`
	Excluded  bool    `json:"excluded"`
}

type voiceSummaryWire struct {
	TotalWords    int            `json:"total_words"`
	TargetWords   int            `json:"target_words"`
	QualityBand   string         `json:"quality_band"`
	RegisterWords map[string]int `json:"register_words"`
	SourceCount   int            `json:"source_count"`
}

type voiceIngestResponse struct {
	Source  voiceSourceWire  `json:"source"`
	Summary voiceSummaryWire `json:"summary"`
}

// The both-sided fixture: Ada owns 8 + 7 + 5 = 20 words, Klaus's turn
// must contribute zero (features/09 §B1.2).
const voiceTestVTT = `WEBVTT

1
00:00:01.000 --> 00:00:04.000
<v Ada Admin>So our pipeline stalls at the offer stage.</v>

2
00:00:04.500 --> 00:00:09.000
<v Klaus Kunde>We usually wait for finance before we reply.</v>

3
00:00:09.500 --> 00:00:12.000
<v Ada Admin>Then I will send the summary today
and follow up on Friday.</v>
`

// createVoiceProfile is the shared first step: one caller-owned user
// profile, asserted to land building and unbuilt.
func createVoiceProfile(t *testing.T, e *env) voiceProfileWire {
	t.Helper()
	var created voiceProfileWire
	if status := e.call(t, "POST", "/v1/voice-profiles", anyMap{}, nil, &created); status != http.StatusCreated {
		t.Fatalf("create → %d", status)
	}
	if created.Scope != "user" || created.Status != "building" || created.ProfileVersion != 0 || created.OwnerID == nil {
		t.Fatalf("created profile = %+v, want a building, unbuilt, caller-owned user profile", created)
	}
	return created
}

func TestVoiceProfileLifecycle(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	created := createVoiceProfile(t, e)
	base := "/v1/voice-profiles/" + created.ID

	// One live personal profile per user: a second create conflicts.
	if status := e.call(t, "POST", "/v1/voice-profiles", anyMap{}, nil, nil); status != http.StatusConflict {
		t.Fatalf("second create → %d, want 409", status)
	}

	// The human-authored half edits under If-Match; skew is refused.
	if status := e.call(t, "PATCH", base, anyMap{"personality_md": "Direct, warm, no filler."},
		map[string]string{"If-Match": "99"}, nil); status != http.StatusConflict {
		t.Fatalf("stale If-Match → %d, want 409", status)
	}
	var edited voiceProfileWire
	if status := e.call(t, "PATCH", base, anyMap{"personality_md": "Direct, warm, no filler."},
		map[string]string{"If-Match": "1"}, &edited); status != http.StatusOK {
		t.Fatalf("personality edit → %d", status)
	}
	if edited.PersonalityMD != "Direct, warm, no filler." || edited.VoiceProfileMD != "" || edited.ProfileVersion != 0 {
		t.Fatalf("personality edit touched the derived half: %+v", edited)
	}

	// Archive: 204, then the profile and its corpus read as absent.
	if status := e.call(t, "DELETE", base, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("archive → %d", status)
	}
	if status := e.call(t, "GET", base, nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("get after archive → %d, want 404", status)
	}
	if status := e.call(t, "GET", base+"/sources", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("sources after archive → %d, want 404 (the corpus rides its parent)", status)
	}
}

// ingest posts one corpus source and asserts the 201.
func ingest(t *testing.T, e *env, base string, body anyMap) voiceIngestResponse {
	t.Helper()
	var resp voiceIngestResponse
	if status := e.call(t, "POST", base+"/sources", body, nil, &resp); status != http.StatusCreated {
		t.Fatalf("ingest %v → %d", body["source_ref"], status)
	}
	return resp
}

func TestVoiceCorpusIngestAndMeter(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	created := createVoiceProfile(t, e)
	base := "/v1/voice-profiles/" + created.ID

	// A pasted post: register defaults to written, words counted.
	post := ingest(t, e, base, anyMap{
		"kind": "post", "source_label": "LinkedIn posts", "source_ref": "post-2026-06",
		"content": "Shipping beats planning. We shipped the audit story this week and it landed.",
	})
	if post.Source.Register != "written" || post.Source.WordCount != 13 {
		t.Fatalf("post ingested as %+v, want register=written word_count=13", post.Source)
	}
	if post.Summary.QualityBand != "thin" || post.Summary.SourceCount != 1 {
		t.Fatalf("summary after one post = %+v", post.Summary)
	}

	// A both-sided transcript: spoken register by default, and ONLY the
	// owner's 20 words enter — zero other-party text (§B1.2).
	transcript := ingest(t, e, base, anyMap{
		"kind": "transcript", "source_label": "Discovery call", "source_ref": "call-77",
		"format": "vtt", "speaker_label": "Ada Admin", "content": voiceTestVTT,
	})
	if transcript.Source.Register != "spoken" || transcript.Source.WordCount != 20 {
		t.Fatalf("transcript ingested as %+v, want register=spoken word_count=20 (owner turns only)", transcript.Source)
	}
	if transcript.Summary.RegisterWords["spoken"] != 20 || transcript.Summary.RegisterWords["written"] != 13 {
		t.Fatalf("register mix = %v, want spoken:20 written:13", transcript.Summary.RegisterWords)
	}

	// Idempotency: re-ingesting source_ref post-2026-06 replaces the row
	// — same source count, new word count, no double-counted meter.
	reingested := ingest(t, e, base, anyMap{
		"kind": "post", "source_label": "LinkedIn posts", "source_ref": "post-2026-06",
		"content": "Five clean words replace thirteen.",
	})
	if reingested.Source.ID != post.Source.ID || reingested.Source.WordCount != 5 {
		t.Fatalf("re-ingest made %+v, want the SAME row (id %s) at word_count=5", reingested.Source, post.Source.ID)
	}
	if reingested.Summary.SourceCount != 2 || reingested.Summary.TotalWords != 25 {
		t.Fatalf("summary after re-ingest = %+v, want 2 sources / 25 words", reingested.Summary)
	}

	// A rich enough source moves the band; the boundary logic itself is
	// unit-tested — here the wire meter reflects the stored corpus.
	bulk := ingest(t, e, base, anyMap{
		"kind": "longform", "source_label": "Blog archive", "source_ref": "blog-dump",
		"content": strings.Repeat("wort ", 8500),
	})
	if bulk.Summary.QualityBand != "good" || bulk.Summary.TotalWords != 8525 {
		t.Fatalf("summary after longform = %+v, want good / 8525", bulk.Summary)
	}

	// Excluding a source pulls it from the meter (manifest opt-out).
	var excluded voiceIngestResponse
	if status := e.call(t, "PATCH", base+"/sources/"+bulk.Source.ID, anyMap{
		"excluded": true,
	}, nil, &excluded); status != http.StatusOK {
		t.Fatalf("exclude → %d", status)
	}
	if !excluded.Source.Excluded || excluded.Summary.TotalWords != 25 || excluded.Summary.QualityBand != "thin" {
		t.Fatalf("after exclude: source=%+v summary=%+v, want the meter back at 25/thin", excluded.Source, excluded.Summary)
	}

	// The manifest lists every row (excluded ones included — the user can
	// opt back in) and never echoes corpus content.
	var manifest struct {
		Data    []voiceSourceWire `json:"data"`
		Summary voiceSummaryWire  `json:"summary"`
	}
	if status := e.call(t, "GET", base+"/sources", nil, nil, &manifest); status != http.StatusOK {
		t.Fatalf("manifest → %d", status)
	}
	if len(manifest.Data) != 3 || manifest.Summary.SourceCount != 2 {
		t.Fatalf("manifest rows=%d meterSources=%d, want 3 rows / 2 counted", len(manifest.Data), manifest.Summary.SourceCount)
	}
}

// A labelled transcript without the owner's label is refused whole —
// never half-ingested with the other side included (§B1.2).
func TestVoiceTranscriptIngestRequiresTheOwnersSpeakerLabel(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	created := createVoiceProfile(t, e)

	if status := e.call(t, "POST", "/v1/voice-profiles/"+created.ID+"/sources", anyMap{
		"kind": "transcript", "source_label": "Unattributed", "source_ref": "call-78",
		"format": "vtt", "content": voiceTestVTT,
	}, nil, nil); status != 422 {
		t.Fatalf("labelled transcript without speaker_label → %d, want 422", status)
	}
}

// An agent passport cannot touch a voice profile: the corpus is the
// human's consented personal material and the derived voice is their
// identity asset (the ADR-0055 human-only class).
func TestVoiceProfileMutationsRejectAgents(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var minted struct {
		Token string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": "voice prober", "scopes": []string{"read", "write"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("mint → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	if status := e.call(t, "POST", "/v1/voice-profiles", anyMap{}, bearer, nil); status != http.StatusForbidden {
		t.Fatalf("agent create voice profile → %d, want 403", status)
	}
}

// ∅-query conformance (B-E07.4 acceptance): a second tenant neither
// reads nor lists the first tenant's voice profile.
func TestVoiceProfileIsInvisibleAcrossTenants(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var created voiceProfileWire
	if status := e.call(t, "POST", "/v1/voice-profiles", anyMap{}, nil, &created); status != http.StatusCreated {
		t.Fatalf("create → %d", status)
	}

	// A second workspace: bootstrapping switches the session cookie and
	// the slug header to tenant B.
	if status := e.call(t, "POST", "/v1/workspaces", anyMap{
		"workspace_name":     "Voice Two",
		"admin_email":        "bea@example.com",
		"admin_display_name": "Bea Boss",
		"admin_password":     "correct-horse-battery",
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("second workspace → %d", status)
	}
	e.slug = "voice-two"

	if status := e.call(t, "GET", "/v1/voice-profiles/"+created.ID, nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("cross-tenant get → %d, want 404 (existence hidden)", status)
	}
	var listed struct {
		Data []voiceProfileWire `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/voice-profiles", nil, nil, &listed); status != http.StatusOK {
		t.Fatalf("cross-tenant list → %d", status)
	}
	if len(listed.Data) != 0 {
		t.Fatalf("tenant B lists %d foreign profiles, want 0", len(listed.Data))
	}
}
