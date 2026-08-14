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

	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/licensecheck"
)

// EnsureLicense resolves the installation's license posture and hands back the
// watcher that holds it. The caller starts the watcher's re-check loop and
// wires its posture into whatever reports it.
//
// It refuses a boot whose configured license the bundled module will not honor,
// and reports — never refuses — an installation that configured none. The
// asymmetry is the whole posture: an unlicensed installation is a supported
// state that every development and CI process here runs in, while a license
// that is present and refused is an operator mistake nobody downstream can
// distinguish from a deliberate downgrade.
func EnsureLicense(ctx context.Context, log *slog.Logger, cfg deployconfig.Config) (*licensecheck.Watcher, error) {
	// The SOURCE is handed over, not a token: the watcher re-reads it, so a
	// license the operator renews in place takes effect on the next re-check
	// instead of waiting for a restart.
	watcher, err := licensecheck.NewWatcher(ctx, cfg.License.Token, time.Now, log)
	if err != nil {
		// The setting to correct is named HERE, where the token's source is
		// known: platform's check is handed a token, not a configuration file.
		return nil, fmt.Errorf("%w — correct or remove license.token_file (or %s) and start again",
			err, deployconfig.LicenseTokenEnvVar)
	}
	logLicensePosture(ctx, log, watcher.Posture(), cfg.License.TokenOrigin())
	return watcher, nil
}

// logLicensePosture writes the one boot line an operator greps for. The module
// version travels with it because a refused license and a stale bundled module
// are different problems that read identically without it, and so does the
// token's origin, because the environment outranks the deployment file and an
// installation licensed from a variable should say so where somebody sees it.
func logLicensePosture(ctx context.Context, log *slog.Logger, posture licensecheck.Posture, origin string) {
	attrs := []any{"state", string(posture.State), "module", licensecheck.ModuleVersion(), "token_from", origin}
	if seats, ok := posture.Seats(); ok {
		attrs = append(attrs, "seats", seats)
	}
	if posture.State == licensecheck.StateAbsent {
		// A warning, not an error: it boots, and it is meant to be noticed. An
		// installation nobody licensed is fine; one that lost its license
		// reference in a redeploy looks exactly the same from here, and only the
		// operator can tell which this is.
		log.WarnContext(ctx, "no license configured; the installation is running unlicensed", attrs...)
		return
	}
	log.InfoContext(ctx, "license verified", attrs...)
}

// WithLicensePosture wires the resolved posture into this role's /metrics. The
// function is read at scrape time rather than the value being copied in, so an
// exposition reports what the watcher last resolved instead of what the process
// booted with.
func WithLicensePosture(posture func() licensecheck.Posture) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.licensePosture = posture
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
