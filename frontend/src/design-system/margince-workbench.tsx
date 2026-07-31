import type { ReactNode } from "react";
import { useEffect, useId, useRef, useState } from "react";
import type { components } from "../api/schema";
import { MarginceCoreScene, type MarginceCoreState } from "./margince-core";
import "./margince-workbench.css";

type AiRunSummary = components["schemas"]["AiRunSummary"];

export type WorkbenchStep = Readonly<{
  label: string;
  state: "done" | "now" | "todo";
}>;

export type WorkbenchRuntimeLabels = Readonly<{
  configured: string;
  used: string;
  route: string;
  calls: string;
  tokens: string;
  latency: string;
  estimatedCost: string;
  partial: string;
  awaiting: string;
  unavailable: string;
  chip: string;
  answering: string;
  scope: string;
}>;

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
  steps,
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
  runtimeLabels: WorkbenchRuntimeLabels;
  steps?: readonly WorkbenchStep[];
  children: ReactNode;
  artifact?: ReactNode;
}>) {
  return (
    <div className="mw-shell">
      <div className={`mw-body ${artifact ? "has-artifact" : ""}`}>
        <section className="mw-conversation">
          {steps && steps.length > 0 && <StepRail steps={steps} />}
          <header className="mw-brand">
            <MarginceCoreScene
              state={state}
              progress={progress}
              feed={false}
              className="mw-core"
            />
            <div className="mw-identity">
              <span>{eyebrow}</span>
              <h1>{title}</h1>
              <p>
                <i data-state={state} aria-hidden /> {status}
              </p>
            </div>
            <AiRuntimeChip
              runtime={runtime}
              configured={configured}
              labels={runtimeLabels}
              locale={locale}
            />
          </header>
          {children}
        </section>
        {artifact && <aside className="mw-artifact">{artifact}</aside>}
      </div>
    </div>
  );
}

// The rail states where the journey is without claiming a step is reachable:
// a `todo` pill is inert text, never a link, because the machine — not the
// rail — decides what comes next.
function StepRail({ steps }: Readonly<{ steps: readonly WorkbenchStep[] }>) {
  return (
    <ol className="mw-steps">
      {steps.map((step, index) => (
        <li key={step.label} className={`mw-step is-${step.state}`}>
          <b aria-hidden>{index + 1}</b>
          {step.label}
        </li>
      ))}
    </ol>
  );
}

// Cost disclosure as an opt-in chip. The summary line (spend so far) is always
// visible because a reader must never have to ask what a run is costing; the
// per-model breakdown opens on demand. Hover reveals, click pins — so a
// pointer user reads it in passing and a keyboard user can keep it open while
// they read it.
function AiRuntimeChip({
  runtime,
  configured,
  labels,
  locale,
}: Readonly<{
  runtime?: AiRunSummary;
  configured: string;
  labels: WorkbenchRuntimeLabels;
  locale: string;
}>) {
  const [pinned, setPinned] = useState(false);
  const [hovered, setHovered] = useState(false);
  const popoverId = useId();
  const wrapper = useRef<HTMLDivElement>(null);
  const open = pinned || hovered;

  // Hover is tracked on the WRAPPER so the pointer can travel from the chip
  // onto the popover without it closing underneath. Native listeners rather
  // than JSX handlers (the same choice artifact.tsx documents): the wrapper
  // stays a plain layout element, and hover is an input to the open state
  // rather than an interaction of its own — the chip inside is the control,
  // and it is a real button reachable by keyboard.
  useEffect(() => {
    const root = wrapper.current;
    if (!root) {
      return;
    }
    const enter = () => setHovered(true);
    const leave = () => setHovered(false);
    root.addEventListener("mouseenter", enter);
    root.addEventListener("mouseleave", leave);
    return () => {
      root.removeEventListener("mouseenter", enter);
      root.removeEventListener("mouseleave", leave);
    };
  }, []);

  // A pinned popover has to close on Escape and on a click elsewhere, or it
  // becomes a panel the reader cannot dismiss without guessing.
  useEffect(() => {
    if (!pinned) {
      return;
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        setPinned(false);
      }
    }
    function onPointerDown(event: PointerEvent) {
      const target = event.target;
      if (target instanceof Node && !wrapper.current?.contains(target)) {
        setPinned(false);
      }
    }
    document.addEventListener("keydown", onKeyDown);
    document.addEventListener("pointerdown", onPointerDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("pointerdown", onPointerDown);
    };
  }, [pinned]);

  const spend = runtime
    ? formatMicroUSD(runtime.estimated_cost_microusd, locale)
    : labels.awaiting;

  return (
    <div className="mw-aistat" ref={wrapper}>
      <button
        type="button"
        className="mw-aistat-btn"
        aria-expanded={open}
        aria-controls={popoverId}
        aria-label={labels.chip}
        onClick={() => setPinned(!pinned)}
        onFocus={() => setHovered(true)}
        onBlur={() => setHovered(false)}
      >
        <i aria-hidden />
        <strong>{spend}</strong>
      </button>
      <div className="mw-aistat-pop" id={popoverId} hidden={!open}>
        <p className="mw-aistat-h">{labels.answering}</p>
        <dl className="mw-aistat-rows">
          <RuntimeRow label={labels.configured} value={configured} />
          <RuntimeRow
            label={labels.used}
            value={servedModels(runtime) || labels.awaiting}
          />
          <RuntimeRow
            label={labels.route}
            value={routes(runtime) || labels.unavailable}
          />
          <RuntimeRow
            label={labels.calls}
            value={runtime ? String(runtime.call_attempts) : labels.unavailable}
          />
          <RuntimeRow
            label={labels.tokens}
            value={
              runtime
                ? new Intl.NumberFormat(locale).format(
                    runtime.tokens_in + runtime.tokens_out,
                  )
                : labels.unavailable
            }
          />
          <RuntimeRow
            label={labels.latency}
            value={
              runtime
                ? `${new Intl.NumberFormat(locale).format(runtime.latency_ms)} ms`
                : labels.unavailable
            }
          />
          <RuntimeRow
            label={labels.estimatedCost}
            value={runtime ? spend : labels.unavailable}
            note={runtime?.unpriced_calls ? labels.partial : undefined}
          />
        </dl>
        <p className="mw-aistat-f">{labels.scope}</p>
      </div>
    </div>
  );
}

function RuntimeRow({
  label,
  value,
  note,
}: Readonly<{ label: string; value: string; note?: string }>) {
  return (
    <div className="mw-aistat-r">
      <dt>{label}</dt>
      <dd>
        {value}
        {note && <small>{note}</small>}
      </dd>
    </div>
  );
}

function servedModels(runtime?: AiRunSummary) {
  return unique(
    (runtime?.models ?? []).map((entry) => entry.served_model).filter(Boolean),
  ).join(" + ");
}

function routes(runtime?: AiRunSummary) {
  return unique(
    (runtime?.models ?? []).map(
      (entry) => `${entry.task} · ${entry.tier} · ${entry.provider}`,
    ),
  ).join(" + ");
}

function unique(values: string[]) {
  return values.filter((value, index) => values.indexOf(value) === index);
}

function formatMicroUSD(value: number, locale: string) {
  return new Intl.NumberFormat(locale, {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: value > 0 && value < 10_000 ? 4 : 2,
    maximumFractionDigits: 6,
  }).format(value / 1_000_000);
}
