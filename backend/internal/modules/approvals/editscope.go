// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// What a modify-then-approve edit may change, and what IS the approval.
//
// ADR-0036 §4 lets a human release a corrected version of a staged action. The
// correction is CONTENT — a name, an amount, a date, a body. It is not WHICH
// RECORD the action lands on: that is the approval's identity, and it is what
// the decide-time target-visibility probe (decidable → targetVisible) and the
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
//
// The pin applies to EVERY edit, including agent-staged ones whose redemption
// re-enters the admission gate and would therefore re-check a swapped id on its
// own. That is deliberate: serverProposed is a subtle discriminator to hang a
// security control on — getting it wrong once already produced a consumed
// approval for an effect that never ran — and no kind stages a payload whose
// entity ids a human is meant to correct. Content stays fully editable, which
// is what ADR-0036 §4 is for.

import (
	"bytes"
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
	// "the record it applies to" covers both callers of this type: an entity
	// reference IS the record (assertSameEntityRefs), and operation/path say
	// WHICH CALL runs against it (assertSameCallIdentity) — an edited
	// operation is a different effect on the same or a different record, not
	// a value change, so the message says what both share rather than
	// naming "entity reference" on a path that is not one.
	return "edited_payload changes what runs at " + strings.Join(e.Paths, ", ") +
		"; an edit may correct a staged action's content, never the call or the record it applies to"
}

// refEscape makes an object key unambiguous inside a path. The editor CHOOSES
// the key names, so the encoding has to be injective or the comparison is
// foolable by construction: without escaping, a key literally spelled "a/b"
// renders the same path as the nested {"a":{"b":…}}, and a key "[0]" the same
// as array index 0. Either collision lets an edit move a reference into a
// place the effect no longer reads while this check sees nothing change.
var refEscape = strings.NewReplacer("~", "~0", "/", "~1", "[", "~2")

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
			entityRefs(child, path+"/"+refEscape.Replace(k), out)
		}
	case []any:
		for i, child := range t {
			entityRefs(child, fmt.Sprintf("%s/[%d]", path, i), out)
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

// callIdentityBody is the ONE top-level member of a REST staging that is
// CONTENT — a human is meant to correct it. Every other top-level member says
// WHICH CALL was staged: the transport writes them (compose.canonicalRESTCall)
// and the redemption re-derives them from the retry's own method, URL and
// headers, so an edit that changes one moves the effect to a different
// operation, a different record, or a different precondition while every
// other check still reads the original.
//
// Pinned by EXCLUDING body rather than by naming "operation" and "path"
// deliberately: a hand-maintained allowlist stays correct only until the
// canonical call grows a member nobody remembered to add here — and it will,
// since headers like If-Match and Idempotency-Key are the version pin and the
// retry key, exactly what this guard exists to protect. Denying by default
// means a member the transport starts writing tomorrow is pinned by
// construction, not by someone updating a list.
const callIdentityBody = "body"

// isRESTStaging reports whether a payload carries the members a REST staging
// always writes. A tool staging (the MCP door stages tool arguments, not a
// method/path/body triple) has neither operation nor path, and pinning every
// one of ITS top-level members here — deny-by-default applied past this gate
// — would make every tool-staged approval uneditable; entityRefs governs its
// content instead.
func isRESTStaging(payload map[string]json.RawMessage) bool {
	_, hasOp := payload["operation"]
	_, hasPath := payload["path"]
	return hasOp || hasPath
}

// assertSameCallIdentity refuses an edit that changes which call was staged.
func assertSameCallIdentity(original, edited json.RawMessage) error {
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal(original, &before); err != nil {
		return fmt.Errorf("approvals: decoding a proposed change to compare its call identity: %w", err)
	}
	if err := json.Unmarshal(edited, &after); err != nil {
		return fmt.Errorf("approvals: decoding an edited change to compare its call identity: %w", err)
	}
	if !isRESTStaging(before) && !isRESTStaging(after) {
		return nil
	}
	members := map[string]struct{}{}
	for member := range before {
		if member != callIdentityBody {
			members[member] = struct{}{}
		}
	}
	for member := range after {
		if member != callIdentityBody {
			members[member] = struct{}{}
		}
	}
	var changed []string
	for member := range members {
		was, had := before[member]
		now, has := after[member]
		if had != has || !bytes.Equal(was, now) {
			changed = append(changed, "/"+member)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	// Sorted for the same reason assertSameEntityRefs sorts: the paths come
	// out of a map, and a refusal that reorders itself on every run is one a
	// reviewer cannot diff against the last one.
	sort.Strings(changed)
	return &RetargetedEditError{Paths: changed}
}
