// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Boot configuration for the worker process role: the parsed flag/env
// surface and the small helpers that back a flag's default with an
// environment variable. Kept out of main.go so that file stays the
// process lifecycle (wire, run, drain) rather than the config vocabulary.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"
)

// workerConfig is the parsed boot configuration of the worker process.
type workerConfig struct {
	dsn                  string
	configPath           string
	freemailExtra        []string
	ratesFx              string
	ratesCurrencies      []string
	ratesModelPricing    map[string]string
	redisAddr            string
	routingPath          string
	fakeBrain            bool
	runnerInterval       time.Duration
	retentionInterval    time.Duration
	closeDateInterval    time.Duration
	reconcileInterval    time.Duration
	timeScanInterval     time.Duration
	gmailClientID        string
	gmailClientSecret    string
	graphClientID        string
	graphClientSecret    string
	graphTenant          string
	gmailSyncInterval    time.Duration
	gmailPubsubTopic     string
	gmailWatchInterval   time.Duration
	gmailWatchRenew      time.Duration
	overlayInterval      time.Duration
	overlayBackfillLimit int
	webhookKey           string
	webhookRetryInterval time.Duration
	deepReadMaxPages     int
	deepReadMaxBytes     int
	deepReadWall         time.Duration
	logLevel             string
	logFormat            string
}

// parseWorkerFlags parses and validates the boot flags; the DSN is the
// one dependency without a sane default, so its absence fails the boot
// here.
func parseWorkerFlags(args []string) (workerConfig, error) {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	var cfg workerConfig
	fs.StringVar(&cfg.dsn, "dsn", os.Getenv("MARGINCE_DSN"), "Postgres DSN (runtime app role)")
	fs.StringVar(&cfg.configPath, "config", envOr("MARGINCE_CONFIG", "margince.yaml"),
		"path to the deployment configuration file (A107/ADR-0061); read for the ai.capture_payloads posture the Surface-B runner honors and the capture pipeline tuning (capture.freemail_extra). A missing file boots with defaults")
	fs.StringVar(&cfg.redisAddr, "redis", envOr("MARGINCE_REDIS", "localhost:56379"), "Redis address (event bus)")
	fs.StringVar(&cfg.routingPath, "ai-routing", os.Getenv("MARGINCE_AI_ROUTING"), "path to ai-routing.yaml; enables the Surface-B runner")
	fs.BoolVar(&cfg.fakeBrain, "ai-fake", false, "run the Surface-B runner on the offline fake model (dev/test only)")
	fs.DurationVar(&cfg.runnerInterval, "runner-interval", 30*time.Second, "Surface-B scheduler tick interval")
	fs.DurationVar(&cfg.retentionInterval, "retention-interval", 24*time.Hour, "retention evaluator pass interval")
	fs.DurationVar(&cfg.closeDateInterval, "close-date-interval", 24*time.Hour, "close-date hygiene sweep interval (INV-CLOSE-PAST)")
	fs.DurationVar(&cfg.reconcileInterval, "reconcile-interval", 24*time.Hour, "overnight follow-up reconciliation pass interval (features/07 §8a)")
	fs.DurationVar(&cfg.timeScanInterval, "time-scan-interval", time.Hour, "clock-trigger scan interval (no_activity_reminder et al., Task 14)")
	fs.StringVar(&cfg.gmailClientID, "gmail-client-id", os.Getenv("MARGINCE_GMAIL_CLIENT_ID"), "Google OAuth client id for the Gmail capture connector; enables the background Gmail sync poll")
	fs.StringVar(&cfg.gmailClientSecret, "gmail-client-secret", os.Getenv("MARGINCE_GMAIL_CLIENT_SECRET"), "Google OAuth client secret for the Gmail capture connector")
	fs.StringVar(&cfg.graphClientID, "graph-client-id", os.Getenv("MARGINCE_GRAPH_CLIENT_ID"), "Microsoft (Entra) application id for the Outlook/M365 capture connector; enables its background sync poll")
	fs.StringVar(&cfg.graphClientSecret, "graph-client-secret", os.Getenv("MARGINCE_GRAPH_CLIENT_SECRET"), "Microsoft client secret for the Outlook/M365 capture connector")
	fs.StringVar(&cfg.graphTenant, "graph-tenant", os.Getenv("MARGINCE_GRAPH_TENANT"), "Microsoft identity tenant for token refresh (default: common — any organization)")
	fs.DurationVar(&cfg.gmailSyncInterval, "gmail-sync-interval", 2*time.Minute, "Gmail incremental-sync poll interval")
	fs.StringVar(&cfg.gmailPubsubTopic, "gmail-pubsub-topic", os.Getenv("MARGINCE_GMAIL_PUBSUB_TOPIC"), "Gmail Pub/Sub topic (projects/<p>/topics/<t>); enables the push-watch register+renew job. Empty leaves capture on the poll.")
	fs.DurationVar(&cfg.gmailWatchInterval, "gmail-watch-interval", 6*time.Hour, "Gmail push-watch maintenance scan interval")
	fs.DurationVar(&cfg.gmailWatchRenew, "gmail-watch-renew-within", 48*time.Hour, "renew a Gmail watch this far ahead of its 7-day expiry")
	fs.DurationVar(&cfg.overlayInterval, "overlay-reconcile-interval", 2*time.Minute, "overlay-mode incumbent mirror reconcile poll interval (design.md §4.4)")
	fs.IntVar(&cfg.overlayBackfillLimit, "overlay-backfill-limit", 0, "cap the overlay initial mirror backfill at this many records per object class (dev/demo; 0 = uncapped)")
	maxPagesDefault, err := envIntOr("MARGINCE_DEEPREAD_MAX_PAGES", 0)
	if err != nil {
		return workerConfig{}, err
	}
	maxBytesDefault, err := envIntOr("MARGINCE_DEEPREAD_MAX_BYTES", 0)
	if err != nil {
		return workerConfig{}, err
	}
	wallDefault, err := envDurationOr("MARGINCE_DEEPREAD_WALL", 0)
	if err != nil {
		return workerConfig{}, err
	}
	fs.IntVar(&cfg.deepReadMaxPages, "deepread-max-pages", maxPagesDefault, "deep-read crawl page cap; 0 takes the built-in default")
	fs.IntVar(&cfg.deepReadMaxBytes, "deepread-max-bytes", maxBytesDefault, "deep-read crawl aggregate byte cap; 0 takes the built-in default")
	fs.DurationVar(&cfg.deepReadWall, "deepread-wall", wallDefault, "deep-read crawl wall clock; 0 takes the built-in default")
	fs.StringVar(&cfg.webhookKey, "webhook-key", os.Getenv("MARGINCE_WEBHOOK_KEY"), "base64 32-byte key sealing outbound-webhook signing secrets; enables the cg:webhooks delivery consumer + retry sweep. Empty leaves the delivery worker off.")
	fs.DurationVar(&cfg.webhookRetryInterval, "webhook-retry-interval", 5*time.Second, "outbound-webhook retry-sweep tick interval")
	fs.StringVar(&cfg.logLevel, "log-level", envOr("MARGINCE_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	fs.StringVar(&cfg.logFormat, "log-format", envOr("MARGINCE_LOG_FORMAT", "text"), "log format: text|json")
	if err := fs.Parse(args); err != nil {
		return workerConfig{}, err
	}
	if cfg.dsn == "" {
		return workerConfig{}, errors.New("worker: --dsn or MARGINCE_DSN required")
	}
	if err := overlayBackfillLimitFromEnv(&cfg.overlayBackfillLimit); err != nil {
		return workerConfig{}, err
	}
	if cfg.deepReadMaxPages < 0 || cfg.deepReadMaxBytes < 0 || cfg.deepReadWall < 0 || cfg.overlayBackfillLimit < 0 {
		return workerConfig{}, errors.New("worker: the deep-read caps and the overlay backfill limit must be zero (default/uncapped) or positive")
	}
	if err := validateSchedulerIntervals(cfg); err != nil {
		return workerConfig{}, err
	}
	return cfg, nil
}

// validateSchedulerIntervals rejects a non-positive value for any duration
// that becomes a time.Ticker period or a River periodic schedule: a
// time.Ticker panics on a non-positive duration, and a non-positive River
// interval continuously reschedules its maintenance job. These are strictly
// scheduling PERIODS. Two duration flags are deliberately NOT in this set:
// gmail-watch-renew-within is a renewal THRESHOLD (a lead time —
// time.Now().Add(within) in DueWatches), so zero validly means "renew
// missing or already-expired watches" and is checked separately (negative
// only); and the deep-read / backfill caps are counts with a documented
// zero-means-default meaning, validated above. Zero and negative here are
// boot errors, never silent defaults.
func validateSchedulerIntervals(cfg workerConfig) error {
	intervals := []struct {
		flag string
		d    time.Duration
	}{
		{"runner-interval", cfg.runnerInterval},
		{"retention-interval", cfg.retentionInterval},
		{"close-date-interval", cfg.closeDateInterval},
		{"reconcile-interval", cfg.reconcileInterval},
		{"time-scan-interval", cfg.timeScanInterval},
		{"gmail-sync-interval", cfg.gmailSyncInterval},
		{"gmail-watch-interval", cfg.gmailWatchInterval},
		{"overlay-reconcile-interval", cfg.overlayInterval},
		{"webhook-retry-interval", cfg.webhookRetryInterval},
	}
	for _, iv := range intervals {
		if iv.d <= 0 {
			return fmt.Errorf("worker: --%s must be a positive duration, got %s", iv.flag, iv.d)
		}
	}
	// A renewal lead time may be zero (renew already-expired watches) but
	// never negative (renew in the past is nonsensical).
	if cfg.gmailWatchRenew < 0 {
		return fmt.Errorf("worker: --gmail-watch-renew-within must be zero or positive, got %s", cfg.gmailWatchRenew)
	}
	return nil
}

// overlayBackfillLimitFromEnv folds MARGINCE_OVERLAY_BACKFILL_LIMIT into
// limit when the flag was left at its 0 default, so either the flag or the
// env sets the cap. An unset env leaves limit untouched; a set-but-invalid
// env (non-integer or negative) is a boot error, never a silent default.
func overlayBackfillLimitFromEnv(limit *int) error {
	v := os.Getenv("MARGINCE_OVERLAY_BACKFILL_LIMIT")
	if v == "" || *limit != 0 {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return fmt.Errorf("invalid MARGINCE_OVERLAY_BACKFILL_LIMIT %q: want a non-negative integer", v)
	}
	*limit = n
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envIntOr / envDurationOr back a numeric flag's default with an
// environment variable; a set-but-unparseable value is a boot error,
// never a silent fallback.
func envIntOr(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("worker: %s=%q is not an integer: %w", key, v, err)
	}
	return parsed, nil
}

// defaultFxBootstrapCurrencies is the candidate set the FX refresh proposes on
// an empty sheet when the operator configured none — the three foreign
// currencies the base-EUR UI and the demo seed already use. Overridable via
// rates.fx_currencies; the FX refresh cannot bootstrap an empty sheet without a
// set, so an unset config takes this default rather than leaving the admin
// button dead on a fresh install.
var defaultFxBootstrapCurrencies = []string{"USD", "GBP", "CHF"}

// fxBootstrapCurrencies takes the operator's configured candidate set, or the
// default when none is configured.
func fxBootstrapCurrencies(configured []string) []string {
	if len(configured) == 0 {
		return defaultFxBootstrapCurrencies
	}
	return configured
}

func envDurationOr(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("worker: %s=%q is not a duration: %w", key, v, err)
	}
	return parsed, nil
}
