import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { ChevronDown } from "lucide-react";
import { type CSSProperties, useId, useState } from "react";
import { api, FIRST_PAGE } from "../api/client";
import type { components } from "../api/schema";
import { useCan } from "../app/capability";
import { Badge, Button, Card, EmptyState } from "../design-system/atoms";
import { Eyebrow } from "../design-system/eyebrow";
import { Panel, PanelBody } from "../design-system/panel";
import { Select } from "../design-system/select";
import { formatDateTime, formatNumber } from "../format/format";
import { useLocale, useT } from "../i18n";
import { ExportScenarioDialog } from "./aiexport";
import { QueryGate, QueryStates, throwProblem, useMe } from "./common";

// The gap under a panel's own subtitle. `Panel` has no `sub` prop, so the line
// is the body's first paragraph and owes its own separation from the content
// under it; it is a token rather than a number so it moves with the scale, and
// it lives here rather than in a screen sheet because it belongs to the panel
// shape, not to this surface. It folds away the day `Panel` takes a `sub`.
const PANEL_SUB: CSSProperties = { marginBottom: "var(--space-3)" };

// A string response is shown verbatim (real newlines); an object is
// pretty-printed. Either way the .code-block surface wraps and scrolls it.
function payloadText(value: unknown): string {
  return typeof value === "string" ? value : JSON.stringify(value, null, 2);
}

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
      if (error) throwProblem(error);
      return data;
    },
  });
  return (
    <QueryStates query={query}>
      {query.data && (
        <Card as="div" inset>
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
          {/* A bare <h3> carries no class, and preflight leaves it at body size
              and body weight — a heading only the document tree can see. The
              eyebrow is the one spelling of a label over a block, and `as="h3"`
              is what keeps it a real heading inside the card's own h2. */}
          <Eyebrow as="h3">{t("aicalls.detail.attempts")}</Eyebrow>
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
              <div
                className="form-stack"
                style={{ marginTop: "var(--space-3)" }}
              >
                <div className="field">
                  <span className="code-label t-eyebrow">
                    {t("aicalls.detail.request")}
                  </span>
                  <pre className="code-block">
                    {payloadText(query.data.payload.request)}
                  </pre>
                </div>
                <div className="field">
                  <span className="code-label t-eyebrow">
                    {t("aicalls.detail.response")}
                  </span>
                  <pre className="code-block">
                    {payloadText(query.data.payload.response)}
                  </pre>
                </div>
                <div>
                  <Button small onClick={() => setExporting(true)}>
                    {t("aiexport.button")}
                  </Button>
                </div>
              </div>
              {exporting && (
                <ExportScenarioDialog
                  call={query.data}
                  onClose={() => setExporting(false)}
                />
              )}
            </>
          )}
        </Card>
      )}
    </QueryStates>
  );
}

export function AiCallsCard() {
  const t = useT();
  const { locale } = useLocale();
  const me = useMe();
  // Same seam as the spend card beside it: the server gates this read on
  // automation:update, a write verb guarding a GET, so the seat ceiling stays out
  // of the question (capability.ts) — a read seat may still read it.
  const canSee = useCan("automation", "update");
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const [task, setTask] = useState("");
  const [expanded, setExpanded] = useState<string | null>(null);
  const query = useInfiniteQuery({
    enabled: canSee,
    queryKey: ["ai-calls", task],
    initialPageParam: FIRST_PAGE,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET("/ai/calls", {
        params: {
          query: { cursor: pageParam ?? undefined, task: task || undefined },
        },
      });
      if (error) throwProblem(error);
      return data;
    },
    getNextPageParam: (last) => last.page.next_cursor ?? null,
  });
  const calls = query.data?.pages.flatMap((page) => page.data) ?? [];
  const captureEnabled = query.data?.pages[0]?.payload_capture_enabled ?? false;
  // The filter options are the server's complete task set (carried on every
  // page), NOT the tasks on the loaded rows: deriving them from `calls` would
  // collapse the dropdown to the one selected task once a filter is applied.
  const tasks = query.data?.pages[0]?.tasks ?? [];

  if (!canSee) {
    // Withheld, not absent — the same choice the spend card above it makes. An
    // absent trace reads as "the installation made no model calls", which is a
    // claim about the data rather than about who may read it. No request either:
    // the query is disabled, because a settled denial is not a failure to retry.
    return (
      <Panel title={t("aicalls.title")}>
        <PanelBody>
          <p className="t-sub" style={PANEL_SUB}>
            {t("aicalls.sub")}
          </p>
          <QueryGate query={me}>
            {() => (
              <EmptyState>
                <p className="t-small">{t("aicalls.withheld")}</p>
              </EmptyState>
            )}
          </QueryGate>
        </PanelBody>
      </Panel>
    );
  }

  // No bottom margin of its own: `.settings-stack` owns the gap between cards.
  return (
    <Panel title={t("aicalls.title")}>
      <PanelBody>
        <p className="t-sub" style={PANEL_SUB}>
          {t("aicalls.sub")}
        </p>
        <QueryStates query={query}>
          <Select
            // The column header names what this filters on; the control sits above
            // the table with nothing beside it, so without a name of its own a
            // screen reader reaches an unnamed combobox.
            aria-label={t("aicalls.col.task")}
            value={task}
            onChange={setTask}
            // "All tasks" is a real option, not the select's placeholder: a
            // reader who filtered to one task has to be able to come back.
            // A task name is a wire value the server owns, so it is its own
            // label — there is nothing to translate.
            options={[
              { value: "", label: t("aicalls.filter.all") },
              ...tasks.map((value) => ({ value, label: value })),
            ]}
          />
          {calls.length === 0 ? (
            <EmptyState>{t("aicalls.empty")}</EmptyState>
          ) : (
            // Six columns of trace, none of them droppable — a call is only
            // diagnosable with its model, its tokens and its latency side by
            // side. The table scrolls sideways inside the card instead of the
            // page scrolling, the same containment DataTable gives every list
            // built from it (atoms.tsx).
            <div className="table-scroll">
              <table className="table">
                <thead>
                  <tr>
                    {/* The disclosure column. Named rather than left blank: a
                      table that announces five headers for six cells makes the
                      reader count. */}
                    <th className="sr-only">{t("aicalls.col.detail")}</th>
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
            </div>
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
      </PanelBody>
    </Panel>
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
  const panelId = useId();
  return (
    <>
      {/* The disclosure is a real button in the first cell, not a click handler on
          the row. A `<tr onClick>` is reachable by pointer alone: it takes no
          focus, answers no key, and announces no state — so the attempt trail
          behind it, which is the whole reason this table expands, was unreachable
          by keyboard and to a screen reader. The subject-request queue
          (screens/privacy.tsx) is the shape this follows. */}
      <tr>
        <td>
          <Button
            small
            variant="ghost"
            aria-expanded={expanded}
            aria-controls={expanded ? panelId : undefined}
            // Named by the call it opens, not "Show detail": a page of twenty
            // rows would otherwise offer twenty identically-named buttons.
            aria-label={t("aicalls.expandCall", { task: call.task, when })}
            onClick={onToggle}
          >
            <ChevronDown
              aria-hidden
              size={14}
              style={{ transform: expanded ? "rotate(180deg)" : undefined }}
            />
          </Button>
        </td>
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
          <td colSpan={6} id={panelId}>
            <CallDetailPanel id={call.id} captureEnabled={captureEnabled} />
          </td>
        </tr>
      )}
    </>
  );
}
