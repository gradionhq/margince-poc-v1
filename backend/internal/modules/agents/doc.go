// Package agents owns the governed agent surface (Layer 1): the MCP
// tool registry, the admission gate (scope ∧ tier ∧ the read/full seat
// ceiling; per-agent quota is specified but not yet enforced), the
// approval flow, and the Surface-B reasoning loop. It reaches records only
// through the sor seam.
package agents
