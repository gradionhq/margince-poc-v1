// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"cmp"
	"errors"
	"flag"

	"github.com/gradionhq/margince/backend/internal/platform/config"
)

// ownerDSN resolves which credential migrates: an explicit --dsn, else
// MARGINCE_OWNER_DSN, else MARGINCE_DSN.
//
// The order is the invariant. Every verb here runs DDL and so needs the owner
// role; MARGINCE_DSN names the APP role everywhere else in the product
// (NOSUPERUSER, NOBYPASSRLS), which holds no DDL rights. It remains the last
// resort because an installation may run everything under one
// sufficiently-privileged credential.
func ownerDSN(flagValue string) string {
	return cmp.Or(flagValue, config.FromOS("MARGINCE_OWNER_DSN"), config.FromOS("MARGINCE_DSN"))
}

// resolveDSN returns the DSN to migrate with, or an error naming every way to
// supply one. It owns the whole decision so run() carries none of it.
func resolveDSN(fs *flag.FlagSet, flagValue string) (string, error) {
	// An explicit `--dsn ""` is refused rather than falling through to the
	// environment. Emptiness alone cannot tell "flag absent" from "flag given,
	// empty", so a wrapper passing --dsn "$UNSET_VAR" would otherwise run
	// `down --steps 5` or `drop-db` against whatever the ambient DSN names.
	if flagWasGiven(fs, "dsn") && flagValue == "" {
		return "", errors.New("migrate: --dsn was given but empty; pass a DSN or omit the flag to use MARGINCE_OWNER_DSN")
	}
	if dsn := ownerDSN(flagValue); dsn != "" {
		return dsn, nil
	}
	return "", errors.New("migrate: --dsn, MARGINCE_OWNER_DSN or MARGINCE_DSN required")
}

// flagWasGiven reports whether the caller passed name, as opposed to it holding
// its default. flag offers no direct query, so Visit is the only way to ask.
func flagWasGiven(fs *flag.FlagSet, name string) bool {
	given := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			given = true
		}
	})
	return given
}
