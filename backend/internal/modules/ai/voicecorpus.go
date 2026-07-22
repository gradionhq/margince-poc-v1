// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Multi-source corpus ingest mechanics (B-E07.5a, features/09 §B1): the
// pure text pipeline the voice store runs before a corpus row persists —
// format normalization (.txt/.md pass through; .vtt/.srt/transcript-JSON
// are parsed as turns), transcript SPEAKER-FILTERING (only the owner's
// own turns are modeled, never the other side — §B1.2, the epic's
// privacy + quality invariant), register tagging with per-kind defaults,
// word counting, and the word-count/quality-band meter (§B1.4).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// CorpusMeterVersion versions the meter thresholds below: the quality
// bands are an eval artifact (B-E07.4 reusable-artifact DoD), so their
// one source of truth carries an explicit version a regression test can
// pin against.
const CorpusMeterVersion = 1

// CorpusTargetWords is the ~30k-word corpus the meter fills toward
// (features/09 §B1.4); reaching it lands the sharp band (§B2 exit gate).
const CorpusTargetWords = 30000

// Quality bands over the non-excluded corpus word total (features/09
// §B1.4). The thin/good boundary at 8k and good/rich at 20k mirror the
// onboarding funnel's meter; rich/sharp sits at the 30k target.
const (
	BandThin  = "thin"
	BandGood  = "good"
	BandRich  = "rich"
	BandSharp = "sharp"
)

// Maturity is the honest build-eligibility axis from ADR-0066. It is
// deliberately independent from corpus quality: an 800-word profile can be
// useful and provisional while its quality band remains thin.
func Maturity(totalWords int) string {
	switch {
	case totalWords < 800:
		return voiceMaturityCollecting
	case totalWords < 4000:
		return voiceMaturityProvisional
	default:
		return voiceMaturityBuilding
	}
}

// QualityBand places a corpus word total in its §B1.4 band.
func QualityBand(totalWords int) string {
	switch {
	case totalWords < 8000:
		return BandThin
	case totalWords < 20000:
		return BandGood
	case totalWords < CorpusTargetWords:
		return BandRich
	default:
		return BandSharp
	}
}

// CorpusIngestError reports an unusable ingest input; the transport maps
// it to a 422 with the field and reason intact. Code, when set, is the
// stable machine-readable refusal class a conversational client can voice
// without parsing prose (empty falls back to the generic "invalid").
type CorpusIngestError struct {
	Field  string
	Code   string
	Reason string
}

// The stable refusal codes for conversational corpus input.
const (
	CorpusErrUnattributedTranscript = "unattributed_transcript"
	CorpusErrSpeakerLabelRequired   = "speaker_label_required"
	CorpusErrSpeakerNotFound        = "speaker_not_found"
	CorpusErrUnsupportedFormat      = "unsupported_format"
)

func (e *CorpusIngestError) Error() string {
	return fmt.Sprintf("voice corpus %s: %s", e.Field, e.Reason)
}

// DefaultRegister tags the closed ADR-0066 source vocabulary. An explicit
// register from the caller remains authoritative.
func DefaultRegister(kind string) string {
	switch kind {
	case voiceSourceKindTranscript:
		return voiceRegisterSpoken
	case voiceSourceKindEmail:
		return voiceRegisterEmail
	case voiceSourceKindLinkedIn:
		return voiceRegisterSocial
	case voiceSourceKindProposal, voiceSourceKindDocument:
		return voiceRegisterLongForm
	default:
		return voiceRegisterGeneral
	}
}

// WordCount counts whitespace-delimited words — the meter's unit.
func WordCount(text string) int {
	return len(strings.Fields(text))
}

// SourceRefForContent derives a stable natural key for sources that have
// none (pasted text): idempotency then binds on the content itself.
func SourceRefForContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// The concrete corpus text formats the pipeline parses.
const (
	corpusFormatTxt  = "txt"
	corpusFormatMd   = "md"
	corpusFormatVTT  = "vtt"
	corpusFormatSRT  = "srt"
	corpusFormatJSON = "json"
)

// speakerTurn is one attributed stretch of transcript text; Speaker is
// empty when the format carries no attribution.
type speakerTurn struct {
	Speaker string
	Text    string
}

// NormalizeCorpusText turns one raw source in the declared format into
// the plain text that enters the corpus. Transcript formats (vtt, srt,
// json) are parsed into speaker turns and filtered: when any turn is
// speaker-labelled — or requireAttribution says the kind is
// conversational — speakerLabel is REQUIRED and only that speaker's
// turns survive; the other side of a conversation is never ingested,
// even by omission (features/09 §B1.2). Unlabelled input passes whole
// only when attribution is not required (a pasted memo, a
// single-speaker dictation).
func NormalizeCorpusText(format, content, speakerLabel string, requireAttribution bool) (string, error) {
	turns, plain, err := corpusTurns(format, content)
	if err != nil {
		return "", err
	}
	if plain {
		return content, nil
	}
	return filterOwnTurns(turns, speakerLabel, requireAttribution)
}

// corpusTurns parses one raw source into speaker turns; plain formats
// (txt/md) carry no turn structure and pass through whole.
func corpusTurns(format, content string) (turns []speakerTurn, plain bool, err error) {
	switch format {
	case corpusFormatTxt, corpusFormatMd:
		return nil, true, nil
	case corpusFormatVTT:
		return parseVTT(content), false, nil
	case corpusFormatSRT:
		return parseSRT(content), false, nil
	case corpusFormatJSON:
		parsed, err := parseTranscriptJSON(content)
		if err != nil {
			return nil, false, err
		}
		return parsed, false, nil
	default:
		// The V1 corpus is text only (ADR-0058, features/09 §B1.1): a
		// binary document (.docx/.pdf) has no honest word count without
		// real extraction, so it is refused, never estimated from bytes.
		return nil, false, &CorpusIngestError{
			Field:  voiceKeyFormat,
			Code:   CorpusErrUnsupportedFormat,
			Reason: "the corpus is text only — must be one of txt, md, vtt, srt, json; convert a binary document or paste its text",
		}
	}
}

// filterOwnTurns applies the §B1.2 speaker filter over parsed turns.
func filterOwnTurns(turns []speakerTurn, speakerLabel string, requireAttribution bool) (string, error) {
	labelled := false
	for _, turn := range turns {
		if turn.Speaker != "" {
			labelled = true
			break
		}
	}
	var kept []string
	if !labelled {
		if requireAttribution {
			return "", &CorpusIngestError{
				Field:  voiceKeyContent,
				Code:   CorpusErrUnattributedTranscript,
				Reason: "a conversational source needs speaker-attributed turns; an unlabelled transcript cannot be filtered to the owner's own words",
			}
		}
		for _, turn := range turns {
			kept = append(kept, turn.Text)
		}
		return strings.Join(kept, "\n"), nil
	}
	if strings.TrimSpace(speakerLabel) == "" {
		return "", &CorpusIngestError{
			Field:  voiceKeySpeakerLabel,
			Code:   CorpusErrSpeakerLabelRequired,
			Reason: "this transcript attributes its turns to speakers; name the owner's label so only their own words are modeled",
		}
	}
	want := normalizeSpeaker(speakerLabel)
	for _, turn := range turns {
		if normalizeSpeaker(turn.Speaker) == want {
			kept = append(kept, turn.Text)
		}
	}
	return strings.Join(kept, "\n"), nil
}

func normalizeSpeaker(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// parseVTT reads WebVTT cue text: header/NOTE/STYLE blocks and timing
// lines are dropped, `<v Speaker>` voice tags and `Speaker:` prefixes
// attribute turns, and attribution persists across a cue's wrapped lines.
func parseVTT(content string) []speakerTurn {
	var turns []speakerTurn
	current := ""
	inBlockComment := false
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			inBlockComment = false
			continue
		case strings.HasPrefix(trimmed, "WEBVTT"):
			continue
		case strings.HasPrefix(trimmed, "NOTE") || strings.HasPrefix(trimmed, "STYLE") || strings.HasPrefix(trimmed, "REGION"):
			inBlockComment = true
			continue
		case inBlockComment:
			continue
		case strings.Contains(trimmed, "-->"):
			// A new cue: attribution resets until the cue text names one.
			current = ""
			continue
		case isCueIdentifier(trimmed):
			continue
		}
		speaker, text := splitSpeakerLine(trimmed)
		if speaker != "" {
			current = speaker
		}
		if text != "" {
			turns = append(turns, speakerTurn{Speaker: current, Text: text})
		}
	}
	return turns
}

// isCueIdentifier recognizes the bare numeric cue counters both VTT and
// SRT interleave with text; named VTT identifiers are indistinguishable
// from dialogue, so only the numeric convention is dropped.
func isCueIdentifier(line string) bool {
	if line == "" {
		return false
	}
	for _, r := range line {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseSRT reads SubRip blocks: index + timing lines dropped,
// `Speaker:` prefixes attribute turns across wrapped lines.
func parseSRT(content string) []speakerTurn {
	var turns []speakerTurn
	current := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		switch {
		case trimmed == "":
			current = ""
			continue
		case strings.Contains(trimmed, "-->") || isCueIdentifier(trimmed):
			continue
		}
		if speaker, ok := timestampSpeakerLine(trimmed); ok {
			current = speaker
			continue
		}
		speaker, text := splitSpeakerLine(trimmed)
		if speaker != "" {
			current = speaker
		}
		if text != "" {
			turns = append(turns, speakerTurn{Speaker: current, Text: text})
		}
	}
	return turns
}

// splitSpeakerLine extracts attribution from one cue line: a WebVTT
// `<v Name>text</v>` voice tag, or the `Name: text` convention. A name is
// letters-first with at most a short trailing number — that admits the
// "Speaker 1" / "Sprecher 2" labels diarizers emit while a URL or clock
// time is still never a speaker.
func splitSpeakerLine(line string) (speaker, text string) {
	if strings.HasPrefix(line, "<v ") {
		if end := strings.Index(line, ">"); end > 3 {
			speaker = strings.TrimSpace(line[3:end])
			text = strings.TrimSpace(strings.ReplaceAll(line[end+1:], "</v>", ""))
			return speaker, text
		}
	}
	if idx := strings.Index(line, ":"); idx > 0 && idx <= 40 && !strings.HasPrefix(line[idx+1:], "//") {
		if candidate := speakerCandidate(line[:idx]); candidate != "" {
			return candidate, strings.TrimSpace(line[idx+1:])
		}
	}
	return "", strings.TrimSpace(line)
}

// timestampSpeakerLine recognizes the "00:03:12 Name" header line the
// meeting exporters (Fathom, Teams, Otter) emit: a leading clock time,
// then the speaker whose turn follows on the next lines.
func timestampSpeakerLine(line string) (speaker string, ok bool) {
	clock, rest, found := strings.Cut(line, " ")
	if !found || !clockTime(clock) {
		return "", false
	}
	name := speakerCandidate(strings.TrimSpace(rest))
	if name == "" {
		return "", false
	}
	return name, true
}

// clockTime matches mm:ss or hh:mm:ss with 1-2 digit groups.
func clockTime(s string) bool {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 2 {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// speakerCandidate accepts a name-shaped prefix: it must start with a
// letter, carry digits only as one trailing run of at most three (the
// diarizer convention), and contain no markup or path characters.
func speakerCandidate(prefix string) string {
	candidate := strings.TrimSpace(prefix)
	if candidate == "" || strings.ContainsAny(candidate, "/<>") {
		return ""
	}
	stem := strings.TrimSpace(strings.TrimRight(candidate, "0123456789"))
	if len(candidate)-len(stem) > 4 || stem == "" {
		// TrimSpace may drop the separator space too, so allow digits+space.
		return ""
	}
	if strings.ContainsAny(stem, "0123456789") {
		return ""
	}
	if first := []rune(stem)[0]; !unicode.IsLetter(first) {
		return ""
	}
	return candidate
}

// transcriptItem is the common shape of one turn across the JSON
// transcript exports in the wild; alternate key spellings are folded in
// UnmarshalJSON below.
type transcriptItem struct {
	Speaker string
	Text    string
}

func (item *transcriptItem) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	firstString := func(keys ...string) string {
		for _, key := range keys {
			if v, ok := fields[key]; ok {
				var s string
				if err := json.Unmarshal(v, &s); err == nil {
					return s
				}
			}
		}
		return ""
	}
	item.Speaker = firstString("speaker", "name", "who")
	item.Text = firstString("text", "content", "message")
	return nil
}

// parseTranscriptJSON auto-detects the two transcript JSON shapes the
// funnel accepts (features/09 §B1.1): a top-level array of turns, or an
// object wrapping that array under segments/turns/messages/entries.
func parseTranscriptJSON(content string) ([]speakerTurn, error) {
	items, err := decodeTranscriptItems(content)
	if err != nil {
		return nil, err
	}
	turns := make([]speakerTurn, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Text) == "" {
			continue
		}
		turns = append(turns, speakerTurn{Speaker: item.Speaker, Text: strings.TrimSpace(item.Text)})
	}
	if len(turns) == 0 {
		return nil, &CorpusIngestError{Field: voiceKeyContent, Reason: "no transcript turns found — expected an array of {speaker, text} objects, optionally under segments/turns/messages/entries"}
	}
	return turns, nil
}

func decodeTranscriptItems(content string) ([]transcriptItem, error) {
	var items []transcriptItem
	if err := json.Unmarshal([]byte(content), &items); err == nil {
		return items, nil
	}
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &wrapper); err != nil {
		return nil, &CorpusIngestError{Field: voiceKeyContent, Reason: "not valid JSON"}
	}
	for _, key := range []string{"segments", "turns", "messages", "entries"} {
		raw, ok := wrapper[key]
		if !ok {
			continue
		}
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, &CorpusIngestError{Field: voiceKeyContent, Reason: fmt.Sprintf("%q is not an array of transcript turns", key)}
		}
		return items, nil
	}
	return nil, &CorpusIngestError{Field: voiceKeyContent, Reason: "no transcript turns found — expected an array of {speaker, text} objects, optionally under segments/turns/messages/entries"}
}
