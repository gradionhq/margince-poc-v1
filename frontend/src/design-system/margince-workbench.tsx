import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { MarginceCoreScene, type MarginceCoreState } from "./margince-core";
import "./margince-workbench.css";

type AiRunSummary = components["schemas"]["AiRunSummary"];

export function MarginceWorkbench({
  state,
  progress,
  eyebrow,
  title,
  status,
  configured,
  locale,
  runtime,
  runtimeLabels,
  children,
  artifact,
}: Readonly<{
  state: MarginceCoreState;
  progress?: number;
  eyebrow: string;
  title: string;
  status: string;
  configured: string;
  locale: string;
  runtime?: AiRunSummary;
  runtimeLabels: {
    configured: string;
    used: string;
    calls: string;
    tokens: string;
    estimatedCost: string;
    partial: string;
    awaiting: string;
  };
  children: ReactNode;
  artifact?: ReactNode;
}>) {
  return (
    <div className="mw-shell">
      <header className="mw-header">
        <MarginceCoreScene
          state={state}
          progress={progress}
          className="mw-core"
        />
        <div className="mw-identity">
          <span>{eyebrow}</span>
          <h1>{title}</h1>
          <p>
            <i data-state={state} aria-hidden /> {status}
          </p>
        </div>
        <div className="mw-configured">
          <span>{runtimeLabels.configured}</span>
          <strong>{configured}</strong>
        </div>
      </header>

      <RuntimeBar runtime={runtime} labels={runtimeLabels} locale={locale} />

      <div className={`mw-body ${artifact ? "has-artifact" : ""}`}>
        <section className="mw-conversation">{children}</section>
        {artifact && <aside className="mw-artifact">{artifact}</aside>}
      </div>
    </div>
  );
}

function RuntimeBar({
  runtime,
  labels,
  locale,
}: Readonly<{
  runtime?: AiRunSummary;
  locale: string;
  labels: {
    used: string;
    calls: string;
    tokens: string;
    estimatedCost: string;
    partial: string;
    awaiting: string;
  };
}>) {
  const models = runtime?.models ?? [];
  const used = models
    .map((entry) => entry.served_model || entry.configured_model)
    .filter((model, index, all) => model && all.indexOf(model) === index)
    .join(" + ");
  return (
    <div className="mw-runtime">
      <RuntimeFact label={labels.used} value={used || labels.awaiting} />
      <RuntimeFact
        label={labels.calls}
        value={runtime ? String(runtime.call_attempts) : "—"}
      />
      <RuntimeFact
        label={labels.tokens}
        value={
          runtime
            ? new Intl.NumberFormat(locale).format(
                runtime.tokens_in + runtime.tokens_out,
              )
            : "—"
        }
      />
      <RuntimeFact
        label={labels.estimatedCost}
        value={
          runtime
            ? formatMicroUSD(runtime.estimated_cost_microusd, locale)
            : "—"
        }
        note={runtime?.unpriced_calls ? labels.partial : undefined}
      />
    </div>
  );
}

function RuntimeFact({
  label,
  value,
  note,
}: Readonly<{ label: string; value: string; note?: string }>) {
  return (
    <div>
      <span>{label}</span>
      <strong title={value}>{value}</strong>
      {note && <small>{note}</small>}
    </div>
  );
}

function formatMicroUSD(value: number, locale: string) {
  return new Intl.NumberFormat(locale, {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: value > 0 && value < 10_000 ? 4 : 2,
    maximumFractionDigits: 6,
  }).format(value / 1_000_000);
}
