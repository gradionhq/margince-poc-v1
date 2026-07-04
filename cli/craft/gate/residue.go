package gate

// The residue gate is the deterministic half of the closed loop. Unlike the
// heuristic reviewer, it is exact: it greps the tree for any CRAFT-FIX/
// CRAFT-DISPUTE marker and fails the merge if any remain. That single rule gives
// the loop three properties:
//
//   - leak-proof: a marker can never reach a merged commit, so review markers
//     never land in the public open-source tree;
//   - no silent ignore: an agent cannot resolve a block by ignoring it — the
//     marker keeps the merge red until the code is actually changed;
//   - convergent: deleting a marker without fixing the code only defers the
//     block — the reviewer re-runs against the still-bad code and re-issues the
//     finding, so the only stable state with zero markers is a genuine PASS.

// CraftToolDir is skipped by the residue gate: the gate's own source and fixtures
// legitimately contain the marker tokens (definitions, tests, golden slop samples).
const CraftToolDir = "cli/craft"

// Residue returns every CRAFT marker left in the tree (outside the tool's own
// dir). A non-empty result means the merge must stay blocked.
func Residue(root string) ([]Marker, error) {
	return Collect(root, CraftToolDir)
}
