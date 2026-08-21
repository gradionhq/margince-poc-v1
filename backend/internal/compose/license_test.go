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

	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/licensecheck"
	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

// Every case here hands EnsureLicense a lookup that answers nothing, which is
// what makes them tests of the code rather than of the shell they run in: the
// token is read through that lookup, so an engineer or CI lane exporting a real
// MARGINCE_LICENSE cannot turn an absent posture into a valid one, or make a
// case that names a token FILE read a variable instead.
//
// The nil pool and nil vault are the point of a second property rather than a
// convenience: with no vault configured there is nothing sealed to open, so the
// declaration is the whole answer and resolving it must touch no database. A
// change that made the license question need one would fail every case here by
// dereferencing the pool — which is the failure to want, because a worker
// resolves its entitlement before it will serve anything.

// A production installation serves on a license or it does not serve. The whole
// point of the gate is that this is the DEFAULT: MARGINCE_ENV is fail-closed, so
// an installation that names no posture is held to a license.
func TestEnsureLicenseRefusesAProductionBootWithNoLicense(t *testing.T) {
	_, err := EnsureLicense(context.Background(), slog.New(slog.DiscardHandler), nil, nil, deployconfig.Config{},
		runtimeenv.Parse(""), config.Static(nil))
	if err == nil {
		t.Fatal("EnsureLicense booted a production installation that configured no license")
	}
	// Both ways out are named, because the operator reading this is either
	// licensed and missing the reference, or running a development installation
	// that never said so — and the message that names one of the two sends the
	// other operator after a problem they do not have.
	//
	// The key it points at is `license.token`, the reference form, not the
	// legacy `license.token_file`: nothing is configured here, so this is the
	// one message that gets to recommend rather than describe.
	for _, want := range []string{"license.token", deployconfig.LicenseTokenEnvVar, runtimeenv.EnvVar, string(runtimeenv.Development)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("boot refusal %q does not name %q", err, want)
		}
	}
}

func TestEnsureLicenseBootsUnlicensedInNonProductionAndSaysSo(t *testing.T) {
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo}))

	watcher, err := EnsureLicense(context.Background(), logger, nil, nil, deployconfig.Config{}, runtimeenv.Development, config.Static(nil))
	if err != nil {
		t.Fatalf("EnsureLicense refused an unlicensed development installation: %v", err)
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

// A refused license refuses the boot in EVERY posture. Only the ABSENT case
// bends for a development installation: naming yourself non-production is how
// you say you have no license, not a way to run one the module judged.
func TestEnsureLicenseRefusesTheBootOnALicenseTheModuleWillNotHonor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "license")
	if err := os.WriteFile(path, []byte("not.a.license"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	cfg := deployconfig.Config{License: deployconfig.License{TokenFile: path}}

	for _, env := range []runtimeenv.Environment{runtimeenv.Production, runtimeenv.Development} {
		_, err := EnsureLicense(context.Background(), slog.New(slog.DiscardHandler), nil, nil, cfg, env, config.Static(nil))
		if err == nil {
			t.Fatalf("EnsureLicense booted a %s installation on a license the bundled module refuses", env)
		}
		// The refusal names the setting THIS deployment used, which is the whole
		// job: an operator is otherwise left guessing which of three places the
		// token came from.
		if !strings.Contains(err.Error(), "license.token_file") {
			t.Errorf("boot refusal %q (%s) does not name license.token_file, the source it read", err, env)
		}
		// And names only that one. Listing the alternatives it did NOT read
		// sends an operator to a knob that is not holding their bad token.
		if strings.Contains(err.Error(), deployconfig.LicenseTokenEnvVar) {
			t.Errorf("boot refusal %q (%s) names %s, which was not the source", err, env, deployconfig.LicenseTokenEnvVar)
		}
	}
}

// A path that does not resolve fails the boot rather than reading as an
// unlicensed installation, which is the same posture to everything downstream.
func TestEnsureLicenseRefusesAnUnreadableTokenFile(t *testing.T) {
	cfg := deployconfig.Config{License: deployconfig.License{TokenFile: filepath.Join(t.TempDir(), "typo")}}
	if _, err := EnsureLicense(context.Background(), slog.New(slog.DiscardHandler), nil, nil, cfg, runtimeenv.Production, config.Static(nil)); err == nil {
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
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if _, err := EnsureLicense(context.Background(), logger, nil, nil, deployconfig.Config{}, runtimeenv.Development, config.Static(nil)); err != nil {
		t.Fatalf("EnsureLicense: %v", err)
	}
	if !strings.Contains(log.String(), "token_from=none") {
		t.Errorf("the boot line does not name the token's source: %q", log.String())
	}
}

// Where the token came from is half the boot line's value, and deployconfig can
// only speak for the file and the environment. Once a token can be sealed in
// the vault, an installation running on one would be recorded as running on
// "none" — which reads as unlicensed to whoever greps for it.
func TestTheTokenOriginNamesTheVaultOnlyWhenAVaultAnsweredIt(t *testing.T) {
	declared, err := deployconfig.Parse([]byte("version: 1\nlicense:\n  token: ${env:MARGINCE_LICENSE_TOKEN}\n"))
	if err != nil {
		t.Fatalf("parsing the deployment file: %v", err)
	}
	for _, tc := range []struct {
		name    string
		cfg     deployconfig.Config
		posture licensecheck.Posture
		want    string
	}{
		{"a declared token names the declaration", declared, licensecheck.Posture{State: licensecheck.StateValid}, "license.token"},
		{"nothing declared and nothing sealed is none", deployconfig.Config{}, licensecheck.Posture{State: licensecheck.StateAbsent}, "none"},
		{"nothing declared but a token in hand came from the vault", deployconfig.Config{}, licensecheck.Posture{State: licensecheck.StateValid}, "keyvault"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := licenseTokenOrigin(tc.cfg, config.Static(nil), tc.posture); got != tc.want {
				t.Errorf("origin %q, want %q", got, tc.want)
			}
		})
	}
}
