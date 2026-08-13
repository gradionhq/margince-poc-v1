// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package licensecheck answers what this installation is entitled to, offline.
//
// It runs the license-validation WebAssembly module margince-constellation
// publishes — bundled under module/ byte-for-byte as published — inside a
// wazero runtime in this process. The module embeds the public keyset it trusts
// and verifies the operator's token against it with no callout of any kind, so
// an air-gapped installation proves its entitlement exactly the way a connected
// one does.
//
// The three values a reader might expect to be configuration are pinned
// constants below. They are not operator choices: the bundled module trusts
// only the production keyset, so a token from any other issuer could never
// verify against it whatever this side passed, and a setting for them would be
// nothing but a way to be wrong.
package licensecheck

import (
	"context"
	_ "embed"
	"strings"
	"time"
)

// The identity of the grant this build accepts, fixed at compile time.
const (
	// issuer is the production license authority. Non-production authorities
	// exist upstream and sign with different keys, which the bundled keyset does
	// not carry — pinning the production name keeps the two layers agreeing.
	issuer = "margince-license-authority"
	// product names this product's grant inside the token's product map. A token
	// that grants other products and not this one is refused.
	product = "margince"
	// generation is the only generation issued today. The module refuses a token
	// granting a different one rather than assuming a future generation is
	// backward-compatible with what this build knows how to honor.
	generation = 0
)

// The bundled module and the release it came from. Both are rewritten together
// by `make license-module`; module_test.go holds the blob to the recorded digest
// so a swapped or truncated one fails the build gate rather than a boot.
//
// The file name says nothing about compression on purpose. Upstream's framing is
// upstream's to change — it moved from gzip to brotli once already — and the host
// reads the format out of the bytes, so a refresh that changes it stays a
// data-only diff instead of also editing this directive. Which artifact was
// fetched is recorded in the digest file beside the blob.
var (
	//go:embed module/licensecheck.wasm.module
	bundledModule []byte
	//go:embed module/VERSION
	moduleVersion string
)

// ModuleVersion is the upstream release tag the bundled module was fetched
// from. It travels with every posture the process reports, because "the license
// was refused" and "the module that refused it is three releases old" are
// different problems and an operator can only tell them apart if the boot log
// names the module.
func ModuleVersion() string { return strings.TrimSpace(moduleVersion) }

// State is what a check concluded.
type State string

const (
	// StateAbsent means no token is configured. An installation without a
	// license runs: there is no callout to fail and no lockout to trip, and
	// every development and CI process in this repository boots this way.
	StateAbsent State = "absent"
	// StateValid means the module verified the token and returned this product's
	// grant, within its expiry plus the grace period the module itself carries.
	StateValid State = "valid"
	// StateRejected means a token was configured and the module would not honor
	// it: an untrusted signature, the wrong issuer, expiry past grace, or no
	// grant for this product at this generation. A module that could not RUN at
	// all lands here too — a validation module this build cannot execute is a
	// broken build, and reading it as an unlicensed installation would turn a
	// packaging mistake into a silent downgrade.
	StateRejected State = "rejected"
)

// SeatsAttribute is the grant attribute carrying how many full seats the
// license admits.
const SeatsAttribute = "seats"

// Posture is one resolved answer about this installation's entitlement.
type Posture struct {
	// State is the conclusion; the rest is detail behind it.
	State State
	// Grants is what the license granted this product, empty unless valid. The
	// attribute set is deliberately open — the token format carries free-form
	// int and bool attributes so a verifier reads older and future licenses
	// without changing — so this is carried whole rather than projected into
	// fields that would drop what this build does not yet know to read.
	Grants Grants
	// Reason is the module's own account of a rejection, empty otherwise. It is
	// operator-facing — a boot error and a log line — and is never served to a
	// client: it describes the installation's configuration, not the caller's
	// request.
	Reason string
	// CheckedAt is when this answer was resolved, so a stale posture is
	// recognizable as one.
	CheckedAt time.Time
}

// Seats reports the full-seat count the license grants. ok is false when there
// is no valid license, or when its grant carries no usable seat count — which
// is not the same as a grant of zero seats, and a caller that collapses the two
// would read "this license does not cap seats" as "this license permits none".
func (p Posture) Seats() (int, bool) {
	if p.State != StateValid {
		return 0, false
	}
	// The module's output arrives as JSON, where every number decodes to
	// float64; the license schema admits only ints and bools, so a value that is
	// not integral did not come from a seat count this build should act on.
	raw, ok := p.Grants[SeatsAttribute].(float64)
	if !ok {
		return 0, false
	}
	seats := int(raw)
	if float64(seats) != raw || seats < 0 {
		return 0, false
	}
	return seats, true
}

// Resolve runs the bundled module against token and reports the posture. now
// stamps CheckedAt, injected so a caller's clock is the one that decides what
// "now" means here; the module checks expiry against the host's real clock
// either way, which is the upstream contract and not ours to fake.
//
// An empty token is absent rather than an error: a configured token that cannot
// be READ is caught where it is read (deployconfig), so by the time a token
// reaches this function, empty means the operator configured none.
func Resolve(ctx context.Context, token string, now time.Time) Posture {
	if strings.TrimSpace(token) == "" {
		return Posture{State: StateAbsent, CheckedAt: now}
	}
	grants, err := check(ctx, bundledModule, issuer, product, generation, token)
	if err != nil {
		return Posture{State: StateRejected, Reason: err.Error(), CheckedAt: now}
	}
	return Posture{State: StateValid, Grants: grants, CheckedAt: now}
}
