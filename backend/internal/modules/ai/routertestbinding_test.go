// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// boundRouter builds a Router over a given binding, for the tests that used to
// set the config-derived fields on the Router directly.
//
// They cannot any more, and the reason is the point of the change: those fields
// move together behind an atomic pointer so a rebind cannot be observed
// half-applied. A test that set one of them in isolation would be constructing
// a state the production code can no longer reach.
func boundRouter(b binding) *Router {
	r := &Router{}
	r.bound.Store(&b)
	return r
}
