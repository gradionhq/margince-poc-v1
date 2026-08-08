// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The ONE door every tool comes through, and what it refuses there.
//
// Split from registry.go on the 500-line cap, along a real boundary rather than
// a convenient one: everything here runs at BOOT, against a spec, before any
// request exists — while registry.go is what happens to a call. A defect this
// file catches is a deployment that does not start; one it misses is a runtime
// authority bug or a broken wire response, which is why each check states the
// failure it prevents rather than the rule it enforces.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// maxDescriptionRunes bounds one tool's written description. See Register for
// why a bound exists at all; the value is roughly three times the longest entry
// this surface ships, so it refuses runaway prose without ever being a number
// an author writing a careful description has to think about.
const maxDescriptionRunes = 3000

// Register refuses, at boot, the spec defects that would otherwise surface as
// a runtime authority bug or a broken wire response: a duplicate name (two
// handlers behind one admission decision), a TierDynamic spec with no resolver
// (a tool whose tier nobody computes would default to whatever the gate
// assumes), a missing display title, a missing description (a tool no client
// can tell apart from its neighbours), and a schema that is not an encodable
// object (see assertObjectSchemas — one bad brace takes the whole tools/list
// down, not just its own tool).
//
// This is the ONE door every tool comes through, core and extension alike, so
// none of it is a list of tools someone has to keep current.
func (r *Registry) Register(t mcp.Tool) {
	spec := t.Spec()
	if spec.Name == "" {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic("crmagents: registering a tool with no name")
	}
	if spec.Tier == mcp.TierDynamic && spec.TierResolver == nil {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("crmagents: %s is TierDynamic without a TierResolver", spec.Name))
	}
	if spec.Tier != mcp.TierDynamic && spec.TierResolver != nil {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("crmagents: %s carries a TierResolver but is not TierDynamic", spec.Name))
	}
	// TrimSpace, because a blank title is worse than none: a client takes it
	// over the name (title outranks name for display) and renders an empty
	// heading, where an absent one would at least have fallen back.
	if strings.TrimSpace(spec.Title) == "" {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("crmagents: %s has no Title — tools/list would render its identifier as its display name", spec.Name))
	}
	// A tool nobody described can be selected only by the shape of its name:
	// the surfaces that serve it have nothing else to say about it, and fall
	// back to describing how it is GOVERNED — which is not the question a
	// caller choosing between thirty tools is asking. Refused at the one door,
	// so no tool can answer it for itself.
	if strings.TrimSpace(spec.Description) == "" {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("crmagents: %s has no Description — a client would be told how it is governed and never what it is for", spec.Name))
	}
	// And an upper bound, because the description is not only served to a
	// client that can ignore it: the Surface-B window prints every registered
	// tool's, and that listing is in the system prompt, which elision never
	// touches. One tool's prose is therefore spent out of every run's own
	// context for the life of the process. The ceiling is several times the
	// longest written entry — it is a bound on the pathological case, not a
	// style rule — and it binds every tool that comes through this door, so an
	// extension unit cannot crowd the prompt on its own.
	if n := len([]rune(spec.Description)); n > maxDescriptionRunes {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("crmagents: %s has a %d-rune Description, past the %d a tool may spend — "+
			"every run's prompt carries it and never elides it", spec.Name, n, maxDescriptionRunes))
	}
	// The version a result declares as its own. It is not documentation: every
	// result this surface seals carries it as `schema_version`, which is the
	// only thing that lets a client tell a shape change from a data change. A
	// tool registered without one would put an empty string in that field on
	// every call — a claim that the contract has no version, made forever.
	if strings.TrimSpace(spec.Version) == "" {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("crmagents: %s declares no Version — every result carries it as schema_version, "+
			"and an empty one tells a client the shape can never be compared", spec.Name))
	}
	if err := assertObjectSchemas(spec); err != nil {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic("crmagents: " + err.Error())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tools[spec.Name]; dup {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("crmagents: duplicate tool %s", spec.Name))
	}
	r.tools[spec.Name] = t
	r.specs[spec.Name] = envelopedSpec(spec)
	r.idArgs[spec.Name] = declaredIDArgs(spec.InputSchema)
	r.numArgs[spec.Name] = declaredNumBounds(spec.InputSchema)
	r.requiredArgs[spec.Name] = declaredRequired(spec.InputSchema)
}

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
