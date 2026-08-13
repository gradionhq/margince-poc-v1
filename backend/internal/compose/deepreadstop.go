// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// How a deep read ends when it does not produce a dossier, and the one question
// that decides whether an automatic read should run at all. Both are about
// stopping rather than reading, which is why they sit apart from the worker's
// crawl-and-extract path.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/webread"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// siteReadRetryAfter is how long a retryable failure waits. Long enough that a
// site under load or rate-limiting us is not hit again while it is still
// unhappy, short enough that a company is not missing from the CRM for a week
// over a transient block.
const siteReadRetryAfter = 6 * time.Hour

// diagnoseCrawlFailure turns a crawl error into the closed code an operator
// groups by and one sentence they can act on.
//
// It reads TYPED errors, never message text: the status travels on
// webread.StatusError and the network faults are net.Error, so the classifier
// asks the error what it is instead of pattern-matching a string that any
// wrapper could reword. The detail is written for a human reading a list of
// failed companies — it says what the site did, not what our stack was doing.
func diagnoseCrawlFailure(cause error) (code, detail string) {
	switch {
	case cause == nil:
		return "internal", "The read failed without recording a cause."
	case errors.Is(cause, webread.ErrRobotsDisallowed):
		return "robots_disallowed", "The site's robots.txt asks this crawler not to read the page."
	}
	var status *webread.StatusError
	if errors.As(cause, &status) {
		if status.Retryable() {
			if status.Status == http.StatusForbidden || status.Status == http.StatusTooManyRequests {
				return "bot_blocked", fmt.Sprintf(
					"The site answered %d — bot protection or rate limiting refused the read. Another attempt is scheduled.",
					status.Status)
			}
			return "http_server_error", fmt.Sprintf(
				"The site answered %d. Another attempt is scheduled.", status.Status)
		}
		return "http_client_error", fmt.Sprintf("The site answered %d for its own front page.", status.Status)
	}
	var netErr net.Error
	if errors.As(cause, &netErr) && netErr.Timeout() || errors.Is(cause, context.DeadlineExceeded) {
		return "timeout", "The site did not answer in time. Another attempt is scheduled."
	}
	var certErr *tls.CertificateVerificationError
	var hostErr x509.HostnameError
	var authErr x509.UnknownAuthorityError
	if errors.As(cause, &certErr) || errors.As(cause, &hostErr) || errors.As(cause, &authErr) {
		return "tls", "The site's HTTPS certificate could not be verified, so it was not read."
	}
	var dnsErr *net.DNSError
	if errors.As(cause, &dnsErr) {
		return "dns", "The domain name does not resolve to a server."
	}
	return "unreadable", "The site could not be read; no page returned usable content."
}

// autoEnrichMaxPages is the page ceiling every AUTOMATIC read runs under
// (ADR-0072 §9). A read nobody asked for should cost a fraction of one somebody
// did: the setting is on by default and sweeps up to ten organizations a day
// per workspace, so the deployment-wide crawler budget is the wrong unit here.
const autoEnrichMaxPages = 12

// pageCeiling is the page cap for one run: the automatic lane's own ceiling,
// else whatever the job asked for. requestedBy comes from the CLAIMED dossier
// row, not the job payload — the row is what says this read was automatic, and
// a payload that disagreed would otherwise buy the wider budget. Both only narrow — withPageCeiling ignores a
// value that is not lower than the configured cap, so neither this nor a job
// payload can spend more than the operator allowed.
func (w *siteDeepReadWorker) pageCeiling(requestedBy string, askedFor int) int {
	if isSystemRead(requestedBy) {
		if askedFor > 0 && askedFor < autoEnrichMaxPages {
			return askedFor
		}
		return autoEnrichMaxPages
	}
	return askedFor
}

// autoEnrichEnabled re-reads the workspace's auto-enrich setting.
func (w *siteDeepReadWorker) autoEnrichEnabled(ctx context.Context) (bool, error) {
	settings, err := w.settings.Get(ctx)
	if err != nil {
		return false, err
	}
	return settings.AutoEnrich, nil
}

// abandon closes a read nobody wants any more. Distinct from fail: nothing went
// wrong, an operator withdrew the standing decision that queued it, and a
// failure is something to investigate while this is not.
func (w *siteDeepReadWorker) abandon(ctx context.Context, readID ids.UUID, reason string) error {
	tctx, cancel := terminalCtx(ctx)
	defer cancel()
	if err := w.people.FinishSiteRead(tctx, readID, people.FinishSiteReadInput{Status: "cancelled"}); err != nil {
		return fmt.Errorf("site deep read %s: recording the cancellation: %w", readID, err)
	}
	w.log.InfoContext(ctx, "site deep read cancelled before spending", "read", readID.String(), "reason", reason)
	return nil
}

// fail records the terminal failure on the dossier, WITH its diagnosis, and
// returns the cause so River logs it on the job. A retry after a recorded
// failure is safe by construction — BeginSiteRead CAS-misses and the attempt
// no-ops.
func (w *siteDeepReadWorker) fail(ctx context.Context, readID ids.UUID, cause error) error {
	tctx, cancel := terminalCtx(ctx)
	defer cancel()
	code, detail := diagnoseCrawlFailure(cause)
	failure := people.FinishSiteReadInput{Status: "failed", StatusCode: code, StatusDetail: detail}
	if people.SiteReadFailureCodes[code] {
		// A cause that commonly clears on its own names its own next attempt,
		// which BeginSiteRead's failed-and-due arm re-claims. Without it a single
		// 403 from an edge's bot protection settles a live company's site for
		// good — the dossier is terminal, and nothing would ever ask again.
		next := w.now().Add(siteReadRetryAfter)
		failure.NextAttemptAt = &next
	}
	// Whether anything re-offers the read is the domain triage's decision, not
	// this function's: the disposition ledger carries its own attempt budget and
	// backoff (domaintriage.go), and it is what brings a domain back around. The
	// retry time here is what stops that second visit from being refused by a
	// dossier that already called itself finished.
	if err := w.people.FinishSiteRead(tctx, readID, failure); err != nil {
		return errors.Join(cause, fmt.Errorf("recording the failure on the dossier: %w", err))
	}
	// The dossier is terminal now, and a failed read is one no confirmation
	// accepts — so a mark this read stored before it failed is bytes nobody can
	// ever adopt.
	w.reclaimParkedLogo(tctx, readID)
	return cause
}
