// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// EnsureInstallation's fail-fast half: configuration defects are refused
// before any database work — provable with a nil pool.

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestEnsureInstallationRefusesAnAdminWithoutAnOrganization(t *testing.T) {
	cfg := deployconfig.Config{
		Version: 1,
		BootstrapAdmin: &deployconfig.BootstrapAdmin{
			Email: "a@b.test", DisplayName: "A", PasswordFile: "/nowhere",
		},
	}
	err := EnsureInstallation(context.Background(), nil, discardLogger(), cfg)
	if err == nil || !strings.Contains(err.Error(), "organization.name") {
		t.Fatalf("err = %v, want the missing-organization refusal", err)
	}
}

// An unreadable password file is proven to surface by
// TestFirstBootStillFailsLoudlyOnAnUnreadableSecret in the integration lane, not
// here. The secret is now read only on the branch that creates the organization
// — after the database reports itself empty — so no assertion driven by a nil
// pool can reach it. The property is unchanged and the coverage is stronger for
// running against a real database; what moved is where it can be observed.
//
// The eager half stays below: configuration that is wrong on its face is still
// refused before anything connects.
