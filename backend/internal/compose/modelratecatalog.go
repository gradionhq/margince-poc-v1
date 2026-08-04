// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Reducing a provider's MODEL CATALOG to something a pricing extraction can
// ground in: a broker publishes hundreds of models as one line of JSON, and each
// of those two facts breaks the extraction in its own way. The crawl that
// consumes this lives in modelraterefresh.go.

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// boundProviderNames lists the providers the routing binds, sorted, so a
// mismatch error can show the operator what the other file actually says.
func boundProviderNames(bound map[string]map[string]bool) []string {
	names := make([]string, 0, len(bound))
	for provider := range bound {
		names = append(names, provider)
	}
	sort.Strings(names)
	return names
}

// catalogPassages reduces a JSON model catalog to ONE LINE PER MODEL, keeping
// only the models this deployment's routing binds. It returns the reduced text,
// how many models survived, and whether the body was a catalog at all.
//
// Two faults it fixes, both of which a byte count hides:
//
//   - A catalog is served as one line, and numberPassages splits on newlines —
//     so the whole document numbered to a SINGLE passage [s0]. Every extracted
//     row could only cite s0, which made the evidence gate vacuous: it passed
//     because there was nothing to disagree with. One line per model gives each
//     row a passage that actually grounds it.
//   - A full catalog asks the model for hundreds of rows. OpenRouter's carries
//     337, about 23k output tokens against an 8192 ceiling, so the reply was
//     truncated mid-JSON and failed to parse EVERY time — a deterministic
//     failure no model quality could have fixed.
//
// Each surviving model is re-emitted as its own compact JSON object, unedited
// apart from being separated: the code selects by identity and never reads,
// converts or rewrites a price. Interpreting the numbers stays the model's job,
// behind the evidence gate and the confirm-first approval that follow.
//
// An empty bound set keeps every model — the honest reading of "this deployment
// binds nothing on this provider to filter by". The caller, not this function,
// decides whether that is acceptable: it knows whether the emptiness means
// "nothing wired" or "everything this provider binds is missing".
func catalogPassages(body string, bound map[string]bool) (string, int, bool) {
	var catalog struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &catalog); err != nil || catalog.Data == nil {
		return "", 0, false
	}
	var b strings.Builder
	kept := 0
	for _, entry := range catalog.Data {
		var identity struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(entry, &identity); err != nil || strings.TrimSpace(identity.ID) == "" {
			continue // an entry naming no model cannot be matched to a rate row
		}
		if len(bound) > 0 && !bound[identity.ID] {
			continue
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, entry); err != nil {
			continue
		}
		b.Write(compact.Bytes())
		b.WriteByte('\n')
		kept++
	}
	return b.String(), kept, true
}
