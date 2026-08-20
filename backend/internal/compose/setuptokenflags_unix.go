// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The build tag is explicit because only GOOS filename suffixes are implicit,
// and "unix" is not one of them.
//go:build !windows

package compose

import "syscall"

// openNoFollow adds nothing that O_EXCL does not already give, and is kept
// because it costs nothing to say so at the call site.
//
// POSIX requires open() with O_CREAT|O_EXCL to fail EEXIST when the final
// component is a symbolic link, whatever that link points at — a dangling one
// included. So this flag guards no case of its own here; it is defence in depth
// against a kernel that does not conform. That is also why Windows, which has
// no equivalent, gives up no promise by omitting it.
const openNoFollow = syscall.O_NOFOLLOW
