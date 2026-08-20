// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// directory removes the ambiguity outright. Windows has no such rule, but the
// two platforms keep one layout so the documentation, the update gesture and
// this file describe both.
//
// The 0700 and 0600 modes below are the macOS half of that shared layout. On
// Windows Go maps the mode to the read-only attribute and nothing else — no
// DACL is set — so a file inherits the permissions of the directory it lands
// in, and what actually protects the folder is where the user put it. Under a
// user profile that is already per-account; somewhere like C:\Margince it is
// not, and every local account can read margince.env and the database
// password beside it. That limit is real and recorded in
// docs/explanation/desktop-distribution.md rather than papered over here.
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

// Replaceable build output. Executables go through exeName so the same call
// finds `api` on macOS and `api.exe` on Windows.
func (l layout) runtimeDir() string        { return filepath.Join(l.root, "runtime") }
func (l layout) pgRoot() string            { return filepath.Join(l.runtimeDir(), "pgsql") }
func (l layout) pgBin(name string) string  { return filepath.Join(l.pgRoot(), "bin", exeName(name)) }
func (l layout) appBin(name string) string { return filepath.Join(l.runtimeDir(), exeName(name)) }
func (l layout) webRoot() string           { return filepath.Join(l.runtimeDir(), "web") }

// The user's, and never replaced by an update.
func (l layout) configPath() string    { return filepath.Join(l.root, "margince.yaml") }
func (l layout) envPath() string       { return filepath.Join(l.root, "margince.env") }
func (l layout) aiRoutingPath() string { return filepath.Join(l.root, "ai-routing.yaml") }

func (l layout) data() string              { return filepath.Join(l.root, "data") }
func (l layout) pgData() string            { return filepath.Join(l.data(), "pg") }
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

	if err := writeFileAtomic(l.configPath(), []byte(config), 0o600); err != nil {
		return "", err
	}
	return password, nil
}

// writeFileAtomic writes through a temporary file beside the destination and
// renames it into place, so the destination is either absent or complete.
//
// os.WriteFile can fail AFTER a partial write — a full disk is the ordinary
// cause — and ensureConfig only writes margince.yaml when it finds none. A
// half-written one therefore survives every later launch: the Stat above sees a
// file and returns early, nothing ever repairs it, and the api refuses to parse
// it. The installation is then stuck in a state no restart clears.
//
// Rename within one directory is atomic, which is the same reason initCluster
// stages the data directory under another name and renames it at the end.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	staging := path + ".new"
	if err := writeAndFlush(staging, data, perm); err != nil {
		if rmErr := os.Remove(staging); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return fmt.Errorf("%w (and %s was left behind: %v)", err, staging, rmErr)
		}
		return err
	}
	if err := os.Rename(staging, path); err != nil {
		if rmErr := os.Remove(staging); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return fmt.Errorf("move %s into place: %w (and it was left behind: %v)", staging, err, rmErr)
		}
		return fmt.Errorf("move %s into place: %w", staging, err)
	}
	return nil
}

// writeAndFlush writes data to path and gets it onto the disk, reporting the
// first thing that went wrong while still always closing the file.
func writeAndFlush(path string, data []byte, perm os.FileMode) error {
	// #nosec G304 -- path is derived by layout from the installation directory, never from input
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	_, writeErr := file.Write(data)
	if writeErr == nil {
		// Before the rename, not after: a rename is atomic with respect to the
		// DIRECTORY and says nothing about the bytes having reached the disk.
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	switch {
	case writeErr != nil:
		return fmt.Errorf("write %s: %w", path, writeErr)
	case closeErr != nil:
		return fmt.Errorf("close %s: %w", path, closeErr)
	}
	return nil
}

// ensureAdminPassword generates the bootstrap admin password once. It returns
// an empty string when the file already existed: the secret is not the
// launcher's to re-disclose on every start, and reading it back would put a
// live credential on screen for anyone walking past.
//
// THE CREATE IS THE ONLY CHECK. There is deliberately no Stat first: a
// stat-then-write leaves a window in which a second launcher creates the file,
// and asking twice is what lets the two answers disagree. O_EXCL answers "did
// it exist" and "claim it" in one syscall, so there is no window to lose. The
// cost is a generated secret discarded on a later start, which is one read from
// crypto/rand.
func (l layout) ensureAdminPassword() (string, error) {
	path := l.adminPasswordPath()
	password, err := generateSecret()
	if err != nil {
		return "", fmt.Errorf("generate the admin password: %w", err)
	}
	if err := writeNewSecret(path, password); err == nil {
		return password, nil
	} else if !errors.Is(err, os.ErrExist) {
		return "", err
	}

	// It already existed — from an earlier start, or from a launcher that got
	// there first while this one was generating. Either way the persisted
	// password is the real one and this one is discarded unannounced: naming a
	// credential that is not on disk would lock the installation out of the
	// account it had just reported creating.
	//
	// Confirmed rather than assumed: O_EXCL refuses a path whose final component
	// is a SYMLINK whether or not its target exists, and so a dangling symlink
	// lands here too — with no credential behind it. Staying quiet about that
	// would start an installation whose password file cannot be read, having just
	// told the user where to find it.
	info, statErr := os.Lstat(path)
	if statErr != nil {
		return "", fmt.Errorf("%s exists but cannot be examined: %w", path, statErr)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf(
			"%s is not a regular file (%s), so no password could be stored there — remove it and start again",
			path, info.Mode().Type())
	}
	return "", nil
}

// writeNewSecret creates path and refuses to touch it if it already exists.
//
// os.WriteFile opens O_CREATE|O_TRUNC, so a stat-then-write would overwrite a
// credential written between the two calls — by a second launcher started
// while the first was mid-bootstrap. The generated password would then be
// announced on one screen while a different one sat in the file, and the
// installation would be locked out of the account it just created. O_EXCL
// makes the create itself the check, and refuses an existing final component
// whether or not it is a symlink; openNoFollow reinforces that where the
// platform has it.
func writeNewSecret(path, secret string) error {
	// #nosec G304 -- path is a layout.data() secret file; the caller derives it from the installation directory, never from input
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|openNoFollow, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := file.WriteString(secret); err != nil {
		// The close error cannot matter once the write has already failed, but
		// the file must still be released before the failure is reported.
		if closeErr := file.Close(); closeErr != nil {
			return fmt.Errorf("write %s: %w (and closing it failed: %v)", path, err, closeErr)
		}
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

// generateSecret mints one credential.
//
// The api requires at least 12 characters; 24 random bytes clears that with
// margin and leaves no room for a guessable default to ship. base64url keeps
// the alphabet to letters, digits, '-' and '_', which is what lets a caller
// put the value in a SQL literal or a URL without escaping it.
func generateSecret() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
