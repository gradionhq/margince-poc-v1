// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package briefs

// The Morning-Brief HTTP surface (E05): the home read (GetMorningBrief),
// the on-open/explicit refresh (GenerateMorningBrief), and the per-rep
// acted/dismissed marks (B-E05.13). It shadows the generated stubs over
// the BriefEngine. The brief is a PERSONAL lens — every operation is
// scoped to the acting rep by the engine, and another rep's item reads as
// not-found (existence-hiding), never forbidden.

import (
	"log/slog"

	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Handlers wires the brief transport to the engine.
type Handlers struct {
	engine *BriefEngine
}

// NewHandlers binds the transport to a ready engine; compose constructs
// it once per process role.
func NewHandlers(engine *BriefEngine) Handlers { return Handlers{engine: engine} }

// WithL2Ranker forwards the api role's model lane to the engine — the
// deterministic §10.1 floor serves either way.
func (h Handlers) WithL2Ranker(brain briefBrain, log *slog.Logger) {
	h.engine.WithL2Ranker(brain, log)
}

// GetMorningBrief re-reads the acting rep's latest persisted run — the
// on-open path that never re-ranks (B-E05.3b). No run yet is a 404, the
// same existence-hiding shape as any absent personal resource.
func (h Handlers) GetMorningBrief(w http.ResponseWriter, r *http.Request) {
	run, err := h.engine.LatestRun(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, briefRunToWire(run))
}

// GenerateMorningBrief ranks the rep's open deals (§10.1 composite + the
// L2 re-order) and persists a fresh run. It reads and stages only — no
// deal field mutates and nothing is sent.
func (h Handlers) GenerateMorningBrief(w http.ResponseWriter, r *http.Request) {
	run, err := h.engine.SnapshotRun(r.Context(), time.Now().UTC())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, briefRunToWire(run))
}

// MarkBriefItemActed records that the rep acted on a queue item; the next
// run drops the deal until it materially changes (B-E05.13).
func (h Handlers) MarkBriefItemActed(w http.ResponseWriter, r *http.Request, itemID openapi_types.UUID) {
	item, err := h.engine.MarkActed(r.Context(), ids.UUID(itemID), time.Now().UTC())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, briefItemToWire(item))
}

// MarkBriefItemDismissed records a dismissal; the deal reappears only if a
// new linked activity arrives after the mark (B-E05.13).
func (h Handlers) MarkBriefItemDismissed(w http.ResponseWriter, r *http.Request, itemID openapi_types.UUID) {
	item, err := h.engine.MarkDismissed(r.Context(), ids.UUID(itemID), time.Now().UTC())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, briefItemToWire(item))
}

func briefRunToWire(run BriefRun) crmcontracts.MorningBrief {
	items := make([]crmcontracts.MorningBriefItem, 0, len(run.Items))
	for _, item := range run.Items {
		items = append(items, briefItemToWire(item))
	}
	norm := run.RevenueNormMinor
	return crmcontracts.MorningBrief{
		Id:               openapi_types.UUID(run.ID),
		GeneratedAt:      run.GeneratedAt,
		AsOf:             run.AsOf,
		CandidateCount:   run.CandidateCount,
		RevenueNormMinor: &norm,
		Items:            items,
	}
}

func briefItemToWire(item BriefRunItem) crmcontracts.MorningBriefItem {
	evidence := make([]openapi_types.UUID, 0, len(item.EvidenceIDs))
	for _, id := range item.EvidenceIDs {
		evidence = append(evidence, openapi_types.UUID(id))
	}
	return crmcontracts.MorningBriefItem{
		Id:        openapi_types.UUID(item.ID),
		DealId:    openapi_types.UUID(item.DealID),
		Rank:      item.Rank,
		Composite: float32(item.Composite),
		FeatureVector: crmcontracts.MorningBriefFeatureVector{
			Winnability: float32(item.Features.Winnability),
			Revenue:     float32(item.Features.Revenue),
			Timing:      float32(item.Features.Timing),
			Momentum:    float32(item.Features.Momentum),
			Warmth:      float32(item.Features.Warmth),
		},
		EvidenceIds: evidence,
		State:       crmcontracts.MorningBriefItemState(item.State),
		StateAt:     item.StateAt,
	}
}
