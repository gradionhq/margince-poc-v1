// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// openNoFollow is zero because Windows has no O_NOFOLLOW. This file is what
// lets the api compile for it at all — the desktop bundle runs the same
// binaries a server does.
//
// Nothing is given up by the omission: O_EXCL carries the refusal, and there
// was never an O_NOFOLLOW here to drop. What IS narrower than on POSIX is one
// residual case, and it is worth naming rather than implying it away. Go maps
// O_CREATE|O_EXCL to CREATE_NEW without FILE_FLAG_OPEN_REPARSE_POINT, so NT
// resolves a reparse point on the final component: a DANGLING file symlink
// there is followed and its target created, where POSIX would fail EEXIST. It
// needs SeCreateSymbolicLinkPrivilege or Developer Mode. Issue #1579 tracks
// closing it, together with the parent-directory case that neither flag covers
// on any platform, and the missing DACL that turns a redirect here into
// disclosure rather than mere relocation.
const openNoFollow = 0
