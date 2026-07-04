// Package agents owns the governed agent surface (Layer 1): the MCP
// tool registry, the admission gate (scope ∧ tier ∧ the read/full seat
// ceiling; per-agent quota is specified but not yet enforced), the
// approval flow, and the Surface-B reasoning loop. It reaches records only
// through the datasource seam.
//
// Tables owned: none — the tool surface holds no state of its own;
// records belong to the domain modules (reached via the injected
// datasource provider) and staged actions to approvals (reached via the
// injected adapter). Imports shared + platform only; never a sibling
// module.
package agents
