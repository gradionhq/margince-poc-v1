// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package runner

import (
	"slices"
	"strings"
	"testing"
)

// A catalog entry runs with NOBODY watching: it fires on a schedule, on a
// passport its owner lent once, and every tool it names it may call without
// anyone seeing the call first.
//
// That is what makes the confirm-first tier work — a 🟡 call from an unattended
// run stops and waits for a person. A run that could ANSWER an approval would
// answer its own: stage the call, release it, re-issue it, and the tier is a
// formality it walks through by itself. The queue tools exist because a person
// in a conversation can now reach their inbox from it; a scheduled run is the
// case where there is no such person.
//
// So the decide verbs are refused HERE, in the allowlist, rather than at the
// gate: a passport that may decide is exactly what an interactive caller needs,
// and it is the RUN that must not be able to spend it. A spec that grew one of
// these names would be the first unattended self-approval in the tree, and it
// would look like an ordinary line in a list of tools.
func TestNoUnattendedAgentSpecCanAnswerAnApproval(t *testing.T) {
	decideVerbs := []string{"decide_approval", "decide_approval_bundle"}
	specs := Catalog()
	if len(specs) == 0 {
		t.Fatal("the catalog is empty — this gate checked nothing")
	}
	for _, spec := range specs {
		for _, tool := range spec.Tools {
			if slices.Contains(decideVerbs, tool) {
				t.Errorf("agent spec %q names %s, so a scheduled run could release the calls it "+
					"stages for itself — the confirm-first tier is a formality for that agent",
					spec.Name, tool)
			}
		}
	}
	// And the same claim about the goals, which is where the instruction to do
	// it would be written if the tool ever reached one: an agent told to answer
	// what is waiting has been given the intent, and the list above is then the
	// only thing standing in its way.
	for _, spec := range specs {
		goal := strings.ToLower(spec.Goal)
		for _, phrase := range []string{"approve the", "approve any", "approve every", "decide_approval"} {
			if strings.Contains(goal, phrase) {
				t.Errorf("agent spec %q is told to %q — an unattended run does not answer approvals", spec.Name, phrase)
			}
		}
	}
}
