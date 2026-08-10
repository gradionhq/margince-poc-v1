import { Clock } from "lucide-react";
import type { components } from "../api/schema";
import { formatDateTime } from "../format/format";
import { type Locale, useT } from "../i18n";
import {
  HealthCard,
  PeopleCard,
  RECORD_ZONE,
  SectionCard,
  SignalsCard,
  SuggestionsSection,
  sectionState,
} from "./company360";

// The record page's right column (mockup State A): the account's context,
// beside the work rather than under it.
//
// Every card here is ALSO a section of the one composite read the page already
// made, except the signals, which run their own query exactly as they did in
// the grid. Moving them changed where they sit, not what they are — and none
// of them is repeated in the grid below, because a fact in two places on one
// page is a fact a reader has to reconcile.
//
// The rail is not rendered while the composer is open. That is the page's
// decision rather than this component's: the drawer opens into this column,
// and two things in one space is the layout the mockups never draw.

type Organization360 = components["schemas"]["Organization360"];

export function CompanyRail({
  orgId,
  view,
  locale,
  writable,
  onOpenRecord,
  withPeople,
  composerOpen,
}: Readonly<{
  orgId: string;
  view?: Organization360;
  locale: Locale;
  // An archived account takes no new roles or tags, so its cards show no verb
  // that would only be refused.
  writable: boolean;
  // Where a cited record opens. Owned by the page, because the grid cites the
  // same records and two owners would mean two receipts open at once.
  onOpenRecord?: (entityType: string, entityId: string) => void;
  // False where the page's own body is already the roster in full.
  withPeople: boolean;
  // A composer drawer is open in this column. The rail stands down entirely
  // rather than narrowing: squeezed to a third of its width it is a column of
  // broken cards, and no mockup draws the two side by side.
  composerOpen: boolean;
}>) {
  if (composerOpen) {
    return null;
  }
  return (
    // A plain div: RecordView's own <aside> is the landmark around this, and a
    // second labelled region inside it would give a reader two names for one
    // column.
    <div className="co-rail">
      {/* What to do next leads the column, as the mockup draws it: the cards
          below describe the account, this one asks for a move.
          It renders the reasons the server actually named — the rules are
          no-reply, stalled, no-next-step and lifecycle-conflict — rather than
          the task-like titles the mockup illustrates, which no field carries. */}
      <SuggestionsSection
        orgId={orgId}
        view={view}
        onOpenRecord={onOpenRecord}
      />
      <HealthCard health={view?.health} />
      {withPeople && (
        <PeopleCard view={view} writable={writable} orgId={orgId} />
      )}
      <SignalsCard orgId={orgId} />
      <RecentActivityCard view={view} locale={locale} />
    </div>
  );
}

// How many entries the rail's chronology carries. The full history is the
// History tab; this is the "what happened lately" a reader wants without
// leaving the overview.
const RECENT_LIMIT = 5;

/**
 * The last few things that happened with this account.
 *
 * Reads the SAME activities section the Today card's last-exchange tile reads,
 * so the two cannot disagree about what happened most recently. It shows more
 * than one and does not filter by kind: this is the chronology, where a task
 * logged against the account is part of the story, and the tile above is the
 * single reading of what was last SAID.
 */
function RecentActivityCard({
  view,
  locale,
}: Readonly<{ view?: Organization360; locale: Locale }>) {
  const t = useT();
  // Every logged activity, not only the ones with a subject. A call or a note
  // often has none, and filtering them out here would both under-report the
  // chronology and — because the count below feeds sectionState — draw
  // "nothing logged with them yet" on an account that has been called five
  // times. Absence of a subject is a fact about the ROW, never about the
  // account.
  const logged = view?.activities?.data ?? [];
  const entries = logged.slice(0, RECENT_LIMIT);
  return (
    <SectionCard
      title={t("co.recent.title")}
      state={sectionState(
        view,
        "activities",
        Boolean(view?.activities),
        logged.length,
      )}
      emptyLabel={t("co.recent.empty")}
    >
      <ul className="co-list">
        {entries.map((entry) => (
          <li key={entry.id} className="co-row">
            <span className="co-row-main">
              <Clock size={14} aria-hidden="true" />
              {/* The same fallback the timeline uses: a subjectless row still
                  has something to show, so it is never a blank line. */}
              {entry.subject || entry.body || entry.kind}
            </span>
            {entry.occurred_at && (
              <span className="co-row-meta">
                {formatDateTime(entry.occurred_at, locale, RECORD_ZONE)}
              </span>
            )}
          </li>
        ))}
      </ul>
    </SectionCard>
  );
}
