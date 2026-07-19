// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Attempt-reason vocabulary (ai_call.attempt_reason, spec §4): why THIS
// attempt ran, distinct from an ordinary first try (which carries "").
const (
	attemptReasonProviderError = "provider_error"
	attemptReasonSchemaInvalid = "schema_invalid"
	attemptReasonBudgetDegrade = "budget_degrade"
)

// logicalCall buffers every attempt of one served-or-failed decision —
// retries, degradations, escalations all included — under one
// LogicalCallID, so the store observes them as a single flush instead of
// several independent, individually-torn-able writes. Exactly one attempt
// carries IsTerminal at any point: append() flips the previously-last
// attempt to non-terminal before adding the new one, so "the last thing
// appended" always names the caller's actual outcome.
type logicalCall struct {
	id       ids.UUID
	attempts []Call
}

func newLogicalCall() *logicalCall {
	return &logicalCall{id: ids.NewV7()}
}

// append records one more attempt under this logical call, numbering it
// and marking it terminal — superseding whichever attempt was terminal
// before.
func (lc *logicalCall) append(c Call) {
	if n := len(lc.attempts); n > 0 {
		lc.attempts[n-1].IsTerminal = false
	}
	c.LogicalCallID = lc.id
	c.Attempt = len(lc.attempts) + 1
	c.IsTerminal = true
	lc.attempts = append(lc.attempts, c)
}

// terminal returns the attempt the caller's outcome came from — the last
// one appended. Panics on an empty logicalCall: flush never calls this on
// one (it checks len(attempts) first), and a caller reaching this on an
// empty buffer is a programming error, not a runtime condition to hide.
func (lc *logicalCall) terminal() Call {
	return lc.attempts[len(lc.attempts)-1]
}

// computeConfigHash digests the four ai_call_config dimension fields into
// the table's primary key: the same task contract, routing config, prompt
// version, and provider params always collapse onto the same row,
// regardless of which workspace or attempt produced it.
func computeConfigHash(taskContractHash, routingConfigHash, promptVersion string, providerParams json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(taskContractHash))
	h.Write([]byte{'|'})
	h.Write([]byte(routingConfigHash))
	h.Write([]byte{'|'})
	h.Write([]byte(promptVersion))
	h.Write([]byte{'|'})
	h.Write(providerParams)
	return hex.EncodeToString(h.Sum(nil))
}

// defaultProviderParams is the config snapshot's provider_params until a
// per-provider tunable (temperature, top_p, …) exists on ProviderConfig to
// snapshot — the routing yaml's tier bindings are already covered by
// RoutingConfigHash, so there is nothing else to capture here yet.
var defaultProviderParams = json.RawMessage("{}")

// newConfigSnapshot builds this Router's config dimension row: the
// generated task-contract hash, the loaded routing yaml's digest, and the
// (currently unset) prompt version and provider params. It is pure — no
// DB access — so it runs once at Router construction; installing the row
// in ai_call_config happens lazily, once per flush, via EnsureConfig.
func newConfigSnapshot(routingConfigHash string) ConfigSnapshot {
	snap := ConfigSnapshot{
		TaskContractHash:  TaskContractHash,
		RoutingConfigHash: routingConfigHash,
		PromptVersion:     "",
		ProviderParams:    defaultProviderParams,
	}
	snap.Hash = computeConfigHash(snap.TaskContractHash, snap.RoutingConfigHash, snap.PromptVersion, snap.ProviderParams)
	return snap
}
