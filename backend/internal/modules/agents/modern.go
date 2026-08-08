// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The 2026-07-28 framing, served beside the handshake era (ADR-0092/A141).
//
// A modern request carries its own protocol version, identity and capabilities
// in `_meta`, so it needs no session and could be answered by any replica; a
// legacy request arrives after `initialize` and is parsed exactly as it was
// before. What the two share is everything that decides AUTHORITY: both reach
// the same Registry.Invoke, and nothing in this file touches admission. A
// framing able to alter what a call may do would be a second admission path,
// which ADR-0055 forbids.
//
// This file owns the BODY half of the framing — era detection, the `_meta`
// contract, server/discover, and the members every modern result carries. The
// header half is httpmodern.go, because mirroring belongs to the transport
// that carries the headers.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// modernProtocolVersion is the revision this server serves in the modern
// framing. It is a single value rather than a list because there is exactly
// one modern revision to serve: the day a second one exists, the compatibility
// window it joins is a spec decision (ADR-0092 §3), not a slice this file
// grows quietly.
const modernProtocolVersion = "2026-07-28"

// The reserved `_meta` keys this server reads and writes. Their prefix is
// reserved to MCP by the specification, which is also why they are spelled
// once here: a typo in one produces a request that looks like it declared
// nothing, and the framing would silently read it as legacy.
const (
	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo         = "io.modelcontextprotocol/serverInfo"
)

// The JSON-RPC codes this server answers with, in both eras — named once so
// the package cannot end up with two spellings of one code.
const (
	codeParseError     = -32700
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// The two codes the MODERN framing adds. -32020..-32099 is the sub-range the
// specification reserves for itself, so a code from it may only ever be
// emitted with the meaning the spec gives it — and never invented here.
const (
	codeHeaderMismatch             = -32020
	codeUnsupportedProtocolVersion = -32022
)

// The members a modern result carries.
const (
	// fieldResultType is required on every modern result. This server answers
	// only "complete": the other value, "input_required", belongs to a
	// multi-round-trip call, and no tool here asks its caller for input —
	// a 🟡 tool stages through the approvals engine, which is a Margince
	// surface a human visits, not a round trip to the agent's client.
	fieldResultType    = "resultType"
	resultTypeComplete = "complete"
	fieldMeta          = "_meta"
	fieldTTLMs         = "ttlMs"
	fieldCacheScope    = "cacheScope"
	cacheScopePublic   = "public"
	cacheScopePrivate  = "private"
)

// How long a client may consider an answer fresh. There are three numbers
// because there are three kinds of answer, and the argument that licenses a
// non-zero TTL holds for only two of them.
//
// A TTL is a freshness hint, never a permission. A client reusing a minute-old
// CATALOG cannot call anything with it: every call re-authenticates and the
// gate re-derives scope, seat and RBAC from live state, so a stale menu can
// mislead a client about what it may try and cannot make the attempt succeed.
// Discovery is the same argument over an answer that does not vary at all.
//
// A DOCUMENT is not a menu. A resources/read result IS the content, so a stale
// copy is the disclosure rather than a hint about one, and nothing downstream
// re-checks it. This server therefore promises no freshness on a read and asks
// the client to come back — which the specification spells `0`, and which
// leaves the required member present rather than absent.
const (
	catalogCacheTTLMs  = 60_000
	discoverCacheTTLMs = 300_000
	documentCacheTTLMs = 0
)

// modernPrivateCatalogs are the cacheable LISTS. Two of them are scope-filtered
// per caller today — the tool list by passport scopes, the resource list by
// what this principal may read — and a shared cache entry on either is a
// disclosure that never reaches the server to be audited (ADR-0092 §5).
//
// The other two answer nothing at all today, and they are `private` for a
// different reason: `public` is a claim about every future answer, not just
// this one, and the cheapest way to never make that claim wrongly is to never
// make it here.
//
// resources/read is deliberately NOT in this list. It is also private, but it
// is a document rather than a catalog and carries its own TTL for the reason
// above.
var modernPrivateCatalogs = []string{
	methodToolsList, methodPromptsList, methodResourcesList, methodResourceTemplatesList,
}

// framing is the protocol era ONE request is parsed under. It is decided once,
// by the transport, and travels down as a value: a second place asking "which
// era is this" is how the two framings would start to disagree.
type framing struct {
	modern bool
	// version is the revision the request declared for itself. It is
	// meaningful only in the modern framing, where the version is a property
	// of the call; a legacy call's version belongs to its session.
	version string
}

// legacyFraming is the handshake era, named rather than spelled as a zero
// value so a reader of a call site can see which era is meant.
var legacyFraming = framing{}

// modernMeta is the per-request protocol metadata a modern client sends, held
// as the RAW members rather than decoded values.
//
// Raw, because this type answers two different questions and they must not be
// confused: whether the request DECLARED itself modern at all, and whether what
// it declared is well formed. A member present but of the wrong type is a
// modern request with a malformed field — it must be refused, never demoted to
// the other era, which is what decoding straight into typed fields would do
// with a version sent as a number.
type modernMeta struct {
	version      json.RawMessage
	capabilities json.RawMessage
}

// declared reports whether the body claims the modern framing at all. EITHER
// reserved per-request key is enough: the specification's dual-era rule keys on
// a request "carrying modern per-request `_meta`", and a body that carries one
// of the two has done so — with the other one missing, which is a refusal
// rather than a reason to read it as a handshake-era call.
func (m modernMeta) declared() bool { return m.version != nil || m.capabilities != nil }

// servesAsModern reports whether version names the revision this server serves
// in the modern framing.
func servesAsModern(version string) bool { return version == modernProtocolVersion }

// supportedProtocolVersions is every revision this server serves, newest
// first: what server/discover advertises and what an UnsupportedProtocolVersion
// refusal lists. The modern revision leads because a client choosing from this
// list should choose it.
func supportedProtocolVersions() []string {
	return append([]string{modernProtocolVersion}, legacyProtocolVersions...)
}

// metaOf reads the modern per-request metadata out of a request's params.
//
// Params that do not decode into an object carrying a `_meta` object declare
// nothing, which is the honest answer rather than an error: JSON-RPC permits
// positional params, this protocol does not use them, and such a body has not
// declared a protocol version by any reading. The legacy path reports whatever
// is actually wrong with it when it tries to use it.
//
// The reserved keys are matched EXACTLY. encoding/json would match them
// case-insensitively, and nothing else on this path reads them, so exact is
// both correct and the stricter of the two.
func metaOf(params json.RawMessage) modernMeta {
	var envelope struct {
		//nolint:tagliatelle // _meta is the protocol's own member name, underscore and all
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if len(params) == 0 {
		return modernMeta{}
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return modernMeta{}
	}
	return modernMeta{
		version:      envelope.Meta[metaProtocolVersion],
		capabilities: envelope.Meta[metaClientCapabilities],
	}
}

// modernPrecheck decides one request's era and, for a modern request, whether
// the body satisfies the framing's own preconditions. It answers the framing
// plus the refusal to write INSTEAD of dispatching, nil when there is none.
//
// transportVersion is the version the transport named alongside the body (the
// MCP-Protocol-Version header on HTTP), and it is load-bearing rather than a
// convenience. Without it, a caller could name the modern revision in the
// header — where every intermediary routes on it — send a body carrying no
// `_meta`, and be parsed as legacy, skipping every check below. With it, that
// request is modern and missing a required field, which is what the
// specification says it is.
func modernPrecheck(params json.RawMessage, transportVersion string) (framing, *rpcError) {
	meta := metaOf(params)
	if !meta.declared() && !servesAsModern(transportVersion) {
		return legacyFraming, nil
	}
	return modernPreconditions(meta)
}

// modernPreconditions holds a modern request to the two things every one of
// them must carry, in the order the specification puts them: a required field
// that is absent is a malformed request (-32602), and a version this server
// does not serve is a refusal that names what it does serve (-32022) so the
// client can retry rather than guess.
//
// -32021 MissingRequiredClientCapability is deliberately never emitted. No
// tool on this surface needs sampling, elicitation or roots, so there is no
// capability whose absence could stop a call; a server that demanded one it
// never uses would refuse callers for nothing.
func modernPreconditions(meta modernMeta) (framing, *rpcError) {
	fr := framing{modern: true}
	version, malformed := declaredVersion(meta.version)
	if malformed != nil {
		return fr, malformed
	}
	fr.version = version
	// Capabilities this server cannot READ are capabilities it would have to
	// assume, which is the one thing a declaration exists to stop — so present
	// is not enough, and a JSON null (which the wire allows anywhere) is the
	// same as absent.
	if !isJSONObject(meta.capabilities) {
		return fr, missingModernField(metaClientCapabilities, "an object")
	}
	if !servesAsModern(fr.version) {
		return fr, unsupportedProtocolVersion(fr.version)
	}
	return fr, nil
}

// declaredVersion reads the revision a modern body names, or the refusal for a
// body that names one this server cannot read. A member of the wrong type is
// NOT an absent member: the request declared itself modern and got the
// declaration wrong, and demoting it to the handshake era would serve a
// malformed modern call instead of refusing it.
func declaredVersion(raw json.RawMessage) (string, *rpcError) {
	var version string
	// A JSON null unmarshals into a string WITHOUT error, leaving it empty, so
	// the empty check is not belt-and-braces: it is what folds absent, null and
	// "" into the one refusal each of them deserves.
	if err := json.Unmarshal(raw, &version); err != nil || version == "" {
		return "", missingModernField(metaProtocolVersion, "a string")
	}
	return version, nil
}

func missingModernField(key, shape string) *rpcError {
	return &rpcError{
		Code: codeInvalidParams,
		Message: fmt.Sprintf("invalid params: a %s request must carry _meta[%q] as %s",
			modernProtocolVersion, key, shape),
	}
}

// isJSONObject reports whether raw is a JSON object — false for an absent
// member, for a null, and for every other JSON type.
//
// The nil-map check carries the null case, which is the one a reader will
// doubt: a JSON null unmarshals into a map without error and leaves it nil, so
// testing only the error would call `null` an object.
func isJSONObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

// unsupportedProtocolVersion is the refusal that names every revision this
// server serves. The message says how the handshake-era ones are reached,
// because the `supported` list alone would tell a modern client that a version
// it cannot use per-request is available to it.
//
// What it echoes back is BOUNDED. `requested` is caller-controlled and its
// length is not — a body is admitted up to 8 MiB — and it is reflected twice,
// so an unbounded echo would answer a large refused request with a larger
// response. A protocol revision is a date; anything longer than that is not
// one, and the client already knows what it sent.
func unsupportedProtocolVersion(requested string) *rpcError {
	echoed := boundedEcho(requested)
	return &rpcError{
		Code: codeUnsupportedProtocolVersion,
		Message: fmt.Sprintf("unsupported protocol version %q: this server serves %s per request, "+
			"and %s through the initialize handshake",
			echoed, modernProtocolVersion, strings.Join(legacyProtocolVersions, ", ")),
		Data: map[string]any{"supported": supportedProtocolVersions(), "requested": echoed},
	}
}

// maxEchoedVersion is how much of a refused version travels back to its
// sender. A revision is a ten-character date, so this is generous for anything
// a client meant and short for anything else.
const maxEchoedVersion = 32

// boundedEcho is caller-controlled text cut to a length this server chose,
// on a rune boundary — a cut through the middle of a multi-byte character
// would put invalid UTF-8 into a JSON string, which the encoder then silently
// rewrites into replacement characters.
func boundedEcho(value string) string {
	if len(value) <= maxEchoedVersion {
		return value
	}
	cut := value[:maxEchoedVersion]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}

// discover answers server/discover: the supported revisions, the capabilities
// and the identity a client would otherwise learn by probing three list
// methods. It reads NOTHING from the caller's context, which is the property
// that lets its result be cached publicly.
func (s *Dispatcher) discover() map[string]any {
	return map[string]any{
		"supportedVersions": supportedProtocolVersions(),
		"capabilities":      s.capabilities(),
		// Guidance for the model on the other side of the client, kept to what
		// is true of every tool: the per-tool text is DescribeForClient's, and
		// a second description of the governance here would be a second answer
		// to the same question.
		"instructions": "A governed CRM tool surface. Every call re-authenticates and is bounded by " +
			"the granting human's own permissions, so a tool may refuse a record this passport cannot " +
			"reach. Tools that a person must approve say so in their own description; calling one " +
			"stages the effect for review rather than performing it.",
	}
}

// finishModern renders one dispatched response in the modern framing: the
// members every modern result carries, and the codes this era spells
// differently.
//
// A legacy response is returned untouched. The framing decides how a call is
// rendered as well as parsed, and a member a 2025-11-25 client never reads is
// not worth handing to one that validates strictly.
func (s *Dispatcher) finishModern(resp rpcResponse, method string) rpcResponse {
	if resp.Error != nil {
		// -32002 was retired with the handshake era and its meaning moved to
		// -32602. Remapping here rather than at the raise site keeps ONE
		// resource-not-found path: the legacy framing still answers the code
		// its own clients recognize.
		if resp.Error.Code == resourceNotFound {
			resp.Error = &rpcError{Code: codeInvalidParams, Message: resp.Error.Message}
		}
		return resp
	}
	members := map[string]any{
		fieldResultType: resultTypeComplete,
		fieldMeta:       map[string]any{metaServerInfo: s.identity()},
	}
	if hint, ok := modernCacheHint(method); ok {
		members[fieldTTLMs], members[fieldCacheScope] = hint.ttlMs, hint.scope
	}
	resp.Result = modernResult{inner: resp.Result, members: members}
	return resp
}

// cacheHint is what a client may do with one result: how long it stays fresh,
// and who may hold the copy.
type cacheHint struct {
	ttlMs int
	scope string
}

// modernCacheHint answers the caching contract for a method's complete result.
// The specification makes the hint a MUST on exactly these methods, so the
// answer is a closed set rather than a default: a method not named here must
// carry no hint, and tools/call is the one that matters — a tool result is not
// a catalog and must never be served twice.
func modernCacheHint(method string) (cacheHint, bool) {
	switch {
	case method == methodDiscover:
		return cacheHint{ttlMs: discoverCacheTTLMs, scope: cacheScopePublic}, true
	case method == methodResourcesRead:
		return cacheHint{ttlMs: documentCacheTTLMs, scope: cacheScopePrivate}, true
	case slices.Contains(modernPrivateCatalogs, method):
		return cacheHint{ttlMs: catalogCacheTTLMs, scope: cacheScopePrivate}, true
	}
	return cacheHint{}, false
}

// modernResult renders a handler's own result value with the framing's members
// merged into it.
//
// It merges at the JSON level rather than by rebuilding a map, so a handler's
// bytes reach the client as the handler wrote them: a round trip through
// map[string]any would widen every integer to a float64, and the two renderings
// of one answer would disagree on exactly the results that carry a count.
type modernResult struct {
	inner   any
	members map[string]any
}

func (m modernResult) MarshalJSON() ([]byte, error) {
	body, err := json.Marshal(m.inner)
	if err != nil {
		return nil, fmt.Errorf("marshalling the result to decorate: %w", err)
	}
	decorated := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &decorated); err != nil {
		// Every method's result is an object; a non-object here is this
		// server's own defect, and the transport turns it into a 500 rather
		// than shipping a result the framing cannot describe.
		return nil, fmt.Errorf("a modern result must be a JSON object: %w", err)
	}
	for name, value := range m.members {
		member, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("marshalling the %q member: %w", name, err)
		}
		decorated[name] = member
	}
	return json.Marshal(decorated)
}
