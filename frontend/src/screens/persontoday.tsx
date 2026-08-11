import {
  AlertTriangle,
  CalendarDays,
  Mail,
  Sparkles,
  Users,
} from "lucide-react";
import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { Button } from "../design-system/atoms";
import { useT } from "../i18n";

// "Today with {first name}" (concept §5.5, ADR-0096 D2).
//
// ONE moment, chosen server-side by the fixed ladder. The client renders what
// it is given and computes nothing: a page that picked its own headline from
// date comparisons would drift from every other client showing the same
// record, which is the drift the rule/version stamp exists to make impossible.
//
// The action is a TYPED descriptor, so this renders only buttons whose
// destination the server named. An action with no destination still renders —
// some are their own destination — but one this client cannot route is not
// invented into a button that 404s.

type PersonMoment = components["schemas"]["PersonMoment"];
type PersonMomentAction = components["schemas"]["PersonMomentAction"];

export function PersonToday({
  moment,
  firstName,
  onAction,
}: Readonly<{
  moment: PersonMoment;
  firstName: string;
  onAction: (action: PersonMomentAction) => void;
}>) {
  const t = useT();
  // The amber treatment is the finding itself — a relationship that stopped,
  // or a promise that is late — so it colours the card rather than a badge
  // inside it.
  const warn =
    moment.rule === "gone_quiet" || moment.rule === "overdue_promise";
  const secondary = moment.secondary_actions ?? [];
  return (
    <section
      className={warn ? "pe-today pe-today-warn" : "pe-today"}
      data-testid="person-today"
      data-rule={moment.rule}
    >
      <div>
        <div className="pe-today-head">
          {warn ? (
            <AlertTriangle size={16} aria-hidden="true" />
          ) : (
            <Sparkles size={16} aria-hidden="true" />
          )}
          <span>{t("person.today.heading", { name: firstName })}</span>
        </div>
        <h2 className="pe-today-headline">{moment.headline}</h2>

        <ul className="pe-today-evidence">
          {moment.evidence.map((item) => (
            <li key={`${item.type}-${item.id ?? item.label}`}>
              {evidenceIcon(item.type)}
              <span>{item.label}</span>
            </li>
          ))}
        </ul>

        {/* The rule that fired, named. A reader who disagrees with the verdict
            can see what produced it, which is the difference between a system
            that judges and one that explains. */}
        {warn && <p className="pe-today-rule">{moment.why_now}</p>}

        <div className="pe-today-foot">
          <span>
            {t(
              moment.evidence.length === 1
                ? "person.today.source"
                : "person.today.sources",
              { count: moment.evidence.length },
            )}
          </span>
          {moment.freshness_at && (
            <>
              <span aria-hidden="true">·</span>
              <span>
                {t("person.today.updated", {
                  when: freshness(moment.freshness_at, t),
                })}
              </span>
            </>
          )}
        </div>
      </div>

      <div className="pe-today-actions">
        <Button
          variant="primary"
          onClick={() => onAction(moment.recommended_action)}
          disabled={moment.recommended_action.state === "blocked"}
          title={moment.recommended_action.blocked_reason}
        >
          {moment.recommended_action.label}
        </Button>
        {secondary.map((action) => (
          <Button
            key={action.label}
            onClick={() => onAction(action)}
            disabled={action.state === "blocked"}
            title={action.blocked_reason}
          >
            {action.label}
          </Button>
        ))}
      </div>
    </section>
  );
}

function evidenceIcon(type: string): ReactNode {
  switch (type) {
    case "activity":
      return <Mail size={15} aria-hidden="true" />;
    case "task":
      return <CalendarDays size={15} aria-hidden="true" />;
    default:
      return <Users size={15} aria-hidden="true" />;
  }
}

// The reader judges the age themselves, so this says when rather than how
// confident anything is. A deterministic rule shows no confidence meter.
function freshness(at: string, t: ReturnType<typeof useT>): string {
  const days = Math.floor((Date.now() - new Date(at).getTime()) / 86_400_000);
  if (days <= 0) {
    return t("person.today.freshToday");
  }
  if (days === 1) {
    return t("person.today.freshYesterday");
  }
  return t("person.today.freshDaysAgo", { count: days });
}

// The quiet-success state renders through the same component: rung 10 is a
// moment like any other, and "nothing needs you today" is the answer a reader
// came for rather than an empty card.
export function isQuiet(moment: PersonMoment): boolean {
  return moment.rule === "nothing_needed";
}
