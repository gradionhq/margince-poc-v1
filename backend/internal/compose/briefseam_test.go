// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/briefs"
	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// withheldFromTheTool names each persisted brief field the agent surface does
// NOT serve, and why.
//
// It is a map rather than an omission because the two are indistinguishable
// from the outside: a field left behind on purpose and one left behind by
// accident both produce a well-formed brief with a reason missing.
var withheldFromTheTool = gatekit.Waive(map[string]string{
	"UserID": "the run is the caller's own — naming them back to themselves adds nothing",
	"RevenueNormMinor": "the workspace-wide base the revenue FACTOR was divided by; the factor is " +
		"served already normalized, so the base explains nothing an agent can act on",
})

// Every persisted field of a brief run reaches the tool, or is named as
// withheld.
//
// The mapping is hand-written and the persisted run carries more than the tool
// serves, so the risk is not a wrong value — it is a field quietly left behind,
// which reads as a complete brief with one of its reasons missing. The check is
// derived from the persisted structs, so a field added tomorrow is covered
// without anyone remembering to cover it, and it asserts the VALUE rather than
// the field name: the two shapes deliberately name things differently.
func TestEveryPersistedBriefFieldIsServedOrNamedAsWithheld(t *testing.T) {
	runID, userID, itemID, dealID, evidence := ids.NewV7(), ids.NewV7(), ids.NewV7(), ids.NewV7(), ids.NewV7()
	generated := time.Date(2026, 8, 8, 6, 11, 0, 0, time.UTC)
	asOf := time.Date(2026, 8, 8, 5, 22, 0, 0, time.UTC)
	stateAt := time.Date(2026, 8, 8, 7, 33, 0, 0, time.UTC)
	snoozedUntil := time.Date(2026, 8, 9, 8, 44, 0, 0, time.UTC)
	run := briefs.BriefRun{
		ID: runID, UserID: userID, GeneratedAt: generated, AsOf: asOf,
		CandidateCount: 17, RevenueNormMinor: 918_273,
		Items: []briefs.BriefRunItem{{
			ID: itemID, DealID: dealID, Rank: 3, Composite: 0.815,
			Features: briefs.BriefFeatureVector{
				Winnability: 0.11, Revenue: 0.22, Timing: 0.33, Momentum: 0.44, Warmth: 0.55,
			},
			EvidenceIDs: []ids.UUID{evidence}, State: "snoozed",
			StateAt: &stateAt, SnoozedUntil: &snoozedUntil,
		}},
	}

	served, err := json.Marshal(briefRunToTool(run))
	if err != nil {
		t.Fatalf("marshalling the served run: %v", err)
	}

	assertEveryFieldSurvives(t, "BriefRun", reflect.TypeOf(run), map[string]string{
		"ID": runID.String(), "UserID": "", "GeneratedAt": "2026-08-08T06:11:00Z",
		"AsOf": "2026-08-08T05:22:00Z", "CandidateCount": "17", "RevenueNormMinor": "",
		// The items are covered field by field below; what this row asserts is
		// that the list itself arrived.
		"Items": dealID.String(),
	}, string(served))
	assertEveryFieldSurvives(t, "BriefRunItem", reflect.TypeOf(run.Items[0]), map[string]string{
		"ID": itemID.String(), "DealID": dealID.String(), "Rank": "3", "Composite": "0.815",
		"Features": "0.44", "EvidenceIDs": evidence.String(), "State": "snoozed",
		"StateAt": "2026-08-08T07:33:00Z", "SnoozedUntil": "2026-08-09T08:44:00Z",
	}, string(served))
	// An entry no field reached names something that is gone, which reads as a
	// deliberate omission while certifying nothing.
	withheldFromTheTool.AssertAllMatched(t)
}

// assertEveryFieldSurvives walks a persisted struct's exported fields and
// requires each one's probe value to appear in what the tool served — unless
// the field is named as withheld, in which case it must NOT appear.
//
// It takes the TYPE rather than a value because that is all it reads: the
// values it checks for are the probes, which the caller wrote and this cannot
// re-derive.
func assertEveryFieldSurvives(t *testing.T, shape string, fields reflect.Type, probes map[string]string, served string) {
	t.Helper()
	for i := range fields.NumField() {
		name := fields.Field(i).Name
		probe, described := probes[name]
		if !described {
			t.Fatalf("%s.%s was added and this test has no probe for it, so nothing here says whether "+
				"the tool serves it or drops it", shape, name)
		}
		if withheldFromTheTool.Waived(t, name) {
			if strings.Contains(served, strconv.Quote(name)) {
				t.Errorf("%s.%s is named as withheld and the tool serves it anyway", shape, name)
			}
			continue
		}
		if !strings.Contains(served, probe) {
			t.Errorf("%s.%s does not reach the served brief: %q is nowhere in\n%s", shape, name, probe, served)
		}
	}
}

// An item with no evidence still carries an empty list rather than null. The
// two are different facts on the wire, and an agent reading `null` has to guess
// which one it is looking at.
func TestABriefItemCarriesAnEmptyEvidenceListRatherThanNull(t *testing.T) {
	served := briefRunToTool(briefs.BriefRun{
		Items: []briefs.BriefRunItem{{ID: ids.NewV7(), DealID: ids.NewV7(), Rank: 1}},
	})

	raw, err := json.Marshal(served)
	if err != nil {
		t.Fatalf("marshalling the served run: %v", err)
	}
	if !strings.Contains(string(raw), `"evidence_ids":[]`) {
		t.Errorf("an item with no evidence serves null:\n%s", raw)
	}
}
