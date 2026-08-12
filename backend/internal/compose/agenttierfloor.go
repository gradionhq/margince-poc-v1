// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The contract's per-record-type tier declarations, handed to the tool door.
//
// `operationSpec` applies the tighten-only floor (A34/ADR-0026) to a REST call,
// because a REST call names its operation. A tools/call names a VERB, and a verb
// serving seven record types has one tier for all of them — so `createProject`
// being confirm-first while `createPerson` is not was a decision only one of the
// two doors could act on, and the write a route staged for a human ran unattended
// through the tool that performs it (#982).
//
// This is the same table, read the other way: keyed by (verb, record_type) instead
// of by route, so both doors resolve one contract declaration. DERIVED from
// agentPolicies rather than listed, so an annotation added to crm.yaml binds the
// tool door by regeneration and not by anyone remembering.

import (
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// toolRecordType is one (verb, record type) pair — the key a tool call can be
// resolved by, where a route key cannot reach it.
type toolRecordType struct{ tool, recordType string }

// contractTierFloors is the tier the contract declares for the operation each verb
// ACTUALLY PERFORMS, per record type.
//
// NOT the strictest tier across every route sharing the pair, which is the shape
// this started as and was wrong in the direction that matters: `updateOrganization`
// is auto-execute, while `updateOrganizationFact` — a different effect, writing a
// sidecar row `update_record` cannot reach — is confirm-first. Collapsing them made
// every ordinary organization patch confirm-first on the tool door while REST kept
// it automatic: the same one-credential-two-answers divergence this file exists to
// close, pointing the other way.
//
// So a pair takes the tier of its CANONICAL route: the collection route the
// generic verb's own effect corresponds to. A route carrying anything further — a
// second path parameter, a trailing action segment — is a different operation
// that merely borrows this verb's tier annotation, and its tightening is not this
// verb's to inherit.
//
// The OTHER half of that question — can this verb carry out its effect for this
// record type at all — is answered by the tool, inside Registry.tightened, where
// the tool is in hand. A floor entry for a type the verb cannot serve is inert
// there rather than wrong here, and it is the tool that knows.
var contractTierFloors = func() map[toolRecordType]mcp.RiskTier {
	floors := map[toolRecordType]mcp.RiskTier{}
	for route, pol := range agentPolicies {
		if pol.Access != accessTool || pol.RecordType == "" || pol.Tier != tierConfirmationRequired {
			continue
		}
		if !isCanonicalRecordRoute(route) {
			continue
		}
		floors[toolRecordType{tool: pol.Tool, recordType: string(pol.RecordType)}] = mcp.TierConfirmationRequired
	}
	return floors
}()

// isCanonicalRecordRoute reports whether a route is a whole-record write on the
// record's own collection — `/v1/<collection>` or `/v1/<collection>/{id}` — which
// is the only shape a generic record verb performs.
//
// Read off the route pattern rather than listed, so a sidecar route added later is
// excluded by being what it is. The deeper shapes this rejects are real and
// current: `/v1/organizations/{id}/facts/{factKey}` writes a fact row,
// `/v1/projects/{id}/stakeholders/{person_id}` a membership, and
// `/v1/custom-fields/{id}/retire` performs an action — none of them a field patch
// of the routed record, and none of them reachable through `update_record`.
func isCanonicalRecordRoute(route string) bool {
	_, path, found := strings.Cut(route, " ")
	if !found {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(path, "/v1/"), "/")
	switch len(segments) {
	case 1:
		return segments[0] != ""
	case 2:
		return segments[1] == "{id}"
	default:
		return false
	}
}

// tierFloor answers the tier the contract declares for this verb against this
// record type. It is agents.TierFloor, injected where the registry is built.
func tierFloor(tool, recordType string) (mcp.RiskTier, bool) {
	floor, declared := contractTierFloors[toolRecordType{tool: tool, recordType: recordType}]
	return floor, declared
}

// withContractTierFloor carries the contract's declarations into a registry. The
// composed api surface passes it; the integration lane is what proves that, since
// a comment cannot.
func withContractTierFloor() agents.RegistryOption { return agents.WithTierFloor(tierFloor) }
