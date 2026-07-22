// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The corpus dry-run and honesty surface (§B1.2's user-facing half): the
// speaker preview that lets the owner be asked "which of these is you?"
// BEFORE any words are committed, and the kept-versus-discarded ingest
// stats the conversational meter narrates from. Pure functions over the
// same parsers the ingest pipeline commits with.

import "strings"

// CorpusSpeaker aggregates one detected speaker in a previewed source.
type CorpusSpeaker struct {
	Label string
	Turns int
	Words int
}

// CorpusPreview is the dry-run answer to "who speaks in this file?" —
// computed without storing anything, so the owner can be asked which
// speaker they are BEFORE any words enter the corpus.
type CorpusPreview struct {
	DetectedFormat         string
	TotalWords             int
	Speakers               []CorpusSpeaker
	UnattributedWords      int
	IngestibleAsTranscript bool
}

// PreviewCorpusText inspects one candidate source in its wire format
// (text | transcript, mirroring ingest). Pure — no persistence, no model.
func PreviewCorpusText(format, content string) (CorpusPreview, error) {
	if strings.TrimSpace(content) == "" {
		return CorpusPreview{}, &CorpusIngestError{Field: voiceKeyContent, Reason: voiceValidationNotEmpty}
	}
	if len(content) > maxCorpusSourceBytes {
		return CorpusPreview{}, &CorpusIngestError{Field: voiceKeyContent, Reason: "one source is capped at 1 MiB of text — split the upload"}
	}
	concrete := corpusFormatTxt
	if format == voiceSourceKindTranscript {
		concrete = transcriptCorpusFormat(content)
	}
	turns, plain, err := corpusTurns(concrete, content)
	if err != nil {
		return CorpusPreview{}, err
	}
	preview := CorpusPreview{DetectedFormat: concrete, TotalWords: WordCount(content)}
	if plain {
		return preview, nil
	}
	index := map[string]int{}
	for _, turn := range turns {
		words := WordCount(turn.Text)
		if turn.Speaker == "" {
			preview.UnattributedWords += words
			continue
		}
		key := normalizeSpeaker(turn.Speaker)
		at, seen := index[key]
		if !seen {
			at = len(preview.Speakers)
			index[key] = at
			preview.Speakers = append(preview.Speakers, CorpusSpeaker{Label: turn.Speaker})
		}
		preview.Speakers[at].Turns++
		preview.Speakers[at].Words += words
	}
	preview.IngestibleAsTranscript = len(preview.Speakers) > 0
	return preview, nil
}

// CorpusIngestStats reports what the §B1.2 speaker filter did to one
// ingested source — the honest kept-versus-discarded story the meter's
// numbers rest on.
type CorpusIngestStats struct {
	InputWords     int
	KeptWords      int
	KeptTurns      int
	DiscardedTurns int
	SpeakersSeen   []string
}

func ingestStats(content string, turns []speakerTurn, plain bool, keptText, speakerLabel string) CorpusIngestStats {
	stats := CorpusIngestStats{InputWords: WordCount(content), KeptWords: WordCount(keptText)}
	if plain {
		stats.KeptTurns = 1
		return stats
	}
	labelled := false
	for _, turn := range turns {
		if turn.Speaker != "" {
			labelled = true
			break
		}
	}
	// Mirrors filterOwnTurns exactly: in a labelled transcript only the
	// owner's turns survive (an unattributed stray is discarded); a fully
	// unlabelled one passes whole when it was allowed through at all.
	want := normalizeSpeaker(speakerLabel)
	seen := map[string]bool{}
	for _, turn := range turns {
		if turn.Speaker != "" && !seen[normalizeSpeaker(turn.Speaker)] {
			seen[normalizeSpeaker(turn.Speaker)] = true
			stats.SpeakersSeen = append(stats.SpeakersSeen, turn.Speaker)
		}
		kept := !labelled || normalizeSpeaker(turn.Speaker) == want
		if kept {
			stats.KeptTurns++
		} else {
			stats.DiscardedTurns++
		}
	}
	return stats
}
