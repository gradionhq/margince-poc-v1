import { ChevronRight, Mail, Phone, Users } from "lucide-react";
import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { Avatar, Button } from "../design-system/atoms";

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
      <h3 className="pe-card-title">Next best actions</h3>
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
          <span className="pe-rail-value-muted">{whenFor(action)}</span>
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
function whenFor(action: PersonMomentAction): string {
  if (action.state === "will_confirm") {
    return "Review first";
  }
  if (action.state === "blocked") {
    return "Blocked";
  }
  return "Ready";
}

// --- Relationship pulse ----------------------------------------------------

// Words and directional facts. The composite score is NOT on the face
// (ADR-0096 D1); Explain reveals it with its factors and arithmetic.
function RelationshipPulse({
  view,
  onExplain,
}: Readonly<{ view: Person360; onExplain: () => void }>) {
  const inbound = view.last_inbound_at;
  const outbound = view.last_outbound_at;
  const twoWay = Boolean(inbound && outbound);
  const colleagues = view.network?.colleagues?.length ?? 0;
  return (
    <section className="pe-card">
      <div className="pe-rail-head">
        <h3 className="pe-card-title" style={{ margin: 0 }}>
          Relationship pulse
        </h3>
        <Button small onClick={onExplain}>
          Explain
        </Button>
      </div>
      <Row label="Direction" value={twoWay ? "Two-way" : "One-sided"} />
      <Row label="Last reply" value={sinceWords(inbound)} />
      <Row
        label="Coverage"
        value={
          colleagues === 1 ? "1 colleague" : `${colleagues} colleagues`
        }
      />
      <Row label="Trend" value={trendWord(view)} />
      <div className="pe-pulse-overall">
        <Row label="Overall" value={overallWord(view)} strong />
      </div>
    </section>
  );
}

function trendWord(view: Person360): string {
  const inbound = view.last_inbound_at;
  const outbound = view.last_outbound_at;
  if (!inbound) {
    return "No inbound";
  }
  if (outbound && new Date(outbound) > new Date(inbound)) {
    return "Cooling";
  }
  return "Warming";
}

function overallWord(view: Person360): string {
  const days = daysSince(view.last_inbound_at);
  if (days == null) {
    return "Thin";
  }
  if (days > 14) {
    return "At risk";
  }
  return "Strong";
}

// --- Who knows them --------------------------------------------------------

function WhoKnows({
  view,
  firstName,
}: Readonly<{ view: Person360; firstName: string }>) {
  const colleagues = view.network?.colleagues ?? [];
  return (
    <section className="pe-card">
      <h3 className="pe-card-title">Who knows {firstName}</h3>
      {colleagues.length === 0 && (
        <p className="pe-prose">Nobody here has corresponded with them yet.</p>
      )}
      {colleagues.slice(0, 3).map((colleague) => (
        <div className="pe-colleague" key={colleague.user_id}>
          <Avatar name={colleague.display_name} />
          <span>
            <span className="pe-colleague-name">{colleague.display_name}</span>
            <span className="pe-colleague-proof">
              {/* The PROOF, never a ranking nobody can check: six unanswered
                  sends must not read as stronger than two real exchanges. */}
              {colleague.interactions} exchanges
            </span>
          </span>
        </div>
      ))}
    </section>
  );
}

// --- Signals and risks -----------------------------------------------------

function SignalsAndRisks({ view }: Readonly<{ view: Person360 }>) {
  const signals = derivedSignals(view);
  return (
    <section className="pe-card">
      <h3 className="pe-card-title">Signals &amp; risks</h3>
      {signals.length === 0 && (
        <p className="pe-prose">Nothing stands out on this relationship.</p>
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
): ReadonlyArray<{ text: string; tone: "good" | "warn" | "bad" }> {
  const out: Array<{ text: string; tone: "good" | "warn" | "bad" }> = [];
  const quiet = daysSince(view.last_inbound_at);
  if (quiet != null && quiet > 14) {
    out.push({ text: `No reply for ${quiet} days`, tone: "bad" });
  } else if (quiet != null) {
    out.push({ text: `Replied ${quiet} days ago`, tone: "good" });
  }
  const committee = view.commercial?.committee?.length ?? 0;
  if (view.commercial?.deal && committee === 0) {
    out.push({ text: "Single-threaded on this deal", tone: "warn" });
  }
  if (!view.next_meeting && view.commercial?.deal) {
    out.push({ text: "No next meeting booked", tone: "warn" });
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
  const entries = guard?.entries ?? [];
  const email = entries.find((entry) => entry.channel === "email");
  const phone = entries.find((entry) => entry.channel === "phone");
  return (
    <section className="pe-card" data-testid="person-consent">
      <h3 className="pe-card-title">Consent &amp; channels</h3>
      <div className="pe-rail-row">
        <span className="pe-rail-label">
          <Mail size={15} aria-hidden="true" />
          Email
        </span>
        <span className={verdictClass(email?.verdict)}>
          {verdictWord(email?.verdict)}
        </span>
      </div>
      <div className="pe-rail-row">
        <span className="pe-rail-label">
          <Phone size={15} aria-hidden="true" />
          Phone
        </span>
        <span className={verdictClass(phone?.verdict)}>
          {verdictWord(phone?.verdict)}
        </span>
      </div>
      {/* The REASON, in the reader's words. A verdict a rep cannot explain to
          the person in front of them is not usable. */}
      {email?.reason && <p className="pe-colleague-proof">{email.reason}</p>}
    </section>
  );
}

function verdictWord(verdict: string | undefined): string {
  switch (verdict) {
    case "allowed":
      return "Allowed";
    case "blocked":
      return "Blocked";
    default:
      return "Unknown";
  }
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
  const rows = (view.activities?.data ?? []).slice(0, 3);
  return (
    <section className="pe-card">
      <h3 className="pe-card-title">Recent activity</h3>
      {rows.length === 0 && <p className="pe-prose">Nothing captured yet.</p>}
      {rows.map((row) => (
        <div className="pe-rail-row" key={row.id}>
          <span className="pe-rail-label">{row.subject ?? row.kind}</span>
          <span className="pe-rail-value-muted">
            {sinceWords(row.occurred_at)}
          </span>
        </div>
      ))}
      <span className="pe-rail-more">
        View all activity <ChevronRight size={13} aria-hidden="true" />
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

function sinceWords(at: string | null | undefined): string {
  const days = daysSince(at);
  if (days == null) {
    return "Never";
  }
  if (days <= 0) {
    return "Today";
  }
  if (days === 1) {
    return "Yesterday";
  }
  return `${days} days`;
}
