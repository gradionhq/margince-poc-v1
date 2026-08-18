// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package buildinfo carries the two immutable facts about the build this binary
// came from: the revision it was compiled at, and the release it was published
// as. Both are stamped at link time, both are absent from an ordinary `go
// build`, and both are compared the same way — but they carry different
// consequences, which is the distinction to hold on to when reading this file.
//
// THE REVISION IS DIAGNOSTIC METADATA, NOT AN INTEGRITY SIGNATURE. What it
// exists for is skew: the api and the web tier deploy separately, so a document
// the api fetched may have been built from a different commit than the api
// reading it. Transport integrity rests on HTTPS and on control of the origin;
// this only answers "are these two halves the same build", and even then it
// reports rather than refuses — a rolling deploy would otherwise take the views
// down for the length of the rollout.
//
// THE RELEASE VERSION DOES REFUSE, and that is the difference to hold on to. A
// set whose roles come from different releases will stay wrong until somebody
// re-pulls it, so skew here is not a rolling deploy passing through and reporting
// it would not be enough. Why such a set can exist at all, and which role decides
// what, is written down once in internal/compose/releaseversion.go.
//
// UNKNOWN IS THE DEFAULT AND IT DISABLES BOTH COMPARISONS. A developer's binary
// is built from a dirty worktree, where a commit SHA describes nothing and there
// is no release at all; equality there would mean nothing, and inequality would
// alarm on — or, for the release version, refuse — every local run.
package buildinfo

// Revision is set at link time:
//
//	go build -ldflags "-X <this package>.Revision=$MARGINCE_BUILD_REVISION"
//
// CI passes the same value as a build arg to the api and the web images, so the
// two halves can be compared. Left empty by an ordinary `go build`, which is
// what Unknown answers for.
var Revision string

// ReleaseVersion is the release this binary was published as, set at link time:
//
//	go build -ldflags "-X <this package>.ReleaseVersion=$MARGINCE_RELEASE_VERSION"
//
// One value for the whole role set, spelled once in docker-bake.hcl (where it is
// also the image tag and the OCI version label) and passed to every role's build
// stage, because "do these roles come from the same release" is a question only a
// single source of the answer can settle. Left "dev" by a plain `docker build`
// and empty by a bare `go build`, both of which Unknown answers for.
//
// The value is the constellation release version (`YYYY.<edition>`; the PoC
// pipeline cuts `1970.<build>`). It is compared for EQUALITY only — never
// ordered. A set is either one release or it is not.
var ReleaseVersion string

// Unknown is the value a comparison must not be made against — either side
// absent, or the literal "dev".
//
// `make dev` deliberately does NOT stamp the revision: it passes the real commit
// to both halves of the stack so a local run exercises the same comparison a
// deployment does, and an operator can force a mismatch by exporting
// MARGINCE_BUILD_REVISION before it. "dev" is what dev.sh falls back to outside
// a git checkout, and what any build that declines to stamp can use.
//
// It is also the release version a local stack carries, and there the default
// matters more: an unstamped local build must never refuse to boot over a
// release it does not have. An operator who wants to see the guard work exports
// MARGINCE_RELEASE_VERSION for one role and not the other.
const Unknown = "dev"

// Comparable reports whether a build fact — a revision or a release version — is
// one worth comparing. Empty and "dev" are both "this build does not know", and
// a comparison against either is a difference that means nothing.
func Comparable(fact string) bool {
	return fact != "" && fact != Unknown
}

// SkewBetween reports whether two build facts are known AND different, which is
// the only combination that says anything. Everything else — either side
// unknown, or both known and equal — answers false.
//
// What a caller DOES with a true answer is the caller's: the api reports
// revision skew and keeps serving, and refuses to let a role join the
// installation under a different release version. This function only decides
// whether there is a difference to act on.
func SkewBetween(mine, theirs string) bool {
	if !Comparable(mine) || !Comparable(theirs) {
		return false
	}
	return mine != theirs
}
