// Package gate is the craftsmanship review agent: it assembles a PR's context,
// runs a fresh-session review against the rubric over an inference seam, and
// returns the canonical verdict. See docs/quality/craftsmanship.md and foundation
// architecture/16 (the review agent) + 17 (the learning flywheel).
package gate

import "context"

// Client is the inference seam the reviewer runs over. It mirrors the core
// crm/model.Client seam by shape (provider-agnostic, single-shot completion) so
// the gate stays model-neutral without coupling this build tool to the crm module.
type Client interface {
	Complete(ctx context.Context, prompt string) (string, error)
}
