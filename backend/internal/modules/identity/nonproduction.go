// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// WithNonProduction injects the deployment posture the composition root
// resolves from runtimeenv.Environment. Without it /me reports production
// (the fail-closed default: hide the destructive reset action rather than
// risk exposing it under an unwired role).
func (h Handlers) WithNonProduction(nonProduction bool) Handlers {
	h.nonProduction = nonProduction
	return h
}
