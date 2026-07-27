// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the human sees before they decide.
//
// The approval row's summary is what an inbox renders and what a triaging
// approver acts on. For every action staged through the REST admission gate
// it used to be the method and the path — "Agent REST POST
// /v1/activities/9f2c…/send-email" — with no recipient, no amount, no field
// value anywhere in it. The content existed only in proposed_change, an
// untyped map the surface hands back raw, so the decision a human was asked
// to make was legible only to someone willing to read a JSON envelope.
//
// The summary is now built from the SAME bytes the diff_hash covers and the
// redemption re-checks, so the text a human reads and the call that executes
// cannot disagree. It stays structural rather than free prose: the operation
// in plain words, then the body's own fields and values. The values are
// agent-authored, so approvals.StageInTx sanitizes whatever lands here — this
// file decides WHAT to say, that one decides what may be rendered.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// summaryFieldLimit bounds how many body fields a summary enumerates. A
// patch wider than this is summarized by its field names, which is still
// more than the method and path said.
const summaryFieldLimit = 8

// summaryValueLimit bounds one rendered value, so a single long string
// cannot crowd out the fields after it.
const summaryValueLimit = 48

// restSummary describes the staged call: the operation and its concrete
// path, plus the request body's own top-level fields. A body-less action
// route (send this offer, archive this record) names the operation alone —
// which is the whole change, and says so.
func restSummary(op, method, path string, body []byte) string {
	head := fmt.Sprintf("%s (%s %s)", op, method, path)
	fields := summaryFields(body)
	if len(fields) == 0 {
		return head
	}
	return head + ": " + strings.Join(fields, ", ")
}

// summaryFields renders the body's top-level entries as key=value, sorted so
// two renderings of the same call read the same way. A nested object or
// array is named and counted rather than expanded: the inbox is a summary,
// and proposed_change carries the whole envelope for anyone who wants it.
func summaryFields(body []byte) []string {
	var payload map[string]json.RawMessage
	if len(strings.TrimSpace(string(body))) == 0 || json.Unmarshal(body, &payload) != nil {
		return nil
	}
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	rendered := make([]string, 0, len(keys))
	for _, key := range keys {
		if len(rendered) == summaryFieldLimit {
			rendered = append(rendered, fmt.Sprintf("+%d more", len(keys)-summaryFieldLimit))
			break
		}
		rendered = append(rendered, key+"="+summaryValue(payload[key]))
	}
	return rendered
}

// summaryValue renders one JSON value for a human. Strings lose their quotes
// (the reader wants the name, not its encoding) and everything is bounded.
func summaryValue(raw json.RawMessage) string {
	// null is recognized BEFORE the string probe, because unmarshaling null
	// into a plain string SUCCEEDS and leaves it empty (encoding/json: null
	// into a non-nullable type "has no effect and produces no error"). Left to
	// the string branch, clearing a field would render exactly like setting it
	// to "" — and clearing an owner or a close date is precisely the change the
	// approving human most needs to see.
	if strings.TrimSpace(string(raw)) == "null" {
		return "null"
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return truncateValue(s)
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		return fmt.Sprintf("[%d]", len(arr))
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		return fmt.Sprintf("{%d fields}", len(obj))
	}
	return truncateValue(string(raw)) // numbers, booleans, null
}

func truncateValue(s string) string {
	if len(s) <= summaryValueLimit {
		return s
	}
	cut := summaryValueLimit
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// isRuneStart reports whether b begins a UTF-8 rune (a continuation byte is
// 10xxxxxx). Cutting mid-rune would put an invalid sequence in front of a
// human, which the summary sanitizer would then drop as noise.
func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
