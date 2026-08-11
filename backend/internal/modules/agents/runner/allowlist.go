// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package runner

// The agent allowlist: what a catalog entry may call, and the two doors that
// hold it.
//
// It exists because the SCOPE MODEL cannot express a goal. A passport carries
// scopes, `write` is all-or-nothing across twelve verbs, and an agent that needs
// one of them is handed all twelve — so the catalog entry is the only place that
// can say "this write and no other", because it is the only place that knows
// what the goal is for.
//
// The entry NARROWS and never grants: every call still passes the same
// admission gate against the same passport (ADR-0009 Decision 5 — one registry,
// one audit stream, agent ≤ human). This is a second lock, not a second key.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// errOutsideAgentSpec refuses a tool this run's catalog entry does not name.
//
// A runner-local sentinel and not an apperrors one: that registry is fixed and
// extends only with the spec's interfaces.md §0, and this refusal never reaches
// an HTTP client — it becomes an observation the model re-plans against.
var errOutsideAgentSpec = errors.New("this agent's catalog entry does not include that tool")

// permits reports whether this run may call the named tool.
//
// The refusal is PERMANENT for the run — the allowlist is code and cannot move
// mid-run — so observeRefusal marks it terminal and the model does not spend
// its budget re-planning into the same no.
func (j Job) permits(tool string) error {
	if len(j.Tools) == 0 || slices.Contains(j.Tools, tool) {
		return nil
	}
	return fmt.Errorf("%w: %s", errOutsideAgentSpec, tool)
}

// offeredToJob is what this run's window lists: what the passport admits,
// narrowed to what the catalog entry names.
//
// Applied to the OFFERED set and never to the `known` vocabulary the window
// attributes observations with — a narrowed offer is exactly the kind of
// narrowing that must not relabel a run's own history (window.go §sourceVocabulary).
func offeredToJob(job Job, offered []mcp.ToolSpec) []mcp.ToolSpec {
	if len(job.Tools) == 0 {
		return offered
	}
	kept := make([]mcp.ToolSpec, 0, len(job.Tools))
	for _, spec := range offered {
		if slices.Contains(job.Tools, spec.Name) {
			kept = append(kept, spec)
		}
	}
	return kept
}

// unfundedTools names what the catalog entry asked for and the passport does
// not admit.
//
// ANY shortfall fails the run, and the PARTIAL case is why. An empty
// intersection is obvious; losing one scope is the realistic
// misconfiguration and the dangerous one — a sweep holding five of its six
// tools finds every at-risk deal, silently logs none of them, and reports a
// quiet night. That is a partial sum wearing the label of a total. The spec is
// this goal's own statement of what it needs, so a run that cannot have it is
// misconfigured rather than merely constrained, and the operator is owed the
// name of the missing grant instead of a thin answer.
func unfundedTools(job Job, offered []mcp.ToolSpec) []string {
	if len(job.Tools) == 0 {
		return nil
	}
	admitted := make(map[string]bool, len(offered))
	for _, spec := range offered {
		admitted[spec.Name] = true
	}
	var missing []string
	for _, name := range job.Tools {
		if !admitted[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// invokePermitted is the ONE door to a tool call, and it exists so the
// allowlist cannot be enforced at one entry point and forgotten at the other.
//
// The scope filter alone does not enforce this: Registry.Offered "narrows what
// is advertised and enforces nothing" in its own words, and Registry.Invoke
// admits against the passport, which knows nothing about a catalog entry. So a
// verb left out of the window is still callable — by a model that names it
// anyway, and by an approved redemption — unless something checks here.
func (r *Runner) invokePermitted(ctx context.Context, job Job, tool string, args json.RawMessage) (json.RawMessage, error) {
	if err := job.permits(tool); err != nil {
		return nil, err
	}
	return r.tools.Invoke(ctx, tool, args)
}
