// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"strings"
	"testing"
)

// TestBothAgentAuthenticationPathsCarryTheSameLivenessRule guards the shape,
// not the SQL: an agent authenticates either by bearer token or by passport id,
// and a liveness rule that binds on one of those and not the other is a live
// credential on whichever path was missed. The rule is therefore written once
// and both paths assemble their statement from it — this test fails if a future
// refactor inlines either query, which is the moment the drift becomes
// possible, rather than in production when it is exploited.
func TestBothAgentAuthenticationPathsCarryTheSameLivenessRule(t *testing.T) {
	queries := map[string]string{
		"by token": agentAuthQuery(agentByHashPredicate),
		"by id":    agentAuthQuery(agentByIDPredicate),
	}
	for path, query := range queries {
		if !strings.Contains(query, agentLivenessJoins) {
			t.Errorf("the %s path omits the liveness joins:\n%s", path, query)
		}
		if !strings.Contains(query, agentLivenessPredicate) {
			t.Errorf("the %s path omits the liveness predicate:\n%s", path, query)
		}
	}

	// And the two differ in NOTHING but the column that names the passport:
	// any other difference is a second place the rules can rot apart.
	byToken := strings.Replace(queries["by token"], agentByHashPredicate, "<predicate>", 1)
	byID := strings.Replace(queries["by id"], agentByIDPredicate, "<predicate>", 1)
	if byToken != byID {
		t.Errorf("the two authentication paths differ beyond their predicate:\n%s\n---\n%s", byToken, byID)
	}
}

// A locally minted passport answers to no OAuth grant, so the liveness rule
// must be a condition on the joined rows and never a requirement that they
// exist — an inner join, or dropping the IS NULL arm, would take the whole A1
// surface down with it.
func TestTheLivenessRuleExemptsLocallyMintedPassports(t *testing.T) {
	query := agentAuthQuery(agentByHashPredicate)
	if strings.Count(query, "LEFT JOIN") != 2 {
		t.Errorf("the connection joins are not both LEFT JOINs, so a passport with no grant cannot match:\n%s", query)
	}
	if !strings.Contains(query, "p.oauth_grant_id IS NULL") {
		t.Errorf("the liveness predicate has no exemption for a passport that answers to no grant:\n%s", query)
	}
}
