// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package password

// The Argon2id cost parameters the INTEGRATION TEST BUILD uses, and only it.
//
// The harness runs the real first-boot ceremony on every test — EnsureInstallation,
// login, change-password, login again — which is five derivations before a test
// asserts anything. At the production parameters that is ~94 ms per test, and
// argon2.processBlocks measures 28.5% of all CPU samples in the overlay package.
// The fixture needs the FLOW to be real, not the work factor: no test in this
// tree asserts the cost, and none can, because Verify reads a hash's parameters
// out of the hash itself.
//
// Measured on this tree, per derivation:
//
//	t=2 m=19MiB p=1   18.861ms   production
//	t=1 m=64KiB p=1       48µs   this file — 395x
//
// 64 KiB rather than the 8 KiB spec floor (m >= 8*p) on purpose. The floor
// saves a further 31µs, which is nothing against ~2200 derivations, and sits
// exactly on the boundary a future argon2 release is most likely to tighten.
//
// The algorithm is untouched: this is the same unmodified golang.org/x/crypto/argon2
// IDKey call the product makes, doing less work.
//
// These values are COMPILED OUT of every production binary rather than refused
// at runtime, which is why this is a build tag and not a config field. A config
// seam would put the weak value into the deployment's vocabulary, and a PHC hash
// carries its own parameters — so one hash written under a wrong config verifies
// successfully forever, because nothing re-hashes on login.
const (
	timeCost  = 1
	memoryKiB = 64
	threads   = 1
)
