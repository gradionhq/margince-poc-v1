import type { components } from "../api/schema";
import { Button } from "../design-system/atoms";
import { formatDate } from "../format/format";
import { viewerZone } from "../format/timezone";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";

type Lead = components["schemas"]["Lead"];

// The ladder a lead climbs, as the page draws it directly under the header:
// New → Contacted → Engaged → Qualified | Disqualified. Steps up to the
// current one are filled; an open step is a button that places the lead
// there by hand; the two terminal steps open their dialogs, because each is
// a governed transition with its own questions (a contact, a reason).
//
// Under it, one line says how the lead got where it is: the system moved it
// from a captured activity, or a human did.

export const LADDER_OPEN_STEPS = ["new", "contacted", "engaged"] as const;
export type LadderOpenStep = (typeof LADDER_OPEN_STEPS)[number];

const STEP_LABEL: Record<Lead["status"], MessageKey> = {
  new: "lead.status.new",
  contacted: "lead.status.contacted",
  engaged: "lead.status.engaged",
  promoted: "lead.statusPromoted",
  disqualified: "lead.statusDisqualified",
};

function rungOf(status: Lead["status"]): number {
  switch (status) {
    case "new":
      return 0;
    case "contacted":
      return 1;
    case "engaged":
      return 2;
    case "promoted":
    case "disqualified":
      return 3;
  }
}

export function LeadStepper({
  lead,
  pending,
  readOnlyReason,
  onStep,
  onQualify,
  onDisqualify,
}: Readonly<{
  lead: Lead;
  pending: boolean;
  // Why no step may be taken — a terminal lead, or an overlay mirror.
  readOnlyReason?: string;
  onStep: (status: LadderOpenStep) => void;
  onQualify: () => void;
  onDisqualify: () => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const zone = viewerZone();
  const current = rungOf(lead.status);
  const steps: {
    key: Lead["status"];
    rung: number;
    onPick: () => void;
    filled: boolean;
    current: boolean;
  }[] = [
    ...LADDER_OPEN_STEPS.map((status, rung) => ({
      key: status,
      rung,
      onPick: () => onStep(status),
      filled: current >= rung,
      current: lead.status === status,
    })),
    {
      key: "promoted" as const,
      rung: 3,
      onPick: onQualify,
      filled: lead.status === "promoted",
      current: lead.status === "promoted",
    },
    {
      key: "disqualified" as const,
      rung: 3,
      onPick: onDisqualify,
      filled: lead.status === "disqualified",
      current: lead.status === "disqualified",
    },
  ];
  return (
    <nav aria-label={t("lead.ladder")} className="lead-ladder">
      <ol className="lead-ladder-steps">
        {steps.map((step, index) => (
          <li
            key={step.key}
            className={[
              "lead-ladder-step",
              step.filled ? "is-filled" : "",
              step.current ? "is-current" : "",
              step.rung === 3 ? "is-terminal" : "",
            ]
              .filter(Boolean)
              .join(" ")}
            aria-current={step.current ? "step" : undefined}
          >
            {index > 0 && (
              <span aria-hidden="true" className="lead-ladder-sep">
                ›
              </span>
            )}
            <Button
              small
              variant={step.current ? "primary" : "ghost"}
              data-testid={`lead-step-${step.key}`}
              // The current step is a fact, not an action; a lead that takes
              // no step — terminal, or a mirror — says why on every other one.
              disabled={step.current || pending}
              reason={!step.current ? readOnlyReason : undefined}
              onClick={step.onPick}
            >
              {t(STEP_LABEL[step.key])}
            </Button>
          </li>
        ))}
      </ol>
      <p className="t-caption lead-ladder-how">
        {ladderExplanation(lead, t, locale, zone)}
      </p>
    </nav>
  );
}

// How the lead got to its step, in one line. The row records WHO moved it
// (status_set_by); the strongest captured signal says what the system read.
function ladderExplanation(
  lead: Lead,
  t: ReturnType<typeof useT>,
  locale: Locale,
  zone: string,
): string {
  const label = t(STEP_LABEL[lead.status]);
  if (lead.status === "new") {
    return t("lead.ladder.new");
  }
  if (lead.status === "disqualified") {
    return lead.disqualify_reason
      ? t("lead.ladder.disqualifiedWithReason", {
          reason: lead.disqualify_reason,
        })
      : t("lead.ladder.disqualified");
  }
  if (lead.status === "promoted") {
    return lead.promoted_at
      ? t("lead.ladder.qualifiedOn", {
          at: formatDate(lead.promoted_at, locale, zone),
        })
      : t("lead.ladder.qualified");
  }
  if (lead.status_set_by === "system") {
    return systemExplanation(lead, label, t, locale, zone);
  }
  if (lead.status_set_by === "human") {
    return t("lead.ladder.byHand", { label });
  }
  // Nobody is recorded: a lead carried over from before the ladder existed.
  return label;
}

// What the system read when it moved the lead: the engagement signal behind
// an engaged step, or simply "captured activity" for a contacted one.
function systemExplanation(
  lead: Lead,
  label: string,
  t: ReturnType<typeof useT>,
  locale: Locale,
  zone: string,
): string {
  const evidence = lead.qualification_evidence;
  if (lead.status !== "engaged" || !evidence?.occurred_at) {
    return t("lead.ladder.automatic", { label });
  }
  const whatKey: MessageKey =
    evidence.trigger === "inbound_reply"
      ? "lead.ladder.theyReplied"
      : evidence.trigger === "meeting_held"
        ? "lead.ladder.meetingHeld"
        : "lead.ladder.meetingBooked";
  return t("lead.ladder.automaticWith", {
    label,
    what: t(whatKey),
    at: formatDate(evidence.occurred_at, locale, zone),
  });
}
