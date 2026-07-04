package compose

// The deterministic automation path, assembled: the workflow engine
// over the composite provider with the starter library registered —
// the worker consumes cg:workflows through it.

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
)

// NewWorkflowEngine builds the engine with the shipped starter set.
func NewWorkflowEngine(pool *pgxpool.Pool) *agents.WorkflowEngine {
	engine := agents.NewWorkflowEngine(pool)
	for _, handler := range agents.StarterWorkflows(NewProvider(pool)) {
		engine.RegisterWorkflow(handler)
	}
	return engine
}
