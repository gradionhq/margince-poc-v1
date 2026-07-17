// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// EnsureInstallation's fail-fast half: configuration defects are refused
// before any database work — provable with a nil pool.

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
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

func TestEnsureInstallationSurfacesAnUnreadablePasswordFile(t *testing.T) {
	cfg := deployconfig.Config{
		Version:      1,
		Organization: deployconfig.Organization{Name: "Gradion"},
		BootstrapAdmin: &deployconfig.BootstrapAdmin{
			Email: "a@b.test", DisplayName: "A",
			PasswordFile: filepath.Join(t.TempDir(), "missing"),
		},
	}
	err := EnsureInstallation(context.Background(), nil, discardLogger(), cfg)
	if err == nil || !strings.Contains(err.Error(), "password_file") {
		t.Fatalf("err = %v, want the password-file read error", err)
	}
}
