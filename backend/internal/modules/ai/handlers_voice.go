// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The voice-profile transport slice (B-E07.4/.5a): compose embeds
// Handlers so the generated stubs are shadowed. Mutations are
// contract-annotated human-only; the store re-gates on the
// `voice_profile` RBAC object and the row-scope clause.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Handlers is the ai module's HTTP surface: the voice-profile slice and
// the AIRT-WIRE-1 usage read (the model runtime itself has no direct
// transport). The BudgetPolicy is compose-injected — the seat-derived
// pool joins identity's tables, which this module never reaches.
type Handlers struct {
	voice           *VoiceStore
	meter           *Meter
	budget          BudgetPolicy
	calls           *CallReadStore
	capturePayloads bool
	// rates is the ADR-0067 price sheet the usage read prices ai_call
	// against at read time (price-on-read) — same pool, RLS scoped like
	// every other tenant read.
	rates *RateStore
	// publicProfile is the minimal anonymous login-presence view. NewHandlers
	// starts honest-unconfigured; the API composition root replaces it from
	// the same routing decision that builds the model path.
	publicProfile PublicProfile
}

// NewHandlers wires the module's stores onto one pool; budget is the
// compose-injected seat-derived policy the usage read prices against.
// rates is the ADR-0067 price sheet the usage read prices ai_call
// against at read time (price-on-read) — the same pool, RLS scoped like
// every other tenant read.
func NewHandlers(pool *pgxpool.Pool, budget BudgetPolicy) Handlers {
	return Handlers{
		voice: NewVoiceStore(pool), meter: NewMeter(pool), budget: budget,
		calls: NewCallReadStore(pool), rates: NewRateStore(pool),
		publicProfile: NewPublicProfile("unconfigured", RoutingConfig{}),
	}
}

// ListVoiceProfiles implements (GET /voice-profiles).
func (h Handlers) ListVoiceProfiles(w http.ResponseWriter, r *http.Request) {
	page, err := h.voice.ListProfiles(r.Context(), nil, nil)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	data := make([]crmcontracts.VoiceProfile, 0, len(page.Items))
	for _, p := range page.Items {
		wire, err := h.wireVoiceProfile(r.Context(), p)
		if err != nil {
			writeVoiceErr(w, r, err)
			return
		}
		data = append(data, wire)
	}
	resp := struct {
		Data []crmcontracts.VoiceProfile `json:"data"`
	}{Data: data}
	httperr.WriteJSON(w, http.StatusOK, resp)
}

// CreateVoiceProfile implements (POST /voice-profiles).
func (h Handlers) CreateVoiceProfile(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateVoiceProfileParams) {
	var req crmcontracts.CreateVoiceProfileRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := CreateVoiceProfileInput{}
	if req.PersonalityMd != nil {
		in.PersonalityMD = *req.PersonalityMd
	}
	created, err := h.voice.CreateProfile(r.Context(), in)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	h.writeVoiceProfile(w, r, http.StatusCreated, created)
}

// GetVoiceProfile implements (GET /voice-profiles/{id}).
func (h Handlers) GetVoiceProfile(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	p, err := h.voice.GetProfile(r.Context(), ids.UUID(id))
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	h.writeVoiceProfile(w, r, http.StatusOK, p)
}

// UpdateVoiceProfile implements (PATCH /voice-profiles/{id}): the
// human-authored personality_md only, under If-Match.
func (h Handlers) UpdateVoiceProfile(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateVoiceProfileParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateVoiceProfileRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	updated, err := h.voice.UpdateProfile(r.Context(), ids.UUID(id), UpdateVoiceProfileInput{
		PersonalityMD: req.PersonalityMd, AutoLearningEnabled: req.AutoLearningEnabled, IfVersion: ifVersion,
	})
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	h.writeVoiceProfile(w, r, http.StatusOK, updated)
}

// DeleteVoiceProfile implements (DELETE /voice-profiles/{id}): soft archive.
func (h Handlers) DeleteVoiceProfile(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.DeleteVoiceProfileParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	archived, err := h.voice.ArchiveProfile(r.Context(), ids.UUID(id), ifVersion)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	h.writeVoiceProfile(w, r, http.StatusOK, archived)
}

// ListVoiceCorpusSources implements (GET /voice-profiles/{id}/sources):
// the manifest + the live word/register meter.
func (h Handlers) ListVoiceCorpusSources(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.ListVoiceCorpusSourcesParams) {
	sources, summary, err := h.voice.ListSources(r.Context(), ids.UUID(id))
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	data := make([]crmcontracts.VoiceCorpusSource, 0, len(sources))
	for _, src := range sources {
		data = append(data, wireVoiceSource(src))
	}
	httperr.WriteJSON(w, http.StatusOK, struct {
		Data    []crmcontracts.VoiceCorpusSource `json:"data"`
		Summary crmcontracts.VoiceCorpusSummary  `json:"summary"`
		Page    crmcontracts.PageInfo            `json:"page"`
	}{Data: data, Summary: wireCorpusSummary(summary), Page: crmcontracts.PageInfo{HasMore: false}})
}

// IngestVoiceCorpusSource implements (POST /voice-profiles/{id}/sources).
func (h Handlers) IngestVoiceCorpusSource(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.IngestVoiceCorpusSourceParams) {
	var req crmcontracts.IngestVoiceCorpusSourceRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := IngestSourceInput{
		Kind:        string(req.Kind),
		SourceLabel: req.SourceLabel,
		Register:    string(req.Register),
		SourceRef:   req.SourceRef,
		Format:      string(req.Format),
	}
	if req.Content != nil {
		in.Content = *req.Content
	}
	if req.Weight != nil {
		in.Weight = float64(*req.Weight)
	}
	if req.SpeakerLabel != nil {
		in.SpeakerLabel = *req.SpeakerLabel
	}
	in.OccurredAt = req.OccurredAt
	source, summary, err := h.voice.IngestSource(r.Context(), ids.UUID(id), in)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireSourceWithSummary(source, summary))
}

// UpdateVoiceCorpusSource implements (PATCH /voice-profiles/{id}/sources/{sourceId}).
func (h Handlers) UpdateVoiceCorpusSource(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, sourceID openapi_types.UUID, _ crmcontracts.UpdateVoiceCorpusSourceParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateVoiceCorpusSourceRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := UpdateSourceInput{Included: req.Included, IfVersion: ifVersion}
	if req.Weight != nil {
		weight := float64(*req.Weight)
		in.Weight = &weight
	}
	source, summary, err := h.voice.UpdateSource(r.Context(), ids.UUID(id), ids.UUID(sourceID), in)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireSourceWithSummary(source, summary))
}

// DeleteVoiceCorpusSource implements (DELETE /voice-profiles/{id}/sources/{sourceId}).
func (h Handlers) DeleteVoiceCorpusSource(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, sourceID openapi_types.UUID, _ crmcontracts.DeleteVoiceCorpusSourceParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	removed, err := h.voice.DeleteSource(r.Context(), ids.UUID(id), ids.UUID(sourceID), ifVersion)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireVoiceSource(removed))
}

func writeVoiceErr(w http.ResponseWriter, r *http.Request, err error) {
	var ingest *CorpusIngestError
	if errors.As(err, &ingest) {
		httperr.Write(w, r, httperr.Validation(ingest.Field, "invalid", ingest.Reason))
		return
	}
	httperr.Write(w, r, err)
}

func (h Handlers) wireVoiceProfile(ctx context.Context, p VoiceProfile) (crmcontracts.VoiceProfile, error) {
	summary, candidateVersion, err := h.voice.ProfilePresentation(ctx, p.ID)
	if err != nil {
		return crmcontracts.VoiceProfile{}, err
	}
	version := int(p.Version)
	profileVersion := p.ProfileVersion
	voiceProfileMD := p.VoiceProfileMD
	maturity := crmcontracts.VoiceProfileMaturity(summary.Maturity)
	qualityBand := crmcontracts.VoiceProfileQualityBand(summary.QualityBand)
	updatedAt := p.CreatedAt
	if p.UpdatedAt != nil {
		updatedAt = *p.UpdatedAt
	}
	wire := crmcontracts.VoiceProfile{
		Id:                  openapi_types.UUID(p.ID),
		Status:              crmcontracts.VoiceProfileStatus(p.Status),
		VoiceProfileMd:      &voiceProfileMD,
		ProfileVersion:      &profileVersion,
		PersonalityMd:       p.PersonalityMD,
		AutoLearningEnabled: p.AutoLearningEnabled,
		ActiveSourceHash:    p.ActiveSourceHash,
		CandidateVersion:    candidateVersion,
		LastBuiltAt:         p.LastBuiltAt,
		Maturity:            &maturity,
		QualityBand:         &qualityBand,
		Source:              p.Source,
		CapturedBy:          &p.CapturedBy,
		Version:             version,
		CreatedAt:           p.CreatedAt,
		UpdatedAt:           updatedAt,
		ArchivedAt:          p.ArchivedAt,
	}
	if p.OwnerID != nil {
		owner := openapi_types.UUID(p.OwnerID.UUID)
		wire.OwnerId = &owner
	}
	return wire, nil
}

func (h Handlers) writeVoiceProfile(w http.ResponseWriter, r *http.Request, status int, p VoiceProfile) {
	wire, err := h.wireVoiceProfile(r.Context(), p)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, status, wire)
}

func wireVoiceSource(s VoiceCorpusSource) crmcontracts.VoiceCorpusSource {
	return crmcontracts.VoiceCorpusSource{
		Id:               openapi_types.UUID(s.ID),
		Kind:             crmcontracts.VoiceCorpusSourceKind(s.Kind),
		Register:         crmcontracts.VoiceCorpusSourceRegister(s.Register),
		Weight:           float32(s.Weight),
		SourceLabel:      s.SourceLabel,
		SourceRef:        s.SourceRef,
		WordCount:        s.WordCount,
		Origin:           crmcontracts.VoiceCorpusSourceOrigin(s.Origin),
		Included:         !s.Excluded,
		ExclusionReason:  s.ExclusionReason,
		ExtractorVersion: s.ExtractorVersion,
		OccurredAt:       s.OccurredAt,
		RetentionUntil:   s.RetentionUntil,
		ContentErasedAt:  s.ContentErasedAt,
		Source:           s.Source,
		CapturedBy:       &s.CapturedBy,
		Version:          int(s.Version),
		CreatedAt:        s.CreatedAt,
		UpdatedAt:        updatedAt(s.CreatedAt, s.UpdatedAt),
		ArchivedAt:       s.ArchivedAt,
	}
}

func wireCorpusSummary(sum CorpusSummary) crmcontracts.VoiceCorpusSummary {
	return crmcontracts.VoiceCorpusSummary{
		TotalWords:    sum.TotalWords,
		TargetWords:   sum.TargetWords,
		QualityBand:   crmcontracts.VoiceCorpusSummaryQualityBand(sum.QualityBand),
		Maturity:      crmcontracts.VoiceCorpusSummaryMaturity(sum.Maturity),
		RegisterWords: sum.RegisterWords,
		SourceCount:   sum.SourceCount,
	}
}

func updatedAt(created time.Time, updated *time.Time) time.Time {
	if updated != nil {
		return *updated
	}
	return created
}

// sourceWithSummary is the ingest/patch response pair: the touched
// manifest row plus the refreshed meter, so the funnel updates its bar
// without a second round trip.
type sourceWithSummary struct {
	Source  crmcontracts.VoiceCorpusSource  `json:"source"`
	Summary crmcontracts.VoiceCorpusSummary `json:"summary"`
}

func wireSourceWithSummary(source VoiceCorpusSource, summary CorpusSummary) sourceWithSummary {
	return sourceWithSummary{Source: wireVoiceSource(source), Summary: wireCorpusSummary(summary)}
}
