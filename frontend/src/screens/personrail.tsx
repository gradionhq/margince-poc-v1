import { ChevronRight, Mail, Phone, Users } from "lucide-react";
import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { Avatar, Button } from "../design-system/atoms";
import { useT } from "../i18n";
import { consentWord } from "./personstrip";

// The right rail (concept §5.11): six cards of context beside the work.
//
// Every card here is a GLANCE. The rail never becomes a second body — a reader
// who has to read the margin has lost the column it sits beside.

type Person360 = components["schemas"]["Person360"];
type PersonConsentGuard = components["schemas"]["PersonConsentGuard"];
type PersonMomentAction = components["schemas"]["PersonMomentAction"];

export function PersonRail({
  view,
  guard,
  firstName,
  onAction,
  onExplain,
}: Readonly<{
  view: Person360;
  guard: PersonConsentGuard | undefined;
  firstName: string;
  onAction: (action: PersonMomentAction) => void;
  onExplain: () => void;
}>) {
  return (
    <div className="pe-rail" data-testid="person-rail">
      <NextBestActions view={view} onAction={onAction} />
      <RelationshipPulse view={view} onExplain={onExplain} />
      <WhoKnows view={view} firstName={firstName} />
      <SignalsAndRisks view={view} />
      <ConsentAndChannels guard={guard} />
      <RecentActivity view={view} />
    </div>
  );
}

// --- Next best actions -----------------------------------------------------

// At most three executable rows. Each one comes from the moment the server
// selected, so the rail cannot offer an action the page's own card disagrees
// with.
function NextBestActions({
  view,
  onAction,
}: Readonly<{
  view: Person360;
  onAction: (action: PersonMomentAction) => void;
}>) {
  const t = useT();
  const moment = view.moment;
  if (!moment) {
    return null;
  }
  const actions = [
    moment.recommended_action,
    ...(moment.secondary_actions ?? []),
  ].slice(0, 3);
  return (
    <section className="pe-card">
      <h3 className="pe-card-title">{t("person.rail.nextActions")}</h3>
      {actions.map((action) => (
        <button
          key={action.label}
          type="button"
          className="pe-rail-row"
          onClick={() => onAction(action)}
          disabled={action.state === "blocked"}
        >
          <span className="pe-rail-label">
            {actionIcon(action.kind)}
            {action.label}
          </span>
          <span className="pe-rail-value-muted">{whenFor(action, t)}</span>
        </button>
      ))}
    </section>
  );
}

function actionIcon(kind: string): ReactNode {
  switch (kind) {
    case "schedule_meeting":
    case "open_meeting_brief":
      return <Users size={15} aria-hidden="true" />;
    case "ask_colleague":
      return <Users size={15} aria-hidden="true" />;
    default:
      return <Mail size={15} aria-hidden="true" />;
  }
}

// A 🟡 action says it will stage rather than send. The tier is the promise the
// reader is making when they click, so it is on the row and not in a tooltip.
function whenFor(
  action: PersonMomentAction,
  t: ReturnType<typeof useT>,
): string {
  if (action.state === "will_confirm") {
    return t("person.rail.reviewFirst");
  }
  if (action.state === "blocked") {
    return t("person.rail.blocked");
  }
  return t("person.rail.ready");
}

// --- Relationship pulse ----------------------------------------------------

// Words and directional facts. The composite score is NOT on the face
// (ADR-0096 D1); Explain reveals it with its factors and arithmetic.
function RelationshipPulse({
  view,
  onExplain,
}: Readonly<{ view: Person360; onExplain: () => void }>) {
  const t = useT();
  const inbound = view.last_inbound_at;
  const outbound = view.last_outbound_at;
  const twoWay = Boolean(inbound && outbound);
  const colleagues = view.network?.colleagues?.length ?? 0;
  return (
    <section className="pe-card">
      <div className="pe-rail-head">
        <h3 className="pe-card-title" style={{ margin: 0 }}>
          {t("person.rail.pulseTitle")}
        </h3>
        <Button small onClick={onExplain}>
          {t("person.rail.explain")}
        </Button>
      </div>
      <Row
        label={t("person.rail.direction")}
        value={twoWay ? t("person.rail.twoWay") : t("person.rail.oneSided")}
      />
      <Row label={t("person.rail.lastReply")} value={sinceWords(inbound, t)} />
      <Row
        label={t("person.rail.coverage")}
        value={
          colleagues === 1
            ? t("person.rail.colleagueOne")
            : t("person.rail.colleagues", { count: colleagues })
        }
      />
      <Row label={t("person.rail.trend")} value={trendWord(view, t)} />
      <div className="pe-pulse-overall">
        <Row
          label={t("person.rail.overall")}
          value={overallWord(view, t)}
          strong
        />
      </div>
    </section>
  );
}

function trendWord(view: Person360, t: ReturnType<typeof useT>): string {
  const inbound = view.last_inbound_at;
  const outbound = view.last_outbound_at;
  if (!inbound) {
    return t("person.rail.noInbound");
  }
  if (outbound && new Date(outbound) > new Date(inbound)) {
    return t("person.rail.cooling");
  }
  return t("person.rail.warming");
}

function overallWord(view: Person360, t: ReturnType<typeof useT>): string {
  const days = daysSince(view.last_inbound_at);
  if (days == null) {
    return t("person.rail.thin");
  }
  if (days > 14) {
    return t("person.rail.atRisk");
  }
  return t("person.rail.strong");
}

// --- Who knows them --------------------------------------------------------

function WhoKnows({
  view,
  firstName,
}: Readonly<{ view: Person360; firstName: string }>) {
  const t = useT();
  const colleagues = view.network?.colleagues ?? [];
  return (
    <section className="pe-card">
      <h3 className="pe-card-title">
        {t("person.rail.whoKnows", { name: firstName })}
      </h3>
      {colleagues.length === 0 && (
        <p className="pe-prose">{t("person.rail.nobodyYet")}</p>
      )}
      {colleagues.slice(0, 3).map((colleague) => (
        <div className="pe-colleague" key={colleague.user_id}>
          <Avatar name={colleague.display_name} />
          <span>
            <span className="pe-colleague-name">{colleague.display_name}</span>
            <span className="pe-colleague-proof">
              {/* The PROOF, never a ranking nobody can check: six unanswered
                  sends must not read as stronger than two real exchanges. */}
              {t("person.rail.exchanges", {
                count: colleague.interactions_90d,
              })}
            </span>
          </span>
        </div>
      ))}
    </section>
  );
}

// --- Signals and risks -----------------------------------------------------

function SignalsAndRisks({ view }: Readonly<{ view: Person360 }>) {
  const t = useT();
  const signals = derivedSignals(view, t);
  return (
    <section className="pe-card">
      <h3 className="pe-card-title">{t("person.rail.signals")}</h3>
      {signals.length === 0 && (
        <p className="pe-prose">{t("person.rail.noSignals")}</p>
      )}
      {signals.map((signal) => (
        <div className="pe-signal" key={signal.text}>
          <span className={`pe-dot pe-dot-${signal.tone}`} />
          <span>{signal.text}</span>
        </div>
      ))}
    </section>
  );
}

// Deterministic, from what the page already read. Each one is a fact the
// reader can check against the cards beside it rather than an assessment.
function derivedSignals(
  view: Person360,
  t: ReturnType<typeof useT>,
): ReadonlyArray<{ text: string; tone: "good" | "warn" | "bad" }> {
  const out: Array<{ text: string; tone: "good" | "warn" | "bad" }> = [];
  const quiet = daysSince(view.last_inbound_at);
  if (quiet != null && quiet > 14) {
    out.push({
      text: t("person.rail.noReplyDays", { count: quiet }),
      tone: "bad",
    });
  } else if (quiet != null) {
    out.push({
      text: t("person.rail.repliedDaysAgo", { count: quiet }),
      tone: "good",
    });
  }
  const committee = view.commercial?.committee?.length ?? 0;
  if (view.commercial?.deal && committee === 0) {
    out.push({ text: t("person.rail.singleThreaded"), tone: "warn" });
  }
  if (!view.next_meeting && view.commercial?.deal) {
    out.push({ text: t("person.rail.noMeetingBooked"), tone: "warn" });
  }
  return out;
}

// --- Consent and channels --------------------------------------------------

// The action guard, not the proof ledger. It renders even on a thin record,
// because "may I write to this person" is a question with an answer whatever
// else is missing.
function ConsentAndChannels({
  guard,
}: Readonly<{ guard: PersonConsentGuard | undefined }>) {
  const t = useT();
  const entries = guard?.entries ?? [];
  const email = entries.find((entry) => entry.channel === "email");
  const phone = entries.find((entry) => entry.channel === "phone");
  return (
    <section className="pe-card" data-testid="person-consent">
      <h3 className="pe-card-title">{t("person.rail.consentTitle")}</h3>
      <div className="pe-rail-row">
        <span className="pe-rail-label">
          <Mail size={15} aria-hidden="true" />
          {t("person.rail.email")}
        </span>
        <span className={verdictClass(email?.verdict)}>
          {consentWord(email?.verdict, t)}
        </span>
      </div>
      <div className="pe-rail-row">
        <span className="pe-rail-label">
          <Phone size={15} aria-hidden="true" />
          {t("person.rail.phone")}
        </span>
        <span className={verdictClass(phone?.verdict)}>
          {consentWord(phone?.verdict, t)}
        </span>
      </div>
      {/* The REASON, in the reader's words. A verdict a rep cannot explain to
          the person in front of them is not usable. */}
      {email?.reason && <p className="pe-colleague-proof">{email.reason}</p>}
    </section>
  );
}

function verdictClass(verdict: string | undefined): string {
  switch (verdict) {
    case "allowed":
      return "pe-rail-value pe-rail-value-good";
    case "blocked":
      return "pe-rail-value pe-rail-value-warn";
    default:
      return "pe-rail-value pe-rail-value-muted";
  }
}

// --- Recent activity -------------------------------------------------------

// Three condensed items. It never duplicates the raw timeline visible beside
// it — this is the glance, the Activity tab is the ledger.
function RecentActivity({ view }: Readonly<{ view: Person360 }>) {
  const t = useT();
  const rows = (view.activities?.data ?? []).slice(0, 3);
  return (
    <section className="pe-card">
      <h3 className="pe-card-title">{t("person.rail.recentActivity")}</h3>
      {rows.length === 0 && (
        <p className="pe-prose">{t("person.rail.nothingCaptured")}</p>
      )}
      {rows.map((row) => (
        <div className="pe-rail-row" key={row.id}>
          <span className="pe-rail-label">{row.subject ?? row.kind}</span>
          <span className="pe-rail-value-muted">
            {sinceWords(row.occurred_at, t)}
          </span>
        </div>
      ))}
      <span className="pe-rail-more">
        {t("person.rail.viewAllActivity")}{" "}
        <ChevronRight size={13} aria-hidden="true" />
      </span>
    </section>
  );
}

// --- shared ----------------------------------------------------------------

function Row({
  label,
  value,
  strong,
}: Readonly<{ label: string; value: string; strong?: boolean }>) {
  return (
    <div className="pe-rail-row">
      <span className="pe-rail-label">{label}</span>
      <span className={strong ? "pe-rail-value-good" : "pe-rail-value"}>
        {value}
      </span>
    </div>
  );
}

function daysSince(at: string | null | undefined): number | null {
  if (!at) {
    return null;
  }
  return Math.floor((Date.now() - new Date(at).getTime()) / 86_400_000);
}

function sinceWords(
  at: string | null | undefined,
  t: ReturnType<typeof useT>,
): string {
  const days = daysSince(at);
  if (days == null) {
    return t("person.strip.never");
  }
  if (days <= 0) {
    return t("person.strip.today");
  }
  if (days === 1) {
    return t("person.strip.yesterday");
  }
  return t("person.strip.days", { count: days });
}
