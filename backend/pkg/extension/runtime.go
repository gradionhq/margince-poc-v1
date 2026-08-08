// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Runtime and its errors are part of the published extension surface.
//
//margince:extension-surface

package extension

import "errors"

// ErrRuntimeExpired reports that a Runtime outlived the call it was built
// for. The core mints one per invocation over call-scoped resources and
// invalidates it the moment the handler returns, so a handler that stashes
// its Runtime in a package variable and reaches for it on a later call is
// told so, rather than quietly working against released state.
var ErrRuntimeExpired = errors.New("extension: this runtime belongs to a call that has finished")

// Runtime is the capability handle a governed tool is invoked with. It is
// the ONLY way an extension reaches anything in the core at run time: the
// Extension value a unit's New() returns is inert declaration and holds no
// handle (see the package doc), so nothing an extension can do is reachable
// without a Runtime the core built for that one call.
//
// The core constructs it and knows which unit it is invoking, which is why
// nothing here takes a unit name or re-scopes to one — a handler holds
// exactly the namespace it was invoked under.
//
// Its lifetime is the invocation. It must not be retained: every method on
// a Runtime the core has released answers ErrRuntimeExpired.
//
// Like Extension, Runtime grows ADDITIVELY — a new capability kind is a new
// method — so a handler written against today's surface keeps compiling.
// Additive growth of an interface is only safe because extensions consume
// Runtime and never implement it.
type Runtime interface {
	// Secrets is the unit's own secret namespace in the calling workspace.
	Secrets() Secrets
}
