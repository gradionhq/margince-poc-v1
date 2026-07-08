// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The B-E07.5a ingest pipeline as specs: register defaults per kind
// (features/09 §B1.1), the word-count meter and its §B1.4 quality bands,
// transcript normalization across .vtt/.srt/JSON, and the §B1.2 speaker
// filter — a both-sided transcript ingests ZERO other-party text.

import (
	"errors"
	"strings"
	"testing"
)

func TestQualityBandsFollowTheVersionedMeterThresholds(t *testing.T) {
	if CorpusMeterVersion != 1 {
		t.Fatalf("meter version = %d — these pinned expectations describe v1; bumping the thresholds must bump the version and this spec together", CorpusMeterVersion)
	}
	cases := []struct {
		words int
		want  string
	}{
		{0, BandThin},
		{4000, BandThin}, // the funnel's minimum build gate is still thin
		{7999, BandThin},
		{8000, BandGood},
		{19999, BandGood},
		{20000, BandRich},
		{29999, BandRich},
		{CorpusTargetWords, BandSharp}, // the 30k target IS the sharp boundary (§B2 exit gate)
		{50000, BandSharp},
	}
	for _, tc := range cases {
		if got := QualityBand(tc.words); got != tc.want {
			t.Errorf("QualityBand(%d) = %q, want %q", tc.words, got, tc.want)
		}
	}
}

func TestDefaultRegisterTagsEachSourceKind(t *testing.T) {
	cases := map[string]string{
		"transcript": "spoken",
		"voice_memo": "spoken",
		"chat":       "casual",
		"post":       "written",
		"longform":   "written",
		"email":      "written",
	}
	for kind, want := range cases {
		if got := DefaultRegister(kind); got != want {
			t.Errorf("DefaultRegister(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestWordCountCountsWhitespaceDelimitedWords(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"   \n\t ", 0},
		{"one", 1},
		// A free-standing dash is a whitespace-delimited token: the meter
		// unit is plain Fields semantics, same as the funnel's counter.
		{"Guten Tag, Herr Schmidt — anbei das Angebot.", 8},
		{"line one\nline two\n", 4},
	}
	for _, tc := range cases {
		if got := WordCount(tc.text); got != tc.want {
			t.Errorf("WordCount(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}

func TestSourceRefForContentIsStableAndContentBound(t *testing.T) {
	a := SourceRefForContent("the same paste")
	b := SourceRefForContent("the same paste")
	c := SourceRefForContent("a different paste")
	if a != b {
		t.Fatalf("same content produced different refs: %q vs %q — idempotency would break", a, b)
	}
	if a == c {
		t.Fatalf("different content collided on ref %q", a)
	}
	if !strings.HasPrefix(a, "sha256:") {
		t.Fatalf("derived ref %q does not declare its scheme", a)
	}
}

func TestPlainTextFormatsPassThroughUnchanged(t *testing.T) {
	for _, format := range []string{"txt", "md"} {
		text, err := NormalizeCorpusText(format, "# Heading\n\nSam: this colon is prose, not a speaker cue.", "", false)
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if !strings.Contains(text, "Sam: this colon is prose") {
			t.Fatalf("%s mangled pass-through text: %q", format, text)
		}
	}
}

// The V1 corpus is text only (features/09 §B1.1): a binary document has
// no honest word count without real extraction (deferred: B-E07.5c), so
// it is refused with the field-level 422 shape naming the accepted set.
func TestBinaryDocumentFormatsAreRefusedNotEstimated(t *testing.T) {
	for _, format := range []string{"docx", "pdf"} {
		_, err := NormalizeCorpusText(format, "binary payload", "", false)
		var ingest *CorpusIngestError
		if !errors.As(err, &ingest) {
			t.Fatalf("%s: err = %v, want a CorpusIngestError", format, err)
		}
		if ingest.Field != "format" || !strings.Contains(ingest.Reason, "txt, md, vtt, srt, json") {
			t.Fatalf("%s: error %+v does not name the field and the accepted formats", format, ingest)
		}
		if !strings.Contains(ingest.Reason, "text only") {
			t.Fatalf("%s: error %+v does not state the text-only rule (ADR-0058)", format, ingest)
		}
	}
}

// The meter's number IS the word count of the text that entered the
// corpus — computed from the content, never derived from its byte size
// (features/09 §B1.1: real words, no estimate). One pinned example per
// accepted format.
func TestIngestWordCountIsTheRealCountOfTheExtractedText(t *testing.T) {
	cases := []struct {
		name string
		in   IngestSourceInput
		want int
	}{
		{"txt post", IngestSourceInput{
			Kind: "post", SourceLabel: "post", Format: "txt",
			Content: "Shipping beats planning, every single quarter.",
		}, 6},
		{"md longform", IngestSourceInput{
			Kind: "longform", SourceLabel: "blog", Format: "md",
			Content: "# Audit story\n\nLead with the number, close with the ask.",
		}, 11},
		{"vtt transcript counts only the owner's turns", IngestSourceInput{
			Kind: "transcript", SourceLabel: "call", Format: "vtt",
			SpeakerLabel: "Ada Admin", Content: bothSidedVTT,
		}, 20},
		{"srt transcript counts only the owner's turns", IngestSourceInput{
			Kind: "transcript", SourceLabel: "call", Format: "srt",
			SpeakerLabel: "Ada Admin", Content: bothSidedSRT,
		}, 20},
		{"json transcript counts only the owner's turns", IngestSourceInput{
			Kind: "transcript", SourceLabel: "call", Format: "json",
			SpeakerLabel: "Ada",
			Content:      `[{"speaker":"Ada","text":"I own this deal."},{"speaker":"Klaus","text":"We are still comparing vendors."}]`,
		}, 4},
		// Whitespace padding inflates the byte size ~40× without adding a
		// word; a size-derived number would move, the real count must not.
		{"count follows words, not bytes", IngestSourceInput{
			Kind: "post", SourceLabel: "padded post", Format: "txt",
			Content: "three real words" + strings.Repeat(" ", 600),
		}, 3},
	}
	for _, tc := range cases {
		prepared, err := prepareSource(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if prepared.Words != tc.want {
			t.Errorf("%s: words = %d, want %d", tc.name, prepared.Words, tc.want)
		}
		if prepared.Words != WordCount(prepared.Text) {
			t.Errorf("%s: stored count %d disagrees with the extracted text's count %d",
				tc.name, prepared.Words, WordCount(prepared.Text))
		}
	}
}

const bothSidedVTT = `WEBVTT

NOTE recorded 2026-07-01

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

func TestVTTSpeakerFilterKeepsOnlyTheOwnersTurns(t *testing.T) {
	text, err := NormalizeCorpusText("vtt", bothSidedVTT, "Ada Admin", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "finance") || strings.Contains(text, "Klaus") {
		t.Fatalf("other-party text survived the speaker filter: %q", text)
	}
	for _, own := range []string{"pipeline stalls", "summary today", "follow up on Friday"} {
		if !strings.Contains(text, own) {
			t.Fatalf("owner's own turn %q was dropped: %q", own, text)
		}
	}
}

func TestLabelledTranscriptWithoutSpeakerLabelIsRefusedNotHalfIngested(t *testing.T) {
	if _, err := NormalizeCorpusText("vtt", bothSidedVTT, "", false); err == nil {
		t.Fatal("a speaker-labelled transcript ingested without naming the owner — the other side would enter the corpus")
	}
}

const bothSidedSRT = `1
00:00:01,000 --> 00:00:04,000
Ada Admin: Let me walk you through the proposal.

2
00:00:04,500 --> 00:00:08,000
Klaus Kunde: Our budget round closes in March.

3
00:00:08,500 --> 00:00:11,000
Ada Admin: Then March it is, I will plan
the rollout around your budget round.
`

func TestSRTSpeakerFilterMatchesCaseInsensitively(t *testing.T) {
	text, err := NormalizeCorpusText("srt", bothSidedSRT, "ada admin", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "budget round closes") {
		t.Fatalf("other-party text survived: %q", text)
	}
	if !strings.Contains(text, "walk you through the proposal") || !strings.Contains(text, "plan\nthe rollout") {
		t.Fatalf("owner turns lost, incl. the wrapped line: %q", text)
	}
}

func TestTranscriptJSONShapesAreAutoDetected(t *testing.T) {
	topLevel := `[
		{"speaker": "Ada", "text": "I own this deal."},
		{"speaker": "Klaus", "text": "We are still comparing vendors."}
	]`
	wrapped := `{"segments": [
		{"name": "Ada", "content": "I own this deal."},
		{"name": "Klaus", "content": "We are still comparing vendors."}
	]}`
	for name, content := range map[string]string{"top-level array": topLevel, "wrapped segments": wrapped} {
		text, err := NormalizeCorpusText("json", content, "Ada", false)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(text, "comparing vendors") {
			t.Fatalf("%s: other-party text survived: %q", name, text)
		}
		if !strings.Contains(text, "I own this deal.") {
			t.Fatalf("%s: owner turn lost: %q", name, text)
		}
	}
}

func TestMalformedTranscriptJSONIsAnActionableError(t *testing.T) {
	for name, content := range map[string]string{
		"not json":       "just a paste",
		"no turns array": `{"title": "weekly sync"}`,
		"empty array":    `[]`,
	} {
		if _, err := NormalizeCorpusText("json", content, "Ada", false); err == nil {
			t.Errorf("%s: accepted as a transcript", name)
		}
	}
}

func TestUnlabelledSingleSpeakerInputPassesWhole(t *testing.T) {
	memo := `[{"text": "Note to self about the Q3 narrative."}, {"text": "Lead with the audit story."}]`
	text, err := NormalizeCorpusText("json", memo, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Q3 narrative") || !strings.Contains(text, "audit story") {
		t.Fatalf("unlabelled memo lost text: %q", text)
	}
}

// The Art. 17 posture of the corpus table rests on these two refusals:
// a conversational source cannot arrive in a format the speaker filter
// cannot run on, and an unlabelled conversation cannot slip through as
// "single-speaker".
func TestConversationalKindsDemandAttributableInput(t *testing.T) {
	if _, err := prepareSource(IngestSourceInput{
		Kind: "transcript", SourceLabel: "sales call", Format: "txt",
		Content: "Ada: hello\nKlaus: our budget is 50k",
	}); err == nil {
		t.Fatal("a plain-text transcript ingested — the counterparty's words would enter the corpus unfiltered")
	}
	unlabelled := `[{"text": "hello"}, {"text": "our budget is 50k"}]`
	if _, err := NormalizeCorpusText("json", unlabelled, "", true); err == nil {
		t.Fatal("an unlabelled conversational transcript passed whole — attribution is required to filter it")
	}
}
