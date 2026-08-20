// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// aiRoutingExample is the name of the file this launcher WRITES. The file the
// api reads is ai-routing.yaml, and the difference is deliberate.
//
// aiFlags treats the presence of ai-routing.yaml as "use real models" and stops
// passing --ai-fake. The parser then rejects an incomplete document at STARTUP,
// so writing a commented-out ai-routing.yaml on first run would trade a working
// installation for one that refuses to boot until the user finished editing a
// file they had not asked for. An example beside it costs nothing and is the
// same gesture the repo itself uses for config/ai-routing.example.yaml.
const aiRoutingExample = "ai-routing.example.yaml"

func (l layout) aiRoutingExamplePath() string {
	return filepath.Join(l.root, aiRoutingExample)
}

// ensureAIRoutingExample writes the example on first run and leaves an existing
// one alone, so an edited copy is never overwritten.
//
// It exists because margince.env advertises GEMINI_API_KEY and the api needs a
// tier→model binding that nothing in the folder showed how to write. A tester
// with a key had to read the backend's source to find the shape, which is the
// bundle failing at the one thing it is for.
func ensureAIRoutingExample(l layout) error {
	path := l.aiRoutingExamplePath()
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(aiRoutingTemplate), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// aiRoutingTemplate is a COMPLETE, valid binding rather than a skeleton: the
// point is that copying it and setting one key works, not that it demonstrates
// the schema. Every tier is Gemini because one provider needs one key.
const aiRoutingTemplate = `# Copy this file to ai-routing.yaml (same folder), then restart Margince.
#
#   copy "ai-routing.example.yaml" "ai-routing.yaml"     Windows
#   cp ai-routing.example.yaml ai-routing.yaml           macOS
#
# Until ai-routing.yaml exists, the AI surfaces answer from an offline fake:
# they respond, but the answers are canned. With it, Margince uses the real
# models below and needs the provider's key in margince.env.
#
# There is deliberately no api_key field here. A cloud key is read from its
# conventional environment variable, which margince.env is the place to set:
#
#   gemini             GEMINI_API_KEY
#   anthropic          ANTHROPIC_API_KEY
#   openai             OPENAI_API_KEY
#   openai_compatible  OPENAI_COMPATIBLE_API_KEY
#
# profile records where this installation is willing to send data:
# eu_hosted, sovereign or cloud_frontier. Gemini is Google-hosted, so a
# Gemini binding is cloud_frontier.
profile: cloud_frontier

# tiers bind the four cost rungs the tasks choose between. All four are
# required, and they may point at the same model.
tiers:
  local_small:
    provider: gemini
    model: gemini-3.1-flash-lite

  cheap_cloud:
    provider: gemini
    model: gemini-3.1-flash-lite

  # premium serves the deep site read's extraction first, and its evidence gate
  # wants verbatim quotes — a flash-lite model here makes those reads come back
  # thin. Bind a mid-tier model at minimum.
  premium:
    provider: gemini
    model: gemini-3.5-flash

  # frontier is the rung above premium. No task selects it today, so binding it
  # costs nothing until one does.
  frontier:
    provider: gemini
    model: gemini-3.1-pro-preview

# embeddings is bound separately from the chat tiers, so retrieval keeps working
# when the chat budget is spent. Required.
embeddings:
  provider: gemini
  model: gemini-embedding-001
  dimensions: 1536
`
