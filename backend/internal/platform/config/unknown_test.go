// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package config_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/config"
)

// ns is the namespace, spelled apart from every suffix below for the reason
// unknown.go gives: a quoted MARGINCE_* literal is read by the documentation
// gate as a variable somebody must document, and these are fixtures.
const ns = "MARGINCE_"

func probeRegistry(t *testing.T) *config.Registry {
	t.Helper()
	r, err := config.NewRegistry([]config.Item{
		{Name: ns + "REDIS", Kind: config.KindString, Doc: "the bus"},
		{Name: ns + "TOKEN", Kind: config.KindString, Secret: true, Doc: "a credential"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAMisspelledVariableIsReported(t *testing.T) {
	// The failure this exists for: MARGINCE_REDDIS is not an error, not a
	// different behaviour, just silently nothing — an operator watching a
	// setting be ignored with no thread to pull.
	got := probeRegistry(t).Undeclared([]string{
		ns + "REDIS=localhost:6379",
		ns + "REDDIS=localhost:6379",
	})
	if len(got) != 1 || got[0] != ns+"REDDIS" {
		t.Fatalf("Undeclared = %v, want exactly the misspelling", got)
	}
}

func TestTheReportNeverCarriesAValue(t *testing.T) {
	// A misspelled secret is still a secret. The report names variables, so a
	// typo'd credential is reported without publishing what it holds — which is
	// the whole reason this returns names rather than entries.
	const credential = "a-real-bearer-token"
	got := probeRegistry(t).Undeclared([]string{ns + "TOKKEN=" + credential})
	for _, name := range got {
		if strings.Contains(name, credential) {
			t.Fatalf("the report carried the value: %q", name)
		}
	}
	if len(got) != 1 || got[0] != ns+"TOKKEN" {
		t.Fatalf("Undeclared = %v, want exactly the misspelling", got)
	}
}

func TestTheReportIgnoresWhatIsNotOursToJudge(t *testing.T) {
	got := probeRegistry(t).Undeclared([]string{
		"PATH=/usr/bin",                  // the platform's, not ours
		ns + "TEST_DSN=postgres://x",     // the suite's own plumbing
		ns + "BENCH_RECORD=1",            // likewise
		ns + "ADMIN_PASSWORD=x",          // the container entrypoint's
		ns + "BUILD_REVISION=abc123",     // stamped into the image
		ns + "COMPOSITION_FRONTEND=stub", // likewise
		ns + "REDIS=",                    // set to empty IS unset, not a mistake
	})
	if len(got) != 0 {
		t.Errorf("Undeclared = %v; a report that cries wolf gets ignored, which costs it the typo it exists to catch", got)
	}
}

func TestAnEntryWithNoValueIsNotAVariable(t *testing.T) {
	// os.Environ yields NAME=value; anything without the separator is not
	// something an operator set, and reading it as a name would invent one.
	if got := probeRegistry(t).Undeclared([]string{ns + "NO_SEPARATOR"}); len(got) != 0 {
		t.Errorf("Undeclared = %v, want none", got)
	}
}

func TestAVariableAnotherRoleReadsIsStillReported(t *testing.T) {
	// A registry is ONE role's surface, so this is all it can know — and the
	// message says exactly that rather than "no such variable". The owner DSN is
	// the case that makes it worth saying: neither serving role reads it, and it
	// is the superuser credential row-level security does not bind.
	got := probeRegistry(t).Undeclared([]string{ns + "OWNER_DSN=postgres://y"})
	if len(got) != 1 || got[0] != ns+"OWNER_DSN" {
		t.Fatalf("Undeclared = %v, want the owner DSN named", got)
	}
}

func TestTheReportIsOrdered(t *testing.T) {
	// Two out-of-order names, because a map's iteration order would make the
	// same environment produce a different line every boot, and an operator
	// diffing two boots would read noise as change.
	got := probeRegistry(t).Undeclared([]string{ns + "ZEBRA=1", ns + "ALPHA=1"})
	if len(got) != 2 || got[0] != ns+"ALPHA" || got[1] != ns+"ZEBRA" {
		t.Fatalf("Undeclared = %v, want alphabetical", got)
	}
}

// The warning itself, because the SENTENCE is the load-bearing part: it must
// name the variables without their values, and it must not claim more than a
// single role's registry can know.
func TestTheWarningNamesVariablesWithoutValues(t *testing.T) {
	const credential = "a-real-bearer-token"
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	config.WarnUndeclared(logger, probeRegistry(t).Undeclared([]string{
		ns + "TOKKEN=" + credential,
		ns + "REDDIS=localhost:6379",
	}))
	line := buf.String()

	if strings.Contains(line, credential) {
		t.Errorf("the warning carried a credential: %s", line)
	}
	for _, name := range []string{ns + "TOKKEN", ns + "REDDIS"} {
		if !strings.Contains(line, name) {
			t.Errorf("the warning does not name %s: %s", name, line)
		}
	}
	// The claim a per-role registry cannot support. An operator who reads "no
	// such variable" about a name the SIBLING role reads will delete working
	// configuration — which is how this wording was got wrong the first time.
	if strings.Contains(line, "no such configuration variable") {
		t.Errorf("the warning claims a variable does not exist, which one role cannot know: %s", line)
	}
}

func TestNothingIsLoggedWhenThereIsNothingToSay(t *testing.T) {
	// A line on every boot of every correctly-configured installation is a line
	// operators learn to scroll past, and it would take the real one with it.
	var buf bytes.Buffer
	config.WarnUndeclared(slog.New(slog.NewTextHandler(&buf, nil)), nil)
	if buf.Len() != 0 {
		t.Errorf("logged %q with nothing to report", buf.String())
	}
}
