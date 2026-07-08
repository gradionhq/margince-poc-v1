// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The corpus-source half of the voice store (B-E07.5a): the manifest
// rows under a profile, the idempotent-per-source_ref ingest write, and
// the live word/register meter. The profile half (artifact + versioned
// rebuild) lives in voice.go; both share VoiceStore and the row-scoped
// visibleProfile gate.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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
	ID          ids.UUID
	ProfileID   ids.UUID
	Kind        string
	Register    string
	Weight      float64
	SourceLabel string
	SourceRef   string
	WordCount   int
	Excluded    bool
	CreatedAt   time.Time
	UpdatedAt   *time.Time
}

// CorpusSummary is the live word-count + register-mix meter over the
// non-excluded manifest (features/09 §B1.4).
type CorpusSummary struct {
	TotalWords    int
	TargetWords   int
	QualityBand   string
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
}

// UpdateSourceInput carries the manifest PATCH subset; nil = unchanged.
type UpdateSourceInput struct {
	Excluded *bool
	Weight   *float64
}

const voiceSourceColumns = `id, voice_profile_id, kind, register, weight, source_label, source_ref, word_count, excluded, created_at, updated_at`

func scanVoiceSource(row pgx.Row) (VoiceCorpusSource, error) {
	var s VoiceCorpusSource
	err := row.Scan(&s.ID, &s.ProfileID, &s.Kind, &s.Register, &s.Weight, &s.SourceLabel,
		&s.SourceRef, &s.WordCount, &s.Excluded, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

// preparedSource is one validated, normalized, speaker-filtered source,
// ready to persist.
type preparedSource struct {
	Kind      string
	Register  string
	Weight    float64
	Label     string
	SourceRef string
	Text      string
	Words     int
}

// prepareSource runs the pure half of the §B1 pipeline: field
// validation, per-kind register defaulting, format normalization with
// the speaker filter, word counting, and the content-hash fallback ref.
func prepareSource(in IngestSourceInput) (preparedSource, error) {
	switch in.Kind {
	case "post", "transcript", "email", "chat", "longform", "voice_memo":
	default:
		return preparedSource{}, &CorpusIngestError{Field: "kind", Reason: "must be one of post, transcript, email, chat, longform, voice_memo"}
	}
	register := in.Register
	if register == "" {
		register = DefaultRegister(in.Kind)
	}
	switch register {
	case "spoken", "written", "casual", "formal":
	default:
		return preparedSource{}, &CorpusIngestError{Field: "register", Reason: "must be one of spoken, written, casual, formal"}
	}
	weight := in.Weight
	if weight == 0 {
		weight = 1.0
	}
	if weight < 0.1 || weight > 5.0 {
		return preparedSource{}, &CorpusIngestError{Field: "weight", Reason: "must be between 0.1 and 5.0"}
	}
	if strings.TrimSpace(in.SourceLabel) == "" {
		return preparedSource{}, &CorpusIngestError{Field: "source_label", Reason: "must not be empty"}
	}
	if strings.TrimSpace(in.Content) == "" {
		return preparedSource{}, &CorpusIngestError{Field: "content", Reason: "must not be empty"}
	}
	if len(in.Content) > maxCorpusSourceBytes {
		return preparedSource{}, &CorpusIngestError{Field: "content", Reason: "one source is capped at 1 MiB of text — split the upload"}
	}
	format := in.Format
	if format == "" {
		format = "txt"
	}
	// Conversational kinds MUST arrive in a speaker-attributed format:
	// the §B1.2 filter is what keeps a counterparty's words out of the
	// corpus, and a plain-text conversation would walk straight past it —
	// the Art. 17 posture of this table rests on this refusal.
	if conversationalKinds[in.Kind] && (format == "txt" || format == "md") {
		return preparedSource{}, &CorpusIngestError{
			Field:  "format",
			Reason: "a " + in.Kind + " source must be a speaker-attributed transcript (vtt, srt, or json) so only the owner's own words are modeled",
		}
	}
	text, err := NormalizeCorpusText(format, in.Content, in.SpeakerLabel, conversationalKinds[in.Kind])
	if err != nil {
		return preparedSource{}, err
	}
	if conversationalKinds[in.Kind] && strings.TrimSpace(text) == "" {
		return preparedSource{}, &CorpusIngestError{
			Field:  "speaker_label",
			Reason: "no turns belong to this speaker label — nothing of the owner's own words to ingest",
		}
	}
	sourceRef := in.SourceRef
	if sourceRef == "" {
		sourceRef = SourceRefForContent(in.Content)
	}
	return preparedSource{
		Kind: in.Kind, Register: register, Weight: weight,
		Label: in.SourceLabel, SourceRef: sourceRef,
		Text: text, Words: WordCount(text),
	}, nil
}

// IngestSource runs the §B1 pipeline (normalize → speaker-filter →
// register-tag → count) and upserts the manifest row by source_ref:
// re-ingesting a source replaces it — the meter never double-counts —
// and re-adding an excluded source is an explicit opt back in.
func (s *VoiceStore) IngestSource(ctx context.Context, profileID ids.UUID, in IngestSourceInput) (VoiceCorpusSource, CorpusSummary, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	prepared, err := prepareSource(in)
	if err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}

	var (
		source  VoiceCorpusSource
		summary CorpusSummary
	)
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := s.visibleProfile(ctx, tx, profileID)
		if err != nil {
			return err
		}
		if err := ownerOnly(ctx, p); err != nil {
			return err
		}
		var inserted bool
		row := tx.QueryRow(ctx, storekit.SQLf(`
			INSERT INTO voice_corpus_source
			  (workspace_id, voice_profile_id, kind, register, weight, source_label, source_ref, content, word_count)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (workspace_id, voice_profile_id, source_ref) DO UPDATE SET
			  kind = EXCLUDED.kind,
			  register = EXCLUDED.register,
			  weight = EXCLUDED.weight,
			  source_label = EXCLUDED.source_label,
			  content = EXCLUDED.content,
			  word_count = EXCLUDED.word_count,
			  excluded = false,
			  updated_at = $9
			RETURNING %s, (xmax = 0)`, voiceSourceColumns),
			profileID, prepared.Kind, prepared.Register, prepared.Weight, prepared.Label,
			prepared.SourceRef, prepared.Text, prepared.Words, s.now().UTC())
		if err := row.Scan(&source.ID, &source.ProfileID, &source.Kind, &source.Register, &source.Weight,
			&source.SourceLabel, &source.SourceRef, &source.WordCount, &source.Excluded,
			&source.CreatedAt, &source.UpdatedAt, &inserted); err != nil {
			return err
		}
		action := "create"
		if !inserted {
			action = "update"
		}
		if _, err := storekit.Audit(ctx, tx, action, "voice_corpus_source", source.ID, nil, map[string]any{
			"voice_profile_id": profileID, "kind": source.Kind, "register": source.Register,
			"source_ref": source.SourceRef, "word_count": source.WordCount,
		}); err != nil {
			return err
		}
		summary, err = corpusSummary(ctx, tx, profileID)
		return err
	})
	if err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	return source, summary, nil
}

// ListSources returns the corpus manifest + the live meter for one
// visible profile (features/09 §B1.4 — the onboarding/voice.html read).
func (s *VoiceStore) ListSources(ctx context.Context, profileID ids.UUID) ([]VoiceCorpusSource, CorpusSummary, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionRead); err != nil {
		return nil, CorpusSummary{}, err
	}
	var (
		sources []VoiceCorpusSource
		summary CorpusSummary
	)
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := s.visibleProfile(ctx, tx, profileID)
		if err != nil {
			return err
		}
		if err := ownerOnly(ctx, p); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, storekit.SQLf(
			`SELECT %s FROM voice_corpus_source WHERE voice_profile_id = $1 ORDER BY created_at DESC, id DESC`,
			voiceSourceColumns), profileID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			src, err := scanVoiceSource(rows)
			if err != nil {
				return err
			}
			sources = append(sources, src)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		summary, err = corpusSummary(ctx, tx, profileID)
		return err
	})
	if err != nil {
		return nil, CorpusSummary{}, err
	}
	return sources, summary, nil
}

// UpdateSource flips a manifest row's opt-out or weight.
func (s *VoiceStore) UpdateSource(ctx context.Context, profileID, sourceID ids.UUID, in UpdateSourceInput) (VoiceCorpusSource, CorpusSummary, error) {
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	if in.Weight != nil && (*in.Weight < 0.1 || *in.Weight > 5.0) {
		return VoiceCorpusSource{}, CorpusSummary{}, &CorpusIngestError{Field: "weight", Reason: "must be between 0.1 and 5.0"}
	}
	var (
		source  VoiceCorpusSource
		summary CorpusSummary
	)
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := s.visibleProfile(ctx, tx, profileID)
		if err != nil {
			return err
		}
		if err := ownerOnly(ctx, p); err != nil {
			return err
		}
		before, err := scanVoiceSource(tx.QueryRow(ctx, storekit.SQLf(
			`SELECT %s FROM voice_corpus_source WHERE id = $1 AND voice_profile_id = $2`,
			voiceSourceColumns), sourceID, profileID))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		source, err = scanVoiceSource(tx.QueryRow(ctx, storekit.SQLf(`
			UPDATE voice_corpus_source SET
			  excluded = coalesce($3, excluded),
			  weight = coalesce($4, weight),
			  updated_at = $5
			WHERE id = $1 AND voice_profile_id = $2
			RETURNING %s`, voiceSourceColumns),
			sourceID, profileID, in.Excluded, in.Weight, s.now().UTC()))
		if err != nil {
			return err
		}
		if _, err := storekit.Audit(ctx, tx, "update", "voice_corpus_source", sourceID,
			map[string]any{"excluded": before.Excluded, "weight": before.Weight},
			map[string]any{"excluded": source.Excluded, "weight": source.Weight}); err != nil {
			return err
		}
		summary, err = corpusSummary(ctx, tx, profileID)
		return err
	})
	if err != nil {
		return VoiceCorpusSource{}, CorpusSummary{}, err
	}
	return source, summary, nil
}

// corpusSummary computes the meter over the non-excluded manifest.
func corpusSummary(ctx context.Context, tx pgx.Tx, profileID ids.UUID) (CorpusSummary, error) {
	summary := CorpusSummary{
		TargetWords:   CorpusTargetWords,
		RegisterWords: map[string]int{},
	}
	rows, err := tx.Query(ctx, `
		SELECT register, sum(word_count)::int, count(*)::int
		FROM voice_corpus_source
		WHERE voice_profile_id = $1 AND NOT excluded
		GROUP BY register`, profileID)
	if err != nil {
		return CorpusSummary{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			register string
			words    int
			count    int
		)
		if err := rows.Scan(&register, &words, &count); err != nil {
			return CorpusSummary{}, err
		}
		summary.RegisterWords[register] = words
		summary.TotalWords += words
		summary.SourceCount += count
	}
	if err := rows.Err(); err != nil {
		return CorpusSummary{}, err
	}
	summary.QualityBand = QualityBand(summary.TotalWords)
	return summary, nil
}
