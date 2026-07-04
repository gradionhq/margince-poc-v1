package compose

// The capture path, assembled: the one Sink over the pool (with the
// approvals engine as its merge-stager — dedupe collisions become 🟡
// proposals, never auto-merges), the connector registry with identity
// as the live-authority resolver — composed here so capture never
// imports identity or approvals (ADR-0054 §9).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// NewCaptureRegistry builds the connector registry; process roles
// register their compiled-in connectors on it and drive SyncOnce.
func NewCaptureRegistry(pool *pgxpool.Pool) *capture.Registry {
	sink := capture.NewSink(pool).WithStager(mergeStager{svc: approvals.NewService(pool)})
	return capture.NewRegistry(pool, sink, identity.NewService(pool))
}

// mergeStager adapts the approvals engine to capture's dedupe seam.
type mergeStager struct {
	svc *approvals.Service
}

func (m mergeStager) StageMerge(ctx context.Context, in capture.MergeProposal) (ids.UUID, error) {
	digest := sha256.Sum256(in.ProposedChange)
	return m.svc.Stage(ctx, approvals.StageInput{
		Kind:           "merge_records",
		ProposedChange: in.ProposedChange,
		DiffHash:       hex.EncodeToString(digest[:]),
		TargetType:     in.TargetType,
		TargetID:       in.TargetID,
		Summary:        in.Summary,
	})
}
