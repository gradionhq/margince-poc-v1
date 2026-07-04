package store

import (
	"context"
	"encoding/json"
	"fmt"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/fable-poc/crmctx"
	"github.com/gradionhq/fable-poc/kernel/ids"
)

// sprintf keeps SQL assembly lines readable; arguments are always
// placeholder indexes or clamped ints, never user input.
func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }

// mustWorkspace is safe inside s.tx: WithWorkspaceTx already failed if no
// workspace was bound.
func mustWorkspace(ctx context.Context) ids.UUID {
	wsID, _ := crmctx.WorkspaceID(ctx)
	return wsID
}

// jsonArg marshals a map for a jsonb parameter, passing NULL for nil.
func jsonArg(m map[string]any) any {
	if m == nil {
		return nil
	}
	raw, _ := json.Marshal(m)
	return raw
}

func uuidPtr(id *ids.UUID) *openapi_types.UUID {
	if id == nil {
		return nil
	}
	converted := openapi_types.UUID(*id)
	return &converted
}
