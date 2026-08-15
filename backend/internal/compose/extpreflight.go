// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The boot preflights for what a unit declares it can DO with the outside
// world: land records, run on a clock, and transmit messages.
//
// Split from extensions.go because they share a shape the other preflights do
// not: each validates through the SAME published Validate the manifest
// generator runs, so what a generated manifest accepted cannot be refused at
// boot (or worse, the reverse). Grouped rather than scattered so the next
// capability added has an obvious home.

import (
	"fmt"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// preflightIngress validates one unit's declared ingress sources through the
// same published IngressSource.Validate the manifest generator runs, and
// rejects the same system declared twice.
//
// A duplicate is not harmless here the way a duplicate description would be:
// the system key is half of every landed record's natural key, so two entries
// naming one system are two declarations an operator resolves separately about
// one provenance namespace — and if they ever disagree about Lands, which of
// them the port answered from would be declaration order.
func preflightIngress(e extension.Extension) error {
	seen := make(map[string]bool, len(e.Ingress))
	for _, source := range e.Ingress {
		if err := source.Validate(); err != nil {
			return fmt.Errorf("compose: extension %q: %w", e.Name, err)
		}
		if seen[source.System] {
			return fmt.Errorf("compose: extension %q declares ingress source %q twice", e.Name, source.System)
		}
		seen[source.System] = true
	}
	return nil
}

// preflightJobs validates one unit's scheduled jobs through the same published
// Job.Validate the manifest generator runs, and rejects a job name declared
// twice within the unit — the same fail-closed boundary preflightTools holds,
// for a declaration that reached the composed set outside the generator path.
func preflightJobs(e extension.Extension) error {
	seen := make(map[string]bool, len(e.Jobs))
	for _, job := range e.Jobs {
		if err := job.Validate(); err != nil {
			return fmt.Errorf("compose: extension %q: %w", e.Name, err)
		}
		if seen[job.Name] {
			return fmt.Errorf("compose: extension %q declares job %q twice", e.Name, job.Name)
		}
		seen[job.Name] = true
	}
	return nil
}

// preflightChannels validates one unit's declared channels, and holds the two
// rules a single declaration cannot check for itself (DESIGN-SP5 §9.1).
//
// Channel.Validate already covers the provider grammar and the Send/Live
// pairing. What is added here is unit-vs-unit COLLISION, which is a fact about
// the composed set rather than about one declaration.
//
// The collision against a CORE connector is the sharper one — a unit shadowing
// `telegram` would have every Telegram reply transmitted on the unit's
// credential instead of the workspace's bot, a silent change of who is sending
// rather than a visible failure — and it is held in the reconcile, which is
// where the core's own transport set is known. Asking here would answer from a
// registry that may not be built yet, and pass the collision it exists to catch.
//
// Both refuse at BOOT rather than at the send, because the send is the moment
// the mistake becomes a message somebody receives.
func preflightChannels(e extension.Extension, claimed map[string]extension.Name) error {
	seen := make(map[string]bool, len(e.Channels))
	for _, ch := range e.Channels {
		if err := ch.Validate(); err != nil {
			return fmt.Errorf("compose: extension %q: %w", e.Name, err)
		}
		if seen[ch.Provider] {
			return fmt.Errorf("compose: extension %q declares channel %q twice", e.Name, ch.Provider)
		}
		seen[ch.Provider] = true
		if other, taken := claimed[ch.Provider]; taken {
			return fmt.Errorf("compose: extension %q declares channel %q, already claimed by extension %q — one provider names one transport",
				e.Name, ch.Provider, other)
		}
		claimed[ch.Provider] = e.Name
	}
	return nil
}
