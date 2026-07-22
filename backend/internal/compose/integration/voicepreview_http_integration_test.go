// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The corpus dry-run over HTTP: the preview names each speaker with their
// SPOKEN word counts before anything is stored, the ingest 201 tells the
// kept-versus-discarded story, and every refusal carries its stable
// machine code — the surface a conversational client narrates from.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

type voicePreviewWire struct {
	DetectedFormat string `json:"detected_format"`
	TotalWords     int    `json:"total_words"`
	Speakers       []struct {
		Label string `json:"label"`
		Turns int    `json:"turns"`
		Words int    `json:"words"`
	} `json:"speakers"`
	UnattributedWords      int  `json:"unattributed_words"`
	IngestibleAsTranscript bool `json:"ingestible_as_transcript"`
}

type problemWire struct {
	Details struct {
		Errors []struct {
			Field   string `json:"field"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"details"`
}

const previewMeetingTranscript = "00:00:00 Lars Jankowfsky\n" +
	"My own words carry the voice signal here.\n" +
	"00:00:07 Anna Schmidt\n" +
	"Her words must never enter the corpus.\n" +
	"00:00:12 Lars Jankowfsky\n" +
	"And a second turn of mine."

func TestVoiceCorpusPreviewAndIngestStatsHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	created := createVoiceProfile(t, e)
	base := "/v1/voice-profiles/" + created.ID

	var preview voicePreviewWire
	if status := e.call(t, "POST", base+"/sources/preview",
		anyMap{"format": "transcript", "content": previewMeetingTranscript},
		nil, &preview); status != http.StatusOK {
		t.Fatalf("preview → %d", status)
	}
	if preview.DetectedFormat != "srt" || !preview.IngestibleAsTranscript {
		t.Fatalf("preview = %+v, want a speaker-attributed srt shape", preview)
	}
	if len(preview.Speakers) != 2 {
		t.Fatalf("speakers = %+v, want both meeting participants", preview.Speakers)
	}
	lars, anna := preview.Speakers[0], preview.Speakers[1]
	if lars.Label != "Lars Jankowfsky" || lars.Turns != 2 || lars.Words != 14 {
		t.Fatalf("lars = %+v, want 14 spoken words over 2 turns", lars)
	}
	if anna.Label != "Anna Schmidt" || anna.Turns != 1 || anna.Words != 7 {
		t.Fatalf("anna = %+v, want her 7 words in 1 turn", anna)
	}
	if preview.TotalWords != 21 || preview.UnattributedWords != 0 {
		t.Fatalf("totals = %d/%d — spoken words only, headers are never words",
			preview.TotalWords, preview.UnattributedWords)
	}

	// The raw request keeps the response headers: the refusal contract is
	// the 422 body AND its problem+json media type.
	rawBody, err := json.Marshal(anyMap{"format": "docx", "content": "binary"})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", e.ts.URL+base+"/sources/preview", bytes.NewReader(rawBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unknown format preview → %d, want 422", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("refusal media type = %q, want application/problem+json", ct)
	}
	var refused problemWire
	if err := json.NewDecoder(resp.Body).Decode(&refused); err != nil {
		t.Fatal(err)
	}
	if len(refused.Details.Errors) != 1 || refused.Details.Errors[0].Code != "unsupported_format" {
		t.Fatalf("refusal = %+v, want the stable unsupported_format code", refused)
	}

	var ingested struct {
		voiceIngestResponse
		IngestStats struct {
			InputWords     int      `json:"input_words"`
			KeptWords      int      `json:"kept_words"`
			KeptTurns      int      `json:"kept_turns"`
			DiscardedTurns int      `json:"discarded_turns"`
			SpeakersSeen   []string `json:"speakers_seen"`
		} `json:"ingest_stats"`
	}
	if status := e.call(t, "POST", base+"/sources", anyMap{
		"kind": "transcript", "register": "spoken", "source_label": "Meeting",
		"source_ref": "meeting-1", "format": "transcript",
		"speaker_label": "Lars Jankowfsky", "content": previewMeetingTranscript,
	}, nil, &ingested); status != http.StatusCreated {
		t.Fatalf("transcript ingest → %d", status)
	}
	if ingested.Source.WordCount != 14 {
		t.Fatalf("stored word_count = %d, want only the owner's 14 words", ingested.Source.WordCount)
	}
	stats := ingested.IngestStats
	if stats.InputWords != 21 || stats.KeptWords != 14 || stats.KeptTurns != 2 || stats.DiscardedTurns != 1 {
		t.Fatalf("ingest_stats = %+v — the kept-versus-discarded story must match the filter", stats)
	}
	if len(stats.SpeakersSeen) != 2 || stats.SpeakersSeen[0] != "Lars Jankowfsky" || stats.SpeakersSeen[1] != "Anna Schmidt" {
		t.Fatalf("speakers_seen = %v, want both participants in first-seen order", stats.SpeakersSeen)
	}

	var unlabeled problemWire
	if status := e.call(t, "POST", base+"/sources", anyMap{
		"kind": "transcript", "register": "spoken", "source_label": "Meeting",
		"source_ref": "meeting-2", "format": "transcript",
		"content": previewMeetingTranscript,
	}, nil, &unlabeled); status != http.StatusUnprocessableEntity {
		t.Fatalf("labelled transcript without speaker_label → %d, want 422", status)
	}
	if len(unlabeled.Details.Errors) != 1 || unlabeled.Details.Errors[0].Code != "speaker_label_required" {
		t.Fatalf("refusal = %+v, want speaker_label_required", unlabeled)
	}
}
