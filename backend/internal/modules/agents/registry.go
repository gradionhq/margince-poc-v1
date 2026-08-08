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
	// reads is the MCP-SESS-READS meter this surface CHARGES. The gate holds
	// the same meter and does the refusing; the split is deliberate — a bound
	// is enforced where admission is decided and paid where records leave.
	reads ReadCharger
	// claims is what makes `idempotency_key` mean something. Nil refuses a
	// keyed call rather than running it unprotected (idempotency.go).
	claims Idempotency
	// replayReader re-reads the records a recorded result rests on, so a replay
	// is gated as the read it is.
	replayReader ReplayReader
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
	// The two schema transforms the SURFACE owns, applied once here so no tool
	// carries either: its result wrapped in the envelope, and — for a mutating
	// tool — the retry key it may be called with.
	r.specs[spec.Name] = withRetryKey(envelopedSpec(spec))
	// Derived from the tool's OWN schema, never the spliced one: the reserved
	// members are popped before any of these checks runs, so a check that knew
	// about them would be describing arguments no handler can be reached with.
	r.idArgs[spec.Name] = declaredIDArgs(spec.InputSchema)
	r.numArgs[spec.Name] = declaredNumBounds(spec.InputSchema)
	r.requiredArgs[spec.Name] = declaredRequired(spec.InputSchema)
}

// Invoke runs the admission gate, then the tool. There is no other path
// to a Handle in this package. A refused 🟡 call is staged for human
// decision; a retry carrying `approval_id` redeems that decision — bound
// to the identical call by content hash — and only then reaches Handle.
//
// A call carrying `idempotency_key` reaches Handle at most once for that key
// (idempotency.go): the claim is taken AFTER admission, so a caller the gate
// turns away never occupies a key, and the effect is what the claim protects.
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

	res, err := splitReserved(in)
	if err != nil {
		return nil, err
	}
	args := res.Args

	admitted, err := r.gate.Admit(ctx, spec, r.tierResolverFor(ctx, t, name, args))
	// Static-tier tools, whose resolver Admit never runs. After authority and
	// before staging, and both halves matter: a caller the gate turns away learns
	// nothing about arguments, while a caller it would send to the approval queue
	// is told about its own bad arguments first — staging an unrunnable call spends
	// a human's yes on something that was never going to happen.
	if err == nil || errors.Is(err, apperrors.ErrRequiresApproval) {
		if argErr := r.requireDeclaredArgs(name, args); argErr != nil {
			return nil, argErr
		}
	}
	ctx = admitted
	switch {
	// An auto-execute call may still carry approval_id: the retry of a per-field
	// precedence staging (interfaces.md §2.1) admits at the auto-execute tier, so its
	// asserted authority is consumed by redeemPresented — validated against the
	// identical-call hash, never ignored.
	case err == nil, !res.ApprovalID.IsZero() && errors.Is(err, apperrors.ErrRequiresApproval) && r.approvals != nil:
		return r.runClaimed(ctx, t, spec, res)
	case !errors.Is(err, apperrors.ErrRequiresApproval) || r.approvals == nil:
		return nil, err
	default:
		// Staged, and deliberately BEFORE any claim: a call that did not run
		// must not leave a key held, and the retry that redeems the approval is
		// the same call under the same key.
		return nil, r.stageRefusedCall(ctx, t, spec.Name, args, res.DiffHash, err)
	}
}

// runClaimed is the one path from admission to a handler: claim the retry key,
// redeem any asserted approval, run, and record what the run produced.
//
// The ORDER is the whole point. The claim comes first, so the retry of an
// approved call reaches its recorded result instead of dying on the
// single-use approval it already spent. Redemption comes second, so a refused
// redemption gives the key straight back — nothing ran.
func (r *Registry) runClaimed(ctx context.Context, t mcp.Tool, spec mcp.ToolSpec, res reserved) (json.RawMessage, error) {
	fresh, answered, err := r.claimFor(ctx, spec, res)
	if !fresh {
		return answered, err
	}
	redeemed, err := r.redeemPresented(ctx, spec, res)
	if err != nil {
		r.releaseUnrunKey(ctx, spec, res)
		return nil, err
	}
	return r.handle(redeemed, t, spec, res)
}

// redeemPresented consumes the approval a retry asserts, and answers the
// context marked as released. A call presenting none passes through: whether
// one was REQUIRED is the gate's question, already answered above.
func (r *Registry) redeemPresented(ctx context.Context, spec mcp.ToolSpec, res reserved) (context.Context, error) {
	if res.ApprovalID.IsZero() {
		return ctx, nil
	}
	if r.approvals == nil {
		return ctx, fmt.Errorf("crmagents: approval_id presented but this surface has no approvals engine: %w", apperrors.ErrApprovalTokenInvalid)
	}
	marked, _, _, err := RedeemAndMark(ctx, r.approvals, res.ApprovalID, spec.Name, res.DiffHash)
	if err != nil {
		return ctx, err
	}
	return marked, nil
}

// handle runs an admitted call and seals its answer into the result envelope.
//
// Every path out of Invoke that reaches a handler comes through here — the
// straight auto-execute call and both approval redemptions — so the envelope is
// a property of the SURFACE rather than of the paths someone remembered. A
// handler still marshals only its own payload and never sees the envelope,
// which is what keeps thirty tools from carrying thirty spellings of it.
//
// It is also where a claimed retry key is settled, and for the same reason: this
// is the one place that knows both that the tool RAN and what it produced. A
// run recorded anywhere else would be a run some later path could forget to
// record, and an unrecorded run is a key whose retry executes again.
//
// The failure path deliberately seals nothing: a refusal is an error, and an
// error carries the sentinel and the message the caller acts on, not a document
// with an empty payload inside it.
func (r *Registry) handle(ctx context.Context, t mcp.Tool, spec mcp.ToolSpec, res reserved) (json.RawMessage, error) {
	sealed, records, err := r.runAndSeal(ctx, t, spec, res.Args)
	r.settleRun(ctx, spec, res, sealed, records, err)
	return sealed, err
}

// runAndSeal is the call itself: run, seal, charge. It answers the record count
// as well as the document, because what an answer COST is what its replay costs,
// and only this frame can see it.
func (r *Registry) runAndSeal(ctx context.Context, t mcp.Tool, spec mcp.ToolSpec, args json.RawMessage) (json.RawMessage, int, error) {
	ctx, trace := withTrace(ctx)
	ctx, facts := withEnvelopeFacts(ctx)
	noteRowScope(ctx)
	out, err := t.Handle(ctx, args)
	if err != nil {
		return nil, 0, err
	}
	sealed, err := sealEnvelope(spec, trace, facts, out)
	if err != nil {
		return nil, 0, err
	}
	// Charged AFTER sealing and BEFORE returning: at this point the answer
	// exists but has not reached the caller, so a charge that cannot be
	// recorded can still refuse it. An answer sealed and then charged in the
	// other order would spend the window on a result a marshalling failure was
	// about to discard.
	served := facts.servedCount()
	if err := r.chargeReads(ctx, spec, served); err != nil {
		return nil, 0, err
	}
	return sealed, served, nil
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
