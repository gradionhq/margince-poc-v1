// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The Voice DNA surface end to end (B-E07.4/.5a): profile lifecycle with
// the one-live-profile-per-user rule, multi-source corpus ingest with
// register tagging + the §B1.2 speaker filter over the wire, idempotency
// on source_ref, the manifest meter with its quality bands, the
// human/derived split under If-Match — and the boundaries: agents are
// shut out of mutations, a second tenant sees nothing.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type voiceProfileWire struct {
	ID             string  `json:"id"`
	OwnerID        *string `json:"owner_id"`
	Status         string  `json:"status"`
	Maturity       string  `json:"maturity"`
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
	Included  bool    `json:"included"`
}

type voiceSummaryWire struct {
	TotalWords    int            `json:"total_words"`
	TargetWords   int            `json:"target_words"`
	QualityBand   string         `json:"quality_band"`
	Maturity      string         `json:"maturity"`
	RegisterWords map[string]int `json:"register_words"`
	SourceCount   int            `json:"source_count"`
}

type voiceIngestResponse struct {
	Source  voiceSourceWire  `json:"source"`
	Summary voiceSummaryWire `json:"summary"`
}

type voiceBuildWire struct {
	ID            string `json:"id"`
	ProfileID     string `json:"profile_id"`
	Reason        string `json:"reason"`
	Status        string `json:"status"`
	SourceHash    string `json:"source_hash"`
	SourceCount   int    `json:"source_count"`
	ResultVersion *int   `json:"result_version"`
	Version       int    `json:"version"`
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
// profile, asserted to land collecting and unbuilt.
func createVoiceProfile(t *testing.T, e *env) voiceProfileWire {
	t.Helper()
	var created voiceProfileWire
	if status := e.call(t, "POST", "/v1/voice-profiles", anyMap{}, nil, &created); status != http.StatusCreated {
		t.Fatalf("create → %d", status)
	}
	if created.Status != "collecting" || created.Maturity != "collecting" || created.ProfileVersion != 0 || created.OwnerID == nil {
		t.Fatalf("created profile = %+v, want a collecting, unbuilt, caller-owned profile", created)
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

	// Archive returns the terminal representation, then reads are absent.
	if status := e.call(t, "DELETE", base, nil, map[string]string{"If-Match": "2"}, nil); status != http.StatusOK {
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
	kind, _ := body["kind"].(string)
	if _, ok := body["register"]; !ok {
		body["register"] = map[string]string{
			"email": "email", "linkedin": "social", "proposal": "long_form",
			"transcript": "spoken", "document": "long_form", "other": "general",
		}[kind]
	}
	if _, ok := body["format"]; !ok {
		body["format"] = "text"
	}
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

	// A pasted post: social register, words counted.
	post := ingest(t, e, base, anyMap{
		"kind": "linkedin", "source_label": "LinkedIn posts", "source_ref": "post-2026-06",
		"content": "Shipping beats planning. We shipped the audit story this week and it landed.",
	})
	if post.Source.Register != "social" || post.Source.WordCount != 13 {
		t.Fatalf("post ingested as %+v, want register=social word_count=13", post.Source)
	}
	if post.Summary.QualityBand != "thin" || post.Summary.SourceCount != 1 {
		t.Fatalf("summary after one post = %+v", post.Summary)
	}

	// A both-sided transcript: spoken register by default, and ONLY the
	// owner's 20 words enter — zero other-party text (§B1.2).
	transcript := ingest(t, e, base, anyMap{
		"kind": "transcript", "source_label": "Discovery call", "source_ref": "call-77",
		"format": "transcript", "speaker_label": "Ada Admin", "content": voiceTestVTT,
	})
	if transcript.Source.Register != "spoken" || transcript.Source.WordCount != 20 {
		t.Fatalf("transcript ingested as %+v, want register=spoken word_count=20 (owner turns only)", transcript.Source)
	}
	if transcript.Summary.RegisterWords["spoken"] != 20 || transcript.Summary.RegisterWords["social"] != 13 {
		t.Fatalf("register mix = %v, want spoken:20 social:13", transcript.Summary.RegisterWords)
	}

	// Idempotency: re-ingesting source_ref post-2026-06 replaces the row
	// — same source count, new word count, no double-counted meter.
	reingested := ingest(t, e, base, anyMap{
		"kind": "linkedin", "source_label": "LinkedIn posts", "source_ref": "post-2026-06",
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
		"kind": "document", "source_label": "Blog archive", "source_ref": "blog-dump",
		"content": strings.Repeat("wort ", 8500),
	})
	if bulk.Summary.QualityBand != "good" || bulk.Summary.TotalWords != 8525 {
		t.Fatalf("summary after longform = %+v, want good / 8525", bulk.Summary)
	}

	// Excluding a source pulls it from the meter (manifest opt-out).
	var excluded voiceIngestResponse
	if status := e.call(t, "PATCH", base+"/sources/"+bulk.Source.ID, anyMap{
		"included": false,
	}, nil, &excluded); status != http.StatusOK {
		t.Fatalf("exclude → %d", status)
	}
	if excluded.Source.Included || excluded.Summary.TotalWords != 25 || excluded.Summary.QualityBand != "thin" {
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

func TestVoiceBuildQueuesOnlyAtTheProvisionalThreshold(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	created := createVoiceProfile(t, e)
	base := "/v1/voice-profiles/" + created.ID

	ingest(t, e, base, anyMap{
		"kind": "document", "source_label": "Working sample", "source_ref": "threshold",
		"content": strings.Repeat("word ", 799),
	})
	if status := e.call(t, "POST", base+"/builds", anyMap{"reason": "onboarding"}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("build below 800 words → %d, want 422", status)
	}

	ingest(t, e, base, anyMap{
		"kind": "document", "source_label": "Working sample", "source_ref": "threshold",
		"content": strings.Repeat("word ", 800),
	})
	var queued voiceBuildWire
	if status := e.call(t, "POST", base+"/builds", anyMap{"reason": "onboarding"}, nil, &queued); status != http.StatusAccepted {
		t.Fatalf("build at 800 words → %d, want 202", status)
	}
	if queued.ProfileID != created.ID || queued.Status != "queued" || queued.SourceCount != 1 || queued.SourceHash == "" || queued.ResultVersion != nil {
		t.Fatalf("queued build = %+v", queued)
	}

	var replay voiceBuildWire
	if status := e.call(t, "POST", base+"/builds", anyMap{"reason": "manual"}, nil, &replay); status != http.StatusAccepted {
		t.Fatalf("active-build replay → %d, want 202", status)
	}
	if replay.ID != queued.ID {
		t.Fatalf("active-build replay id = %s, want %s", replay.ID, queued.ID)
	}
	var polled voiceBuildWire
	if status := e.call(t, "GET", base+"/builds/"+queued.ID, nil, nil, &polled); status != http.StatusOK {
		t.Fatalf("poll build → %d", status)
	}
	if polled.ID != queued.ID || polled.Version != 1 {
		t.Fatalf("polled build = %+v", polled)
	}
}

// The V1 corpus is text only (features/09 §B1.1): a binary document has
// no honest word count, so the wire answer is a 422 naming the accepted
// formats — never a size-derived estimate. Real extraction is deferred
// (B-E07.5c).
func TestVoiceBinaryDocumentIngestIsRefusedWith422(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	created := createVoiceProfile(t, e)

	for _, format := range []string{"docx", "pdf"} {
		var problem struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		if status := e.call(t, "POST", "/v1/voice-profiles/"+created.ID+"/sources", anyMap{
			"kind": "document", "register": "long_form", "source_label": "office upload", "source_ref": "bin-" + format,
			"format": format, "content": "opaque binary payload",
		}, nil, &problem); status != http.StatusUnprocessableEntity {
			t.Fatalf("%s ingest → %d, want 422", format, status)
		}
		if problem.Code != "validation_error" || !strings.Contains(problem.Detail, "txt, md, vtt, srt, json") {
			t.Fatalf("%s ingest problem = %+v, want validation_error naming the accepted formats", format, problem)
		}
	}

	// Nothing was persisted for the refused uploads: the manifest is empty.
	var manifest struct {
		Data    []voiceSourceWire `json:"data"`
		Summary voiceSummaryWire  `json:"summary"`
	}
	if status := e.call(t, "GET", "/v1/voice-profiles/"+created.ID+"/sources", nil, nil, &manifest); status != http.StatusOK {
		t.Fatalf("manifest → %d", status)
	}
	if len(manifest.Data) != 0 || manifest.Summary.TotalWords != 0 {
		t.Fatalf("refused uploads left corpus rows behind: %+v", manifest)
	}
}

// A labelled transcript without the owner's label is refused whole —
// never half-ingested with the other side included (§B1.2).
func TestVoiceTranscriptIngestRequiresTheOwnersSpeakerLabel(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	created := createVoiceProfile(t, e)

	if status := e.call(t, "POST", "/v1/voice-profiles/"+created.ID+"/sources", anyMap{
		"kind": "transcript", "register": "spoken", "source_label": "Unattributed", "source_ref": "call-78",
		"format": "transcript", "content": voiceTestVTT,
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
// reads nor lists the first tenant's voice profile. HTTP can no longer
// select a tenant (one installation serves one organization,
// A107/ADR-0061), so tenant B exists only as directly seeded rows and
// the assertion runs through the store — the same RBAC + row-scope path
// the HTTP surface drives.
func TestVoiceProfileIsInvisibleAcrossTenants(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var created voiceProfileWire
	if status := e.call(t, "POST", "/v1/voice-profiles", anyMap{}, nil, &created); status != http.StatusCreated {
		t.Fatalf("create → %d", status)
	}

	// Tenant B: a second workspace + user seeded past the boot invariant
	// (which binds the process, not the schema — RLS still isolates rows).
	ctx := context.Background()
	wsB, userB := ids.NewV7(), ids.NewV7()
	if _, err := e.owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Voice Two', 'voice-two', 'EUR')`, wsB); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, 'bea@example.com', 'Bea Boss')`,
		userB, wsB); err != nil {
		t.Fatal(err)
	}
	ctxB := principal.WithCorrelationID(principal.WithWorkspaceID(ctx, wsB), ids.NewV7())
	ctxB = principal.WithActor(ctxB, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + userB.String(), UserID: userB,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects:  map[string]principal.ObjectGrant{"voice_profile": {Create: true, Read: true, Update: true, Delete: true}},
			RowScope: principal.RowScopeAll,
		},
	})

	store := ai.NewVoiceStore(e.pool)
	profileID, err := ids.Parse(created.ID)
	if err != nil {
		t.Fatalf("created profile id: %v", err)
	}
	if _, err := store.GetProfile(ctxB, profileID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("cross-tenant get err = %v, want ErrNotFound (existence hidden)", err)
	}
	page, err := store.ListProfiles(ctxB, nil, nil)
	if err != nil {
		t.Fatalf("cross-tenant list: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("tenant B lists %d foreign profiles, want 0", len(page.Items))
	}
}
