package compose

// The capture path, assembled: the one Sink over the pool, the
// connector registry with identity as the live-authority resolver —
// composed here so capture never imports identity (ADR-0054 §9).

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
)

// NewCaptureRegistry builds the connector registry; process roles
// register their compiled-in connectors on it and drive SyncOnce.
func NewCaptureRegistry(pool *pgxpool.Pool) *capture.Registry {
	return capture.NewRegistry(pool, capture.NewSink(pool), identity.NewService(pool))
}
