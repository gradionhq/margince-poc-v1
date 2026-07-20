// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"encoding/json"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ClearVoiceCorpus implements (DELETE /voice-profiles/{id}/corpus).
func (h Handlers) ClearVoiceCorpus(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.ClearVoiceCorpusParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	cleared, err := h.voice.ClearCorpus(r.Context(), ids.UUID(id), ifVersion)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	h.writeVoiceProfile(w, r, http.StatusOK, cleared)
}

// CreateVoiceBuild implements (POST /voice-profiles/{id}/builds).
func (h Handlers) CreateVoiceBuild(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.CreateVoiceBuildParams) {
	var req crmcontracts.CreateVoiceBuildRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	build, err := h.voice.CreateBuild(r.Context(), ids.UUID(id), CreateVoiceBuildInput{Reason: string(req.Reason)})
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
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
func (h Handlers) ListVoiceProfileVersions(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.ListVoiceProfileVersionsParams) {
	page, err := h.voice.ListVersions(r.Context(), ids.UUID(id), params.Cursor, params.Limit)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	data := make([]crmcontracts.VoiceProfileVersion, 0, len(page.Items))
	for _, item := range page.Items {
		wire, err := wireVoiceVersion(item)
		if err != nil {
			writeVoiceErr(w, r, err)
			return
		}
		data = append(data, wire)
	}
	info := crmcontracts.PageInfo{HasMore: page.HasMore}
	if page.NextCursor != "" {
		info.NextCursor = &page.NextCursor
	}
	httperr.WriteJSON(w, http.StatusOK, struct {
		Data []crmcontracts.VoiceProfileVersion `json:"data"`
		Page crmcontracts.PageInfo              `json:"page"`
	}{Data: data, Page: info})
}

// ApplyVoiceProfileVersion implements (POST /voice-profiles/{id}/versions/{profileVersion}/apply).
func (h Handlers) ApplyVoiceProfileVersion(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, profileVersion crmcontracts.VoiceProfileVersionNumber, _ crmcontracts.ApplyVoiceProfileVersionParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	version, err := h.voice.ApplyVersion(r.Context(), ids.UUID(id), profileVersion, ifVersion)
	h.writeVoiceVersion(w, r, version, err)
}

// RejectVoiceProfileVersion implements (POST /voice-profiles/{id}/versions/{profileVersion}/reject).
func (h Handlers) RejectVoiceProfileVersion(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, profileVersion crmcontracts.VoiceProfileVersionNumber, _ crmcontracts.RejectVoiceProfileVersionParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	version, err := h.voice.RejectVersion(r.Context(), ids.UUID(id), profileVersion, ifVersion)
	h.writeVoiceVersion(w, r, version, err)
}

// RollbackVoiceProfileVersion implements (POST /voice-profiles/{id}/versions/{profileVersion}/rollback).
func (h Handlers) RollbackVoiceProfileVersion(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, profileVersion crmcontracts.VoiceProfileVersionNumber, _ crmcontracts.RollbackVoiceProfileVersionParams) {
	version, err := h.voice.RollbackVersion(r.Context(), ids.UUID(id), profileVersion)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	wire, err := wireVoiceVersion(version)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wire)
}

// ListVoiceProfileDeltas implements (GET /voice-profiles/{id}/deltas).
func (h Handlers) ListVoiceProfileDeltas(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.ListVoiceProfileDeltasParams) {
	page, err := h.voice.ListDeltas(r.Context(), ids.UUID(id), params.Cursor, params.Limit)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	data := make([]crmcontracts.VoiceProfileDelta, 0, len(page.Items))
	for _, item := range page.Items {
		data = append(data, wireVoiceDelta(item))
	}
	info := crmcontracts.PageInfo{HasMore: page.HasMore}
	if page.NextCursor != "" {
		info.NextCursor = &page.NextCursor
	}
	httperr.WriteJSON(w, http.StatusOK, struct {
		Data []crmcontracts.VoiceProfileDelta `json:"data"`
		Page crmcontracts.PageInfo            `json:"page"`
	}{Data: data, Page: info})
}

// GetVoiceLearningSummary implements (GET /voice-profiles/{id}/learning-summary).
func (h Handlers) GetVoiceLearningSummary(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	summary, err := h.voice.LearningSummary(r.Context(), ids.UUID(id))
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireLearningSummary(summary))
}

// RejectVoiceDraft implements (POST /voice-profiles/{id}/learning/drafts/reject).
func (h Handlers) RejectVoiceDraft(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.RejectVoiceDraftParams) {
	var req crmcontracts.RejectVoiceDraftRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	summary, err := h.voice.RejectDraft(r.Context(), ids.UUID(id), req.DraftRef)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireLearningSummary(summary))
}

func (h Handlers) writeVoiceVersion(w http.ResponseWriter, r *http.Request, version VoiceProfileVersion, err error) {
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	wire, err := wireVoiceVersion(version)
	if err != nil {
		writeVoiceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wire)
}

func wireVoiceBuild(build VoiceBuild) crmcontracts.VoiceBuild {
	var stage *crmcontracts.VoiceBuildStage
	if build.Stage != nil {
		value := crmcontracts.VoiceBuildStage(*build.Stage)
		stage = &value
	}
	var statusCode *crmcontracts.VoiceBuildStatusCode
	if build.StatusCode != nil {
		value := crmcontracts.VoiceBuildStatusCode(*build.StatusCode)
		statusCode = &value
	}
	return crmcontracts.VoiceBuild{
		Id: openapi_types.UUID(build.ID), ProfileId: openapi_types.UUID(build.ProfileID),
		Reason: crmcontracts.VoiceBuildReason(build.Reason), Status: crmcontracts.VoiceBuildStatus(build.Status),
		Stage: stage, SourceHash: build.SourceHash, SourceCount: build.SourceCount,
		ResultVersion:   build.ResultVersion,
		CandidateAction: crmcontracts.VoiceBuildCandidateAction(build.CandidateAction),
		StatusCode:      statusCode, StatusDetail: build.StatusDetail, NextAttemptAt: build.NextAttemptAt,
		Version: int(build.Version), CreatedAt: build.CreatedAt, StartedAt: build.StartedAt,
		CompletedAt: build.CompletedAt, UpdatedAt: updatedAt(build.CreatedAt, build.UpdatedAt),
		ArchivedAt: build.ArchivedAt,
	}
}

func wireVoiceVersion(version VoiceProfileVersion) (crmcontracts.VoiceProfileVersion, error) {
	raw, err := json.Marshal(version.Evaluation)
	if err != nil {
		return crmcontracts.VoiceProfileVersion{}, err
	}
	var evaluation crmcontracts.VoiceProfileEvaluation
	if err := json.Unmarshal(raw, &evaluation); err != nil {
		return crmcontracts.VoiceProfileVersion{}, err
	}
	return crmcontracts.VoiceProfileVersion{
		Id: openapi_types.UUID(version.ID), ProfileId: openapi_types.UUID(version.ProfileID),
		ProfileVersion: version.ProfileVersion, Status: crmcontracts.VoiceProfileVersionStatus(version.Status),
		VoiceProfileMd: version.VoiceProfileMD, ProfileJson: version.ProfileJSON, StatsJson: version.StatsJSON,
		SourceHash: version.SourceHash, SourceCount: version.SourceCount,
		Reason: crmcontracts.VoiceProfileVersionReason(version.Reason), PredecessorVersion: version.PredecessorVersion,
		ModelProvider: version.ModelProvider, ModelName: version.ModelName, BuilderVersion: version.BuilderVersion,
		ActivationPolicyVersion: version.ActivationPolicyVersion, Evaluation: evaluation,
		ReviewReasons: version.ReviewReasons, Source: version.Source, CapturedBy: &version.CapturedBy,
		Version: int(version.Version), CreatedAt: version.CreatedAt,
		UpdatedAt: updatedAt(version.CreatedAt, version.UpdatedAt), ArchivedAt: version.ArchivedAt,
		ActivatedAt: version.ActivatedAt,
	}, nil
}

func wireVoiceDelta(delta VoiceProfileDelta) crmcontracts.VoiceProfileDelta {
	return crmcontracts.VoiceProfileDelta{
		Id: openapi_types.UUID(delta.ID), ProfileId: openapi_types.UUID(delta.ProfileID),
		FromVersion: delta.FromVersion, ToVersion: delta.ToVersion,
		Classification:    crmcontracts.VoiceProfileDeltaClassification(delta.Classification),
		ActivationOutcome: crmcontracts.VoiceProfileDeltaActivationOutcome(delta.ActivationOutcome),
		WordsAdded:        delta.WordsAdded, SourcesAdded: delta.SourcesAdded, SourcesExcluded: delta.SourcesExcluded,
		IdentityWordJaccard: float64To32(delta.IdentityWordJaccard),
		SignatureSetJaccard: float64To32(delta.SignatureSetJaccard),
		AvoidRulesAdded:     delta.AvoidRulesAdded, AvoidRulesRemoved: delta.AvoidRulesRemoved,
		RegisterRulesRemoved: delta.RegisterRulesRemoved, CreatedAt: delta.CreatedAt,
		UpdatedAt: updatedAt(delta.CreatedAt, delta.UpdatedAt), ArchivedAt: delta.ArchivedAt,
	}
}

func float64To32(value *float64) *float32 {
	if value == nil {
		return nil
	}
	converted := float32(*value)
	return &converted
}

func wireLearningSummary(summary VoiceLearningSummary) crmcontracts.VoiceLearningSummary {
	transformations := make([]crmcontracts.VoiceLearningTransformation, 0, len(summary.Transformations))
	for _, transformation := range summary.Transformations {
		transformations = append(transformations, crmcontracts.VoiceLearningTransformation{
			Key: transformation.Key, ObservationCount: transformation.ObservationCount,
			Description: transformation.Description,
		})
	}
	return crmcontracts.VoiceLearningSummary{
		Drafted: summary.Drafted, Accepted: summary.Accepted, EditedSent: summary.EditedSent,
		Rejected: summary.Rejected, QualifyingSourceCount: summary.QualifyingSourceCount,
		QualifyingWords: summary.QualifyingWords, Transformations: transformations,
	}
}
