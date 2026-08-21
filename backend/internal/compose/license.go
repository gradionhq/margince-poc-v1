// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The installation's entitlement, resolved before a process role serves
// (UC-E11-05 E1). Composed here because both serving roles need the same
// answer from the same two inputs — the deployment file's token reference and
// the bundled validation module — and one spelling of "what happens when the
// license is refused" is the point: an api that refuses to boot beside a worker
// that shrugs would be a licensing posture nobody could describe.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/licensecheck"
	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

// EnsureLicense resolves the installation's license posture and hands back the
// watcher that holds it. The caller starts the watcher's re-check loop and
// wires its posture into whatever reports it.
//
// A serving role boots on a license or it does not boot. Three postures refuse
// it — a license the bundled module will not honor, a module that could not run
// at all, and, in production, no license configured — and the third is the one
// the deployment posture decides: an installation that names itself
// non-production through MARGINCE_ENV keeps running unlicensed, which is how
// every development, test and CI process in this repository boots. MARGINCE_ENV
// is fail-closed, so an installation that names nothing is production and is
// held to a license.
//
// Both serving roles ask this question, and neither serves without an answer: an
// api that refuses to boot beside a worker that shrugs would be a licensing
// posture nobody could describe.
func EnsureLicense(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, vault keyvault.Vault, cfg deployconfig.Config, env runtimeenv.Environment, lookup config.Lookup) (*licensecheck.Watcher, error) {
	// The SOURCE is handed over, not a token: the watcher re-reads it, so a
	// license the operator renews in place takes effect on the next re-check
	// instead of waiting for a restart.
	//
	// The source reads the deployment's declaration first and the key vault
	// where there is none, so an installation that has sealed its token and
	// dropped the variable keeps booting — and one whose vault it cannot open
	// is told THAT, rather than being told it has no license.
	source := SealedLicenseTokenSource(ctx, pool, vault, cfg, lookup, log)
	watcher, err := licensecheck.NewWatcher(ctx, source, time.Now, log, env)
	if err != nil {
		// The setting to correct is named HERE, where the token's source is
		// known: platform's check is handed a token, not a configuration file.
		return nil, fmt.Errorf("%w — %s", err, correctTheToken(cfg, lookup))
	}
	if err := refuseUnlicensedProduction(watcher.Posture(), env); err != nil {
		return nil, err
	}
	logLicensePosture(ctx, log, watcher.Posture(), licenseTokenOrigin(cfg, lookup, watcher.Posture()))
	return watcher, nil
}

// correctTheToken is the second half of a refused-license error: what to do.
//
// It is not one sentence for both sources. "Remove the key vault and start
// again" — which naming the origin uniformly would produce — is advice that
// destroys the only copy of the license along with every connector credential
// the installation holds, to fix a token the operator can simply re-declare.
func correctTheToken(cfg deployconfig.Config, lookup config.Lookup) string {
	if origin := cfg.License.TokenOrigin(lookup); origin != deployconfig.TokenOriginNone {
		return "correct or remove " + origin + " and start again"
	}
	return "this token came from the key vault, where it was sealed from a declaration since deleted: " +
		"re-declare license.token and the next boot re-seals it"
}

// licenseTokenOrigin names where the token came from, for the boot log and for
// the refusal that tells an operator what to correct.
//
// deployconfig can only speak for the deployment file and the environment; it
// has never heard of the vault, and an installation that sealed its token and
// dropped the declaration would otherwise be told its token came from "none" —
// while running on one. Naming the vault is the difference between an operator
// who knows where to look and one who goes looking in the file that no longer
// mentions it. A grep token rather than prose, because every other value of
// this field is one.
func licenseTokenOrigin(cfg deployconfig.Config, lookup config.Lookup, posture licensecheck.Posture) string {
	if origin := cfg.License.TokenOrigin(lookup); origin != deployconfig.TokenOriginNone {
		return origin
	}
	if posture.State == licensecheck.StateAbsent {
		// Nothing declared and nothing sealed. Naming the vault here would
		// describe an installation that had a token, which this one does not.
		return deployconfig.TokenOriginNone
	}
	return "keyvault"
}

// refuseUnlicensedProduction stops a production boot that configured no license.
//
// The error says both halves, because the operator reading it is in one of two
// situations that look identical from here: the installation is licensed and
// lost its token reference in a redeploy, or it is a development installation
// that never named itself one. Naming only the license would send the second
// operator looking for a token they were never issued.
func refuseUnlicensedProduction(posture licensecheck.Posture, env runtimeenv.Environment) error {
	if posture.State != licensecheck.StateAbsent || env.IsNonProduction() {
		return nil
	}
	return fmt.Errorf("no license is configured and this installation is production: "+
		"point license.token (or %s) at the license token issued for this installation, "+
		"or, if this is a development or test installation, set %s=%s",
		deployconfig.LicenseTokenEnvVar, runtimeenv.EnvVar, runtimeenv.Development)
}

// logLicensePosture writes the one boot line an operator greps for. The module
// version travels with it because a refused license and a stale bundled module
// are different problems that read identically without it, and so does the
// token's origin, because the environment outranks the deployment file and an
// installation licensed from a variable should say so where somebody sees it.
func logLicensePosture(ctx context.Context, log *slog.Logger, posture licensecheck.Posture, origin string) {
	attrs := []any{"state", string(posture.State), "module", licensecheck.ModuleVersion(), "token_from", origin}
	if posture.Issuer != "" && posture.Issuer != licensecheck.ProductionIssuer {
		// A license minted by a non-production authority. Only a non-production
		// installation accepts one, and an operator reading this log has to be
		// able to tell such an installation from a licensed customer's.
		attrs = append(attrs, "issuer", posture.Issuer)
	}
	if seats, ok := posture.Seats(); ok {
		attrs = append(attrs, "seats", seats)
	}
	if posture.State == licensecheck.StateAbsent {
		// Only a non-production installation reaches this line — production
		// refused the boot above — and it is a warning rather than an info
		// because the posture is worth noticing in a log that gets read: an
		// installation running unlicensed is entitled to nothing, so nothing it
		// does here proves what a licensed installation would do.
		log.WarnContext(ctx, "no license configured; this non-production installation is running unlicensed", attrs...)
		return
	}
	log.InfoContext(ctx, "license verified", attrs...)
}

// WithLicensePosture wires the resolved posture into everything this role does
// with it: the /metrics section, the entitlement surface, and the ceiling seat
// creation is held to. The function is read at scrape and call time rather than
// the value being copied in, so each of them reports and enforces what the
// watcher last resolved instead of what the process booted with.
func WithLicensePosture(posture func() licensecheck.Posture) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.licensePosture = posture
		// The entitlement surface is built HERE rather than in the assembly, and
		// both halves together: the assembly runs BEFORE the options, so a handler
		// wired there would have captured a nil posture and answered 501 for the
		// life of the process. One wiring point also means one answer to "does this
		// role report entitlement at all" — a role that never applies this option
		// serves no /metrics section and no surface, declared or absent in both.
		s.licenseHandlers = licenseHandlers{
			seats:   identity.NewSeatUsage(InstallationDB(pool)),
			posture: posture,
		}
		// The same posture reaches identity's seat writer as a ceiling, from the
		// same call: a role that reports what a license grants must not be able to
		// show an admin a number it does not hold them to. The posture is read at
		// CALL time, so a license renewed in place raises the ceiling on the next
		// re-check and one that lapses lowers it, without a restart — and nothing
		// here touches a seat already in use, only the next one.
		s.authHandlers = s.WithSeatCeiling(func() (int, bool) { return posture().Seats() })
	}
}

// writeLicenseMetrics renders the entitlement section. A role that wired no
// posture writes nothing, the same "declared or absent" posture every other
// section takes — a state gauge nobody resolved would read as an installation
// that had been checked.
//
// The state is exposed as one series per state with a single 1, rather than a
// number encoding the states, so a query can name what it is asking about and
// adding a fourth state later does not silently change what an existing
// dashboard's threshold means.
func (s Server) writeLicenseMetrics(w io.Writer) {
	if s.licensePosture == nil {
		return
	}
	posture := s.licensePosture()
	var section strings.Builder
	section.WriteString("# HELP margince_license_posture Whether this installation's license verified against the bundled validation module.\n")
	section.WriteString("# TYPE margince_license_posture gauge\n")
	for _, state := range []licensecheck.State{licensecheck.StateValid, licensecheck.StateAbsent, licensecheck.StateRejected} {
		value := 0
		if state == posture.State {
			value = 1
		}
		fmt.Fprintf(&section, "margince_license_posture{state=%q} %d\n", string(state), value)
	}
	// Omitted rather than zeroed when the license caps nothing: a gauge reading
	// zero seats is a license that permits none, which is the opposite of what an
	// uncapped or unlicensed installation means.
	if seats, ok := posture.Seats(); ok {
		fmt.Fprintf(&section, "# HELP margince_license_seats Full seats the verified license grants.\n"+
			"# TYPE margince_license_seats gauge\n"+
			"margince_license_seats %d\n", seats)
	}
	// Assembled first and written once, so a refused write cannot leave half a
	// gauge family in the exposition.
	//craft:ignore swallowed-errors the renderer httpserver.Metrics takes for this section has no error return; the job section, which does, is a separate parameter that reports its own
	_, _ = io.WriteString(w, section.String())
}
