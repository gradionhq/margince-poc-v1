// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import "strings"

// Unfence strips a ```json … ``` code fence some models wrap JSON in, so
// one reduction defines what every downstream shape check and gate
// parses — the callers (enrichment extraction, the brief L2 re-order)
// must not each invent their own trim.
func Unfence(text string) string {
	raw := strings.TrimSpace(text)
	raw = strings.TrimPrefix(raw, "```json")
	return strings.Trim(raw, "` \n")
}
