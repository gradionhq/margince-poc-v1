import type { components } from "../api/schema";
import { SectionHeader } from "../design-system/atoms";
import { useT } from "../i18n";
import { readAccount } from "./accountread";
import "./meetingbrief.css";
import { EntityRef } from "./entityref";

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
  // The clock is a parameter so the quiet-account rule can be tested against a
  // fixed instant rather than whenever the suite happens to run.
  now = new Date(),
}: Readonly<{
  view: Organization360;
  now?: Date;
}>) {
  const t = useT();
  const findings = readAccount(view, now);
  const firstVisit = !view.since_last_visit?.baseline_at;
  // Withheld sections are named once, here, rather than as a refusal beside
  // every line the reader did not get. The brief is a synthesis, so the honest
  // caveat is about the whole of it.
  const partial = (view.sections_omitted ?? []).length > 0;

  return (
    <section className="card co-prep">
      <SectionHeader title={t("co.prep.title")} sub={t("co.prep.sub")} />
      {findings.length === 0 ? (
        <p className="co-empty">{t("co.prep.sparse")}</p>
      ) : (
        <ul className="co-prep-lines">
          {findings.map((finding) => (
            <li
              key={finding.id}
              className={`co-prep-line co-prep-${finding.tone}`}
            >
              <span>{t(finding.key, finding.params)}</span>
              {finding.personId && (
                <EntityRef kind="person" id={finding.personId} />
              )}
              {finding.dealId && <EntityRef kind="deal" id={finding.dealId} />}
            </li>
          ))}
        </ul>
      )}
      {/* The way into the decision queue stays in the header pulse, one line
          above. Repeating it here gave the same count two buttons. */}
      <p className="co-prep-foot">
        {firstVisit && <span className="t-caption">{t("co.since.first")}</span>}
        {partial && <span className="t-caption">{t("co.prep.withheld")}</span>}
      </p>
    </section>
  );
}
