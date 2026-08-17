// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// WithNonProduction injects the deployment posture the composition root
// resolves from runtimeenv.Environment. Without it /me reports production
// (the fail-closed default).
func (h Handlers) WithNonProduction(nonProduction bool) Handlers {
	h.nonProduction = nonProduction
	return h
}

// WithDataResetAvailable injects whether this installation armed the data
// reset (operations.allow_data_reset). It is a SEPARATE fact from the posture,
// because a deployment being non-production is not consent to purge its tenant
// data — that is what the switch is for.
//
// It is the same value the endpoint gates on, so the action a client offers and
// the route it would call cannot disagree. Without it /me reports unavailable,
// the fail-closed default that hides the action rather than risk offering one
// the server will refuse.
func (h Handlers) WithDataResetAvailable(available bool) Handlers {
	h.dataResetAvailable = available
	return h
}
