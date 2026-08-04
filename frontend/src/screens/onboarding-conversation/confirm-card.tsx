import {
  AlertTriangle,
  Check,
  ChevronRight,
  Circle,
  Sparkles,
} from "lucide-react";
import type { ChangeEvent } from "react";
import { Fragment, useEffect, useRef, useState } from "react";
import type { components } from "../../api/schema";
import { Avatar, Button } from "../../design-system/atoms";
import {
  ConfidenceMeter,
  type Evidence,
  EvidenceChip,
  ProvenanceTag,
} from "../../design-system/trust";
import { useLocale, useT } from "../../i18n";
import type { MessageKey } from "../../i18n/en";
import { coldFieldLabel } from "../common";
import type { CompanyDraft, CompanyFieldName } from "../onboarding";
import {
  CUSTOMER_FIELDS,
  LEGAL_IDENTITY_FIELDS,
  normalizeUrl,
  OFFER_FIELDS,
  REQUIRED_FIELDS,
  SALES_FIELDS,
} from "../onboarding";
import {
  CapNotice,
  type FactSelection,
  saveDisabled,
  useFactSelection,
} from "../onboarding-facts";
import { CoverageCard } from "../onboarding-live-panel";
import type { ClarifyAnswer } from "./company-proposal";
import { evidencedFields, isCompanyField } from "./company-proposal";
import {
  isWork,
  type ReviewRow,
  type RowState,
  rowFor,
  STATE_RANK,
} from "./company-review-state";
import {
  FINDING_EXPAND_EVENT,
  jumpToFindings,
  NarrationBubble,
} from "./entries";

// The company review as a triage surface: every profile field is on the
// board, ordered so the work sits on top: required gaps first, weak evidence
// next, empty-but-optional after, and the solid rows last where a skim
// suffices. A company identity card leads the surface, a plain section list
// navigates it and names what's outstanding, and a progress bar pinned to
// the foot carries the one action that matters — no colour-coded tally of
// the board's own rows, which only ever repeated what the nav already says
// by name. Evidence-or-omit holds throughout: a grounded value keeps its
// verbatim snippet one interaction away, a human value says so, and an
// empty field is shown AS empty, fillable in place, rather than silently
// absent.

type Proposal = components["schemas"]["OnboardingCompanyProposal"];
type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type SitePerson = components["schemas"]["CompanySiteReadPerson"];
type SiteFact = components["schemas"]["CompanySiteReadFact"];

// This card no longer re-asks a clarify of its own — that surface is the
// live DecisionScene's alone, and `open_questions` past the one currently
// live are promoted there in turn (use-company-read.ts) rather than shown
// here — so it carries no clarify-answering props at all: no comparisons to
// label a dismiss against, no pendingQuestionId, no answer/dismiss handlers.
type CompanyConfirmCardProps = Readonly<{
  proposal: Proposal;
  draft: CompanyDraft;
  answers: readonly ClarifyAnswer[];
  /** The read behind the proposal; the card keeps its people and coverage
   * honesty reachable below the fields. Null on a proposal-only render. */
  read?: CompanySiteRead | null;
  selectedFactKeys: readonly string[];
  setSelectedFactKeys: (keys: string[]) => void;
  missingRequired: readonly CompanyFieldName[];
  setField: (field: CompanyFieldName, value: string) => void;
  onAcceptAll: () => void;
  pending: boolean;
  /** A clarify authorization is still in flight; accepting must wait for it. */
  authorizing: boolean;
  error: string | null;
}>;

// The four field groups of the classic form, in its order: one vocabulary
// for what belongs where, whichever surface is doing the reviewing. A
// function, not a module-level const: this module and ../onboarding sit on
// an import cycle, so the group arrays only exist by the time a render asks.
function reviewGroups(): readonly Readonly<{
  key: string;
  labelKey: MessageKey;
  fields: readonly CompanyFieldName[];
}>[] {
  return [
    {
      key: "identity",
      labelKey: "ob.s1.identityLabel",
      fields: LEGAL_IDENTITY_FIELDS,
    },
    { key: "offer", labelKey: "ob.s1.offerLabel", fields: OFFER_FIELDS },
    {
      key: "customer",
      labelKey: "ob.s1.customerLabel",
      fields: CUSTOMER_FIELDS,
    },
    { key: "sales", labelKey: "ob.s1.salesLabel", fields: SALES_FIELDS },
  ];
}

const STATE_WORD: Readonly<Record<RowState, MessageKey>> = {
  required: "ob.conv.triage.stateRequired",
  empty: "ob.conv.triage.stateEmpty",
  typed: "ob.conv.triage.stateTyped",
  stored: "ob.conv.triage.stateStored",
  high: "confidence.high",
  med: "confidence.med",
  low: "confidence.low",
};

function isBand(state: RowState): state is "high" | "med" | "low" {
  return state === "high" || state === "med" || state === "low";
}

// isWork's set split in two: the banded half (low, med) already reads as a
// worded mark through ConfidenceMeter; this is the rest of it — the states
// with no value to grade at all. Required outranks empty in the same order
// the blocker banner uses, so a row's mark and the banner never disagree
// about which gap is the urgent one.
function isOutstandingUnbanded(state: RowState): state is "required" | "empty" {
  return state === "required" || state === "empty";
}

/** The settled half of the same split: `typed` and `stored` both have a
 * value and no confidence to grade, so `isBand` skips them too. */
function isSettledUnbanded(state: RowState): state is "typed" | "stored" {
  return state === "typed" || state === "stored";
}

// The mark a row with no gradable value carries: an icon shaped, not
// coloured, to tell the two states apart (a solid warning glyph for the
// blocking kind, a hollow ring for the merely-empty one) plus the same
// words a screen reader gets for every other row's state.
function OutstandingMark({
  state,
  t,
}: Readonly<{
  state: "required" | "empty";
  t: ReturnType<typeof useT>;
}>) {
  return (
    <span className="ob-triage-row-outstanding" data-state={state}>
      {state === "required" ? (
        <AlertTriangle aria-hidden />
      ) : (
        <Circle aria-hidden />
      )}
      <span className="sr-only">{t(STATE_WORD[state])}</span>
    </span>
  );
}

// isWork's complement, the settled half: `typed` already has a word
// (ProvenanceTag's fixed human/agent/connector vocabulary covers it —
// "typed by you"), but `stored` has no entry in that vocabulary at all. A
// row a human typed and a row still carrying an untouched profile value are
// different truths; saying "typed by you" over the second would be wrong,
// not just imprecise, so it gets its own quiet label instead, reusing the
// exact words the expanded row already says for the same state.
function ProvenanceMark({
  state,
  t,
}: Readonly<{ state: "typed" | "stored"; t: ReturnType<typeof useT> }>) {
  if (state === "typed") {
    return <ProvenanceTag provenance={{ kind: "human", self: true }} />;
  }
  return (
    <span className="ob-triage-row-provenance">{t(STATE_WORD.stored)}</span>
  );
}

// A collapsed value reads one short line's worth in the row; the cut lands
// on a word boundary rather than mid-word, and the rest of the text stays
// out of the DOM until the row expands — the same evidence-or-omit-shaped
// rule the collapsed EvidenceChip keeps for its own snippet.
const PROSE_PREVIEW_CHARS = 140;

function prosePreview(value: string): string {
  if (value.length <= PROSE_PREVIEW_CHARS) {
    return value;
  }
  const cut = value.lastIndexOf(" ", PROSE_PREVIEW_CHARS);
  return `${value.slice(0, cut > 0 ? cut : PROSE_PREVIEW_CHARS).trimEnd()}…`;
}

/**
 * One reviewable field, in one of two weights — the slicing that keeps 16
 * fields readable at once:
 * - Collapsed: a 1-line summary (label, value preview, band) that is itself
 *   the button to open. Nothing interactive rides inside it, so the evidence
 *   chip's own button never nests in another.
 * - Expanded: the full card — the value editable in place (confirm or
 *   correct, without leaving for the form), the state named in words, the
 *   verbatim evidence one toggle away.
 * The work states (required, weak, empty) open by default: they ARE the
 * work. The settled ones open on demand.
 */
function FieldRow({
  row,
  setField,
  defaultExpanded,
}: Readonly<{
  row: ReviewRow;
  setField: (field: CompanyFieldName, value: string) => void;
  defaultExpanded: boolean;
}>) {
  const t = useT();
  const [expanded, setExpanded] = useState(defaultExpanded);
  const controlId = `confirm-missing-${row.field}`;
  const onChange = (
    event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>,
  ) => setField(row.field, event.target.value);

  // Opts into the jump's "open it, then focus the real control" contract
  // (entries.tsx's jumpToFindings): a collapsed row has nothing typeable
  // for a keyboard or screen-reader user to land on, so a jump here means
  // "open this" as much as "look at this". A row already expanded ignores
  // the request — there is nothing more to do.
  const li = useRef<HTMLLIElement>(null);
  useEffect(() => {
    const node = li.current;
    if (node === null) {
      return;
    }
    const onExpandRequest = () => setExpanded(true);
    node.addEventListener(FINDING_EXPAND_EVENT, onExpandRequest);
    return () =>
      node.removeEventListener(FINDING_EXPAND_EVENT, onExpandRequest);
  }, []);

  if (!expanded) {
    return (
      <li
        ref={li}
        id={rowDomId(row.field)}
        tabIndex={-1}
        data-finding-id={row.field}
        data-state={row.state}
        className="ob-triage-row"
      >
        <button
          type="button"
          className="ob-triage-row-summary"
          aria-expanded={false}
          onClick={() => setExpanded(true)}
        >
          <ChevronRight aria-hidden className="ob-triage-row-caret" />
          <span className="t-label">{row.label}</span>
          <span className="ob-conv-field-value">
            {row.value.trim() === ""
              ? t(row.emptyHintKey)
              : prosePreview(row.value)}
          </span>
          <span className="ob-triage-row-meta">
            {row.confidence !== null && (
              <span className="ob-triage-score">
                {Math.round(row.confidence * 100)}
                {row.evidence !== null && (
                  <span className="ob-triage-source">
                    {t("ob.conv.triage.sourceCount", { count: 1 })}
                  </span>
                )}
              </span>
            )}
            {isBand(row.state) && <ConfidenceMeter level={row.state} />}
            {/* The band states already carry a worded mark (ConfidenceMeter);
                required/empty are the rest of isWork's set and get the same
                treatment here — an icon that survives greyscale (shape, not
                colour, tells them apart) plus the words a screen reader gets. */}
            {isOutstandingUnbanded(row.state) && (
              <OutstandingMark state={row.state} t={t} />
            )}
            {isSettledUnbanded(row.state) && (
              <ProvenanceMark state={row.state} t={t} />
            )}
          </span>
        </button>
      </li>
    );
  }

  return (
    <li
      ref={li}
      id={rowDomId(row.field)}
      tabIndex={-1}
      data-finding-id={row.field}
      data-state={row.state}
      className="ob-triage-row ob-triage-row-open ob-conv-confirm-missing-row"
    >
      <div className="ob-triage-row-head">
        <label className="t-label" htmlFor={controlId}>
          {row.label}
          <em>{t(STATE_WORD[row.state])}</em>
        </label>
        <button
          type="button"
          className="ob-conv-field-expand"
          aria-expanded
          onClick={() => setExpanded(false)}
        >
          {t("ob.conv.review.showLess")}
        </button>
      </div>
      {row.multiline ? (
        <textarea id={controlId} value={row.value} onChange={onChange} />
      ) : (
        <input id={controlId} value={row.value} onChange={onChange} />
      )}
      {/* Only the evidence pair below the control: the label's state word
          already says "typed by you", so the human tag would say it twice. */}
      {row.evidence !== null && isBand(row.state) && (
        <span className="ob-triage-row-proof">
          <ConfidenceMeter level={row.state} />
          <EvidenceChip evidence={row.evidence} collapsed />
        </span>
      )}
    </li>
  );
}

function rowDomId(field: CompanyFieldName): string {
  return `ob-triage-row-${field}`;
}

function groupDomId(key: string): string {
  return `ob-triage-group-${key}`;
}

function jumpToRow(field: CompanyFieldName): void {
  const node = document.getElementById(rowDomId(field));
  // jsdom has no scrollIntoView; in the browser it always exists.
  node?.scrollIntoView?.({ block: "center", behavior: "smooth" });
  node?.focus({ preventScroll: true });
}

// The layout frozen at mount, once per group: the row order and which ones
// open by default. `workCount` is frozen too, but ONLY for where the
// settled-cluster divider falls inside the group — that line must not jump
// as a row is filled. Anything that has to stay live and correct as the
// human types (the nav's outstanding count and named list, a row's own
// mark) is derived fresh every render instead; see `outstandingRows`.
type FrozenGroup = {
  key: string;
  labelKey: MessageKey;
  order: readonly CompanyFieldName[];
  workCount: number;
  openByDefault: ReadonlySet<CompanyFieldName>;
};

// Which section is under the reader's eye right now, so the nav can mark it
// current. A real IntersectionObserver only exists in the browser; jsdom has
// none, so the hook degrades to "the first section", which is an honest
// answer rather than a crash.
function useActiveSection(groups: readonly FrozenGroup[]): string | null {
  const [active, setActive] = useState<string | null>(groups[0]?.key ?? null);
  useEffect(() => {
    if (typeof IntersectionObserver === "undefined") {
      return;
    }
    const sections = groups
      .map((group) => document.getElementById(groupDomId(group.key)))
      .filter((node): node is HTMLElement => node !== null);
    if (sections.length === 0) {
      return;
    }
    const observer = new IntersectionObserver(
      (entries) => {
        const topmost = entries
          .filter((entry) => entry.isIntersecting)
          .sort(
            (a, b) => a.boundingClientRect.top - b.boundingClientRect.top,
          )[0];
        if (topmost) {
          setActive(topmost.target.id.replace("ob-triage-group-", ""));
        }
      },
      { rootMargin: "-10% 0px -70% 0px" },
    );
    for (const section of sections) {
      observer.observe(section);
    }
    return () => observer.disconnect();
  }, [groups]);
  return active;
}

// The server 422s confirm on exactly one thing: a REQUIRED_FIELDS entry
// left empty. `rowFor` already folds that into the "required" state (see
// company-review-state.ts), so checking the state IS checking
// REQUIRED_FIELDS — there is no second predicate to drift from the first.
function isBlocking(state: RowState): boolean {
  return state === "required";
}

// isWork's set minus the blocking one: everything the server never gates
// on — optional-empty, weak or middling evidence. Worth a look, never an
// obstacle, and never worded or coloured as though it were one.
function isAdvisory(state: RowState): boolean {
  return isWork(state) && state !== "required";
}

// The one split that decides what the nav counts, what it lists by name
// (in two labelled clusters, not one undifferentiated pile), and what a row
// marks: blocking vs advisory, read live off each row's CURRENT state every
// time — never off the order frozen at mount. Filling the one required
// field a section still needs, or raising a weak value's confidence, both
// recompute this on the same render, so the count, the named lists and the
// row's own mark can never drift apart.
function outstandingSplit(
  group: FrozenGroup,
  rowByField: ReadonlyMap<CompanyFieldName, ReviewRow>,
): { blocking: readonly ReviewRow[]; advisory: readonly ReviewRow[] } {
  const rows = group.order
    .map((field) => rowByField.get(field))
    .filter((row): row is ReviewRow => row !== undefined && isWork(row.state));
  return {
    blocking: rows.filter((row) => isBlocking(row.state)),
    advisory: rows.filter((row) => isAdvisory(row.state)),
  };
}

// How many of a section's fields the nav names, per cluster, before it
// falls back to a plain overflow count: past this, the list would grow
// taller than the section it navigates, which defeats the point of a nav.
const NAV_NAMED_LIMIT = 5;

// A section's badge over its jump button: a blocking count is coloured and
// worded as what it is — the server will refuse to confirm until these are
// filled — and an advisory count sits beside it as plain words, never a
// second shade of the same pill. A section with neither reads as settled.
function SectionBadge({
  blocking,
  advisory,
  t,
}: Readonly<{
  blocking: readonly ReviewRow[];
  advisory: readonly ReviewRow[];
  t: ReturnType<typeof useT>;
}>) {
  if (blocking.length === 0 && advisory.length === 0) {
    return (
      <span className="ob-triage-nav-settled">
        <Check aria-hidden />
        <span className="sr-only">{t("ob.conv.triage.sectionSettled")}</span>
      </span>
    );
  }
  return (
    <>
      {blocking.length > 0 && (
        <span className="ob-triage-nav-badge" data-blocking="true">
          <AlertTriangle aria-hidden />
          <b aria-hidden>{blocking.length}</b>
          <span className="sr-only">
            {t("ob.conv.triage.sectionBlocking", { count: blocking.length })}
          </span>
        </span>
      )}
      {advisory.length > 0 && (
        <span className="ob-triage-nav-advisory">
          {t("ob.conv.triage.sectionAdvisory", { count: advisory.length })}
        </span>
      )}
    </>
  );
}

// One labelled cluster of the named to-do list: a heading that says in
// words what the rest of the row marks only say in shape, then the fields
// themselves, each a jump straight to its row through the same jump
// machinery the rest of the board uses (the pulse and its reduced-motion
// opt-out come along for free).
function NavFieldCluster({
  headingKey,
  rows,
  icon,
  blocking,
  t,
}: Readonly<{
  headingKey: MessageKey;
  rows: readonly ReviewRow[];
  icon: typeof AlertTriangle;
  blocking: boolean;
  t: ReturnType<typeof useT>;
}>) {
  if (rows.length === 0) {
    return null;
  }
  const Icon = icon;
  const named = rows.slice(0, NAV_NAMED_LIMIT);
  const overflow = rows.length - named.length;
  return (
    <>
      <li className="ob-triage-nav-subhead">{t(headingKey)}</li>
      {named.map((row) => (
        <li key={row.field}>
          <button
            type="button"
            className="ob-triage-nav-item"
            data-blocking={blocking ? "true" : undefined}
            onClick={() => jumpToFindings([row.field])}
          >
            <Icon aria-hidden />
            <span>{row.label}</span>
          </button>
        </li>
      ))}
      {overflow > 0 && (
        <li className="ob-triage-nav-more">
          {t("ob.conv.triage.sectionMore", { count: overflow })}
        </li>
      )}
    </>
  );
}

// The named to-do list under a section, in its two clusters: the fields the
// server actually gates confirm on, then the fields that are merely worth a
// look. The count above and the names below are the same split read twice,
// never a second tally of "outstanding" that blurs the two back together.
function NavOutstandingList({
  blocking,
  advisory,
  t,
}: Readonly<{
  blocking: readonly ReviewRow[];
  advisory: readonly ReviewRow[];
  t: ReturnType<typeof useT>;
}>) {
  if (blocking.length === 0 && advisory.length === 0) {
    return null;
  }
  return (
    <ul className="ob-triage-nav-sublist">
      <NavFieldCluster
        headingKey="ob.conv.triage.blockingHead"
        rows={blocking}
        icon={AlertTriangle}
        blocking
        t={t}
      />
      <NavFieldCluster
        headingKey="ob.conv.triage.advisoryHead"
        rows={advisory}
        icon={Circle}
        blocking={false}
        t={t}
      />
    </ul>
  );
}

// The map replaced: a plain list of section names, each a jump link, each
// naming the fields still open under it in two labelled clusters — a real
// to-do list, not a count to decode, and one that never lets a merely
// advisory field read as an obstacle. A section with nothing outstanding
// says so quietly (a settled mark) rather than rendering nothing, so "done"
// and "not built" can never look alike.
function SectionNav({
  groups,
  activeKey,
  rowByField,
  t,
}: Readonly<{
  groups: readonly FrozenGroup[];
  activeKey: string | null;
  rowByField: ReadonlyMap<CompanyFieldName, ReviewRow>;
  t: ReturnType<typeof useT>;
}>) {
  return (
    <nav className="ob-triage-nav" aria-label={t("ob.conv.triage.mapLabel")}>
      <ul className="ob-triage-nav-list">
        {groups.map((group) => {
          const { blocking, advisory } = outstandingSplit(group, rowByField);
          return (
            <li key={group.key}>
              <button
                type="button"
                className="ob-triage-nav-link"
                aria-current={group.key === activeKey ? "true" : undefined}
                onClick={() => {
                  const node = document.getElementById(groupDomId(group.key));
                  // jsdom has no scrollIntoView; the browser always does.
                  node?.scrollIntoView?.({
                    block: "start",
                    behavior: "smooth",
                  });
                }}
              >
                <span>{t(group.labelKey)}</span>
                <SectionBadge blocking={blocking} advisory={advisory} t={t} />
              </button>
              <NavOutstandingList
                blocking={blocking}
                advisory={advisory}
                t={t}
              />
            </li>
          );
        })}
      </ul>
    </nav>
  );
}

// The one field the review can't derive a row for: no group carries
// "website" (it is the gate's field, not a reviewable profile row), so its
// domain renders as plain text rather than a jump link.
function companyWebsiteDomain(website: string): string {
  const trimmed = website.trim();
  return trimmed === "" ? "" : normalizeUrl(trimmed).host || trimmed;
}

type CompanyCardFact = {
  label: string;
  value: string;
  field: CompanyFieldName | null;
};

// The identity summary that leads the review: brand mark, name, and the
// handful of fields that actually identify the business. It is a summary,
// not another editable form — every value with a matching row below is a
// jump link into it; the rest (website has none) render as plain text.
function CompanyIdentityCard({
  draft,
  t,
}: Readonly<{ draft: CompanyDraft; t: ReturnType<typeof useT> }>) {
  const legalName = draft.values.legal_name.trim();
  const name = draft.values.display_name.trim() || legalName;
  if (name === "") {
    return null;
  }
  const facts: CompanyCardFact[] = [];
  if (legalName !== "" && legalName !== name) {
    facts.push({
      label: coldFieldLabel("legal_name", t),
      value: legalName,
      field: "legal_name",
    });
  }
  const domain = companyWebsiteDomain(draft.values.website);
  if (domain !== "") {
    facts.push({
      label: t("ob.conv.triage.companyWebsite"),
      value: domain,
      field: null,
    });
  }
  const headquarters = draft.values.registered_address.trim();
  if (headquarters !== "") {
    facts.push({
      label: coldFieldLabel("registered_address", t),
      value: headquarters,
      field: "registered_address",
    });
  }
  const industry = draft.values.industry.trim();
  if (industry !== "") {
    facts.push({
      label: coldFieldLabel("industry", t),
      value: industry,
      field: "industry",
    });
  }
  return (
    <div className="ob-company-card">
      {/* The contract carries no logo/favicon field for a company; the
          monogram is the floor, not a fallback for a missing fetch. */}
      <Avatar name={name} size="lg" />
      <div className="ob-company-card-body">
        <h3>{name}</h3>
        {facts.length > 0 && (
          <dl className="ob-company-card-facts">
            {facts.map((fact) => {
              const { field } = fact;
              return (
                <div key={fact.label}>
                  <dt>{fact.label}</dt>
                  <dd>
                    {field === null ? (
                      fact.value
                    ) : (
                      <button type="button" onClick={() => jumpToRow(field)}>
                        {fact.value}
                      </button>
                    )}
                  </dd>
                </div>
              );
            })}
          </dl>
        )}
      </div>
    </div>
  );
}

// The people section's key: fixed, so the frozen layout, the section nav and
// the group renderer all agree on which slot is the read's people rather
// than a profile field.
const PEOPLE_KEY = "people";

// A person is evidence-or-omit like any other finding: the contract requires
// a snippet and a source for every one the read reports, so this is never a
// guess at whether they exist, only a defensive floor against an empty string.
function personEvidence(person: SitePerson): Evidence | null {
  return person.evidence_snippet === "" || person.evidence_url === ""
    ? null
    : { snippet: person.evidence_snippet, source: person.evidence_url };
}

// One person the read found: name, role, and whatever else it carries
// (a published address, a network profile), with the page it read them from
// one toggle away — the same collapsed evidence chip a profile field uses.
function PersonRow({ person }: Readonly<{ person: SitePerson }>) {
  const evidence = personEvidence(person);
  return (
    <li className="ob-triage-person">
      <span className="ob-triage-person-name">{person.name}</span>
      <span className="ob-triage-person-role">{person.role}</span>
      {person.published_email && (
        <span className="ob-triage-person-meta">{person.published_email}</span>
      )}
      {person.linkedin_url && (
        <span className="ob-triage-person-meta">{person.linkedin_url}</span>
      )}
      {evidence && <EvidenceChip evidence={evidence} collapsed />}
    </li>
  );
}

/**
 * The people the read found, promoted to a section of their own: they are
 * company facts (who to talk to), the same class of thing as an office or a
 * service line, not a footnote under "everything else". There is nothing
 * here for a human to resolve — no field, no confidence band — so the
 * section carries no outstanding count; it only ever states what was found,
 * or says plainly that nothing was.
 */
function PeopleGroupSection({
  people,
  t,
}: Readonly<{ people: readonly SitePerson[]; t: ReturnType<typeof useT> }>) {
  return (
    <section id={groupDomId(PEOPLE_KEY)} className="ob-triage-group">
      <div className="ob-triage-group-head">
        <h3>{t("ob.conv.triage.peopleLabel")}</h3>
        {people.length > 0 && (
          <span>
            {t("ob.conv.triage.peopleCount", { count: people.length })}
          </span>
        )}
      </div>
      {people.length === 0 ? (
        <p className="ob-triage-people-empty">
          {t("ob.conv.triage.peopleEmpty")}
        </p>
      ) : (
        <ul className="ob-triage-people-rows">
          {people.map((person) => (
            <PersonRow
              key={`${person.name}:${person.evidence_url}`}
              person={person}
            />
          ))}
        </ul>
      )}
    </section>
  );
}

// The facts section's key, alongside PEOPLE_KEY: another slot the frozen
// layout, the section nav and the group renderer all agree carries no
// profile field.
const FACTS_KEY = "facts";

// The wire's own field enum, in its declared order — not alphabetical, not
// discovery order, so the same read always groups its facts the same way.
// Only the types a given read actually produced get a heading; the closed
// set is what fixes the ORDER, not what it forces onto the page.
const FACT_FIELD_ORDER: readonly SiteFact["field"][] = [
  "founded_year",
  "employee_range",
  "phone",
  "contact_email",
  "location",
  "service",
  "product",
  "capability",
  "served_industry",
  "company_size",
  "geography",
  "language",
  "certification",
  "partner",
  "named_customer",
  "technology",
  "quantified_outcome",
];

function factsByType(facts: readonly SiteFact[]): readonly Readonly<{
  field: SiteFact["field"];
  facts: readonly SiteFact[];
}>[] {
  return FACT_FIELD_ORDER.map((field) => ({
    field,
    facts: facts.filter((fact) => fact.field === field),
  })).filter((group) => group.facts.length > 0);
}

// One finding, dense: the value, its confidence, the source one toggle
// away, and the same save/drop control the classic form's fact grid uses —
// a fact is a line, not a field-sized card, so nothing here grows past one.
function FactRow({
  fact,
  selection,
  t,
}: Readonly<{
  fact: SiteFact;
  selection: FactSelection;
  t: ReturnType<typeof useT>;
}>) {
  const selected = selection.isSelected(fact);
  const evidence: Evidence = {
    snippet: fact.evidence_snippet,
    source: fact.evidence_url,
  };
  return (
    <li className="ob-triage-fact">
      <button
        type="button"
        className="ob-triage-fact-toggle"
        aria-pressed={selected}
        aria-label={t("ob.facts.rowSave", { fact: fact.value })}
        disabled={saveDisabled(selection, selected)}
        onClick={() => selection.toggle(fact)}
      >
        {selected ? <Check aria-hidden /> : <Circle aria-hidden />}
      </button>
      <span className="ob-triage-fact-value">{fact.value}</span>
      <span className="ob-triage-fact-meta">
        <span className="ob-triage-score">
          {Math.round(fact.confidence * 100)}
        </span>
        <EvidenceChip evidence={evidence} collapsed />
      </span>
    </li>
  );
}

// One type's worth of facts under its own quiet heading — the grouping that
// keeps a hundred rows skimmable: the reader sees the SHAPE of what a type
// found (three services, one certification) before reading any of it.
function FactTypeGroup({
  field,
  facts,
  selection,
  t,
}: Readonly<{
  field: SiteFact["field"];
  facts: readonly SiteFact[];
  selection: FactSelection;
  t: ReturnType<typeof useT>;
}>) {
  return (
    <div className="ob-triage-fact-type">
      <p className="ob-triage-fact-type-head">
        <span>{coldFieldLabel(field, t)}</span>
        <span>{facts.length}</span>
      </p>
      <ul className="ob-triage-fact-rows">
        {facts.map((fact) => (
          <FactRow
            key={fact.value_key}
            fact={fact}
            selection={selection}
            t={t}
          />
        ))}
      </ul>
    </div>
  );
}

/**
 * Every fact the read produced, grouped by type and visible without a
 * click — the densest, most interesting thing the crawl made must not read
 * as an appendix behind "other facts I found". The selection stays live
 * here: ticking or unticking still writes through the same `FactSelection`
 * the classic form's fact grid and table use, so accepting the review still
 * sends exactly the keys the reader chose.
 */
function FactsGroupSection({
  facts,
  selection,
  locale,
  t,
}: Readonly<{
  facts: readonly SiteFact[];
  selection: FactSelection;
  locale: string;
  t: ReturnType<typeof useT>;
}>) {
  return (
    <section id={groupDomId(FACTS_KEY)} className="ob-triage-group">
      <div className="ob-triage-group-head">
        <h3>{t("ob.conv.triage.factsLabel")}</h3>
        <span>
          {t("ob.factsSelected", {
            selected: selection.selectedCount,
            total: facts.length,
          })}
        </span>
      </div>
      <p className="ob-sub">{t("ob.factsSub")}</p>
      {/* The thread beside this pane owns no cap sentence of its own; this
          card draws the ceiling and a second live region on the same
          boundary would read the sentence twice. */}
      <CapNotice atCap={selection.atCap} locale={locale} live={false} />
      {factsByType(facts).map(({ field, facts: typeFacts }) => (
        <FactTypeGroup
          key={field}
          field={field}
          facts={typeFacts}
          selection={selection}
          t={t}
        />
      ))}
    </section>
  );
}

// One field group's board: its head (filled/total), the settled-cluster
// divider once the work rows give way to the solid ones, and the rows
// themselves in the order frozen at mount.
function FieldGroupSection({
  group,
  rowByField,
  setField,
  t,
}: Readonly<{
  group: FrozenGroup;
  rowByField: ReadonlyMap<CompanyFieldName, ReviewRow>;
  setField: (field: CompanyFieldName, value: string) => void;
  t: ReturnType<typeof useT>;
}>) {
  const ordered = group.order
    .map((field) => rowByField.get(field))
    .filter((row): row is ReviewRow => row !== undefined);
  const filled = ordered.filter((row) => row.value.trim() !== "").length;
  const solidCount = group.order.length - group.workCount;
  return (
    <section id={groupDomId(group.key)} className="ob-triage-group">
      <div className="ob-triage-group-head">
        <h3>{t(group.labelKey)}</h3>
        <span>
          {filled}/{group.order.length}
        </span>
      </div>
      <ul className="ob-conv-confirm-fields ob-triage-rows">
        {ordered.map((row, index) => (
          <Fragment key={row.field}>
            {/* The settled cluster sits under its own quiet rule, so the eye
                can stop reading a group the moment it crosses it. */}
            {index === group.workCount &&
              group.workCount > 0 &&
              solidCount > 0 && (
                <li className="ob-triage-solid-divider" aria-hidden>
                  <span>
                    {t("ob.conv.triage.looksSolid", { count: solidCount })}
                  </span>
                </li>
              )}
            <FieldRow
              row={row}
              setField={setField}
              defaultExpanded={group.openByDefault.has(row.field)}
            />
          </Fragment>
        ))}
      </ul>
    </section>
  );
}

// Which section body a slot in the frozen layout renders: the two
// non-field slots first, the ordinary field group otherwise. One switch,
// so the board's own render stays a plain map over `frozen` rather than a
// nested ternary repeated at the call site.
function GroupBody({
  group,
  rowByField,
  setField,
  people,
  facts,
  factSelection,
  locale,
  t,
}: Readonly<{
  group: FrozenGroup;
  rowByField: ReadonlyMap<CompanyFieldName, ReviewRow>;
  setField: (field: CompanyFieldName, value: string) => void;
  people: readonly SitePerson[] | null;
  facts: readonly SiteFact[];
  factSelection: FactSelection;
  locale: string;
  t: ReturnType<typeof useT>;
}>) {
  if (group.key === PEOPLE_KEY) {
    return people === null ? null : (
      <PeopleGroupSection people={people} t={t} />
    );
  }
  if (group.key === FACTS_KEY) {
    return (
      <FactsGroupSection
        facts={facts}
        selection={factSelection}
        locale={locale}
        t={t}
      />
    );
  }
  return (
    <FieldGroupSection
      group={group}
      rowByField={rowByField}
      setField={setField}
      t={t}
    />
  );
}

// The one place the review is left from, pinned to the foot of the work
// surface so it survives however far the board is scrolled. It carries
// exactly three things: real progress toward being ABLE to continue (the
// required fields, not a tally of every row), the plain-words reason
// continuing is blocked when it is, and the one action that moves on —
// labelled Continue, a step transition, not "Accept all": pressing it does
// not bulk-apply the AI's proposed values (the human already settled each
// row by editing), it advances past a review that is done. That action is
// the same `onAcceptAll` prop the board has always had — there is no
// separate continue affordance to consolidate with, so this is a
// relocation, not a second control: the count that disables it is
// `missingRequired`, the exact predicate the section nav's blocking count
// already reads (`isRequired(field) && value === ""`), so the two can never
// disagree about how many gaps are left.
function ReviewContinueBar({
  filled,
  total,
  remaining,
  pending,
  authorizing,
  onContinue,
  t,
}: Readonly<{
  filled: number;
  total: number;
  remaining: number;
  pending: boolean;
  authorizing: boolean;
  onContinue: () => void;
  t: ReturnType<typeof useT>;
}>) {
  const statusId = "ob-triage-continue-status";
  return (
    <div className="ob-triage-continue">
      <progress
        className="ob-triage-continue-bar"
        value={filled}
        max={total}
        aria-label={t("ob.conv.review.progressLabel")}
      />
      {/* A live region, not merely visible text: the count named here is
          exactly what the button below is disabled on, so a screen reader
          hears why the moment it changes rather than only if focus happens
          to land on this paragraph first. */}
      <p id={statusId} className="ob-triage-continue-status" role="status">
        {remaining > 0
          ? t(
              remaining === 1
                ? "ob.conv.review.requiredRemaining.one"
                : "ob.conv.review.requiredRemaining.other",
              { count: remaining },
            )
          : t("ob.conv.review.requiredDone")}
      </p>
      <Button
        variant="primary"
        aria-describedby={statusId}
        disabled={pending || authorizing || remaining > 0}
        onClick={onContinue}
      >
        {pending ? (
          <>
            <span className="ob-spinner" /> {t("ob.s1.saving")}
          </>
        ) : (
          <>
            <Check aria-hidden /> {t("ob.conv.review.continue")}
          </>
        )}
      </Button>
    </div>
  );
}

export function CompanyConfirmCard(props: CompanyConfirmCardProps) {
  const t = useT();
  const { locale } = useLocale();
  const byName = new Map(
    evidencedFields(props.proposal.fields)
      .filter((field) => isCompanyField(field.field, props.draft.values))
      .map((field) => [field.field, field]),
  );
  const groups = reviewGroups().map((group) => {
    const rows = group.fields
      .map((field) => rowFor(field, props.draft, byName, t, props.read?.pages))
      .sort((a, b) => STATE_RANK[a.state] - STATE_RANK[b.state]);
    return { ...group, rows };
  });
  const rows = groups.flatMap((group) => group.rows);
  const facts = props.proposal.facts ?? [];
  // The LAYOUT is frozen at mount — row order, where the solid rule falls,
  // which rows open by default, which of the two non-field sections exist
  // at all — while every row's state, colour and value stay live. Sorting
  // live instead would reshuffle the board under the cursor: typing one
  // character into an empty field re-ranks it settled and the row would
  // move mid-keystroke.
  const readAtMount = props.read;
  const factsAtMount = facts;
  const [frozen] = useState<readonly FrozenGroup[]>(() => {
    const fieldGroups = groups.map((group) => ({
      key: group.key,
      labelKey: group.labelKey,
      order: group.rows.map((row) => row.field),
      workCount: group.rows.filter((row) => isWork(row.state)).length,
      openByDefault: new Set(
        group.rows
          .filter((row) => STATE_RANK[row.state] <= STATE_RANK.empty)
          .map((row) => row.field),
      ),
    }));
    // People join the board only once there is a read to report on: without
    // one, "no people found" would be a guess rather than a finding. They
    // carry no field order and no outstanding count — nothing here is the
    // human's to resolve, only theirs to see. Facts join once the read
    // actually produced any — an empty facts section would be nothing to
    // navigate to. Fields come first in the list on purpose: they are the
    // decisions the human owes, people and facts are what the read found —
    // last in the nav and last in the scroll order keeps the board's
    // weight on the work, however many facts the crawl turned up.
    const extra: FrozenGroup[] = [];
    if (readAtMount != null) {
      extra.push({
        key: PEOPLE_KEY,
        labelKey: "ob.conv.triage.peopleLabel" as const,
        order: [],
        workCount: 0,
        openByDefault: new Set<CompanyFieldName>(),
      });
    }
    if (factsAtMount.length > 0) {
      extra.push({
        key: FACTS_KEY,
        labelKey: "ob.conv.triage.factsLabel" as const,
        order: [],
        workCount: 0,
        openByDefault: new Set<CompanyFieldName>(),
      });
    }
    return [...fieldGroups, ...extra];
  });
  const activeSection = useActiveSection(frozen);
  const rowByField = new Map(rows.map((row) => [row.field, row]));
  // The contract ceiling on `selected_fact_keys` is the selection model's to
  // enforce, wherever a fact is picked: this card's toggles and the fact
  // table's checkboxes write the same key list, so they refuse on the same
  // terms.
  const factSelection = useFactSelection(
    facts,
    props.selectedFactKeys,
    props.setSelectedFactKeys,
  );
  // Dismissed questions are named honestly: nothing was written, the field
  // stays the human's to edit, never silently swallowed.
  const dismissedLabels = props.answers
    .filter((answer) => answer.dismissed === true)
    .map((answer) => coldFieldLabel(answer.field, t))
    .join(", ");
  const missingLabels = props.missingRequired
    .map((field) => coldFieldLabel(field, t))
    .join(", ");
  // "Fix these first" lands on the worst row anywhere on the board, not the
  // first in reading order: a required gap in the last group outranks an
  // amber row in the first.
  const firstWork =
    rows
      .filter((row) => isWork(row.state))
      .sort((a, b) => STATE_RANK[a.state] - STATE_RANK[b.state])[0]?.field ??
    null;
  // What the bottom bar's progress and gate read: real completion toward
  // being ABLE to continue, not a count of every field the board carries —
  // an optional field left empty or lightly grounded must never subtract
  // from a bar that claims to measure "can I move on".
  const requiredTotal = REQUIRED_FIELDS.length;
  const requiredRemaining = props.missingRequired.length;
  const requiredFilled = requiredTotal - requiredRemaining;

  // Focus lands once on the first blocking row when the card first appears;
  // it must not jump the human back there every time filling one re-renders.
  const card = useRef<HTMLElement>(null);
  useEffect(() => {
    const control = card.current?.querySelector<
      HTMLInputElement | HTMLTextAreaElement
    >("li[data-state='required'] input, li[data-state='required'] textarea");
    if (control == null) {
      return;
    }
    // Never steal focus from someone typing: any focused text field wins,
    // and a composer still holding a draft keeps its claim even unfocused.
    const active = control.ownerDocument.activeElement;
    if (
      active instanceof HTMLTextAreaElement ||
      active instanceof HTMLInputElement
    ) {
      return;
    }
    const composer = control
      .closest(".ob-workbench-panel")
      ?.querySelector<HTMLTextAreaElement>(".mw-composer textarea");
    if (composer != null && composer.value !== "") {
      return;
    }
    control.focus();
  }, []);

  return (
    <section className="ob-conv-confirm ob-triage" ref={card}>
      <header>
        <Sparkles aria-hidden />
        <h2>{t("ob.conv.review.title")}</h2>
      </header>
      <CompanyIdentityCard draft={props.draft} t={t} />
      {props.missingRequired.length > 0 && (
        // The blocking gaps as a banner, not a chat line: a count, the
        // sentence naming the fields, and the jump that starts fixing them.
        <div className="ob-triage-blocker" role="status">
          <b>{props.missingRequired.length}</b>
          <p>{t("ob.conv.review.missing", { fields: missingLabels })}</p>
          {firstWork !== null && (
            <Button small variant="ghost" onClick={() => jumpToRow(firstWork)}>
              {t("ob.conv.triage.fixFirst")}
            </Button>
          )}
        </div>
      )}
      <div className="ob-triage-body">
        <SectionNav
          groups={frozen}
          activeKey={activeSection}
          rowByField={rowByField}
          t={t}
        />
        <div className="ob-triage-groups">
          {frozen.map((group) => (
            <GroupBody
              key={group.key}
              group={group}
              rowByField={rowByField}
              setField={props.setField}
              people={props.read?.people ?? null}
              facts={facts}
              factSelection={factSelection}
              locale={locale}
              t={t}
            />
          ))}
        </div>
      </div>
      {dismissedLabels !== "" && (
        <div className="ob-conv-confirm-skipped">
          <p>{t("ob.conv.review.skipped", { fields: dismissedLabels })}</p>
        </div>
      )}
      {/* The read's remaining honesty: what it read or skipped. People and
          facts both moved up onto the board as findings of their own — this
          is crawl provenance, not a finding, so it is the one thing left
          under a tail head of its own. */}
      {props.read != null && (
        <div className="ob-triage-readmore">
          <p className="ob-triage-rest-head">{t("ob.conv.triage.restTitle")}</p>
          <CoverageCard
            pages={props.read.pages}
            warnings={props.read.warnings}
          />
        </div>
      )}
      {props.error && (
        // A failed save speaks as Margince, not as a bare server string
        // floating in the card; the safe problem detail rides as a param.
        <div role="alert">
          <NarrationBubble
            entry={{
              kind: "narration",
              id: "review:confirm-failed",
              i18nKey: "ob.conv.review.confirmFailed",
              params: { detail: props.error },
            }}
          />
        </div>
      )}
      <ReviewContinueBar
        filled={requiredFilled}
        total={requiredTotal}
        remaining={requiredRemaining}
        pending={props.pending}
        authorizing={props.authorizing}
        onContinue={props.onAcceptAll}
        t={t}
      />
    </section>
  );
}
