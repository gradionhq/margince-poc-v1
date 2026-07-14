import { ArrowRight } from "lucide-react";
import { type ReactNode, useState } from "react";
import { useT } from "../i18n";
import "./trust.css";

// The Margince trust primitives (B-EP09.3a, design-language §4): the
// vocabulary that makes AI-authored state legible — where a value came from,
// how sure the system is, and whether it is real yet. The universal triad is
// Accept / Edit / Dismiss; an Edit flips the value to human-typed while
// retaining the original evidence (§4.4).

export type ConfidenceLevel = "high" | "med" | "low";

export type Evidence = {
  snippet: string;
  source: string;
};

// The contract's `evidence` is an untyped free-form object (agent actors
// only; no fixed shape yet at the contract level) — narrow it to the trust
// vocabulary's Evidence before handing it to EvidenceChip. Anything that
// doesn't carry both fields is treated as "no evidence" rather than guessed.
// Shared by every screen that renders an audit/history row's evidence
// (settings' audit log, the record History timelines) so there is one
// narrowing, not a copy per call site.
export function toEvidence(
  raw: { [key: string]: unknown } | null | undefined,
): Evidence | null {
  if (
    raw &&
    typeof raw.snippet === "string" &&
    typeof raw.source === "string"
  ) {
    return { snippet: raw.snippet, source: raw.source };
  }
  return null;
}

export function AutonomyDot({ tier }: Readonly<{ tier: "auto" | "confirm" }>) {
  const t = useT();
  return (
    <span
      className={`dot dot-${tier}`}
      // NOSONAR: CSS-drawn status glyph (no bitmap); <img> would need a src the design has none of, and .dot styling targets the span
      role="img"
      aria-label={tier === "auto" ? t("autonomy.auto") : t("autonomy.confirm")}
    />
  );
}

export function EvidenceChip({
  evidence,
  onOpen,
}: Readonly<{
  evidence: Evidence;
  onOpen?: () => void;
}>) {
  const text = (
    <>
      "{evidence.snippet}" · {evidence.source}
    </>
  );
  if (onOpen) {
    return (
      <button type="button" className="evidence-chip" onClick={onOpen}>
        {text}
      </button>
    );
  }
  return <span className="evidence-chip">{text}</span>;
}

// Low confidence is shown as low, never hidden (§4.2) — there is no prop to
// suppress the glyph.
export function ConfidenceMeter({
  level,
}: Readonly<{ level: ConfidenceLevel }>) {
  const t = useT();
  return (
    <span className={`confidence confidence-${level}`}>
      <span className="dot" />
      {t(`confidence.${level}`)}
    </span>
  );
}

// Provenance is either an agent (`agent:capture`) or the human user.
export type Provenance = { kind: "agent"; agent: string } | { kind: "human" };

export function ProvenanceTag({
  provenance,
}: Readonly<{ provenance: Provenance }>) {
  const t = useT();
  if (provenance.kind === "agent") {
    return (
      <span className="provenance provenance-agent">
        {t("trust.agentTag", { agent: provenance.agent })}
      </span>
    );
  }
  return (
    <span className="provenance provenance-human">{t("trust.typedByYou")}</span>
  );
}

export function ApprovalGate({
  onAccept,
  onEdit,
  onDismiss,
}: Readonly<{
  onAccept: () => void;
  onEdit: () => void;
  onDismiss: () => void;
}>) {
  const t = useT();
  return (
    <div className="approval-gate">
      <button
        type="button"
        className="btn btn-primary btn-sm"
        onClick={onAccept}
      >
        {t("trust.accept")}
      </button>
      <button type="button" className="btn btn-ghost btn-sm" onClick={onEdit}>
        {t("trust.edit")}
      </button>
      <button
        type="button"
        className="btn btn-ghost btn-sm"
        onClick={onDismiss}
      >
        {t("trust.dismiss")}
      </button>
    </div>
  );
}

export function StagingCard({ children }: Readonly<{ children: ReactNode }>) {
  const t = useT();
  return (
    <section className="staging-card" aria-label={t("trust.stagedProposal")}>
      {children}
    </section>
  );
}

export type Proposal = {
  description: string;
  value: string;
  agent: string;
  confidence: ConfidenceLevel;
  evidence?: Evidence;
};

export type Resolution =
  | { outcome: "accepted"; value: string }
  | { outcome: "edited"; value: string }
  | { outcome: "dismissed" };

type ProposalState =
  | { phase: "staged" }
  | { phase: "editing"; draft: string }
  | { phase: "resolved"; resolution: Resolution };

// StagedProposal drives one proposal through the triad. It owns only the
// presentation state machine — persisting the outcome is the caller's job via
// onResolve (the approvals API, once the screens wire in).
export function StagedProposal({
  proposal,
  onResolve,
}: Readonly<{
  proposal: Proposal;
  onResolve?: (resolution: Resolution) => void;
}>) {
  const t = useT();
  const [state, setState] = useState<ProposalState>({ phase: "staged" });

  const resolve = (resolution: Resolution) => {
    setState({ phase: "resolved", resolution });
    onResolve?.(resolution);
  };

  if (state.phase === "resolved") {
    const { resolution } = state;
    if (resolution.outcome === "dismissed") {
      return <p className="t-small">{t("trust.dismissed")}</p>;
    }
    // Accepted keeps agent provenance; an edit makes the value human-typed.
    // Either way the original evidence stays attached (§4.4).
    const provenance: Provenance =
      resolution.outcome === "edited"
        ? { kind: "human" }
        : { kind: "agent", agent: proposal.agent };
    return (
      <section className="real-card" aria-label={t("trust.resolvedValue")}>
        <ProvenanceTag provenance={provenance} />
        <p style={{ marginTop: 8 }}>
          {proposal.description}: <strong>{resolution.value}</strong>
        </p>
        {proposal.evidence && <EvidenceChip evidence={proposal.evidence} />}
      </section>
    );
  }

  return (
    <StagingCard>
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <ProvenanceTag provenance={{ kind: "agent", agent: proposal.agent }} />
        <ConfidenceMeter level={proposal.confidence} />
      </div>
      <p style={{ marginTop: 8 }}>
        {proposal.description}:{" "}
        <span className="staged-value">{proposal.value}</span>
      </p>
      {proposal.evidence && <EvidenceChip evidence={proposal.evidence} />}
      {state.phase === "editing" ? (
        <form
          className="approval-gate"
          onSubmit={(event) => {
            event.preventDefault();
            resolve({ outcome: "edited", value: state.draft });
          }}
        >
          <input
            className="staged-edit"
            aria-label={t("trust.editValue", {
              description: proposal.description,
            })}
            value={state.draft}
            onChange={(event) =>
              setState({ phase: "editing", draft: event.target.value })
            }
          />
          <button type="submit" className="btn btn-primary btn-sm">
            {t("trust.save")}
          </button>
        </form>
      ) : (
        <ApprovalGate
          onAccept={() =>
            resolve({ outcome: "accepted", value: proposal.value })
          }
          onEdit={() => setState({ phase: "editing", draft: proposal.value })}
          onDismiss={() => resolve({ outcome: "dismissed" })}
        />
      )}
    </StagingCard>
  );
}

// The inline old→new field diff: struck-through prior value, arrow, highlighted
// new value. A null side is an honest marker, never a blank or a guessed value.
export function FieldDiff({
  oldValue,
  newValue,
}: Readonly<{ oldValue: string | null; newValue: string | null }>) {
  const t = useT();
  return (
    <span className="field-diff">
      {oldValue === null ? (
        <span className="field-diff-empty">{t("history.created")}</span>
      ) : (
        <span className="field-diff-from">{oldValue}</span>
      )}
      <ArrowRight className="field-diff-arrow" aria-hidden size={14} />
      {newValue === null ? (
        <span className="field-diff-empty">{t("history.cleared")}</span>
      ) : (
        <span className="field-diff-to">{newValue}</span>
      )}
    </span>
  );
}

// A governed agent's passport id, shown mono so it reads as an identifier.
export function PassportChip({ id }: Readonly<{ id: string }>) {
  const t = useT();
  return (
    <span className="passport-chip" title={t("history.passport")}>
      {id}
    </span>
  );
}
