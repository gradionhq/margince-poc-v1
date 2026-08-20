// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ownedfile

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// OpenNoFollow is zero because Windows has no O_NOFOLLOW. This file is what
// lets the api compile for it at all — the desktop bundle runs the same
// binaries a server does.
//
// Nothing is given up by the omission: O_EXCL carries the refusal, and there
// was never an O_NOFOLLOW here to drop.
//
// One residual case IS narrower than on POSIX, and it stays open as a decision
// rather than as a gap tracked elsewhere. Go maps O_CREATE|O_EXCL to CREATE_NEW
// without FILE_FLAG_OPEN_REPARSE_POINT, so NT resolves a reparse point on the
// final component: a DANGLING file symlink there is followed and its target
// created, where POSIX fails EEXIST. Closing it means not using os.OpenFile at
// all — CreateFileW with the flag, then wrapping the handle — which replaces the
// one line both platforms share with a Windows-only open path that no lane in
// this repository executes. Creating a reparse point needs
// SeCreateSymbolicLinkPrivilege or Developer Mode, so the attacker who can do it
// on the server's own working directory can generally do more direct things; and
// the DACL below means the followed file is owner-only either way, which is what
// turned this from disclosure into relocation. Reconsider if a Windows CI lane
// ever exists to prove the replacement.
const OpenNoFollow = 0

// RestrictToOwner replaces the file's DACL with one entry: full control for its
// owner, inheritance severed.
//
// The 0600 handed to OpenFile is ADVISORY here — Windows derives permissions
// from the parent's inheritable ACEs, so a credential created in a directory
// that grants Users read is readable by Users whatever mode Go was asked for.
// That is what makes the missing DACL the difference between a redirect being a
// relocation and being a disclosure.
//
// PROTECTED_DACL_SECURITY_INFORMATION is the half that severs inheritance; a
// DACL set without it is merged with the parent's inheritable entries and the
// grant it was meant to remove survives.
func RestrictToOwner(f *os.File) error {
	handle := windows.Handle(f.Fd())
	owner, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("ownedfile: reading the setup-token file's owner: %w", err)
	}
	ownerSID, _, err := owner.Owner()
	if err != nil {
		return fmt.Errorf("ownedfile: resolving the setup-token file's owner: %w", err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(ownerSID),
		},
	}}, nil)
	if err != nil {
		return fmt.Errorf("ownedfile: building the setup-token file's access list: %w", err)
	}
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		return fmt.Errorf("ownedfile: restricting the setup-token file to its owner: %w", err)
	}
	return nil
}
