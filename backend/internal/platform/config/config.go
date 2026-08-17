// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package config is the one place this product reads the process environment.
//
// OPS-CFG-2 says configuration is loaded once at the composition root into a
// typed, validated struct, and that modules never read the environment
// directly. That rule had nothing holding it: six packages under platform/,
// modules/ and compose/ called os.Getenv themselves, which is how the
// documented surface drifted from the real one — 61 variables live in Go, 34
// were written down, and six production variables appeared nowhere at all.
// A variable nobody can enumerate is a variable nobody can document, validate
// or generate a template from.
//
// So the read becomes a seam rather than a syscall scattered through the tree.
// A package that needs configuration declares what it needs and takes a Lookup;
// the composition root passes FromOS, and a test passes a map. Nothing else
// changes about where values come from: OPS-CFG-1's precedence (flags →
// environment → file → compiled defaults) is resolved by the caller, exactly as
// before.
//
// backend/configseam_test.go derives the obligation from the tree rather than
// from a list, so a new os.Getenv anywhere outside this package fails the build
// instead of quietly reopening the gap.
package config

import "os"

// Lookup reads one configuration variable by name, answering "" when it is not
// set. It is a function rather than an interface because there is exactly one
// operation and every caller wants it inline; FromOS is the production value
// and a map's index expression is the test one.
type Lookup func(name string) string

// FromOS is the process environment — the single os.Getenv in the product.
//
// Passed down from each cmd/<role> rather than reached for, so that what a
// package reads is visible in its signature. A package holding a Lookup can be
// exercised without mutating global state, which is the other half of why this
// exists: a test that sets a real environment variable to steer a package it is
// not testing leaks into every test that runs after it.
func FromOS(name string) string { return os.Getenv(name) }

// Static answers from a fixed map. It is the test-time Lookup, and it is here
// rather than in a test helper so that a package with configuration can be
// exercised from any suite without each one inventing its own.
func Static(values map[string]string) Lookup {
	return func(name string) string { return values[name] }
}
