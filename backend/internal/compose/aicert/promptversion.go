// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// The certification stamp: what a record says it was scored against. It lives
// beside the runner rather than inside it because it drives nothing — it is a
// pure digest over the corpus a run consumed, and staleness is read off it long
// after that run is over.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// PromptVersion is a task's certification stamp: a digest of the exact
// SCENARIOS a run was scored against.
//
// It used to be the constant "v1", which meant a record could never stop being a
// proof — edit a scenario and the committed record went on claiming "certified"
// for text that no longer existed. Deriving it from content makes staleness
// visible instead: a record whose stamp is not the one this corpus computes was
// scored against something else, and says so.
//
// The WHOLE scenario is digested, not just its fixture. The rubric is read to
// the grader, the expected answer decides what "right" means, and the caps and
// bands decide what passes — each of them changes what a score means, so each of
// them has to move the stamp. Nothing is canonicalised out: no per-call data
// boundary appears in a scenario at all now, because the product mints every
// fence itself when the case builds the request.
//
// What it does NOT cover: the product's own code. A scenario is the data a site
// is given; the request built from it is a pure function of that data and the
// code that ships, and that code is covered by the commit that changes it.
func PromptVersion(scenarios []Scenario) string {
	ordered := make([]string, 0, len(scenarios))
	for _, sc := range scenarios {
		// Hash each scenario on its own, then order the digests: joining raw
		// fields would let text shift across a separator and collide.
		encoded, err := json.Marshal(sc)
		if err != nil {
			// Scenario is a plain data struct loaded from YAML; a value that
			// cannot be marshalled is a programming error, not input.
			panic(fmt.Sprintf("aicert: scenario %q cannot be digested: %v", sc.Name, err))
		}
		sum := sha256.Sum256(encoded)
		ordered = append(ordered, hex.EncodeToString(sum[:]))
	}
	sort.Strings(ordered)
	sum := sha256.Sum256([]byte(strings.Join(ordered, "")))
	return "p" + hex.EncodeToString(sum[:16])
}
