import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  EmptyState,
  SectionHeader,
} from "../design-system/atoms";
import { formatDateTime, formatNumber } from "../format/format";
import { useLocale, useT } from "../i18n";
import { ExportScenarioDialog } from "./aiexport";
import { problemMessage, QueryStates } from "./common";

type AiCall = components["schemas"]["AiCall"];

export function CallDetailPanel({
  id,
  captureEnabled,
}: Readonly<{ id: string; captureEnabled: boolean }>) {
  const t = useT();
  const [exporting, setExporting] = useState(false);
  const query = useQuery({
    queryKey: ["ai-call", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/ai/calls/{id}", {
        params: { path: { id } },
      });
      if (error) throw new Error(problemMessage(error));
      return data;
    },
  });
  return (
    <QueryStates query={query}>
      {query.data && (
        <div className="card card-inset">
          <p>
            {t("aicalls.detail.identity", {
              served: query.data.served_model,
              provider: query.data.provider,
              configured: query.data.model_id,
            })}
          </p>
          <p>
            {t("aicalls.detail.source", {
              source: query.data.served_identity_source,
            })}
          </p>
          <p>
            {query.data.context_scopes.length > 0
              ? t("aicalls.detail.context", {
                  scopes: query.data.context_scopes.join(", "),
                })
              : t("aicalls.detail.contextNone")}
          </p>
          <h3>{t("aicalls.detail.attempts")}</h3>
          <ol>
            {query.data.attempts.map((attempt) => (
              <li key={attempt.attempt}>
                <span className="t-mono">#{attempt.attempt}</span>{" "}
                {attempt.attempt_reason || "—"} ·{" "}
                {t("aicalls.ms", { value: attempt.latency_ms })}
                {attempt.error_sentinel && (
                  <Badge tone="danger">{attempt.error_sentinel}</Badge>
                )}
              </li>
            ))}
          </ol>
          {!captureEnabled ? (
            <p>{t("aicalls.payload.off")}</p>
          ) : !query.data.payload_captured || !query.data.payload ? (
            <p>{t("aicalls.payload.none")}</p>
          ) : (
            <>
              <h3>{t("aicalls.detail.request")}</h3>
              <pre
                className="t-mono"
                style={{ maxHeight: 260, overflow: "auto" }}
              >
                {JSON.stringify(query.data.payload.request, null, 2)}
              </pre>
              <h3>{t("aicalls.detail.response")}</h3>
              <pre
                className="t-mono"
                style={{ maxHeight: 260, overflow: "auto" }}
              >
                {JSON.stringify(query.data.payload.response, null, 2)}
              </pre>
              <Button small onClick={() => setExporting(true)}>
                {t("aiexport.button")}
              </Button>
              {exporting && (
                <ExportScenarioDialog
                  call={query.data as AiCall}
                  onClose={() => setExporting(false)}
                />
              )}
            </>
          )}
        </div>
      )}
    </QueryStates>
  );
}

export function AiCallsCard() {
  const t = useT();
  const { locale } = useLocale();
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const [task, setTask] = useState("");
  const [expanded, setExpanded] = useState<string | null>(null);
  const query = useInfiniteQuery({
    queryKey: ["ai-calls", task],
    initialPageParam: null as string | null,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET("/ai/calls", {
        params: {
          query: { cursor: pageParam ?? undefined, task: task || undefined },
        },
      });
      if (error) throw new Error(problemMessage(error));
      return data;
    },
    getNextPageParam: (last) => last.page.next_cursor ?? null,
  });
  const calls = query.data?.pages.flatMap((page) => page.data) ?? [];
  const captureEnabled = query.data?.pages[0]?.payload_capture_enabled ?? false;
  const tasks = [...new Set(calls.map((call) => call.task))].sort();

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <SectionHeader title={t("aicalls.title")} sub={t("aicalls.sub")} />
      <QueryStates query={query}>
        <select
          className="input"
          value={task}
          onChange={(event) => setTask(event.target.value)}
        >
          <option value="">{t("aicalls.filter.all")}</option>
          {tasks.map((value) => (
            <option key={value}>{value}</option>
          ))}
        </select>
        {calls.length === 0 ? (
          <EmptyState>{t("aicalls.empty")}</EmptyState>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>{t("aicalls.col.when")}</th>
                <th>{t("aicalls.col.task")}</th>
                <th>{t("aicalls.col.model")}</th>
                <th>{t("aicalls.col.tokens")}</th>
                <th>{t("aicalls.col.latency")}</th>
              </tr>
            </thead>
            <tbody>
              {calls.map((call) => (
                <FragmentRow
                  key={call.id}
                  call={call}
                  expanded={expanded === call.id}
                  captureEnabled={captureEnabled}
                  onToggle={() =>
                    setExpanded(expanded === call.id ? null : call.id)
                  }
                  when={formatDateTime(call.occurred_at, locale, zone)}
                  tokens={`${formatNumber(call.tokens_in, locale)} / ${formatNumber(call.tokens_out, locale)}`}
                />
              ))}
            </tbody>
          </table>
        )}
        {query.hasNextPage && (
          <Button
            small
            disabled={query.isFetchingNextPage}
            onClick={() => void query.fetchNextPage()}
          >
            {t("aicalls.loadMore")}
          </Button>
        )}
      </QueryStates>
    </section>
  );
}

function FragmentRow({
  call,
  expanded,
  captureEnabled,
  onToggle,
  when,
  tokens,
}: Readonly<{
  call: components["schemas"]["AiCallSummary"];
  expanded: boolean;
  captureEnabled: boolean;
  onToggle: () => void;
  when: string;
  tokens: string;
}>) {
  const t = useT();
  return (
    <>
      <tr onClick={onToggle} style={{ cursor: "pointer" }}>
        <td>{when}</td>
        <td>
          {call.task}
          <div style={{ display: "flex", gap: "var(--space-1)" }}>
            {call.cache_hit && <Badge>{t("aicalls.badge.cacheHit")}</Badge>}
            {call.degraded && (
              <Badge tone="warn">{t("aicalls.badge.degraded")}</Badge>
            )}
            {call.error_sentinel && (
              <Badge tone="danger">{call.error_sentinel}</Badge>
            )}
            {call.calls_attempted > 1 && (
              <Badge>
                {t("aicalls.badge.retries", { count: call.calls_attempted })}
              </Badge>
            )}
          </div>
        </td>
        <td>
          {call.tier} · {call.provider}/{call.served_model}
        </td>
        <td>{tokens}</td>
        <td>{t("aicalls.ms", { value: call.latency_ms })}</td>
      </tr>
      {expanded && (
        <tr>
          <td colSpan={5}>
            <CallDetailPanel id={call.id} captureEnabled={captureEnabled} />
          </td>
        </tr>
      )}
    </>
  );
}
