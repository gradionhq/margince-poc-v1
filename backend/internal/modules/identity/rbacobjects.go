// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import "github.com/gradionhq/margince/backend/internal/modules/identity/internal/policy"

// RbacObject is an RBAC object name an extension contributes to the
// vocabulary. It is an ALIAS, not a wrapper: identity owns the vocabulary but
// internal/policy owns the grammar, and a wrapper type here would need a
// conversion that could be written without the grammar's Validate — which is
// the one thing standing between a directory an operator dropped in and what a
// stored role document may grant.
type RbacObject = policy.Object

// RegisterRbacObjects extends the RBAC object vocabulary with the objects a
// composed extension set declares. Called once at boot, before any surface
// serves; see policy/composable.go for why the vocabulary has to be composable
// at all and what "boot-only" is guarding.
//
// It lives on identity because identity OWNS the vocabulary — internal/policy
// is unreachable from the composition root, and identity is already the only
// module that can answer "could any principal hold a grant on this object"
// (RBACObjectGrantable).
func RegisterRbacObjects(objects ...RbacObject) error {
	return policy.Register(objects...)
}

// ResetRbacObjectsForTest clears the registered vocabulary. Test-only seam,
// exported because the composition root's tests register objects through
// RegisterRbacObjects and must not leak them into the next test.
func ResetRbacObjectsForTest() {
	policy.ResetRegisteredForTest()
}
