// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The corpus-source half of the voice store (B-E07.5a): the manifest
// rows under a profile, the idempotent-per-source_ref ingest write, and
// the live word/register meter. The profile half (artifact + versioned
// rebuild) lives in voice.go; both share VoiceStore and the row-scoped
// visibleProfile gate.

import (
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// maxCorpusSourceBytes bounds one ingested source (≈150k words of plain
// text) — the corpus target is 30k words total, so anything larger is a
// wrong upload, not a bigger voice.
const maxCorpusSourceBytes = 1 << 20

// VoiceCorpusSource is one manifest row; the ingested text stays
// store-internal (the builder reads it, the API never echoes it).
type VoiceCorpusSource struct {
	// note: neither voice_corpus_source nor its parent voice_profile is a
	// kernel entity kind, so both ids stay untyped (rule 7 kernel gap).
	ID               ids.UUID
	ProfileID        ids.UUID
	Origin           string
	Kind             string
	Register         string
	Weight           float64
	SourceLabel      string
	SourceRef        string
	WordCount        int
	Excluded         bool
	ExclusionReason  *string
	ExtractorVersion string
	OccurredAt       time.Time
	RetentionUntil   *time.Time
	ContentErasedAt  *time.Time
	Source           string
	CapturedBy       string
	Version          int64
	CreatedAt        time.Time
	UpdatedAt        *time.Time
	ArchivedAt       *time.Time
}

// CorpusSummary is the live word-count + register-mix meter over the
// non-excluded manifest (features/09 §B1.4).
type CorpusSummary struct {
	TotalWords    int
	TargetWords   int
	QualityBand   string
	Maturity      string
	RegisterWords map[string]int
	SourceCount   int
}

// IngestSourceInput is one corpus source in its raw declared format.
type IngestSourceInput struct {
	Kind         string
	Register     string // empty → DefaultRegister(kind)
	Weight       float64
	SourceLabel  string
	SourceRef    string // empty → SourceRefForContent
	Format       string // empty → txt
	SpeakerLabel string
	Content      string
	OccurredAt   *time.Time
}

// UpdateSourceInput carries the manifest PATCH subset; nil = unchanged.
type UpdateSourceInput struct {
	Included  *bool
	Excluded  *bool
	Weight    *float64
	IfVersion *int64
}

const voiceSourceColumns = `id, voice_profile_id, origin, kind, register, weight, source_label, source_ref, word_count, excluded, exclusion_reason, extractor_version, occurred_at, retention_until, content_erased_at, source, captured_by, version, created_at, updated_at, archived_at`

func scanVoiceSource(row pgx.Row) (VoiceCorpusSource, error) {
	var s VoiceCorpusSource
	err := row.Scan(&s.ID, &s.ProfileID, &s.Origin, &s.Kind, &s.Register, &s.Weight,
		&s.SourceLabel, &s.SourceRef, &s.WordCount, &s.Excluded, &s.ExclusionReason,
		&s.ExtractorVersion, &s.OccurredAt, &s.RetentionUntil, &s.ContentErasedAt,
		&s.Source, &s.CapturedBy, &s.Version, &s.CreatedAt, &s.UpdatedAt, &s.ArchivedAt)
	return s, err
}

// preparedSource is one validated, normalized, speaker-filtered source,
// ready to persist.
type preparedSource struct {
	Kind       string
	Register   string
	Weight     float64
	Label      string
	SourceRef  string
	Text       string
	Words      int
	OccurredAt time.Time
}

// prepareSource runs the pure half of the §B1 pipeline: field
// validation, per-kind register defaulting, format normalization with
// the speaker filter, word counting, and the content-hash fallback ref.
func prepareSource(in IngestSourceInput) (preparedSource, error) {
	switch in.Kind {
	case voiceSourceKindEmail, voiceSourceKindLinkedIn, voiceSourceKindProposal,
		voiceSourceKindTranscript, voiceSourceKindDocument, voiceSourceKindOther:
	default:
		return preparedSource{}, &CorpusIngestError{Field: voiceKeyKind, Reason: "must be one of email, linkedin, proposal, transcript, document, other"}
	}
	register := in.Register
	if register == "" {
		register = DefaultRegister(in.Kind)
	}
	switch register {
	case voiceRegisterEmail, voiceRegisterSocial, voiceRegisterLongForm,
		voiceRegisterSpoken, voiceRegisterGeneral:
	default:
		return preparedSource{}, &CorpusIngestError{Field: voiceKeyRegister, Reason: "must be one of email, social, long_form, spoken, general"}
	}
	weight := in.Weight
	if weight == 0 {
		weight = 1.0
	}
	if weight < 0 || weight > 2.0 {
		return preparedSource{}, &CorpusIngestError{Field: voiceKeyWeight, Reason: "must be between 0 and 2"}
	}
	if strings.TrimSpace(in.SourceLabel) == "" {
		return preparedSource{}, &CorpusIngestError{Field: voiceKeySourceLabel, Reason: voiceValidationNotEmpty}
	}
	if strings.TrimSpace(in.Content) == "" {
		return preparedSource{}, &CorpusIngestError{Field: voiceKeyContent, Reason: voiceValidationNotEmpty}
	}
	if len(in.Content) > maxCorpusSourceBytes {
		return preparedSource{}, &CorpusIngestError{Field: voiceKeyContent, Reason: "one source is capped at 1 MiB of text — split the upload"}
	}
	format := in.Format
	switch format {
	case "", "text":
		format = "txt"
	case voiceSourceKindTranscript:
		format = transcriptCorpusFormat(in.Content)
	}
	// Conversational kinds MUST arrive in a speaker-attributed format:
	// the §B1.2 filter is what keeps a counterparty's words out of the
	// corpus, and a plain-text conversation would walk straight past it —
	// the Art. 17 posture of this table rests on this refusal.
	if in.Kind == voiceSourceKindTranscript && (format == "txt" || format == "md") {
		return preparedSource{}, &CorpusIngestError{
			Field:  voiceKeyFormat,
			Reason: "a " + in.Kind + " source must be a speaker-attributed transcript (vtt, srt, or json) so only the owner's own words are modeled",
		}
	}
	text, err := NormalizeCorpusText(format, in.Content, in.SpeakerLabel, in.Kind == voiceSourceKindTranscript)
	if err != nil {
		return preparedSource{}, err
	}
	if in.Kind == voiceSourceKindTranscript && strings.TrimSpace(text) == "" {
		return preparedSource{}, &CorpusIngestError{
			Field:  voiceKeySpeakerLabel,
			Reason: "no turns belong to this speaker label — nothing of the owner's own words to ingest",
		}
	}
	sourceRef := in.SourceRef
	if sourceRef == "" {
		sourceRef = SourceRefForContent(in.Content)
	}
	var occurredAt time.Time
	if in.OccurredAt != nil {
		occurredAt = in.OccurredAt.UTC()
	}
	return preparedSource{
		Kind: in.Kind, Register: register, Weight: weight,
		Label: in.SourceLabel, SourceRef: sourceRef,
		Text: text, Words: WordCount(text), OccurredAt: occurredAt,
	}, nil
}

func transcriptCorpusFormat(content string) string {
	trimmed := strings.TrimSpace(content)
	switch {
	case strings.HasPrefix(trimmed, "WEBVTT"):
		return "vtt"
	case strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{"):
		return "json"
	default:
		return "srt"
	}
}
