// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Which credential this command migrates with, and what its usage text may
// contain. Every verb here runs DDL, so picking the app role over the owner is a
// failure the integration lane cannot see — it supplies its own DSN — and the
// usage text is printed on any parse error, where an echoed default becomes a
// credential in a build log.

import (
	"context"
	"io"
	"strings"
	"testing"
)

// Which credential migrates, asserted through run() rather than the helper alone.
// The helper in isolation proves arithmetic; only run() proves the wiring the
// entrypoint, the Makefile, dev.sh, lib-testdb.sh and check-ext-migrations.sh all
// depend on. Each case names an unroutable host so the connect error reports which
// DSN was chosen, and carries no password so a failure log stays clean.
func TestRunChoosesTheOwnerCredential(t *testing.T) {
	const (
		owner = "postgres://u@127.0.0.1:1/from-owner-var"
		app   = "postgres://u@127.0.0.1:1/from-app-var"
		flagv = "postgres://u@127.0.0.1:1/from-flag"
	)
	for _, tc := range []struct {
		name             string
		ownerVar, appVar string
		args             []string
		wantDB           string
	}{
		{"owner wins over app", owner, app, []string{"up"}, "from-owner-var"},
		{"app is the last resort", "", app, []string{"up"}, "from-app-var"},
		{"the flag beats both", owner, app, []string{"up", "--dsn", flagv}, "from-flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MARGINCE_OWNER_DSN", tc.ownerVar)
			t.Setenv("MARGINCE_DSN", tc.appVar)

			err := run(context.Background(), tc.args, io.Discard, io.Discard)
			if err == nil {
				t.Fatal("connecting to an unroutable host succeeded")
			}
			if !strings.Contains(err.Error(), tc.wantDB) {
				t.Errorf("migrate connected with the wrong credential: %v, want the DSN naming %q", err, tc.wantDB)
			}
		})
	}
}

// An explicitly empty --dsn is refused, not treated as absent. A wrapper passing
// --dsn "$UNSET_VAR" must abort, because the alternative is `down --steps 5` or
// `drop-db` running against whatever the ambient owner DSN happens to name.
func TestRunRefusesAnExplicitlyEmptyDSN(t *testing.T) {
	t.Setenv("MARGINCE_OWNER_DSN", "postgres://u@127.0.0.1:1/ambient")
	t.Setenv("MARGINCE_DSN", "")

	err := run(context.Background(), []string{"up", "--dsn", ""}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("an empty --dsn was accepted")
	}
	if strings.Contains(err.Error(), "ambient") {
		t.Errorf("an empty --dsn fell through to the environment: %v", err)
	}
	if !strings.Contains(err.Error(), "given but empty") {
		t.Errorf("the error does not say the flag was the problem: %v", err)
	}
}

// With nothing anywhere, run refuses rather than connecting to whatever libpq
// defaults to, and the message names every way to supply one.
func TestRunRefusesWithNoDSNAnywhere(t *testing.T) {
	t.Setenv("MARGINCE_OWNER_DSN", "")
	t.Setenv("MARGINCE_DSN", "")

	err := run(context.Background(), []string{"up"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("run() accepted no DSN at all")
	}
	for _, want := range []string{"--dsn", "MARGINCE_OWNER_DSN", "MARGINCE_DSN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error %q does not name %q, so an operator cannot tell how to supply one", err, want)
		}
	}
}

// The usage output carries no credential — asserted against the FlagSet run()
// itself builds, by capturing the writer it was handed. Re-declaring a local
// FlagSet here would assert a copy and pass while production regressed.
func TestUsageOutputNeverEchoesTheDSN(t *testing.T) {
	const secret = "postgres://owner:SUPERSECRET@db.internal:5432/prod"
	t.Setenv("MARGINCE_OWNER_DSN", secret)

	var stderr strings.Builder
	err := run(context.Background(), []string{"up", "--nosuchflag"}, io.Discard, &stderr)
	if err == nil {
		t.Fatal("an undefined flag was accepted")
	}
	if strings.Contains(stderr.String(), "SUPERSECRET") {
		t.Errorf("the usage output carries the credential:\n%s", stderr.String())
	}
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Errorf("the parse error carries the credential: %v", err)
	}
	// The capture has to be real: a usage block that never rendered would pass the
	// assertions above by writing nothing at all.
	if !strings.Contains(stderr.String(), "-dsn") {
		t.Fatalf("no usage text was captured, so nothing above was actually checked:\n%s", stderr.String())
	}
}
