// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// What a modify-then-approve edit may change, and what IS the approval.
//
// ADR-0036 §4 lets a human release a corrected version of a staged action. The
// correction is CONTENT — a name, an amount, a date, a body. It is not WHICH
// RECORD the action lands on: that is the approval's identity, and it is what
// the decide-time row-scope probe (decidable → targetVisible) and the
// redemption-time version pin were both evaluated against, before the edit
// existed.
//
// The gap that makes this load-bearing: every server-proposed effect resolves
// the record it writes from an entity id INSIDE the payload rather than from
// approval.target_entity_id, and several run under a system principal, which
// makes auth.Require return nil and empties every row-scope clause. So an edit
// that swaps an id turns an approval a human legitimately holds into a write
// against a record their own row scope hides — while the version pin still
// passes, because it re-reads the untouched original target. Pinning the
// references is what keeps "the action that was admitted" and "the action that
// runs" the same action.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// RetargetedEditError maps to 422: the payload is well-formed, but it describes
// an action against different records than the one that was admitted.
type RetargetedEditError struct{ Paths []string }

func (e *RetargetedEditError) Error() string {
	return "edited_payload changes the entity reference at " + strings.Join(e.Paths, ", ") +
		"; an edit may correct a staged action's content, never the record it applies to"
}

// entityRefs collects every entity id in a decoded proposed change, keyed by
// its JSON path. It walks to any depth: a nested object or a list of ids names
// records exactly as a top-level field does, so a rule that only looked at the
// top level would leave the nested spelling of the same swap open.
//
//craft:ignore naked-any the input IS an arbitrary decoded JSON value — proposed_change is open by kind (contract: additionalProperties true), so a concrete type would be a claim about payload shape this must not make
func entityRefs(v any, path string, out map[string]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			entityRefs(child, path+"."+k, out)
		}
	case []any:
		for i, child := range t {
			entityRefs(child, fmt.Sprintf("%s[%d]", path, i), out)
		}
	case string:
		if _, err := ids.Parse(t); err == nil {
			out[path] = t
		}
	}
}

// refsOf decodes one proposed change and returns its entity references.
func refsOf(raw json.RawMessage) (map[string]string, error) {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("approvals: decoding a proposed change to compare its entity references: %w", err)
	}
	refs := map[string]string{}
	entityRefs(decoded, "", refs)
	return refs, nil
}

// assertSameEntityRefs refuses an edit that adds, drops or repoints ANY entity
// reference the staged proposal carried. Equality of the whole set — not just
// of the ids the effect happens to read today — is what makes this survive a
// new kind: an executor added tomorrow that resolves a record from a field
// nobody pinned would otherwise reopen the hole silently.
func assertSameEntityRefs(original, edited json.RawMessage) error {
	before, err := refsOf(original)
	if err != nil {
		return err
	}
	after, err := refsOf(edited)
	if err != nil {
		return err
	}
	var changed []string
	for path, was := range before {
		if now, ok := after[path]; !ok || now != was {
			changed = append(changed, path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			changed = append(changed, path)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	// Sorted so the refusal reads the same on every run — the paths come out
	// of a map, and an error message that reorders itself is one a reviewer
	// cannot diff against the last one.
	sort.Strings(changed)
	return &RetargetedEditError{Paths: changed}
}
