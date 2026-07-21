// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import "testing"

func TestAssistantProfileIsExplicitlyPublic(t *testing.T) {
	if !publicPaths["/v1/assistant/profile"] {
		t.Fatal("assistant profile must pass the session gate for the login presence")
	}
}
