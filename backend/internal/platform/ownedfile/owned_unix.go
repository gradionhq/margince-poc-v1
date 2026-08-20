// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The build tag is explicit because only GOOS filename suffixes are implicit,
// and "unix" is not one of them.
//go:build !windows

package ownedfile

import (
	"os"
	"syscall"
)

// OpenNoFollow adds nothing that O_EXCL does not already give, and is kept
// because it costs nothing to say so at the call site.
//
// POSIX requires open() with O_CREAT|O_EXCL to fail EEXIST when the final
// component is a symbolic link, whatever that link points at — a dangling one
// included. So this flag guards no case of its own here; it is defence in depth
// against a kernel that does not conform. That is also why Windows, which has
// no equivalent, gives up no promise by omitting it.
const OpenNoFollow = syscall.O_NOFOLLOW

// RestrictToOwner is a no-op on POSIX: the 0600 passed to OpenFile is the
// permission, applied by the kernel at creation, and there is nothing further to
// establish. It exists so the caller has one spelling for a promise the two
// platforms keep by different means — on Windows the mode argument is advisory
// and an ACL has to be set explicitly.
//
// umask cannot widen it. A umask only ever REMOVES bits, so a permissive one
// produces a file narrower than 0600, never wider.
// RestrictToOwner is a no-op on POSIX; see the comment above.
func RestrictToOwner(*os.File) error { return nil }
