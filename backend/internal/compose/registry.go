package compose

// The governed MCP tool surface, assembled: the agents registry over the
// composite datasource provider, with the approvals engine injected as
// the staging/redemption dependency — composed here so agents never
// imports a sibling module (ADR-0054 §9).

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// NewRegistry wires the full 🟢/🟡 tool set over the composite provider.
func NewRegistry(pool *pgxpool.Pool) *agents.Registry {
	provider := NewProvider(pool)
	registry := agents.NewRegistry(approvalsAdapter{svc: approvals.NewService(pool)})
	agents.RegisterCoreTools(registry, provider, provider, provider)
	return registry
}

// approvalsAdapter maps the tool surface's staging/redemption dependency
// onto the approvals module.
type approvalsAdapter struct{ svc *approvals.Service }

func (a approvalsAdapter) Stage(ctx context.Context, in agents.StageRequest) (ids.UUID, error) {
	return a.svc.Stage(ctx, approvals.StageInput{
		Kind:           in.Tool,
		ProposedChange: in.ProposedChange,
		DiffHash:       in.DiffHash,
		TargetType:     in.TargetType,
		TargetID:       in.TargetID,
		TargetVersion:  in.TargetVersion,
		Summary:        in.Summary,
	})
}

func (a approvalsAdapter) Redeem(ctx context.Context, approvalID ids.UUID, tool, diffHash string) error {
	return a.svc.Redeem(ctx, approvalID, tool, diffHash)
}
