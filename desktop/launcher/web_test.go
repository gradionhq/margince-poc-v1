// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRegularFileExistsSeparatesAbsentFromUnreadable is the whole point of the
// function returning an error at all.
//
// Folded into a bool, an unreadable shipped file becomes "not there", the SPA
// fallback answers with index.html, and a broken installation is served as a
// working one with a 200 and a blank app. Absent and unreadable have to stay
// distinguishable for the handler to answer each correctly.
func TestRegularFileExistsSeparatesAbsentFromUnreadable(t *testing.T) {
	dir := t.TempDir()

	file := filepath.Join(dir, "index.html")
	if err := os.WriteFile(file, []byte("<html>"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	sub := filepath.Join(dir, "assets")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("make fixture dir: %v", err)
	}

	t.Run("a regular file", func(t *testing.T) {
		isFile, err := regularFileExists(file)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !isFile {
			t.Fatal("a regular file was not reported as one")
		}
	})

	t.Run("a directory is not a file to serve", func(t *testing.T) {
		isFile, err := regularFileExists(sub)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if isFile {
			t.Fatal("a directory was reported as a servable file")
		}
	})

	t.Run("absent is not an error, it is a client-side route", func(t *testing.T) {
		isFile, err := regularFileExists(filepath.Join(dir, "deals"))
		if err != nil {
			t.Fatalf("a missing path must not be an error, it is how a route looks: %v", err)
		}
		if isFile {
			t.Fatal("a missing path was reported as a file")
		}
	})

	// A path whose parent is a regular file produces ENOTDIR — a Stat failure
	// that is NOT os.ErrNotExist. It is used here in preference to a chmod,
	// because a permission fixture silently stops failing when the tests run as
	// root and the case would pass while testing nothing.
	t.Run("a Stat failure that is not absence is reported", func(t *testing.T) {
		isFile, err := regularFileExists(filepath.Join(file, "nested"))
		if err == nil {
			t.Fatalf("an unreadable path was accepted (isFile=%v), so the handler would answer\n"+
				"a broken installation with the SPA shell and a 200", isFile)
		}
		if isFile {
			t.Fatal("reported a file alongside an error")
		}
	})
}
