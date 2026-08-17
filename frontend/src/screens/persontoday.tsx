import {
  AlertTriangle,
  CalendarDays,
  CheckSquare,
  FileText,
  Send,
  Sparkles,
  Users,
} from "lucide-react";
import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { Button } from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import { useT } from "../i18n";
import { interactionIcon } from "./interactionchrome";

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
    // The page's lead panel: the same titled-card shape as every panel under
    // it, tinted so it reads as the thing to DO rather than one more thing to
    // read — the company record's lead panel in the person's own words.
    <Panel
      className={warn ? "pe-today pe-today-warn" : "pe-today"}
      title={
        <span className="pe-today-head">
          {warn ? (
            <AlertTriangle size={16} aria-hidden="true" />
          ) : (
            <Sparkles size={16} aria-hidden="true" />
          )}
          {t("person.today.heading", { name: firstName })}
        </span>
      }
      footer={
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
      }
    >
      <PanelBody className="pe-today-lead">
        <div>
          <h3 className="pe-today-headline">{moment.headline}</h3>

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
        </div>

        {/* Every action the moment carries, with its readiness under it: the
            verbs live beside the moment they act on rather than in a second
            list elsewhere on the page, which would let the two disagree about
            what to do next. Readiness is stated rather than left to a disabled
            button, because "you may not do this yet" and "this will ask you to
            confirm" are different answers. */}
        <div className="pe-today-actions">
          <ActionVerb
            action={moment.recommended_action}
            primary
            onAction={onAction}
          />
          {secondary.map((action) => (
            <ActionVerb
              key={action.label}
              action={action}
              onAction={onAction}
            />
          ))}
        </div>
      </PanelBody>
    </Panel>
  );
}

// One verb: the button, and under it what will happen when it is pressed.
function ActionVerb({
  action,
  primary,
  onAction,
}: Readonly<{
  action: PersonMomentAction;
  primary?: boolean;
  onAction: (action: PersonMomentAction) => void;
}>) {
  const t = useT();
  return (
    <span className="pe-today-verb">
      <Button
        variant={primary ? "primary" : "ghost"}
        onClick={() => onAction(action)}
        disabled={action.state === "blocked"}
        title={action.blocked_reason}
      >
        {actionIcon(action.kind)}
        {action.label}
      </Button>
      <span className="pe-today-verb-state">{readiness(action, t)}</span>
    </span>
  );
}

// A moment says what to do; it never says what carries it. So the reply verb is
// drawn as a send and not as an envelope — the envelope told a contact reached
// only over a chat channel that the button would mail them, which is neither
// what the action does nor something this card can know.
function actionIcon(kind: string): ReactNode {
  switch (kind) {
    case "schedule_meeting":
    case "open_meeting_brief":
    case "ask_colleague":
      return <Users size={15} aria-hidden="true" />;
    case "draft_reply":
      return <Send size={15} aria-hidden="true" />;
    case "complete_task":
      return <CheckSquare size={15} aria-hidden="true" />;
    default:
      return <FileText size={15} aria-hidden="true" />;
  }
}

// What pressing it will do, in the three states the server distinguishes. A
// blocked action says so in words, not only by being unpressable: a disabled
// button carries no title a keyboard or touch reader ever sees, so the
// server's own reason (WHY this one is blocked) renders here when it sent
// one, and the generic word is the fallback for the rare blocked action that
// carries none.
function readiness(
  action: PersonMomentAction,
  t: ReturnType<typeof useT>,
): string {
  if (action.state === "will_confirm") {
    return t("person.rail.reviewFirst");
  }
  if (action.state === "blocked") {
    return action.blocked_reason ?? t("person.rail.blocked");
  }
  return t("person.rail.ready");
}

// A moment's evidence names a record TYPE and nothing about the transport, so
// there is no honest envelope to draw beside an activity: it is as likely to
// be a chat message or a call as a mail, and on a contact reached only over a
// channel the envelope was simply wrong. It is drawn as the record it is.
function evidenceIcon(type: string): ReactNode {
  switch (type) {
    case "task":
      return <CalendarDays size={15} aria-hidden="true" />;
    case "relationship_change":
      return <Users size={15} aria-hidden="true" />;
    default:
      return interactionIcon(null, 15);
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
