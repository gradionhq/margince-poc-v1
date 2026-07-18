// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package ai owns the model runtime behind the shared/ports/model seam:
// the provider adapters (Anthropic cloud-frontier BYOK, Ollama local,
// and the deterministic offline fake every test drives), the
// SelectBrain factory that turns config into a Client, and the
// credential SecretStripper that runs over every outbound payload.
//
// Model choice is config, not architecture (ADR-0020): vendor names
// appear only here and in ai-routing.yaml — callers name capability
// tiers. The stripper is hygiene, not privacy: privacy is the location
// ladder (A8 revised), so nothing here pseudonymizes PII.
//
// Tables owned: ai_usage (per-workspace metering; feeds the budget
// guardrail), voice_profile and voice_corpus_source (Voice DNA,
// B-E07.4/.5a — the derived artifact + corpus manifest are
// model-adjacent assets, so they live beside the runtime that consumes
// them), ai_call (per-completion trace metadata) and ai_call_payload
// (the opt-in post-stripper captured content, Layer 3 — the payload
// row that privacy's retention and Art. 17 erasure age out and purge).
// Imports shared + platform only; never a sibling module.
package ai
