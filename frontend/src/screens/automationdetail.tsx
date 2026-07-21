// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Ban, Check, Clock, Minus, X } from "lucide-react";
import { type ReactElement, type ReactNode, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  EmptyState,
  SectionHeader,
  SegmentedControl,
} from "../design-system/atoms";
import { AutonomyDot } from "../design-system/trust";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { LoadMoreButton, problemMessage, QueryStates } from "./common";

// The human surface for the two already-live, human-only automation ops
// (listAutomationRuns / previewAutomation). Co-located with automations.tsx
// (the strength.tsx / company-context.tsx precedent: a row's expandable body
// in its own file) so the screen stays legible. Both panels are pure reads;
// neither writes.

type AutomationRun = components["schemas"]["AutomationRun"];
type Outcome = AutomationRun["outcome"];

// Outcome → tone + glyph + label, TOTAL over the five-value enum. The explicit
// ReactElement return type is what makes the exhaustiveness real: if the
// contract grows a sixth outcome, the missing arm's implicit `undefined` return
// no longer satisfies ReactElement, so THIS function fails to compile — a new
// outcome cannot ship as a silent "unknown" badge to an operator.
export function OutcomeBadge({
  outcome,
}: Readonly<{ outcome: Outcome }>): ReactElement {
  const t = useT();
  switch (outcome) {
    case "fired":
      return (
        <Badge tone="success">
          <Check size={12} aria-hidden /> {t("auto.runs.outcomeFired")}
        </Badge>
      );
    case "failed":
      return (
        <Badge tone="danger">
          <X size={12} aria-hidden /> {t("auto.runs.outcomeFailed")}
        </Badge>
      );
    case "blocked":
      return (
        <Badge tone="danger">
          <Ban size={12} aria-hidden /> {t("auto.runs.outcomeBlocked")}
        </Badge>
      );
    case "skipped":
      return (
        <Badge tone="warn">
          <Minus size={12} aria-hidden /> {t("auto.runs.outcomeSkipped")}
        </Badge>
      );
    case "queued_for_approval":
      return (
        <Badge tone="warn">
          <Clock size={12} aria-hidden /> {t("auto.runs.outcomeQueued")}
        </Badge>
      );
  }
}

// failed/blocked read as an error (danger); skipped/queued as an advisory
// (warn) — the reason line tone matches its badge so the row reads honestly
// at a glance.
function reasonColor(outcome: Outcome): string {
  return outcome === "failed" || outcome === "blocked"
    ? "var(--danger)"
    : "var(--warn)";
}

// A labelled detail line, rendered ONLY by the caller when the field is
// present — never a blank "Label:" row for a null optional field (T7).
function DetailLine({
  label,
  value,
  color,
}: Readonly<{ label: string; value: string; color?: string }>) {
  return (
    <p className="t-small" style={{ marginTop: "var(--space-1)", color }}>
      <span className="t-label">{label}</span> {value}
    </p>
  );
}

function RunRow({ run }: Readonly<{ run: AutomationRun }>) {
  const t = useT();
  const { locale } = useLocale();
  // A run is an audit-style event: read its time in the VIEWER's own timezone
  // (as audit.tsx / aicalls.tsx do), never a fixed one — an operator in any
  // region sees the firing in their local wall-clock.
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  return (
    <li className="card card-inset" style={{ marginTop: "var(--space-2)" }}>
      <div
        style={{
          display: "flex",
          gap: "var(--space-2)",
          alignItems: "center",
          flexWrap: "wrap",
        }}
      >
        <OutcomeBadge outcome={run.outcome} />
        <time
          className="t-small"
          dateTime={run.occurred_at}
          title={run.occurred_at}
        >
          {formatDateTime(run.occurred_at, locale, zone)}
        </time>
        <AutonomyDot tier={run.tier === "green" ? "auto" : "confirm"} />
        {run.approval_required && (
          <span className="t-caption">{t("auto.runs.needsApproval")}</span>
        )}
      </div>
      {run.trigger_evidence && (
        <DetailLine label={t("auto.runs.why")} value={run.trigger_evidence} />
      )}
      {run.target_ref && (
        <DetailLine label={t("auto.runs.target")} value={run.target_ref} />
      )}
      {run.action_result && (
        <DetailLine label={t("auto.runs.result")} value={run.action_result} />
      )}
      {run.reason && (
        <DetailLine
          label={t("auto.runs.reason")}
          value={run.reason}
          color={reasonColor(run.outcome)}
        />
      )}
    </li>
  );
}

// The outcome filter chip row: `all` clears the filter, every other chip pins
// one outcome. `all` is a distinct sentinel (not an Outcome) so the query key
// carries `undefined` when unfiltered — changing it resets keyset paging.
const FILTER_OPTIONS = [
  "all",
  "fired",
  "failed",
  "blocked",
  "skipped",
  "queued_for_approval",
] as const;
type FilterOption = (typeof FILTER_OPTIONS)[number];

const FILTER_LABELS: Record<FilterOption, MessageKey> = {
  all: "auto.runs.filterAll",
  fired: "auto.runs.filterFired",
  failed: "auto.runs.filterFailed",
  blocked: "auto.runs.filterBlocked",
  skipped: "auto.runs.filterSkipped",
  queued_for_approval: "auto.runs.filterQueued",
};

// AU-2: the run-history panel over GET /automations/{id}/runs — keyset-paged,
// outcome-filterable, newest-first, every outcome first-class. Mirrors the
// history.tsx useInfiniteQuery + LoadMoreButton + QueryStates shape exactly.
export function AutomationRuns({
  automationId,
}: Readonly<{ automationId: string }>) {
  const t = useT();
  const [outcome, setOutcome] = useState<Outcome | undefined>(undefined);

  const query = useInfiniteQuery({
    // outcome is part of the key: changing the filter starts a fresh first
    // page rather than reusing a cursor minted under a different filter.
    queryKey: ["automation-runs", automationId, outcome ?? ""],
    initialPageParam: null as string | null,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET("/automations/{id}/runs", {
        params: {
          path: { id: automationId },
          query: {
            limit: 20,
            ...(pageParam ? { cursor: pageParam } : {}),
            ...(outcome ? { outcome } : {}),
          },
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    getNextPageParam: (last) => last.page.next_cursor ?? null,
  });

  const runs = query.data?.pages.flatMap((page) => page.data) ?? [];

  let body: ReactNode;
  if (runs.length === 0) {
    // filtered-empty (a narrowing that found nothing) reads differently from
    // never-fired — the operator should know whether the automation is idle
    // or just quiet for this outcome.
    body = (
      <EmptyState>
        {outcome ? t("auto.runs.emptyFiltered") : t("auto.runs.empty")}
      </EmptyState>
    );
  } else {
    body = (
      <>
        <ul style={{ listStyle: "none" }}>
          {runs.map((run) => (
            <RunRow key={run.id} run={run} />
          ))}
        </ul>
        <LoadMoreButton query={query} />
      </>
    );
  }

  return (
    <section
      className="card card-inset"
      style={{ marginTop: "var(--space-3)" }}
      data-testid="automation-runs"
    >
      <SectionHeader title={t("auto.runs.title")} />
      <div
        style={{
          display: "flex",
          flexWrap: "wrap",
          gap: "var(--space-2)",
          marginBottom: "var(--space-3)",
        }}
      >
        {FILTER_OPTIONS.map((option) => {
          const active =
            option === "all" ? outcome === undefined : outcome === option;
          return (
            <Button
              key={option}
              small
              variant={active ? "primary" : "ghost"}
              aria-pressed={active}
              onClick={() => setOutcome(option === "all" ? undefined : option)}
            >
              {t(FILTER_LABELS[option])}
            </Button>
          );
        })}
      </div>
      <QueryStates query={query}>{body}</QueryStates>
    </section>
  );
}

type AutomationPreviewResult = components["schemas"]["AutomationPreview"];

// The three offered windows, as the SegmentedControl's string options (the atom
// is keyed on strings). Kept as a typed tuple so the control and its labels can
// never drift apart; each parses to a valid 1..90 the server accepts (the 422
// branch below stays a defensive honesty guard, not a path the UI can reach).
const WINDOWS = ["7", "30", "90"] as const;
type WindowOption = (typeof WINDOWS)[number];

const WINDOW_LABELS: Record<WindowOption, MessageKey> = {
  "7": "auto.preview.window7",
  "30": "auto.preview.window30",
  "90": "auto.preview.window90",
};

// AU-1: the dry-run blast-radius panel over POST /automations/{id}/preview.
// A pure 🟢 read (no writes) modelled as a mutation because it POSTs a body
// and re-runs on demand — fired on open (the panel only mounts when open) and
// on every window change.
export function AutomationPreview({
  automationId,
}: Readonly<{ automationId: string }>) {
  const t = useT();
  const [windowDays, setWindowDays] = useState<WindowOption>("30");

  // A preview is a pure read keyed on the selected window — so it is a query,
  // not a mutation. useQuery refetches when the window changes, dedupes React
  // StrictMode's double-mount (one in-flight request per key), starts in the
  // pending state (no empty first frame), and routes loading/error/retry
  // through the shared QueryStates — the same recovery the runs panel gets.
  const query = useQuery({
    queryKey: ["automation-preview", automationId, windowDays],
    queryFn: async (): Promise<AutomationPreviewResult> => {
      const { data, error } = await api.POST("/automations/{id}/preview", {
        params: { path: { id: automationId } },
        body: { window_days: Number(windowDays) },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const result = query.data;
  const hidden = result?.excluded_by_permission ?? 0;

  // SegmentedControl takes resolved label strings (not i18n keys); build them
  // from the one WINDOW_LABELS map so the control and its copy stay in step.
  const windowLabels: Record<WindowOption, string> = {
    "7": t(WINDOW_LABELS["7"]),
    "30": t(WINDOW_LABELS["30"]),
    "90": t(WINDOW_LABELS["90"]),
  };

  return (
    <section
      className="card card-inset"
      style={{ marginTop: "var(--space-3)" }}
      data-testid="automation-preview"
    >
      <SectionHeader title={t("auto.preview.title")} />
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: "var(--space-2)",
          marginBottom: "var(--space-3)",
        }}
      >
        <span className="t-label">{t("auto.preview.window")}</span>
        <SegmentedControl
          options={WINDOWS}
          value={windowDays}
          onChange={setWindowDays}
          labels={windowLabels}
          label={t("auto.preview.window")}
        />
      </div>
      {/* Polite live region: announce when the estimate resolves or the window
          result changes, without stealing focus from the control. */}
      <div aria-live="polite">
        <QueryStates query={query}>
          {result && (
            <div
              style={{
                display: "flex",
                flexDirection: "column",
                gap: "var(--space-1)",
              }}
            >
              <p className="t-body">
                {t("auto.preview.matchesNow", { n: result.matches_now })}
              </p>
              <p className="t-small">
                {result.would_have_fired == null
                  ? t("auto.preview.notComputable")
                  : t("auto.preview.wouldFire", {
                      n: result.would_have_fired,
                      days: result.window_days,
                    })}
              </p>
              {hidden > 0 && (
                <p className="t-small">
                  {t("auto.preview.hidden", { n: hidden })}
                </p>
              )}
            </div>
          )}
        </QueryStates>
      </div>
      <p className="t-caption" style={{ marginTop: "var(--space-3)" }}>
        {t("auto.preview.explainer")}
      </p>
    </section>
  );
}

// The two expandable panels the automation row mounts below its header. Kept as
// one component so the row owns only the open/close toggles, not the panels'
// conditional rendering — each panel still mounts lazily, only while open.
//
// `canConfigure` gates the panels themselves, not only the toggle buttons: if a
// live `/me` refresh drops the caller's admin/ops role while a panel is open,
// the read surface (and its polling) disappears with the buttons. The server's
// RBAC gate is still the real boundary — this keeps the UI honest about it.
export function AutomationInspectors({
  automationId,
  runsOpen,
  previewOpen,
  canConfigure,
}: Readonly<{
  automationId: string;
  runsOpen: boolean;
  previewOpen: boolean;
  canConfigure: boolean;
}>) {
  if (!canConfigure) {
    return null;
  }
  return (
    <>
      {runsOpen && <AutomationRuns automationId={automationId} />}
      {previewOpen && <AutomationPreview automationId={automationId} />}
    </>
  );
}
