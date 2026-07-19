// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The voice-profile transport slice (B-E07.4/.5a): compose embeds
// Handlers so the generated stubs are shadowed. Mutations are
// contract-annotated human-only; the store re-gates on the
// `voice_profile` RBAC object and the row-scope clause.

import (
	"encoding/json"
	"errors"
	"net/http"

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
	voice   *VoiceStore
	meter   *Meter
	budget  BudgetPolicy
	enqueue VoiceBuildEnqueue
}

// NewHandlers wires the module's stores onto one pool; budget is the
// compose-injected seat-derived policy the usage read prices against.
func NewHandlers(pool *pgxpool.Pool, budget BudgetPolicy) Handlers {
	return Handlers{voice: NewVoiceStore(pool), meter: NewMeter(pool), budget: budget}
}

// WithVoiceBuildEnqueue wires the api role's insert-only durable job client.
// Without it build creation answers 501 while profile/source reads remain live.
func (h Handlers) WithVoiceBuildEnqueue(enqueue VoiceBuildEnqueue) Handlers {
	h.enqueue = enqueue
	return h
}

// ListVoiceProfiles implements (GET /voice-profiles).
func (h Handlers) ListVoiceProfiles(w http.ResponseWriter, r *http.Request, params crmcontracts.ListVoiceProfilesParams) {
	page, err := h.voice.ListProfiles(r.Context(), params.Cursor, params.Limit)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	data := make([]crmcontracts.VoiceProfile, 0, len(page.Items))
	for _, p := range page.Items {
		data = append(data, wireVoiceProfile(p))
	}
	resp := struct {
		Data []crmcontracts.VoiceProfile `json:"data"`
		Page crmcontracts.PageInfo       `json:"page"`
	}{Data: data, Page: crmcontracts.PageInfo{HasMore: page.HasMore}}
	if page.NextCursor != "" {
		resp.Page.NextCursor = &page.NextCursor
	}
	httperr.WriteJSON(w, http.StatusOK, resp)
}

// CreateVoiceProfile implements (POST /voice-profiles).
func (h Handlers) CreateVoiceProfile(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.CreateVoiceProfileRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := CreateVoiceProfileInput{}
	if req.Scope != nil {
		in.Scope = string(*req.Scope)
	}
	if req.PersonalityMd != nil {
		in.PersonalityMD = *req.PersonalityMd
	}
	created, err := h.voice.CreateProfile(r.Context(), in)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireVoiceProfile(created))
}

// GetVoiceProfile implements (GET /voice-profiles/{id}).
func (h Handlers) GetVoiceProfile(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	p, err := h.voice.GetProfile(r.Context(), ids.UUID(id))
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireVoiceProfile(p))
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
	updated, err := h.voice.UpdateProfile(r.Context(), ids.UUID(id), req.PersonalityMd, req.AutoLearningEnabled, ifVersion)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireVoiceProfile(updated))
}

// DeleteVoiceProfile implements (DELETE /voice-profiles/{id}): soft archive.
func (h Handlers) DeleteVoiceProfile(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if err := h.voice.ArchiveProfile(r.Context(), ids.UUID(id)); err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListVoiceCorpusSources implements (GET /voice-profiles/{id}/sources):
// the manifest + the live word/register meter.
func (h Handlers) ListVoiceCorpusSources(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
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
	}{Data: data, Summary: wireCorpusSummary(summary)})
}

// IngestVoiceCorpusSource implements (POST /voice-profiles/{id}/sources).
func (h Handlers) IngestVoiceCorpusSource(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req crmcontracts.IngestVoiceCorpusSourceRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := IngestSourceInput{
		Kind:        string(req.Kind),
		SourceLabel: req.SourceLabel,
		Content:     req.Content,
	}
	if req.Register != nil {
		in.Register = string(*req.Register)
	}
	if req.Weight != nil {
		in.Weight = float64(*req.Weight)
	}
	if req.SourceRef != nil {
		in.SourceRef = *req.SourceRef
	}
	if req.Format != nil {
		in.Format = string(*req.Format)
	}
	if req.SpeakerLabel != nil {
		in.SpeakerLabel = *req.SpeakerLabel
	}
	source, summary, err := h.voice.IngestSource(r.Context(), ids.UUID(id), in)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireSourceWithSummary(source, summary))
}

// UpdateVoiceCorpusSource implements (PATCH /voice-profiles/{id}/sources/{sourceId}).
func (h Handlers) UpdateVoiceCorpusSource(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, sourceID openapi_types.UUID) {
	var req crmcontracts.UpdateVoiceCorpusSourceRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := UpdateSourceInput{Excluded: req.Excluded, ExclusionReason: req.ExclusionReason}
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

// ClearVoiceCorpus permanently removes retained evidence and derived history
// while preserving the owner's explicit personality instructions.
func (h Handlers) ClearVoiceCorpus(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if err := h.voice.ClearCorpus(r.Context(), ids.UUID(id)); err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CreateVoiceBuild implements (POST /voice-profiles/{id}/builds).
func (h Handlers) CreateVoiceBuild(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if h.enqueue == nil {
		httperr.NotImplemented(w, r, "CreateVoiceBuild")
		return
	}
	var req crmcontracts.CreateVoiceBuildRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	build, err := h.voice.StartBuild(r.Context(), ids.UUID(id), string(req.Reason), h.enqueue)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	location := "/v1/voice-profiles/" + id.String() + "/builds/" + build.ID.String()
	w.Header().Set("Location", location)
	httperr.WriteJSON(w, http.StatusAccepted, wireVoiceBuild(build))
}

// GetVoiceBuild implements (GET /voice-profiles/{id}/builds/{buildId}).
func (h Handlers) GetVoiceBuild(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, buildID openapi_types.UUID) {
	build, err := h.voice.GetBuild(r.Context(), ids.UUID(id), ids.UUID(buildID))
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireVoiceBuild(build))
}

// ListVoiceProfileVersions implements (GET /voice-profiles/{id}/versions).
func (h Handlers) ListVoiceProfileVersions(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	versions, err := h.voice.ListVersions(r.Context(), ids.UUID(id))
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	data := make([]crmcontracts.VoiceProfileVersion, 0, len(versions))
	for _, version := range versions {
		data = append(data, wireVoiceVersion(version))
	}
	httperr.WriteJSON(w, http.StatusOK, struct {
		Data []crmcontracts.VoiceProfileVersion `json:"data"`
	}{Data: data})
}

// RollbackVoiceProfileVersion implements the append-only forward rollback.
func (h Handlers) RollbackVoiceProfileVersion(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, profileVersion int) {
	version, err := h.voice.RollbackVersion(r.Context(), ids.UUID(id), profileVersion)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireVoiceVersion(version))
}

// ListVoiceProfileDeltas implements (GET /voice-profiles/{id}/deltas).
func (h Handlers) ListVoiceProfileDeltas(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	deltas, err := h.voice.ListDeltas(r.Context(), ids.UUID(id))
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	data := make([]crmcontracts.VoiceProfileDelta, 0, len(deltas))
	for _, delta := range deltas {
		data = append(data, wireVoiceDelta(delta))
	}
	httperr.WriteJSON(w, http.StatusOK, struct {
		Data []crmcontracts.VoiceProfileDelta `json:"data"`
	}{Data: data})
}

func writeVoiceErr(w http.ResponseWriter, r *http.Request, err error) {
	var ingest *CorpusIngestError
	if errors.As(err, &ingest) {
		httperr.Write(w, r, httperr.Validation(ingest.Field, "invalid", ingest.Reason))
		return
	}
	httperr.Write(w, r, err)
}

func wireVoiceProfile(p VoiceProfile) crmcontracts.VoiceProfile {
	version := int(p.Version)
	wire := crmcontracts.VoiceProfile{
		Id:                  openapi_types.UUID(p.ID),
		Scope:               crmcontracts.VoiceProfileScope(p.Scope),
		Status:              crmcontracts.VoiceProfileStatus(p.Status),
		VoiceProfileMd:      p.VoiceProfileMD,
		ProfileVersion:      p.ProfileVersion,
		PersonalityMd:       p.PersonalityMD,
		AutoLearningEnabled: p.AutoLearning,
		ActiveSourceHash:    p.ActiveSourceHash,
		LastBuiltAt:         p.LastBuiltAt,
		ModelRef:            p.ModelRef,
		Version:             &version,
		CreatedAt:           p.CreatedAt,
		UpdatedAt:           p.UpdatedAt,
	}
	if p.OwnerID != nil {
		owner := openapi_types.UUID(p.OwnerID.UUID)
		wire.OwnerId = &owner
	}
	return wire
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
		Excluded:         s.Excluded,
		Origin:           crmcontracts.VoiceCorpusSourceOrigin(s.Origin),
		ExclusionReason:  s.ExclusionReason,
		ExtractorVersion: s.ExtractorVersion,
		OccurredAt:       s.OccurredAt,
		CreatedAt:        s.CreatedAt,
		UpdatedAt:        s.UpdatedAt,
	}
}

func wireVoiceBuild(build VoiceBuild) crmcontracts.VoiceBuild {
	return crmcontracts.VoiceBuild{
		Id: openapi_types.UUID(build.ID), VoiceProfileId: openapi_types.UUID(build.ProfileID),
		Reason: crmcontracts.VoiceBuildReason(build.Reason), Status: crmcontracts.VoiceBuildStatus(build.Status),
		Stage: build.Stage, SourceHash: build.SourceHash, SourceWordCount: build.SourceWordCount,
		ResultVersion: build.ResultVersion, FailureDetail: build.FailureDetail, CreatedAt: build.CreatedAt,
		StartedAt: build.StartedAt, FinishedAt: build.FinishedAt,
	}
}

func wireVoiceVersion(version VoiceProfileVersion) crmcontracts.VoiceProfileVersion {
	profile := map[string]any{}
	stats := map[string]any{}
	if err := json.Unmarshal(version.ProfileJSON, &profile); err != nil {
		panic(err)
	}
	if err := json.Unmarshal(version.StatsJSON, &stats); err != nil {
		panic(err)
	}
	return crmcontracts.VoiceProfileVersion{
		Id: openapi_types.UUID(version.ID), VoiceProfileId: openapi_types.UUID(version.ProfileID),
		ProfileVersion: version.ProfileVersion, VoiceProfileMd: version.VoiceProfileMD,
		Profile: profile, Stats: stats, ModelRef: version.ModelRef, BuilderVersion: version.BuilderVersion,
		SourceHash: version.SourceHash, SourceWordCount: version.SourceWordCount,
		Reason: crmcontracts.VoiceProfileVersionReason(version.Reason), PredecessorVersion: version.PredecessorVersion,
		Active: version.Active, CreatedAt: version.CreatedAt,
	}
}

func wireVoiceDelta(delta VoiceProfileDelta) crmcontracts.VoiceProfileDelta {
	summary := map[string]any{}
	if err := json.Unmarshal(delta.Summary, &summary); err != nil {
		panic(err)
	}
	return crmcontracts.VoiceProfileDelta{
		Id: openapi_types.UUID(delta.ID), VoiceProfileId: openapi_types.UUID(delta.ProfileID),
		FromVersion: delta.FromVersion, ToVersion: delta.ToVersion, Summary: summary, CreatedAt: delta.CreatedAt,
	}
}

func wireCorpusSummary(sum CorpusSummary) crmcontracts.VoiceCorpusSummary {
	return crmcontracts.VoiceCorpusSummary{
		TotalWords:    sum.TotalWords,
		TargetWords:   sum.TargetWords,
		QualityBand:   crmcontracts.VoiceCorpusSummaryQualityBand(sum.QualityBand),
		RegisterWords: sum.RegisterWords,
		SourceCount:   sum.SourceCount,
	}
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
