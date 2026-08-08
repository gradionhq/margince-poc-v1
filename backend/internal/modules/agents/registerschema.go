// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The SCHEMA half of registration, split out of registry.go when that file hit
// the 500-line cap. The boundary is the same one idargs.go draws: registry.go
// decides whether a call may run, and this decides what a registered tool's
// schemas must be — what a spec has to declare to be servable at all, and what
// the surface adds to it before any client sees it.
//
// Both additions are made HERE, once, at the one door every tool comes through,
// so no tool carries either and a tool added tomorrow gets both without its
// author having read this file: the result shape wrapped in the envelope, and —
// for a mutating tool — the retry key it may be called with.

import (
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// envelopedSpec is the spec every surface is served: the tool's own, with its
// declared output shape wrapped in the envelope Invoke seals results into.
//
// It is computed HERE, once at registration, rather than where each surface
// serves it. The advertised schema and the answered document are two halves of
// one promise, and the only way they cannot drift is for one wrapper to produce
// both — the tool declares the shape of its payload and knows nothing about the
// envelope, exactly as its handler does.
func envelopedSpec(spec mcp.ToolSpec) mcp.ToolSpec {
	if spec.OutputSchema == nil {
		// A tool promising no output shape owes tools/call no structured
		// content; its result is still sealed, but there is nothing to wrap.
		return spec
	}
	sealed, err := envelopedSchema(spec.OutputSchema)
	if err != nil {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("crmagents: cannot advertise %s's result inside the envelope: %v", spec.Name, err))
	}
	spec.OutputSchema = sealed
	return spec
}

// assertObjectSchemas holds two promises tools/list and tools/call have to
// keep, at the one door every tool comes through.
//
// The first is ENCODABILITY. Both schemas are hand-written JSON literals
// spliced together from constants, and they reach the client by being embedded
// verbatim into the tools/list response — so ONE misplaced brace does not
// break one tool, it makes the whole listing unencodable and every tool
// disappears behind a 500. That is a boot-time defect discovered on a client's
// first request, which is exactly the wrong end.
//
// The second is that both are OBJECT schemas. MCP requires an object input
// schema, and a declared outputSchema obliges the server to answer with
// structured content conforming to it — which the dispatcher can only do for
// an object, because structuredContent is typed as one. A schema written some
// other way (a $ref, a bare allOf) fails here on purpose: not wrong, but not
// something the dispatcher has been taught to honour, and failing at boot
// beats advertising a shape the results miss.
func assertObjectSchemas(spec mcp.ToolSpec) error {
	if spec.InputSchema == nil {
		// The protocol requires one. A tool taking no arguments still declares
		// `{"type":"object"}`; nil would put a bare null on tools/list.
		return fmt.Errorf("%s declares no InputSchema; MCP requires every tool to advertise an object input schema", spec.Name)
	}
	for _, s := range []struct {
		field string
		raw   json.RawMessage
	}{
		{field: "InputSchema", raw: spec.InputSchema},
		// Optional: a tool promising no output shape owes tools/call no
		// structured content.
		{field: "OutputSchema", raw: spec.OutputSchema},
	} {
		if s.raw == nil {
			continue
		}
		var declared struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(s.raw, &declared); err != nil {
			return fmt.Errorf("%s has an %s that is not valid JSON, which makes the whole tools/list response unencodable: %w",
				spec.Name, s.field, err)
		}
		if declared.Type != "object" {
			return fmt.Errorf("%s declares %s type %q; this surface serves object schemas only",
				spec.Name, s.field, declared.Type)
		}
	}
	return nil
}

// retryKeyProperty is the advertised member. The prose is short on purpose: it
// is served in the tools/list catalog AND printed into every Surface-B run's
// system prompt, once per mutating tool, so a sentence here is eighteen
// sentences of every run's context.
const retryKeyProperty = `{"type":"string","maxLength":255,` +
	`"description":"Optional. Repeating a call under the same key returns the first result instead of acting twice; ` +
	`different arguments under one key are refused."}`

// withRetryKey advertises the retry key on a mutating tool's input schema, and
// leaves a read-only tool's schema alone.
//
// The decision is DERIVED from the tool's required scope (ToolSpec.ReadOnly),
// which is the same answer the admission gate enforces — so the schema cannot
// claim retry safety for a tool the surface would not claim it for.
func withRetryKey(spec mcp.ToolSpec) mcp.ToolSpec {
	if spec.ReadOnly() {
		return spec
	}
	spliced, err := spliceRetryKey(spec.InputSchema)
	if err != nil {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("crmagents: cannot advertise the retry key on %s: %v", spec.Name, err))
	}
	spec.InputSchema = spliced
	return spec
}

// spliceRetryKey adds the member to one schema's `properties`.
//
// Separate from withRetryKey so its refusals can be exercised: the schemas the
// caller passes have already survived assertObjectSchemas, and a guard that
// only ever holds against an argument nothing can supply is a guard nobody has
// read.
//
// The whole schema is read back as raw members and re-marshalled, rather than
// edited as a string, for the reason spliceResultSchema gives: marshalling a
// map sorts its keys, so every process produces the same bytes — and these
// bytes are embedded verbatim into tools/list, which a client caches.
func spliceRetryKey(inputSchema json.RawMessage) (json.RawMessage, error) {
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(inputSchema, &shape); err != nil {
		return nil, fmt.Errorf("its input schema is not a JSON object: %w", err)
	}
	var properties map[string]json.RawMessage
	if raw, declared := shape["properties"]; declared {
		if err := json.Unmarshal(raw, &properties); err != nil {
			return nil, fmt.Errorf("its input schema's `properties` is not an object: %w", err)
		}
	}
	if properties == nil {
		// A mutating tool taking no arguments still gets the key: `log_activity`
		// having arguments and a hypothetical argument-less mutation not having
		// them says nothing about whether repeating it twice is safe.
		properties = map[string]json.RawMessage{}
	}
	if _, taken := properties[idempotencyKeyArg]; taken {
		// A tool that wrote the member itself would have TWO definitions of it —
		// its own, and this one — and only one can win a splice. Refused at boot
		// rather than resolved silently, because the two could disagree about
		// type or bound and the surface would enforce whichever this happened to
		// keep.
		return nil, fmt.Errorf("it declares `%s` itself; the surface owns that argument", idempotencyKeyArg)
	}
	properties[idempotencyKeyArg] = json.RawMessage(retryKeyProperty)
	encoded, err := json.Marshal(properties)
	if err != nil {
		return nil, fmt.Errorf("cannot encode its properties: %w", err)
	}
	shape["properties"] = encoded
	sealed, err := json.Marshal(shape)
	if err != nil {
		return nil, fmt.Errorf("cannot encode the spliced schema: %w", err)
	}
	return sealed, nil
}
