// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// The generated policy table and the live tool registry declare the same
// tier truth from two sources (contract annotation vs ToolSpec). The
// contract may TIGHTEN (a 🟢-verbed op declared 🟡) but must never sit
// BELOW the tool's own tier — that asymmetry would let the REST twin of a
// 🟡 tool run 🟢. Derived from both live artifacts, not maintained as a
// list.
func TestContractTierNeverBelowRegistryTier(t *testing.T) {
	registry := agents.NewRegistry(stubApprovals{}, nil)
	agents.RegisterCoreTools(registry, nil, nil, nil, nil)

	for route, pol := range agentPolicies {
		if pol.Access != accessTool {
			continue
		}
		spec, registered := registry.Spec(pol.Tool)
		if !registered {
			continue // unregistered verbs default-deny (🟡) or admit at the annotation tier — never below it
		}
		switch spec.Tier {
		case mcp.TierConfirmationRequired:
			if pol.Tier != tierConfirmationRequired {
				t.Errorf("%s (%s): tool %s is 🟡 but the contract annotates %q", route, pol.Op, pol.Tool, pol.Tier)
			}
		case mcp.TierDynamic:
			if pol.Tier != tierDynamic && pol.Tier != tierConfirmationRequired {
				t.Errorf("%s (%s): tool %s is dynamic but the contract annotates %q — the resolver would never run", route, pol.Op, pol.Tool, pol.Tier)
			}
		}
	}
}

// Human-edit precedence is per FIELD, not per call (interfaces.md §2.1):
// update_record is 🟢 in the tool registry AND in every contract
// annotation that rides it — the split into a 🟡 staged residue happens
// inside the auto-execute Update path, never by re-tiering the whole verb. A
// dynamic or confirmation_required update_record annotation would resurrect whole-patch
// staging, so both artifacts are pinned.
func TestUpdateRecordIsAutoExecuteOnBothArtifacts(t *testing.T) {
	registry := agents.NewRegistry(stubApprovals{}, nil)
	agents.RegisterCoreTools(registry, nil, nil, nil, nil)

	spec, ok := registry.Spec("update_record")
	if !ok || spec.Tier != mcp.TierAutoExecute {
		t.Fatalf("update_record registry tier = %v (registered %v), want TierAutoExecute", spec.Tier, ok)
	}
	seen := 0
	for route, pol := range agentPolicies {
		if pol.Tool != "update_record" {
			continue
		}
		seen++
		// DELETE-shaped rides may tighten to confirmation_required (archive semantics);
		// a field-patch op must be auto_execute and none may say dynamic.
		if pol.Tier != tierAutoExecute && pol.Tier != tierConfirmationRequired {
			t.Errorf("%s (%s): update_record annotated %q — the per-field split runs inside the auto-execute path", route, pol.Op, pol.Tier)
		}
	}
	if seen == 0 {
		t.Fatal("no update_record operations in the generated policy — the pin no longer covers anything")
	}
}

// The self-approval class and the config surface the advance_deal floor
// reads must stay human-only in the contract: an agent may stage a 🟡
// action but never approve one — its own least of all — and must not
// move which stages count as won/lost.
func TestGovernanceOperationsAreHumanOnly(t *testing.T) {
	humanOnly := map[string]bool{
		"approveApproval": true, "rejectApproval": true,
		"recordConsent": true, "createConsentPurpose": true,
		"createDataSubjectRequest": true, "updateDataSubjectRequest": true,
		"createPipeline": true, "updatePipeline": true,
		"createStage": true, "updateStage": true,
		"issuePassport": true, "revokePassport": true,
		"issueDoubleOptIn": true,
		// The overlay entries below are pinned because the contract
		// annotation alone is a line someone can re-add: without them a
		// revert restores an agent-reachable op with every other gate,
		// arch and drift test still green.
		//
		// The overlay→native cutover: the typed confirmation phrase is
		// the human-intent control, so an agent supplying it in staged
		// arguments would collapse confirm-first to one approval click
		// on a one-way, estate-wide change.
		"preflightOverlayFlip": true, "executeOverlayFlip": true,
		// The export is human-only for a different reason: it streams
		// the whole estate, audit log included, in a single GET.
		"downloadOverlayExport": true,
		// The consent screen's read model answers with the granting
		// human's whole lendable-passport inventory — ids, labels,
		// scopes, expiries — the same credential class as
		// GET /v1/passports (issuePassport's sibling read, pinned
		// above). Lending authority is a decision only the human
		// holding it may take, and anything that can enumerate the
		// list can pick from it.
		"getConsentRequest": true,
	}
	seen := map[string]bool{}
	for route, pol := range agentPolicies {
		if humanOnly[pol.Op] {
			seen[pol.Op] = true
			if pol.Access != accessHumanOnly {
				t.Errorf("%s (%s) must be human-only, contract says %q", route, pol.Op, pol.Access)
			}
		}
	}
	for op := range humanOnly {
		if !seen[op] {
			t.Errorf("governance operation %s vanished from the mutating policy table — the human-only pin no longer covers it", op)
		}
	}
}

// operationSpec applies the tighten-only rule: the contract can raise an
// op above its verb's base tier (archive-by-DELETE rides update_record
// but stays 🟡) and a dynamic annotation without a resolvable dynamic
// tool fails closed.
func TestOperationSpecTightenOnly(t *testing.T) {
	registry := agents.NewRegistry(stubApprovals{}, nil)
	agents.RegisterCoreTools(registry, nil, nil, nil, nil)

	spec, _, ok := operationSpec(agentPolicy{Op: "archivePerson", Access: accessTool, Tool: "update_record", Tier: tierConfirmationRequired}, registry)
	if !ok || spec.Tier != mcp.TierConfirmationRequired {
		t.Fatalf("🟡 annotation over a 🟢 verb → tier %v ok=%v, want TierConfirmationRequired (tighten-only)", spec.Tier, ok)
	}

	if _, _, ok := operationSpec(agentPolicy{Op: "phantom", Access: accessTool, Tool: "no_such_tool", Tier: tierDynamic}, registry); ok {
		t.Fatal("dynamic annotation without a registered dynamic tool must fail closed")
	}
}

// The redemption key is content, not serialization: key order and
// whitespace hash equal; a changed value, path, or operation does not.
func TestCanonicalRESTCallHashesContent(t *testing.T) {
	_, h1, err := canonicalRESTCall("updatePerson", "/v1/people/x", http.Header{}, []byte(`{"b":2,"a":1}`), keyBindsTheRetry)
	if err != nil {
		t.Fatal(err)
	}
	_, h2, err := canonicalRESTCall("updatePerson", "/v1/people/x", http.Header{}, []byte(` {"a": 1, "b": 2} `), keyBindsTheRetry)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("equivalent bodies must hash equal — redemption would refuse the identical call")
	}
	// Both errors are asserted, not dropped: a refused input hashes to "", and an
	// empty hash satisfies the inequality below while proving nothing about a
	// different body or a different path.
	_, h3, err := canonicalRESTCall("updatePerson", "/v1/people/x", http.Header{}, []byte(`{"a":1,"b":3}`), keyBindsTheRetry)
	if err != nil {
		t.Fatal(err)
	}
	_, h4, err := canonicalRESTCall("updatePerson", "/v1/people/y", http.Header{}, []byte(`{"a":1,"b":2}`), keyBindsTheRetry)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h3 || h1 == h4 {
		t.Fatal("a different body or target must not ride the staged approval")
	}
	if _, _, err := canonicalRESTCall("op", "/p", http.Header{}, []byte(`{broken`), keyBindsTheRetry); err == nil {
		t.Fatal("malformed JSON must be refused, not hashed")
	}
	_, hEmpty, err := canonicalRESTCall("archivePerson", "/v1/people/x", http.Header{}, nil, keyBindsTheRetry)
	if err != nil || hEmpty == "" {
		t.Fatalf("bodyless mutations (DELETE) must canonicalize: %v", err)
	}
}

// A call carrying Idempotency-Key must not hash the same as one without it:
// the header decides whether a retry is a fresh effect, a replay or a
// conflict, and it reaches the handler untouched.
//
// If-Match is deliberately NOT exercised here the same way: the gate itself
// overwrites it with the server-side version pin on redemption
// (agentgatestaging.go), so the caller's own If-Match never reaches the
// handler unchanged, and hashing it would make the split-update residue
// (agentsplit.go) unredeemable by the very version the agent just read.
// canonicalHeaders' own doc comment carries the full reasoning.
func TestTheCanonicalCallBindsIdempotencyKey(t *testing.T) {
	body := []byte(`{"to_stage_id":"a"}`)
	withKey := http.Header{}
	withKey.Set("Idempotency-Key", "k")
	base, _, err := canonicalRESTCall("advanceDeal", "/v1/deals/d/advance", http.Header{}, body, keyBindsTheRetry)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := canonicalRESTCall("advanceDeal", "/v1/deals/d/advance", withKey, body, keyBindsTheRetry)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(base, other) {
		t.Error("a call carrying Idempotency-Key canonicalized identically to one without it")
	}
}

// A header that never changes what a handler does — Authorization names WHO
// is calling, User-Agent names WHAT is calling, neither decides WHAT RUNS —
// must leave the hash untouched. This is the half TestTheCanonicalCallBindsIdempotencyKey
// cannot prove on its own: that test shows a hashed header changes the hash,
// this one shows an unhashed header does not, which is what stops a future
// header creeping into the digest by sitting next to one that belongs there.
func TestTheCanonicalCallIgnoresHeadersThatDoNotChangeExecution(t *testing.T) {
	body := []byte(`{"to_stage_id":"a"}`)
	base, _, err := canonicalRESTCall("advanceDeal", "/v1/deals/d/advance", http.Header{}, body, keyBindsTheRetry)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []http.Header{
		{"Authorization": []string{"Bearer secret"}},
		{"User-Agent": []string{"some-agent/1.0"}},
	} {
		other, _, err := canonicalRESTCall("advanceDeal", "/v1/deals/d/advance", h, body, keyBindsTheRetry)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(base, other) {
			t.Errorf("a call carrying %v canonicalized differently from one without it", h)
		}
	}
}

// A call carrying no hashed header must canonicalize BYTE-FOR-BYTE as it did
// before the `headers` member existed, or every pending REST-agent approval
// and every issued redemption token minted before this task ships becomes
// unredeemable. Pinned against literal golden bytes and a literal golden
// hash — not two in-process hashes compared to each other, which cannot see
// a canonical-form change that moves both sides of the comparison together.
func TestTheCanonicalCallIsByteCompatibleWithoutAHashedHeader(t *testing.T) {
	const wantCanonical = `{"body":{"a":1,"b":2},"operation":"updatePerson","path":"/v1/people/x"}`
	const wantHash = "8924889e55733baa0964dc3aa1929f9af6f315b49ed82d4c11e8c2c1190bba84"
	canonical, hash, err := canonicalRESTCall("updatePerson", "/v1/people/x", http.Header{}, []byte(`{"a":1,"b":2}`), keyBindsTheRetry)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != wantCanonical {
		t.Errorf("canonical = %s, want %s", canonical, wantCanonical)
	}
	if hash != wantHash {
		t.Errorf("hash = %s, want %s", hash, wantHash)
	}
}

// encoding/json replaces invalid bytes with U+FFFD, so two distinct wire calls arrive as
// one string and would redeem each other's approval. The tool door rejects this before
// decoding (reserved.go); this door did not.
func TestTheCanonicalCallRefusesInvalidUTF8(t *testing.T) {
	if _, _, err := canonicalRESTCall("x", "/v1/x", http.Header{}, []byte("{\"a\":\"\xff\"}"), keyBindsTheRetry); err == nil {
		t.Error("a body carrying invalid UTF-8 canonicalized cleanly")
	}
}

// An escaped unpaired surrogate is valid UTF-8 on the wire and still decodes to U+FFFD,
// so the byte check above cannot see it. reserved.go checks both halves; so must this.
func TestTheCanonicalCallRefusesAnEscapedUnpairedSurrogate(t *testing.T) {
	if _, _, err := canonicalRESTCall("x", "/v1/x", http.Header{}, []byte(`{"a":"\udcff"}`), keyBindsTheRetry); err == nil {
		t.Error("an escaped unpaired surrogate canonicalized cleanly")
	}
}

// json.Unmarshal into `any` refuses a body that is not exactly one JSON value —
// unlike json.Decoder.Decode, which stops after the first value and leaves the
// rest unread. Without this, `{"a":1} garbage` and `{"a":1}{"b":2}` would both
// canonicalize to `{"a":1}` and hash identically to it, letting a distinct wire
// body redeem another call's approval. The same one-value boundary httperr.Decode
// and modules/agents/badargs.go draw elsewhere in this tree.
func TestTheCanonicalCallRefusesTrailingContentAfterTheJSONValue(t *testing.T) {
	for _, body := range []string{`{"a":1} garbage`, `{"a":1}{"b":2}`} {
		if _, _, err := canonicalRESTCall("x", "/v1/x", http.Header{}, []byte(body), keyBindsTheRetry); err == nil {
			t.Errorf("body %q canonicalized cleanly despite trailing content after the JSON value", body)
		}
	}
}

// A confirm-first route that resolves a concrete {id} must declare what
// KIND of record that id names. The approvals surface scopes an inbox row
// by probing its target's own/team visibility, and it can only probe a
// type it was told: a staged row carrying an id with no type is decidable
// by everyone holding the object grant, whatever their row scope, and its
// summary and proposed change sit in all their inboxes.
//
// Derived from the generated table and the route patterns themselves, so a
// NEW confirm-first {id} route that forgets record_type fails here rather
// than in production.
func TestConfirmFirstIdRoutesDeclareARecordType(t *testing.T) {
	seen := 0
	for route, pol := range agentPolicies {
		if pol.Access != accessTool || pol.Tier == tierAutoExecute || !strings.Contains(route, "{id}") {
			continue
		}
		seen++
		if pol.RecordType == "" {
			t.Errorf("%s (%s) stages against a concrete record but declares no record_type — the approval it mints cannot be row-scoped", route, pol.Op)
		}
	}
	if seen == 0 {
		t.Fatal("no confirm-first {id} routes in the generated policy — the pin no longer covers anything")
	}
}
