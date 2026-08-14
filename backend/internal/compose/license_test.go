// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/licensecheck"
)

// unlicensedEnvironment makes the environment say what these tests need it to
// say: nothing. deployconfig.License.Token reads MARGINCE_LICENSE before it
// looks at the file reference, so an engineer or CI lane that exports a real
// license would otherwise fail all three for a reason that has nothing to do
// with the code — one would stop being absent, and two would stop exercising
// the file path they name. (t.Setenv forbids t.Parallel, which is why these
// three do not run parallel.)
func unlicensedEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv(deployconfig.LicenseTokenEnvVar, "")
}

func TestEnsureLicenseBootsUnlicensedAndSaysSo(t *testing.T) {
	unlicensedEnvironment(t)
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo}))

	watcher, err := EnsureLicense(context.Background(), logger, deployconfig.Config{})
	if err != nil {
		t.Fatalf("EnsureLicense refused an unlicensed installation: %v", err)
	}
	if got := watcher.Posture().State; got != licensecheck.StateAbsent {
		t.Errorf("posture = %q, want %q", got, licensecheck.StateAbsent)
	}
	line := log.String()
	if !strings.Contains(line, "running unlicensed") {
		t.Errorf("boot log %q does not say the installation is unlicensed", line)
	}
	// The bundled module's release travels with the posture: a refused license
	// and a stale module are different problems that read alike without it.
	if !strings.Contains(line, licensecheck.ModuleVersion()) {
		t.Errorf("boot log %q does not name the bundled module %q", line, licensecheck.ModuleVersion())
	}
}

func TestEnsureLicenseRefusesTheBootOnALicenseTheModuleWillNotHonor(t *testing.T) {
	unlicensedEnvironment(t)
	path := filepath.Join(t.TempDir(), "license")
	if err := os.WriteFile(path, []byte("not.a.license"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	cfg := deployconfig.Config{License: deployconfig.License{TokenFile: path}}

	_, err := EnsureLicense(context.Background(), slog.New(slog.DiscardHandler), cfg)
	if err == nil {
		t.Fatal("EnsureLicense booted on a license the bundled module refuses")
	}
	// The refusal has to name the setting to correct, or an operator is left to
	// guess which of two places the token came from.
	for _, want := range []string{"license.token_file", deployconfig.LicenseTokenEnvVar} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("boot refusal %q does not name %q", err, want)
		}
	}
}

// A path that does not resolve fails the boot rather than reading as an
// unlicensed installation, which is the same posture to everything downstream.
func TestEnsureLicenseRefusesAnUnreadableTokenFile(t *testing.T) {
	unlicensedEnvironment(t)
	cfg := deployconfig.Config{License: deployconfig.License{TokenFile: filepath.Join(t.TempDir(), "typo")}}
	if _, err := EnsureLicense(context.Background(), slog.New(slog.DiscardHandler), cfg); err == nil {
		t.Fatal("EnsureLicense booted with a token_file that does not exist")
	}
}

func TestWriteLicenseMetrics(t *testing.T) {
	t.Parallel()
	granted := licensecheck.Posture{
		State:     licensecheck.StateValid,
		Grants:    licensecheck.Grants{licensecheck.SeatsAttribute: float64(25)},
		CheckedAt: time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC),
	}
	for _, tc := range []struct {
		name    string
		posture func() licensecheck.Posture
		want    []string
		absent  []string
	}{
		{
			name:   "a role that resolved no posture reports no section",
			absent: []string{"margince_license_posture", "margince_license_seats"},
		},
		{
			name:    "a verified license reports its state and its seat grant",
			posture: func() licensecheck.Posture { return granted },
			want: []string{
				`margince_license_posture{state="valid"} 1`,
				`margince_license_posture{state="absent"} 0`,
				`margince_license_posture{state="rejected"} 0`,
				"margince_license_seats 25",
			},
		},
		{
			name: "an unlicensed installation reports a state and no seat gauge",
			posture: func() licensecheck.Posture {
				return licensecheck.Posture{State: licensecheck.StateAbsent}
			},
			want: []string{
				`margince_license_posture{state="absent"} 1`,
				`margince_license_posture{state="valid"} 0`,
			},
			// A seat gauge reading zero would be a license permitting no seats,
			// which is the opposite of an installation nothing caps.
			absent: []string{"margince_license_seats"},
		},
		{
			name: "a license the module refused reports the refusal, never its reason",
			posture: func() licensecheck.Posture {
				return licensecheck.Posture{State: licensecheck.StateRejected, Reason: "licensecheck: signature is not trusted"}
			},
			want:   []string{`margince_license_posture{state="rejected"} 1`},
			absent: []string{"signature is not trusted", "margince_license_seats"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			Server{licensePosture: tc.posture}.writeLicenseMetrics(&out)
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("exposition is missing %q:\n%s", want, out.String())
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(out.String(), absent) {
					t.Errorf("exposition carries %q, which it must not:\n%s", absent, out.String())
				}
			}
		})
	}
}

// The whole path a process role actually takes: the option a boot applies, the
// field it sets, and the one renderer httpserver.Metrics is handed. Asserted
// through WithLicensePosture rather than by setting the field, so a wiring that
// stopped reaching the exposition fails here.
func TestWithLicensePostureReachesTheAssembledMetricsSections(t *testing.T) {
	t.Parallel()
	var srv Server
	WithLicensePosture(func() licensecheck.Posture {
		return licensecheck.Posture{State: licensecheck.StateAbsent}
	})(&srv, nil)

	var out bytes.Buffer
	srv.writeMetricsSections(&out)
	if !strings.Contains(out.String(), `margince_license_posture{state="absent"} 1`) {
		t.Errorf("the assembled sections carry no license posture:\n%s", out.String())
	}
}

// A role that never applied the option reports no license section rather than a
// state nobody resolved — the same posture every other section here takes.
func TestAssembledMetricsSectionsOmitTheLicenseWhenNoneWasWired(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	Server{}.writeMetricsSections(&out)
	if strings.Contains(out.String(), "margince_license") {
		t.Errorf("a role with no posture wired reported one:\n%s", out.String())
	}
}

// The boot line names which source the token came from. The environment outranks
// the deployment file, so an installation licensed from a variable — set by
// whoever controls the deploy pipeline rather than by whoever reviews the config
// — should say so where somebody reads it.
// Which source the token came from reaches the boot line. deployconfig owns the
// naming (TestTokenOriginNamesTheSourceThatWins covers all four); what is
// asserted here is that EnsureLicense actually puts it in the record, since a
// posture logged without its source cannot answer "which license is this
// installation running on".
func TestEnsureLicenseBootLineNamesTheTokenSource(t *testing.T) {
	unlicensedEnvironment(t)
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if _, err := EnsureLicense(context.Background(), logger, deployconfig.Config{}); err != nil {
		t.Fatalf("EnsureLicense: %v", err)
	}
	if !strings.Contains(log.String(), "token_from=none") {
		t.Errorf("the boot line does not name the token's source: %q", log.String())
	}
}
