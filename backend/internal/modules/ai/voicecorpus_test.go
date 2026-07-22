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
		"email":      "email",
		"linkedin":   "social",
		"proposal":   "long_form",
		"document":   "long_form",
		"other":      "general",
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
		{"txt LinkedIn post", IngestSourceInput{
			Kind: "linkedin", SourceLabel: "post", Format: "text",
			Content: "Shipping beats planning, every single quarter.",
		}, 6},
		{"text document", IngestSourceInput{
			Kind: "document", SourceLabel: "blog", Format: "text",
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
			Kind: "linkedin", SourceLabel: "padded post", Format: "text",
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

func TestSpeakerPrefixAcceptsDiarizerNumberedLabels(t *testing.T) {
	cases := map[string]struct {
		line    string
		speaker string
	}{
		"plain name":            {"Lars: we ship on Monday", "Lars"},
		"numbered diarizer":     {"Speaker 1: we ship on Monday", "Speaker 1"},
		"german diarizer":       {"Sprecher 2: wir liefern am Montag", "Sprecher 2"},
		"attached number":       {"Speaker2: we ship", "Speaker2"},
		"clock time":            {"12:30 we ship", ""},
		"url":                   {"https://example.test/page", ""},
		"timestamp with colons": {"00:01:02: hello", ""},
		"long number run":       {"Speaker 12345: hello", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			speaker, _ := splitSpeakerLine(tc.line)
			if speaker != tc.speaker {
				t.Fatalf("speaker = %q, want %q", speaker, tc.speaker)
			}
		})
	}
}

func TestNumberedSpeakerTranscriptFiltersToTheOwnersTurns(t *testing.T) {
	content := "Speaker 1: my words here today\nSpeaker 2: their words never counted at all"
	text, err := NormalizeCorpusText("srt", content, "Speaker 1", true)
	if err != nil {
		t.Fatal(err)
	}
	if text != "my words here today" {
		t.Fatalf("kept %q — only Speaker 1's turns may survive", text)
	}
}

func TestBracketOpenedLabelledTextIsNotMistakenForJSON(t *testing.T) {
	content := "[10:03] intro\nLars: my own words\nAnna: her words"
	if format := transcriptCorpusFormat(content); format != "srt" {
		t.Fatalf("sniffed %q, want srt — a bracket-opened labelled transcript is not JSON", format)
	}
	prepared, err := prepareSource(IngestSourceInput{
		Kind: "transcript", SourceLabel: "call", Format: "transcript",
		SpeakerLabel: "Lars", Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Text != "my own words" {
		t.Fatalf("kept %q, want only Lars's turn", prepared.Text)
	}
}

func TestPreviewReportsSpeakersWithoutStoringAnything(t *testing.T) {
	content := "WEBVTT\n\n00:00.000 --> 00:04.000\n<v Lars>one two three\n\n00:04.000 --> 00:08.000\n<v Anna>four five six seven\n\n00:08.000 --> 00:10.000\n<v Lars>eight nine"
	preview, err := PreviewCorpusText("transcript", content)
	if err != nil {
		t.Fatal(err)
	}
	if preview.DetectedFormat != "vtt" || !preview.IngestibleAsTranscript {
		t.Fatalf("detected %q ingestible=%v", preview.DetectedFormat, preview.IngestibleAsTranscript)
	}
	if len(preview.Speakers) != 2 {
		t.Fatalf("speakers = %+v, want Lars and Anna", preview.Speakers)
	}
	lars, anna := preview.Speakers[0], preview.Speakers[1]
	if lars.Label != "Lars" || lars.Turns != 2 || lars.Words != 5 {
		t.Fatalf("lars = %+v", lars)
	}
	if anna.Label != "Anna" || anna.Turns != 1 || anna.Words != 4 {
		t.Fatalf("anna = %+v", anna)
	}
}

func TestPreviewOfPlainProseCarriesNoSpeakers(t *testing.T) {
	preview, err := PreviewCorpusText("text", "just my own six words of prose")
	if err != nil {
		t.Fatal(err)
	}
	if preview.DetectedFormat != "txt" || preview.IngestibleAsTranscript || len(preview.Speakers) != 0 {
		t.Fatalf("preview = %+v — plain prose has no transcript structure", preview)
	}
	if preview.TotalWords != 7 {
		t.Fatalf("total = %d, want 7", preview.TotalWords)
	}
}

func TestIngestStatsTellTheKeptVersusDiscardedStory(t *testing.T) {
	content := "Lars: one two three\nAnna: four five six seven\nLars: eight"
	prepared, err := prepareSource(IngestSourceInput{
		Kind: "transcript", SourceLabel: "call", Format: "transcript",
		SpeakerLabel: "lars", Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}
	stats := prepared.Stats
	if stats.KeptWords != 4 || stats.KeptTurns != 2 || stats.DiscardedTurns != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.InputWords <= stats.KeptWords {
		t.Fatalf("input %d must exceed kept %d — the counterparty's words were seen but not counted", stats.InputWords, stats.KeptWords)
	}
	if len(stats.SpeakersSeen) != 2 {
		t.Fatalf("speakers seen = %v", stats.SpeakersSeen)
	}
}

func TestCorpusRefusalsCarryStableMachineCodes(t *testing.T) {
	cases := map[string]struct {
		run  func() error
		code string
	}{
		"unattributed transcript": {func() error {
			_, err := NormalizeCorpusText("json", `[{"text": "hello there"}]`, "", true)
			return err
		}, CorpusErrUnattributedTranscript},
		"missing speaker label": {func() error {
			_, err := NormalizeCorpusText("srt", "Lars: hello", "", false)
			return err
		}, CorpusErrSpeakerLabelRequired},
		"speaker not found": {func() error {
			_, err := prepareSource(IngestSourceInput{
				Kind: "transcript", SourceLabel: "call",
				Format: "transcript", SpeakerLabel: "Nobody", Content: "Lars: hello there",
			})
			return err
		}, CorpusErrSpeakerNotFound},
		"unsupported format": {func() error {
			_, _, err := corpusTurns("docx", "binary")
			return err
		}, CorpusErrUnsupportedFormat},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.run()
			var ingest *CorpusIngestError
			if !errors.As(err, &ingest) || ingest.Code != tc.code {
				t.Fatalf("err = %v, want code %q", err, tc.code)
			}
		})
	}
}

func TestTimestampHeaderTranscriptsAttributeTheFollowingLines(t *testing.T) {
	content := "00:00:00 Daniel Pohlmann\nEbenso, wo erreiche ich dich?\n00:00:03 Lars Jankowfsky\nDu, ich bin heute in Bangkok.\n00:00:38 Lars Jankowfsky\nUnd dann war ich einen Tag unterwegs.\n00:00:49 Daniel Pohlmann\nDann machen wir das entspannt."
	preview, err := PreviewCorpusText("transcript", content)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Speakers) != 2 {
		t.Fatalf("speakers = %+v, want the two meeting participants", preview.Speakers)
	}
	text, err := NormalizeCorpusText("srt", content, "Lars Jankowfsky", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "erreiche") || !strings.Contains(text, "Bangkok") {
		t.Fatalf("kept %q — only Lars's turns may survive", text)
	}
}
