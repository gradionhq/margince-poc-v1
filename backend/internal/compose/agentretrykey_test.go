// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Which tools the retry key reaches, held against the REAL composed surface
// rather than against the list someone wrote down.
//
// Both halves are asserted, because both can be wrong and only one of them is
// visible from outside: a mutating tool missing the key refuses an argument
// nobody knew to send, and a read tool carrying one advertises a promise that
// protects nothing and costs every run's prompt to print.

import (
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// retryKeyMember is the argument name the agents module owns. Spelled here
// because a test that read it from that package would agree with a rename by
// construction and prove nothing about what a client is served.
const retryKeyMember = "idempotency_key"

func advertisesRetryKey(t *testing.T, spec mcp.ToolSpec) bool {
	t.Helper()
	var shape struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(spec.InputSchema, &shape); err != nil {
		t.Fatalf("%s: input schema is not readable: %v", spec.Name, err)
	}
	_, advertised := shape.Properties[retryKeyMember]
	return advertised
}

func TestEveryMutatingToolIsAdvertisedWithTheRetryKey(t *testing.T) {
	specs := servedSurface(t).Specs()
	if len(specs) == 0 {
		t.Fatal("the composed surface registers no tools, so this asserts nothing")
	}
	var mutating, readOnly int
	for _, spec := range specs {
		advertised := advertisesRetryKey(t, spec)
		switch {
		case spec.ReadOnly():
			readOnly++
			if advertised {
				t.Errorf("%s only reads, and advertises `%s` — a promise that protects nothing, "+
					"printed into every run's prompt", spec.Name, retryKeyMember)
			}
		default:
			mutating++
			if !advertised {
				t.Errorf("%s can change something and does not advertise `%s`, so a caller that sent one "+
					"would be refused an argument nobody told it about", spec.Name, retryKeyMember)
			}
		}
	}
	// Neither side may be empty, or the loop above would pass by having nothing
	// to check — the shape of green this whole surface's fitness tests guard
	// against.
	if mutating == 0 || readOnly == 0 {
		t.Fatalf("the surface has %d mutating and %d read-only tools; both halves must be exercised",
			mutating, readOnly)
	}
}

// The claim endpoint namespaces a tool's keys inside the table the REST door
// shares. A tool name that could render as a REST endpoint would let one
// caller's key collide with another's.
func TestAToolsClaimEndpointCannotCollideWithARestOne(t *testing.T) {
	seen := map[string]string{}
	for _, spec := range servedSurface(t).Specs() {
		endpoint := mcpClaimEndpoint(spec.Name)
		if _, replayable := replayableOperations[endpoint]; replayable {
			t.Errorf("%s claims keys under %q, which is also a replayable REST route", spec.Name, endpoint)
		}
		if other, dup := seen[endpoint]; dup {
			t.Errorf("%s and %s claim keys under the same endpoint %q", spec.Name, other, endpoint)
		}
		seen[endpoint] = spec.Name
	}
}
