// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package buildinfo carries the one immutable revision this binary was built
// from.
//
// IT IS DIAGNOSTIC METADATA, NOT AN INTEGRITY SIGNATURE. What it exists for is
// skew: the api and the web tier deploy separately, so a document the api
// fetched may have been built from a different commit than the api reading it.
// Transport integrity rests on HTTPS and on control of the origin; this only
// answers "are these two halves the same build", and even then it reports rather
// than refuses — a rolling deploy would otherwise take the views down for the
// length of the rollout.
//
// UNKNOWN IS THE DEFAULT AND IT DISABLES THE COMPARISON. A developer's binary is
// built from a dirty worktree, where a commit SHA describes nothing; equality
// there would mean nothing, and inequality would alarm on every local run.
package buildinfo

// Revision is set at link time:
//
//	go build -ldflags "-X <this package>.Revision=$MARGINCE_BUILD_REVISION"
//
// CI passes the same value as a build arg to the api and the web images, so the
// two halves can be compared. Left empty by an ordinary `go build`, which is
// what Unknown answers for.
var Revision string

// Unknown is the value a comparison must not be made against — either side
// absent, or the literal "dev".
//
// `make dev` deliberately does NOT stamp this: it passes the real commit to both
// halves of the stack so a local run exercises the same comparison a deployment
// does, and an operator can force a mismatch by exporting
// MARGINCE_BUILD_REVISION before it. "dev" is what dev.sh falls back to outside
// a git checkout, and what any build that declines to stamp can use.
const Unknown = "dev"

// Comparable reports whether a revision is one worth comparing. Empty and "dev"
// are both "this build does not know", and a comparison against either is a
// difference that means nothing.
func Comparable(revision string) bool {
	return revision != "" && revision != Unknown
}

// SkewBetween reports whether two revisions are known AND different, which is
// the only combination that says anything. Everything else — either side
// unknown, or both known and equal — answers false.
func SkewBetween(mine, theirs string) bool {
	if !Comparable(mine) || !Comparable(theirs) {
		return false
	}
	return mine != theirs
}
