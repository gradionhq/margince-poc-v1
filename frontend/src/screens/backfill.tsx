import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Building2, CheckCircle2, History, Mail, Users } from "lucide-react";
import { useEffect, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button } from "../design-system/atoms";
import { formatDuration } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { ProblemError, problemCode, throwProblem } from "./common";

// The bounded connect-time backfill (ADR-0063): pick a window, see the scope
// BEFORE anything spends (ADR-0020 preview-before-spend — the estimate card
// is the consent surface), then watch real progress. Every number rendered
// here is a persisted-row count from the single-row status read; nothing is
// fabricated client-side (CAP-AC-OPEN-1). The scope preview auto-loads so the
// first thing a newly-connected user sees is honest scope, not a blank form —
// but the spend still waits for the explicit "Start the import" consent.
//
// This panel is mounted in two places now: the onboarding coldstart (no
// `initial`, always fetches) and the Settings connected-inboxes card (which
// already holds the run row via the embedded `CaptureConnection.backfill` —
// seeding from it renders a live run with no extra request).

type BackfillStatus = components["schemas"]["BackfillStatus"];
type Provider = components["schemas"]["CaptureConnection"]["provider"];
type BackfillWindow = "3m" | "6m" | "12m";

const WINDOWS: { value: BackfillWindow; label: MessageKey }[] = [
  { value: "3m", label: "backfill.window3m" },
  { value: "6m", label: "backfill.window6m" },
  { value: "12m", label: "backfill.window12m" },
];

// A run whose updated_at hasn't moved in this long is honestly "stuck", not
// "in progress" — the contract's own doc comment on BackfillStatus.updated_at
// calls this out ("a killed worker leaves this honest"). Long enough that
// ordinary poll jitter or a slow provider batch never false-positives, short
// enough that a genuinely dead worker surfaces within a couple of polls of
// the threshold rather than staying "live" indefinitely.
const STALE_AFTER_MS = 3 * 60_000;

const isLiveState = (state: BackfillStatus["state"] | undefined) =>
  state === "running" || state === "queued";

// Both preview and start can answer connector_unsupported (a provider with no
// Backfiller — IMAP today) or window_narrowing (start only, a widen-only
// re-run) — pull the RFC 7807 code out of a thrown ProblemError so the render
// can branch to its own honest sentence instead of the raw server detail.
function errorCodeOf(error: unknown): string | null {
  return error instanceof ProblemError ? problemCode(error.problem) : null;
}

// statusQueryKey is shared by every reader of the run row so a start or
// cancel invalidates them all.
const statusQueryKey = (provider: string) => ["backfill-status", provider];

export function BackfillPanel({
  provider,
  initial,
}: {
  provider: Provider;
  // The run row already embedded in GET /connectors (CaptureConnection.
  // backfill) — seeds the first render so a live run shows immediately.
  initial?: BackfillStatus;
}) {
  const t = useT();
  const { locale } = useLocale();
  const qc = useQueryClient();
  const [window, setWindow] = useState<BackfillWindow>("6m");
  const [skipped, setSkipped] = useState(false);

  const status = useQuery({
    queryKey: statusQueryKey(provider),
    queryFn: async () => {
      const { data, error } = await api.GET("/connectors/{provider}/backfill", {
        params: { path: { provider } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    initialData: initial,
    // The embedded snapshot already answered this read once; skip the
    // mount-time re-fetch it would otherwise trigger (react-query treats
    // fresh-but-present data as needing revalidation by default) and rely on
    // the live poll below, or an explicit invalidate (start/cancel), for a
    // fresher row. Without a seed, behave exactly as before: fetch on mount.
    staleTime: initial !== undefined ? Number.POSITIVE_INFINITY : 0,
    // Poll while a run is live: the status read is a single indexed row
    // (CAP-PARAM-2), so polling is indistinguishable from push here.
    refetchInterval: (q) => (isLiveState(q.state.data?.state) ? 2500 : false),
  });

  const preview = useMutation({
    mutationFn: async (w: BackfillWindow) => {
      const { data, error } = await api.POST(
        "/connectors/{provider}/backfill/preview",
        { params: { path: { provider } }, body: { window: w } },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  const start = useMutation({
    mutationFn: async (w: BackfillWindow) => {
      const { data, error } = await api.POST(
        "/connectors/{provider}/backfill",
        {
          params: { path: { provider } },
          body: { window: w },
        },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: statusQueryKey(provider) }),
  });

  const cancel = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.DELETE(
        "/connectors/{provider}/backfill",
        { params: { path: { provider } } },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: statusQueryKey(provider) }),
  });

  // connector_unsupported is a structural fact about the provider (no
  // Backfiller behind it), independent of which window was picked — either
  // op can be the one that surfaces it, depending on whether the setup
  // screen's auto-preview or an explicit start round-trips first.
  const unsupported =
    errorCodeOf(preview.error) === "connector_unsupported" ||
    errorCodeOf(start.error) === "connector_unsupported";
  // window_narrowing only ever comes from start (preview never enqueues a
  // run), so it needs no such either/or.
  const narrowing = errorCodeOf(start.error) === "window_narrowing";

  // Auto-load the scope for the selected window the moment the setup view is
  // live (no run yet, not skipped) — the user sees honest scope immediately.
  // The mutation is single-flight per window; previewedWindow guards against
  // re-firing on unrelated re-renders while still refreshing on a window
  // change. This never spends: the estimate is a read; the import waits for
  // the explicit start.
  const isSetup = !skipped && status.data?.state === "none";
  const [previewedWindow, setPreviewedWindow] = useState<BackfillWindow | null>(
    null,
  );
  useEffect(() => {
    if (!isSetup || previewedWindow === window || preview.isPending) {
      return;
    }
    setPreviewedWindow(window);
    preview.mutate(window);
  }, [isSetup, window, previewedWindow, preview]);

  if (skipped) {
    return (
      <p className="t-small backfill-skipped">{t("backfill.skippedNote")}</p>
    );
  }
  if (status.isPending) {
    return <p className="t-small">{t("backfill.loading")}</p>;
  }
  if (status.isError) {
    // The status read failing must not block the wizard — the nightly sweep
    // still runs; the user just loses the live view here.
    return <p className="t-small">{t("backfill.statusUnavailable")}</p>;
  }

  const run = status.data;
  if (run.state === "none") {
    // A provider with no Backfiller behind it (IMAP today) can't run this
    // op at all, whichever window is picked — the honest answer is a
    // capability statement, not a retryable error inside the setup form.
    if (unsupported) {
      return (
        <div className="backfill-setup">
          <h3 className="backfill-h">
            <History aria-hidden /> {t("backfill.title")}
          </h3>
          <p className="t-small backfill-unsupported">
            {t("backfill.unsupportedNote")}
          </p>
        </div>
      );
    }
    return (
      <div className="backfill-setup">
        <h3 className="backfill-h">
          <History aria-hidden /> {t("backfill.title")}
        </h3>
        <p className="t-small">{t("backfill.intro")}</p>
        <div
          className="backfill-windows"
          role="radiogroup"
          aria-label={t("backfill.windowLabel")}
        >
          {WINDOWS.map((w) => (
            <label key={w.value} className="backfill-window">
              <input
                type="radio"
                name="backfill-window"
                checked={window === w.value}
                onChange={() => setWindow(w.value)}
              />
              {t(w.label)}
            </label>
          ))}
        </div>
        {preview.isPending && !preview.data && (
          <p className="t-small">{t("backfill.previewLoading")}</p>
        )}
        {preview.isError && (
          <p className="t-small backfill-error">{preview.error.message}</p>
        )}
        {preview.data && (
          <EstimateCard
            preview={preview.data}
            starting={start.isPending}
            onStart={() => start.mutate(window)}
          />
        )}
        {start.isError && (
          <p className="t-small backfill-error">
            {narrowing ? t("backfill.narrowingNote") : start.error.message}
          </p>
        )}
        <button
          type="button"
          className="backfill-skip"
          onClick={() => setSkipped(true)}
        >
          {t("backfill.skip")}
        </button>
      </div>
    );
  }

  return (
    <RunView
      run={run}
      cancelling={cancel.isPending}
      cancelError={cancel.isError ? cancel.error.message : null}
      onCancel={() => cancel.mutate()}
    />
  );
}

// EstimateCard is the consent surface: the labeled estimate the user acts on.
function EstimateCard({
  preview,
  starting,
  onStart,
}: {
  preview: components["schemas"]["BackfillPreview"];
  starting: boolean;
  onStart: () => void;
}) {
  const t = useT();
  const cost = ((preview.estimated_cost_minor ?? 0) / 100).toFixed(2);
  return (
    <div className="backfill-estimate">
      <p>
        {t("backfill.estimateMessages")}{" "}
        <strong>~{preview.estimated_messages.toLocaleString()}</strong>
      </p>
      {(preview.estimated_cost_minor ?? 0) > 0 && (
        <p className="t-small">
          {t("backfill.estimateCost")} ~{cost} {preview.currency ?? "EUR"}
        </p>
      )}
      <p className="t-small">{t("backfill.estimateNote")}</p>
      <Button variant="primary" disabled={starting} onClick={onStart}>
        {starting ? t("backfill.starting") : t("backfill.startCta")}
      </Button>
    </div>
  );
}

// The three headline figures of a capture run — captured mail and the two
// record kinds it grows. Each is a live persisted-row count; the value's
// `key` changes with the number so the CSS pop fires on every increment
// (reduced-motion users get the number without the motion).
const CAPTURE_STATS: {
  key: "captured" | "people_created" | "organizations_created";
  label: MessageKey;
  icon: typeof Mail;
}[] = [
  { key: "captured", label: "backfill.statEmails", icon: Mail },
  { key: "people_created", label: "backfill.statPeople", icon: Users },
  {
    key: "organizations_created",
    label: "backfill.statCompanies",
    icon: Building2,
  },
];

function CaptureStat({
  value,
  label,
  icon: Icon,
}: {
  value: number;
  label: string;
  icon: typeof Mail;
}) {
  return (
    <div className="capture-stat">
      <Icon aria-hidden />
      <b key={value} className="capture-stat-value">
        {value.toLocaleString()}
      </b>
      <span>{label}</span>
    </div>
  );
}

function RunView({
  run,
  cancelling,
  cancelError,
  onCancel,
}: {
  run: BackfillStatus;
  cancelling: boolean;
  cancelError: string | null;
  onCancel: () => void;
}) {
  const t = useT();
  const { locale } = useLocale();
  const counts = run.counts;
  const scanned = counts?.messages_scanned ?? 0;
  const live = run.state === "running" || run.state === "queued";
  const done = run.state === "done";
  const fraction =
    run.estimated_messages && run.estimated_messages > 0
      ? Math.min(1, scanned / run.estimated_messages)
      : null;
  // updated_at is the contract's own staleness stamp ("a killed worker
  // leaves this honest") — a live run that hasn't moved past the threshold
  // isn't progressing, whatever the fraction says, so the progress bar and
  // the spinning icon both stop claiming otherwise.
  const updatedAgoMs = run.updated_at
    ? Math.max(0, Date.now() - new Date(run.updated_at).getTime())
    : null;
  const stale = live && updatedAgoMs !== null && updatedAgoMs > STALE_AFTER_MS;

  return (
    <div className={`capture-hero${done ? " done" : ""}`} aria-live="polite">
      <h3 className="backfill-h">
        {done ? (
          <>
            <CheckCircle2 aria-hidden /> {t("backfill.doneTitle")}
          </>
        ) : (
          <>
            <History aria-hidden className={live && !stale ? "spin-slow" : ""} />{" "}
            {t(stateTitle(run.state))}
          </>
        )}
      </h3>
      <div className="capture-stats">
        {CAPTURE_STATS.map((stat) => (
          <CaptureStat
            key={stat.key}
            value={counts?.[stat.key] ?? 0}
            label={t(stat.label)}
            icon={stat.icon}
          />
        ))}
      </div>
      {fraction !== null && live && !stale && (
        <progress value={fraction} aria-label={t("backfill.progressLabel")} />
      )}
      {stale && (
        <p className="t-small backfill-stale">
          {t("backfill.staleUpdated", {
            duration: formatDuration(updatedAgoMs ?? 0, locale),
          })}
        </p>
      )}
      <p className="t-small capture-scanned">
        {t("backfill.countScanned")} {scanned.toLocaleString()}
      </p>
      {run.state === "error" && (
        <p className="t-small backfill-error">
          {t("backfill.errorNote")}
          {run.last_error_class ? ` (${run.last_error_class})` : ""}
        </p>
      )}
      {live && (
        <button
          type="button"
          className="backfill-skip"
          disabled={cancelling}
          onClick={onCancel}
        >
          {t("backfill.cancel")}
        </button>
      )}
      {live && cancelError && (
        <p className="t-small backfill-error">{cancelError}</p>
      )}
      {run.state === "cancelled" && (
        <p className="t-small">{t("backfill.cancelledNote")}</p>
      )}
    </div>
  );
}

function stateTitle(state: BackfillStatus["state"]): MessageKey {
  switch (state) {
    case "queued":
      return "backfill.queuedTitle";
    case "running":
      return "backfill.runningTitle";
    case "error":
      return "backfill.errorTitle";
    case "cancelled":
      return "backfill.cancelledTitle";
    default:
      return "backfill.doneTitle";
  }
}
