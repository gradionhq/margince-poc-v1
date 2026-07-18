import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, History } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button } from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessage } from "./common";

// The bounded connect-time backfill (ADR-0063): pick a window, see the scope
// BEFORE anything spends (ADR-0020 preview-before-spend — the estimate card
// is the consent surface), then watch real progress. Every number rendered
// here is a persisted-row count from the single-row status read; nothing is
// fabricated client-side (CAP-AC-OPEN-1).

type BackfillStatus = components["schemas"]["BackfillStatus"];
type BackfillWindow = "3m" | "6m" | "12m";

const WINDOWS: { value: BackfillWindow; label: MessageKey }[] = [
  { value: "3m", label: "backfill.window3m" },
  { value: "6m", label: "backfill.window6m" },
  { value: "12m", label: "backfill.window12m" },
];

// statusQueryKey is shared by every reader of the run row so a start or
// cancel invalidates them all.
const statusQueryKey = (provider: string) => ["backfill-status", provider];

export function BackfillPanel({ provider }: { provider: "gmail" }) {
  const t = useT();
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
        throw new Error(problemMessage(error));
      }
      return data;
    },
    // Poll while a run is live: the status read is a single indexed row
    // (CAP-PARAM-2), so polling is indistinguishable from push here.
    refetchInterval: (q) =>
      q.state.data?.state === "running" || q.state.data?.state === "queued"
        ? 2500
        : false,
  });

  const preview = useMutation({
    mutationFn: async (w: BackfillWindow) => {
      const { data, error } = await api.POST(
        "/connectors/{provider}/backfill/preview",
        { params: { path: { provider } }, body: { window: w } },
      );
      if (error) {
        throw new Error(problemMessage(error));
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
        throw new Error(problemMessage(error));
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
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: statusQueryKey(provider) }),
  });

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
                onChange={() => {
                  setWindow(w.value);
                  preview.reset();
                }}
              />
              {t(w.label)}
            </label>
          ))}
        </div>
        {!preview.data && (
          <Button
            variant="ghost"
            disabled={preview.isPending}
            onClick={() => preview.mutate(window)}
          >
            {preview.isPending
              ? t("backfill.previewLoading")
              : t("backfill.previewCta")}
          </Button>
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
          <p className="t-small backfill-error">{start.error.message}</p>
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

function RunView({
  run,
  cancelling,
  onCancel,
}: {
  run: BackfillStatus;
  cancelling: boolean;
  onCancel: () => void;
}) {
  const t = useT();
  const counts = run.counts;
  const scanned = counts?.messages_scanned ?? 0;
  const captured = counts?.captured ?? 0;
  const live = run.state === "running" || run.state === "queued";
  const fraction =
    run.estimated_messages && run.estimated_messages > 0
      ? Math.min(1, scanned / run.estimated_messages)
      : null;

  return (
    <div className="backfill-run" aria-live="polite">
      <h3 className="backfill-h">
        {run.state === "done" ? (
          <>
            <CheckCircle2 aria-hidden /> {t("backfill.doneTitle")}
          </>
        ) : (
          <>
            <History aria-hidden /> {t(stateTitle(run.state))}
          </>
        )}
      </h3>
      {fraction !== null && live && (
        <progress value={fraction} aria-label={t("backfill.progressLabel")} />
      )}
      <p className="t-small">
        {t("backfill.countScanned")} {scanned.toLocaleString()} ·{" "}
        {t("backfill.countCaptured")} {captured.toLocaleString()}
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
