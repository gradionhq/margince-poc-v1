// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// layout is the installation directory — everything is relative to it, so the
// whole folder can be moved or copied and still work.
//
// The split inside it is what makes updating safe: everything under
// runtime/ is replaceable build output, while margince.yaml, margince.env
// and data/ are the user's and must survive. An update replaces the launcher
// and runtime/ and touches nothing else. Putting data/ under runtime/ would
// make the natural "replace the folder" update destroy the records it exists
// to keep.
//
// The program directory is named runtime/ and NOT resources/ on purpose:
// codesign reads a directory that contains both a same-named executable and
// a "resources" subdirectory as a legacy bundle, then fails to verify the
// launcher because the folder has no signed resource envelope. Renaming the
// directory removes the ambiguity outright.
type layout struct {
	root string
}

// envHome overrides the installation directory, so a stack can be driven from
// a staging tree during development without a packaged folder.
const envHome = "MARGINCE_HOME"

func resolveLayout() (layout, error) {
	root := os.Getenv(envHome)
	if root == "" {
		exe, err := os.Executable()
		if err != nil {
			return layout{}, fmt.Errorf("locate the running executable: %w", err)
		}
		// The launcher sits at the top of the installation folder.
		root = filepath.Dir(exe)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return layout{}, fmt.Errorf("resolve the installation directory: %w", err)
	}

	l := layout{root: root}
	if _, err := os.Stat(l.runtimeDir()); err != nil {
		return layout{}, fmt.Errorf(
			"this installation is incomplete: %s is unusable (set %s to override): %w",
			l.runtimeDir(), envHome, err,
		)
	}
	if err := os.MkdirAll(l.data(), 0o700); err != nil {
		return layout{}, fmt.Errorf("create the data directory %s: %w", l.data(), err)
	}
	return l, nil
}

// Replaceable build output.
func (l layout) runtimeDir() string        { return filepath.Join(l.root, "runtime") }
func (l layout) pgRoot() string            { return filepath.Join(l.runtimeDir(), "pgsql") }
func (l layout) pgBin(name string) string  { return filepath.Join(l.pgRoot(), "bin", name) }
func (l layout) appBin(name string) string { return filepath.Join(l.runtimeDir(), name) }
func (l layout) webRoot() string           { return filepath.Join(l.runtimeDir(), "web") }

// The user's, and never replaced by an update.
func (l layout) configPath() string    { return filepath.Join(l.root, "margince.yaml") }
func (l layout) envPath() string       { return filepath.Join(l.root, "margince.env") }
func (l layout) aiRoutingPath() string { return filepath.Join(l.root, "ai-routing.yaml") }

func (l layout) data() string              { return filepath.Join(l.root, "data") }
func (l layout) pgData() string            { return filepath.Join(l.data(), "pg") }
func (l layout) sockets() string           { return filepath.Join(l.data(), "sockets") }
func (l layout) logs() string              { return filepath.Join(l.data(), "logs") }
func (l layout) adminPasswordPath() string { return filepath.Join(l.data(), "admin-password") }

// ensureConfig writes the deployment configuration on first run and leaves an
// existing one alone, matching the create-if-missing / leave-if-exists rule
// the api documents for margince.yaml (A107/ADR-0061). Overwriting it would
// discard settings the user changed.
//
// It returns the admin password only when it created the credentials, so the
// caller can show them once; on later launches there is nothing to disclose.
func (l layout) ensureConfig() (string, error) {
	password, err := l.ensureAdminPassword()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(l.configPath()); err == nil {
		return password, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat %s: %w", l.configPath(), err)
	}

	// password_file is written relative to this file's own directory so the
	// whole installation folder stays portable; an absolute path baked in
	// here would break the moment the folder is moved or copied.
	config := fmt.Sprintf(`# Margince deployment configuration (A107/ADR-0061).
# Created on first run and never overwritten — your edits survive a restart.
# Restart Margince after changing anything here.
version: 1

organization:
  name: Margince
  base_currency: USD
  timezone: %s

bootstrap_admin:
  email: owner@margince.local
  display_name: Owner
  password_file: data/admin-password
`, localTimezone())

	if err := os.WriteFile(l.configPath(), []byte(config), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", l.configPath(), err)
	}
	return password, nil
}

// ensureAdminPassword generates the bootstrap admin password once. It returns
// an empty string when the file already existed: the secret is not the
// launcher's to re-disclose on every start, and reading it back would put a
// live credential on screen for anyone walking past.
func (l layout) ensureAdminPassword() (string, error) {
	path := l.adminPasswordPath()
	if _, err := os.Stat(path); err == nil {
		return "", nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}

	// The api requires at least 12 characters; 24 random bytes clears that
	// with margin and leaves no room for a guessable default to ship.
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate the admin password: %w", err)
	}
	password := base64.RawURLEncoding.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(password), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return password, nil
}

// localTimezone reports the IANA zone name macOS records, so the first-run
// organization is created in the user's own time rather than UTC.
func localTimezone() string {
	target, err := os.Readlink("/etc/localtime")
	if err != nil {
		return "UTC"
	}
	// /var/db/timezone/zoneinfo/Europe/Berlin -> Europe/Berlin
	const marker = "/zoneinfo/"
	if idx := strings.Index(target, marker); idx >= 0 {
		return target[idx+len(marker):]
	}
	return "UTC"
}
