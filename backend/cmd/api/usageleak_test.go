// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The api's usage output must never carry a value the environment supplied.
//
// `flag` renders a non-empty default as `(default "…")`, and this role's
// environment holds the owner DSN (the entrypoint sets MARGINCE_SCHEMA_DSN from
// MARGINCE_OWNER_DSN), the app DSN, both OAuth client secrets, the connector-state
// HMAC key, the webhook sealing key and the /metrics bearer token. A single
// mistyped argument in a container command, or `-h` while debugging, sends the lot
// to stderr — the pod's log stream, or a public CI build log.
//
// The obligation is DERIVED, not listed: the variable set comes from the
// registrations in config.go itself, so a flag added later is covered the day it
// is added rather than the day someone remembers to extend a fixture.

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// envBoundInSource returns every MARGINCE_* variable config.go binds to a flag.
// Only a flag DEFAULT can reach the usage text, so the sweep is deliberately
// narrowed to cliflags registrations rather than every env read in the file.
var envBoundInSource = regexp.MustCompile(`env\.String\(fs, &[\w.]+, "[\w-]+", "(MARGINCE_\w+)"`)

func flagBoundEnvVars(t *testing.T, sourceFile string) []string {
	t.Helper()
	src, err := os.ReadFile(filepath.Clean(sourceFile))
	if err != nil {
		t.Fatalf("reading %s: %v", sourceFile, err)
	}
	matches := envBoundInSource.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatalf("no flag-bound MARGINCE_* variables found in %s — the registration shape changed and this gate is now checking nothing", sourceFile)
	}
	vars := make([]string, 0, len(matches))
	for _, m := range matches {
		vars = append(vars, m[1])
	}
	return vars
}

// captureStderr runs fn with os.Stderr redirected, returning what was written.
// The FlagSet inside parseAPIFlags writes its usage there, and that writer is the
// one that matters — asserting on a locally-built FlagSet would assert a copy.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, copyErr := io.Copy(&b, r)
		if copyErr != nil {
			done <- "READ FAILED: " + copyErr.Error()
			return
		}
		done <- b.String()
	}()

	fn()

	os.Stderr = saved
	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("closing the capture pipe: %v", closeErr)
	}
	return <-done
}

func TestAPIUsageOutputCarriesNoEnvironmentValue(t *testing.T) {
	const sentinel = "SENTINEL_VALUE_MUST_NOT_APPEAR"

	vars := flagBoundEnvVars(t, "config.go")
	for _, v := range vars {
		t.Setenv(v, sentinel)
	}

	var parseErr error
	usage := captureStderr(t, func() {
		_, parseErr = parseAPIFlags([]string{"--nosuchflag"})
	})
	if parseErr == nil {
		t.Fatal("an undefined flag was accepted")
	}
	if strings.Contains(usage, sentinel) {
		for _, line := range strings.Split(usage, "\n") {
			if strings.Contains(line, sentinel) {
				t.Errorf("the usage text echoes an environment value: %s", strings.TrimSpace(line))
			}
		}
	}
	if strings.Contains(parseErr.Error(), sentinel) {
		t.Errorf("the parse error echoes an environment value: %v", parseErr)
	}
	// The capture has to be real: an empty usage block would satisfy every
	// assertion above by containing nothing at all.
	if !strings.Contains(usage, "-dsn") {
		t.Fatalf("no usage text was captured, so nothing above was checked:\n%s", usage)
	}
	t.Logf("checked %d flag-bound variables", len(vars))
}

// The environment still reaches the flags — the leak fix must not have quietly
// disconnected them, which would leave a deployment booting on literal defaults.
func TestAPIStillReadsTheEnvironment(t *testing.T) {
	t.Setenv("MARGINCE_DSN", "postgres://u@127.0.0.1:1/from-env")
	t.Setenv("MARGINCE_CONFIG", "/from/env.yaml")

	cfg, err := parseAPIFlags(nil)
	if err != nil {
		t.Fatalf("parsing with no arguments: %v", err)
	}
	if cfg.dsn != "postgres://u@127.0.0.1:1/from-env" {
		t.Errorf("--dsn did not come from the environment: %q", cfg.dsn)
	}
	if cfg.configPath != "/from/env.yaml" {
		t.Errorf("--config did not come from the environment: %q", cfg.configPath)
	}
}

// And an explicit flag still beats the environment.
func TestAPIFlagBeatsTheEnvironment(t *testing.T) {
	t.Setenv("MARGINCE_DSN", "postgres://u@127.0.0.1:1/db") // parseAPIFlags requires one
	t.Setenv("MARGINCE_CONFIG", "/from/env.yaml")

	cfg, err := parseAPIFlags([]string{"--config", "/from/flag.yaml"})
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if cfg.configPath != "/from/flag.yaml" {
		t.Errorf("the environment overrode an explicit flag: %q", cfg.configPath)
	}
}
