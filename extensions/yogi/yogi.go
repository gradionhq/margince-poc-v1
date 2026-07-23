// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package yogi is a first-party reference extension shipping one governed
// agent tool: yogi_quote returns a random Yogi Berra quote. It exercises
// the whole served-tool path (ADR-0069) — the published Tool declaration,
// the manifest's autonomy-tier request, and boot registration into the
// same MCP registry and admission gate the core tools ride. The tool is
// read-only with no arguments, so it requests the 🟢 auto-execute tier and
// the read scope: nothing to confirm, nothing to mutate.
package yogi

import (
	"context"
	"encoding/json"
	"math/rand/v2"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// New returns the unit's declaration (the ADR-0069 §4 constructor
// contract the generated composition calls).
func New() extension.Extension {
	return extension.Extension{
		Name:    "yogi",
		Version: "1.0.0",
		Tools: []extension.Tool{{
			Name:           "yogi_quote",
			Version:        "1.0.0",
			Tier:           extension.TierAutoExecute,
			RequestedScope: extension.ScopeRead,
			InputSchema: json.RawMessage(`{
  "type": "object",
  "additionalProperties": false
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "quote": {"type": "string"}
  },
  "required": ["quote"],
  "additionalProperties": false
}`),
			Handle: quote,
		}},
	}
}

// quotes are attributed to Yogi Berra. A short fixed set keeps the tool
// self-contained — no store, no network, nothing to govern beyond read.
var quotes = []string{
	"It ain't over till it's over.",
	"When you come to a fork in the road, take it.",
	"It's like déjà vu all over again.",
	"No one goes there nowadays, it's too crowded.",
	"You can observe a lot by just watching.",
	"The future ain't what it used to be.",
	"We made too many wrong mistakes.",
	"You've got to be very careful if you don't know where you are going, because you might not get there.",
	"Always go to other people's funerals, otherwise they won't come to yours.",
	"If the world were perfect, it wouldn't be.",
}

// quoteOut is the tool's result shape (mirrors OutputSchema).
type quoteOut struct {
	Quote string `json:"quote"`
}

// quote returns a random quote. It takes no arguments — the input is
// ignored rather than decoded — so there is nothing to validate and
// nothing that can fail but the JSON encode.
func quote(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return json.Marshal(quoteOut{Quote: quotes[rand.IntN(len(quotes))]})
}
