// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture

// boolPtr addresses a literal for the optional-bool fields the settings
// endpoints take. A one-line generic, deliberately re-declared here rather than
// exported from the harness: the parent package keeps its own, and coupling two
// packages over `&v` buys nothing.
func boolPtr(v bool) *bool { return &v }
