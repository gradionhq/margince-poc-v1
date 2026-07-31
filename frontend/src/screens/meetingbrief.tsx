import type { components } from "../api/schema";
import { navigate } from "../app/router";
import { SectionHeader } from "../design-system/atoms";
import { useT } from "../i18n";
import { type AccountFinding, readAccount } from "./accountread";
import { SuggestionsSection } from "./company360";
import "./meetingbrief.css";

type Organization360 = components["schemas"]["Organization360"];

/**
 * MeetingBrief is the first thing on the account, and the only block that
 * tries to say what the records MEAN rather than what they contain.
 *
 * The page around it already reports the account faithfully: contacts, deals,
 * signals, a timeline. What it did not do was answer the question a rep
 * actually arrives with — is this account warm, who carries it, what is at
 * risk, what do I owe them — leaving them to read four cards and draw the
 * conclusion themselves, every time, before every call.
 *
 * Every line here is a rule over the 360 payload this page already holds, so
 * the brief costs no extra read and states nothing the reader could not check.
 * A section their grants withheld contributes no line at all: see readAccount.
 */
export function MeetingBrief({
  view,
  orgId,
  onOpenRecord,
  // The clock is a parameter so the quiet-account rule can be tested against a
  // fixed instant rather than whenever the suite happens to run.
  now = new Date(),
}: Readonly<{
  view: Organization360;
  orgId: string;
  onOpenRecord?: (entityType: string, entityId: string) => void;
  now?: Date;
}>) {
  const t = useT();
  const findings = readAccount(view, now);
  const omitted = view.sections_omitted ?? [];
  // A reader whose grants withheld since_last_visit has no baseline, which is
  // NOT the same as never having opened the account. Saying "first visit" off
  // an absent section turns withheld data into a claim about their history.
  const firstVisit =
    !omitted.includes("since_last_visit") &&
    Boolean(view.since_last_visit) &&
    !view.since_last_visit?.baseline_at;
  // Withheld sections are named once, here, rather than as a refusal beside
  // every line the reader did not get. The brief is a synthesis, so the honest
  // caveat is about the whole of it.
  const partial = omitted.length > 0;

  return (
    <section className="card co-prep">
      <SectionHeader title={t("co.prep.title")} />
      {findings.length === 0 ? (
        <p className="co-empty">{t("co.prep.sparse")}</p>
      ) : (
        <ul className="co-prep-lines">
          {findings.map((finding) => {
            const subject = finding.subject;
            return (
              <li
                key={finding.id}
                className={`co-prep-line co-prep-${finding.tone}`}
              >
                <span>{t(finding.key, finding.params)}</span>
                {/* The subject is named from the 360's own payload and routed
                  directly. EntityRef would resolve the id with a record read
                  per referenced line, which is a fan-out of lookups on the
                  page whose whole design is one composite read. */}
                {subject && (
                  <button
                    type="button"
                    className="co-rowlink"
                    onClick={() => openSubject(subject)}
                  >
                    {subject.label}
                  </button>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {/* The way into the decision queue stays in the header pulse, one line
          above. Repeating it here gave the same count two buttons. */}
      {/* What to do about it, in the same block that said what it is. These
          were two cards: one describing the account and one advising on it,
          each with its own heading, so the reader had to carry the reading
          from the first into the second themselves. */}
      <SuggestionsSection
        orgId={orgId}
        view={view}
        onOpenRecord={onOpenRecord}
      />
      <p className="co-prep-foot">
        {firstVisit && <span className="t-caption">{t("co.since.first")}</span>}
        {partial && <span className="t-caption">{t("co.prep.withheld")}</span>}
      </p>
    </section>
  );
}

// openSubject routes a finding's record to its own screen.
function openSubject(subject: NonNullable<AccountFinding["subject"]>) {
  navigate({
    screen: subject.kind === "deal" ? "deals" : "contacts",
    id: subject.id,
  });
}
