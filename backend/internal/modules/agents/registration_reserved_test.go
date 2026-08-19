// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// A tool that REQUIRES a reserved member can never be called: the surface pops
// `approval_id` and `idempotency_key` out of every call before the argument
// check reads it, so the caller who sent one is refused for omitting it. No
// call reveals this — the refusal reads like a caller mistake and can be
// retried forever — so it is a boot failure.
//
// Advertising `approval_id` is the ordinary 🟡 shape and must keep working:
// that is how a caller learns what to send on the retry a human released.
func TestAToolRequiringAReservedArgumentIsRefusedAtBoot(t *testing.T) {
	reserved := func(name, required string) mcp.ToolSpec {
		spec := mutatingSpec(name)
		spec.InputSchema = json.RawMessage(`{"type":"object","properties":{` +
			`"` + required + `":{"type":"string","format":"uuid"}},` +
			`"required":["` + required + `"],"additionalProperties":false}`)
		return spec
	}
	for _, arg := range []string{approvalIDArg, idempotencyKeyArg} {
		t.Run(arg, func(t *testing.T) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					t.Fatalf("a tool requiring `%s` registered — every call to it would be refused "+
						"for omitting what it sent", arg)
				}
				if message, _ := recovered.(string); !strings.Contains(message, arg) {
					t.Errorf("the refusal never names %s: %v", arg, recovered)
				}
			}()
			NewRegistry(nil, nil).Register(&fakeTool{spec: reserved("requires_"+arg, arg)})
		})
	}
}

func TestAToolMayStillAdvertiseTheRedemptionToken(t *testing.T) {
	spec := mutatingSpec("advertises_the_token")
	spec.InputSchema = json.RawMessage(`{"type":"object","properties":{` +
		`"deal_id":{"type":"string","format":"uuid"},` +
		`"` + approvalIDArg + `":{"type":"string","format":"uuid"}},` +
		`"required":["deal_id"],"additionalProperties":false}`)
	// Registers without panicking, which is the whole assertion: a 🟡 tool
	// naming the token it will be retried with is the shape this surface ships.
	registered := registeredSpec(t, spec)
	if _, offered := declaredProperties(t, registered.InputSchema)[approvalIDArg]; !offered {
		t.Errorf("the registered schema dropped `%s`, so a caller cannot learn what to send on the retry", approvalIDArg)
	}
	if registered.RequiredScope != principal.ScopeWrite {
		t.Errorf("RequiredScope = %v, want write", registered.RequiredScope)
	}
}
