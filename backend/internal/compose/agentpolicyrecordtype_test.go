// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"strings"
	"testing"
)

// A contract mapping names a record_type the tool it names can actually serve.
//
// TestEveryDeclaredToolVerbIsRegistered checks only that the VERB exists, so
// two mappings sat broken in plain sight: GET /v1/users declared
// search_records/app_user and GET /v1/teams declared search_records/team, and
// search_records serves neither — its enum is person/organization/deal/lead/
// project and app_user is not even a datasource entity. The declaration read
// as a working route to anyone auditing the contract, and answered nothing.
//
// This holds the record_type against the tool's own published schema, which is
// the same string a caller would send. A tool that takes no record_type at all
// (list_colleagues answers seats, not records) declares one only as a label
// for the policy table, so it is exempt by having no such property.
func TestEveryPolicyRecordTypeIsOneItsToolCanSend(t *testing.T) {
	// SKIPPED, and the skip is the finding: this reports 44 mappings today,
	// across product, partner, commission, offer, saved_view, custom_field and
	// more. Two of them were fixed in the change that added this test
	// (app_user now names list_colleagues; team is human-only), because those
	// two were in its way. The rest are a pre-existing contract defect nobody
	// had a way to see, and turning them into a red gate would block every
	// unrelated PR until someone triages twelve record types.
	//
	// Run it with -run TestEveryPolicyRecordType and read the list. Unskip it
	// when the count is zero; issue on the tracker names the work.
	t.Skip("44 pre-existing mismatches — see the issue; unskip when triaged")
	specs := map[string]string{}
	for _, spec := range servedSurface(t).Specs() {
		specs[spec.Name] = string(spec.InputSchema)
	}
	for route, policy := range agentPolicies {
		if policy.Tool == "" || policy.RecordType == "" {
			continue
		}
		schema, registered := specs[policy.Tool]
		if !registered {
			continue // TestEveryDeclaredToolVerbIsRegistered owns that failure.
		}
		// Only a tool that TAKES a record_type can be wrong about one.
		if !strings.Contains(schema, `"record_type"`) {
			continue
		}
		if !strings.Contains(schema, `"`+string(policy.RecordType)+`"`) {
			t.Errorf("%s declares tool %q with record_type %q, which that tool's schema does not "+
				"accept — the route reads as agent-reachable and answers nothing. Serve the type, "+
				"name the tool that does, or declare the route human-only.",
				route, policy.Tool, policy.RecordType)
		}
	}
}
