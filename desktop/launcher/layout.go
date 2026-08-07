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

// layout resolves the two directories the bundle distinguishes between.
//
// The split is the point: resources live inside the .app and are replaced
// wholesale by an update, while data lives outside it and must survive one.
// A user updates by dragging the new app over the old one, so anything
// durable kept inside the bundle would be destroyed by the very gesture the
// update instructions ask for.
type layout struct {
	resources string
	data      string
}

// Environment overrides exist so the stack can be driven before an .app
// exists to run it from — the PoC harness and the integration check both
// point these at a staging tree.
const (
	envResources = "MARGINCE_DESKTOP_RESOURCES"
	envData      = "MARGINCE_DESKTOP_DATA"
)

func resolveLayout() (layout, error) {
	resources := os.Getenv(envResources)
	if resources == "" {
		exe, err := os.Executable()
		if err != nil {
			return layout{}, fmt.Errorf("locate the running executable: %w", err)
		}
		// Contents/MacOS/Margince -> Contents/Resources
		resources = filepath.Join(filepath.Dir(exe), "..", "Resources")
	}
	resources, err := filepath.Abs(resources)
	if err != nil {
		return layout{}, fmt.Errorf("resolve the resources directory: %w", err)
	}
	if _, err := os.Stat(resources); err != nil {
		return layout{}, fmt.Errorf("resources directory %s is unusable (set %s to override): %w", resources, envResources, err)
	}

	data := os.Getenv(envData)
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return layout{}, fmt.Errorf("locate the home directory: %w", err)
		}
		data = filepath.Join(home, "Library", "Application Support", "Margince")
	}
	data, err = filepath.Abs(data)
	if err != nil {
		return layout{}, fmt.Errorf("resolve the data directory: %w", err)
	}
	if err := os.MkdirAll(data, 0o700); err != nil {
		return layout{}, fmt.Errorf("create the data directory %s: %w", data, err)
	}
	return layout{resources: resources, data: data}, nil
}

func (l layout) pgRoot() string { return filepath.Join(l.resources, "pgsql") }
func (l layout) pgBin(name string) string {
	return filepath.Join(l.pgRoot(), "bin", name)
}
func (l layout) appBin(name string) string { return filepath.Join(l.resources, name) }
func (l layout) webRoot() string           { return filepath.Join(l.resources, "web") }

func (l layout) pgData() string  { return filepath.Join(l.data, "pg") }
func (l layout) sockets() string { return filepath.Join(l.data, "sockets") }
func (l layout) logs() string    { return filepath.Join(l.data, "logs") }
func (l layout) configPath() string {
	return filepath.Join(l.data, "margince.yaml")
}
func (l layout) adminPasswordPath() string {
	return filepath.Join(l.data, "admin-password")
}

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

	config := fmt.Sprintf(`version: 1

organization:
  name: Margince
  base_currency: USD
  timezone: %s

bootstrap_admin:
  email: owner@margince.local
  display_name: Owner
  password_file: %s
`, localTimezone(), l.adminPasswordPath())

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
