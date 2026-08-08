// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package agents is the governed MCP tool surface (03b Layer 1,
// interfaces.md §2): the ONE artifact every agent surface consumes — the
// local stdio server (A1) today, the hosted HTTPS server (A2) and the
// first-party Surface-B runner later. All of them dispatch through this
// registry, and the registry admits every call through platform/auth
// before a handler runs: no back door, no privileged registry (ADR-0013).
package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// Registry implements mcp.Registry. Registration happens at composition
// time and is then read-only; Invoke is safe for concurrent callers.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]mcp.Tool
	// specs[tool] is what every surface is SERVED for that tool: its own spec
	// with the declared output shape wrapped in the result envelope. Held here
	// rather than re-derived per request because tools/list embeds it verbatim
	// and a client caches it — one derivation, one set of bytes, forever.
	specs map[string]mcp.ToolSpec
	// idArgs[tool] is what that tool's schema says about its uuid arguments,
	// read off the schema once at registration. Invoke enforces it, so the
	// schema's claims are true of the surface rather than of whichever handlers
	// remembered to check them.
	idArgs map[string]idArgSpec
	// numArgs[tool] is the range its schema advertises for each numeric
	// argument, read off the schema once at registration. Invoke holds a
	// supplied value to it, so `minimum`/`maximum` bind the surface instead of
	// describing an intention.
	numArgs map[string][]numBound
	// requiredArgs[tool] is what that tool's schema says it cannot run without,
	// read off the schema once at registration. Invoke holds a call to it, so
	// `required` binds the surface rather than describing an intention that
	// each handler then re-states in its own words.
	requiredArgs map[string][]string
	// approvals closes the 🟡 loop (stage on refusal, redeem on retry).
	// Nil is a legal composition — the gate still refuses; refused calls
	// just have nowhere to land.
	approvals Approvals
	// gate is the platform/auth admission point; it re-derives the
	// granting human's authority live per call. A nil gate fails closed
	// for agent principals (Gate.Admit owns that rule).
	gate *auth.Gate
	// quota is the MCP-SESS-* meter this surface CHARGES. The gate holds the
	// same meter and does the refusing; the split is deliberate — a bound is
	// enforced where admission is decided and paid where records and effects
	// leave.
	quota QuotaCharger
	// cost answers the SOFT budget-share counter, whose only effect is a
	// warning on the answer (quota.go).
	cost CostShareReader
}

// NewRegistry builds the tool surface over its approvals engine and admission
// gate. Options add the dependencies only some roles have — today, the meter
// served records are charged against.
func NewRegistry(approvals Approvals, gate *auth.Gate, opts ...RegistryOption) *Registry {
	r := &Registry{
		tools:        map[string]mcp.Tool{},
		specs:        map[string]mcp.ToolSpec{},
		idArgs:       map[string]idArgSpec{},
		numArgs:      map[string][]numBound{},
		requiredArgs: map[string][]string{},
		approvals:    approvals,
		gate:         gate,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

var _ mcp.Registry = (*Registry)(nil)

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

// Invoke runs the admission gate, then the tool. There is no other path
// to a Handle in this package. A refused 🟡 call is staged for human
// decision; a retry carrying `approval_id` redeems that decision — bound
// to the identical call by content hash — and only then reaches Handle.
func (r *Registry) Invoke(ctx context.Context, name string, in json.RawMessage) (json.RawMessage, error) {
	// The REGISTERED spec, not a fresh Spec() call. The two are the same for a
	// tool whose spec is a literal, and every tool here is — but the registered
	// one is what tools/list advertised and what the argument constraints were
	// read off, so serving a call from anything else would let the version a
	// result reports and the schema a client cached come from two different
	// readings. One reading, taken once, under the same lock as the handler.
	r.mu.RLock()
	t, ok := r.tools[name]
	spec := r.specs[name]
	r.mu.RUnlock()
	if !ok {
		return nil, &UnknownToolError{Name: name}
	}

	args, approvalID, diffHash, err := splitApproval(in)
	if err != nil {
		return nil, err
	}

	admitted, err := r.gate.Admit(ctx, spec, r.tierResolverFor(ctx, t, name, args))
	// Static-tier tools, whose resolver Admit never runs. After authority and
	// before staging, and both halves matter: a caller the gate turns away learns
	// nothing about arguments, while a caller it would send to the approval queue
	// is told about its own bad arguments first — staging an unrunnable call spends
	// a human's yes on something that was never going to happen. A step-up is
	// staging too, and spends the same yes.
	if askedOfAHuman(err) {
		if argErr := r.requireDeclaredArgs(name, args); argErr != nil {
			return nil, argErr
		}
	}
	ctx = admitted
	if stepUp := releasableQuotaRefusal(err); stepUp != nil {
		return nil, r.stageStepUp(ctx, stepUp)
	}
	switch {
	case err == nil:
		// An auto-execute call may still carry approval_id: the retry of a
		// per-field precedence staging (interfaces.md §2.1) admits at the
		// auto-execute tier, so its asserted authority is consumed HERE — validated against
		// the identical-call hash, never ignored. The redeemed mark tells
		// the handler the overwrite it is about to make was human-released.
		if !approvalID.IsZero() {
			if r.approvals == nil {
				return nil, fmt.Errorf("crmagents: approval_id presented but this surface has no approvals engine: %w", apperrors.ErrApprovalTokenInvalid)
			}
			marked, _, _, rErr := RedeemAndMark(ctx, r.approvals, approvalID, spec.Name, diffHash)
			if rErr != nil {
				return nil, rErr
			}
			ctx = marked
		}
		return r.handle(ctx, t, spec, args)
	case !errors.Is(err, apperrors.ErrRequiresApproval) || r.approvals == nil:
		return nil, err
	case !approvalID.IsZero():
		marked, _, _, rErr := RedeemAndMark(ctx, r.approvals, approvalID, spec.Name, diffHash)
		if rErr != nil {
			return nil, rErr
		}
		return r.handle(marked, t, spec, args)
	default:
		return nil, r.stageRefusedCall(ctx, t, spec.Name, args, diffHash, err)
	}
}

// handle runs an admitted call and seals its answer into the result envelope.
//
// Every path out of Invoke that reaches a handler comes through here — the
// straight auto-execute call and both approval redemptions — so the envelope is
// a property of the SURFACE rather than of the paths someone remembered. A
// handler still marshals only its own payload and never sees the envelope,
// which is what keeps thirty tools from carrying thirty spellings of it.
//
// The failure path deliberately seals nothing: a refusal is an error, and an
// error carries the sentinel and the message the caller acts on, not a document
// with an empty payload inside it.
func (r *Registry) handle(ctx context.Context, t mcp.Tool, spec mcp.ToolSpec, args json.RawMessage) (json.RawMessage, error) {
	// The call ceiling is charged HERE, at the one point every admitted call
	// passes on its way to a handler, and BEFORE the handler runs — the only
	// moment at which "has anything happened yet?" is still no, which is what
	// lets an uncountable call be refused rather than absorbed.
	if err := r.chargeCall(ctx, spec); err != nil {
		return nil, err
	}
	ctx, trace := withTrace(ctx)
	ctx, facts := withEnvelopeFacts(ctx)
	noteRowScope(ctx)
	out, err := t.Handle(ctx, args)
	if err != nil {
		return nil, err
	}
	r.noteCostShare(ctx)
	sealed, err := sealEnvelope(spec, trace, facts, out)
	if err != nil {
		return nil, err
	}
	// Charged AFTER sealing and BEFORE returning: at this point the answer
	// exists but has not reached the caller, so a charge that cannot be
	// recorded can still refuse it. An answer sealed and then charged in the
	// other order would spend the window on a result a marshalling failure was
	// about to discard.
	if err := r.chargeAnswer(ctx, spec, facts.servedCount()); err != nil {
		return nil, err
	}
	return sealed, nil
}

// tierResolverFor builds the resolver Admit consults for the call's tier. A
// static-tier tool needs nothing but its own arguments; Admit never invokes
// the resolver for one at all.
//
// A dynamic tool's argument checks run HERE, and only for a dynamic tool,
// because a dynamic tool decides its own tier by READING the record an
// argument names: a zero deal_id would reach the stage lookup and come back
// as a bare not-found from inside the gate, where no downstream check can
// reach it. Admit calls the resolver after scope and seat, so this still sits
// behind the authority checks that do not depend on arguments. Static-tier
// tools are covered by the call after Admit.
func (r *Registry) tierResolverFor(ctx context.Context, t mcp.Tool, name string, args json.RawMessage) func() (mcp.TierResolverInput, error) {
	dyn, ok := t.(dynamicTool)
	if !ok {
		return func() (mcp.TierResolverInput, error) {
			return mcp.TierResolverInput{Args: args}, nil
		}
	}
	return func() (mcp.TierResolverInput, error) {
		if err := r.requireDeclaredArgs(name, args); err != nil {
			return mcp.TierResolverInput{}, err
		}
		return dyn.ResolverInput(ctx, args)
	}
}

// stageRefusedCall parks a 🟡 call the gate refused as a staged approval, so
// the human decision the refusal asks for has somewhere to land; a retry
// carrying its approval_id redeems it. A tool that cannot describe its own
// staging target has nothing to park, so the refusal stands as the answer.
func (r *Registry) stageRefusedCall(ctx context.Context, t mcp.Tool, tool string, args json.RawMessage, diffHash string, refusal error) error {
	stageable, ok := t.(stageableTool)
	if !ok {
		return refusal
	}
	info, err := stageable.StageInfo(ctx, args)
	if err != nil {
		// The staging read failed (bad args, out-of-scope target) —
		// that is the real answer, not "needs approval".
		return err
	}
	id, err := r.approvals.Stage(ctx, StageRequest{
		Tool:           tool,
		ProposedChange: args,
		DiffHash:       diffHash,
		TargetType:     info.TargetType,
		TargetID:       info.TargetID,
		TargetVersion:  info.TargetVersion,
		Summary:        info.Summary,
	})
	if err != nil {
		return err
	}
	return &workflow.StagedApprovalError{ApprovalID: id}
}

// Spec returns the registered spec for name — the REST admission path
// (ADR-0055) resolves a mutating operation's tool twin through this.
func (r *Registry) Spec(name string) (mcp.ToolSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	spec, ok := r.specs[name]
	return copySchemas(spec), ok
}

// copySchemas hands out a spec whose schemas are the CALLER's bytes.
//
// A json.RawMessage is a slice, so returning the registered one shares its
// backing array: a caller that wrote through it would rewrite what tools/list
// advertises and what results are validated against, for every later request,
// from outside the lock. The schemas are the two members that can be written
// through; everything else on a ToolSpec is copied by the assignment.
func copySchemas(spec mcp.ToolSpec) mcp.ToolSpec {
	spec.InputSchema = bytes.Clone(spec.InputSchema)
	spec.OutputSchema = bytes.Clone(spec.OutputSchema)
	return spec
}

// Specs lists the registered surface, stably ordered for tools/list.
func (r *Registry) Specs() []mcp.ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]mcp.ToolSpec, 0, len(r.specs))
	for _, spec := range r.specs {
		out = append(out, copySchemas(spec))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Offered is the surface THIS caller may invoke: the catalog both the external
// tools/list and a Surface-B run's tool listing are drawn from.
//
// One function serves both, rather than two filters that agree today. A run
// offered a verb its passport cannot spend is being asked to choose among names
// it will be refused for, and every one of them rides in a system prompt that
// elision never touches.
//
// It answers the SCOPE axis only. Invoke remains the authority on the seat
// ceiling and object RBAC, which are re-derived per call — this narrows what is
// advertised and enforces nothing.
func (r *Registry) Offered(ctx context.Context) []mcp.ToolSpec {
	all := r.Specs()
	out := make([]mcp.ToolSpec, 0, len(all))
	for _, spec := range all {
		if invocableByCaller(ctx, spec) {
			out = append(out, spec)
		}
	}
	return out
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

// dynamicTool is implemented by TierDynamic tools that need more than the
// raw args to resolve their tier — advance_deal reads the target stage's
// semantic from pipeline configuration, which costs a database read the
// gate should pay only for dynamic calls.
type dynamicTool interface {
	ResolverInput(ctx context.Context, in json.RawMessage) (mcp.TierResolverInput, error)
}

// UnknownToolError answers a tools/call for a name outside the surface.
type UnknownToolError struct{ Name string }

// maxToolNameEcho bounds the caller-supplied name this error quotes back.
// The name is chosen freely by the model and lands in a transcript that the
// same run's later prompts read, so an unbounded echo is an unbounded write
// into those prompts. Generous next to the longest real tool name, short
// enough that the field cannot carry prose.
const maxToolNameEcho = 64

// Error renders the echo HERE rather than at each surface, so no consumer —
// the tool result, the server log, a future transport — can quote the name
// back raw by forgetting to. Bounded AND escaped: the name is chosen by the
// model, and a newline in it would otherwise open what reads as a new line of
// the transcript that the same run's later prompts go on to read.
func (e *UnknownToolError) Error() string {
	return "unknown tool " + echoSafe(e.Name, maxToolNameEcho)
}
