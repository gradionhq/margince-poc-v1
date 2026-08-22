// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useInfiniteQuery } from "@tanstack/react-query";
import "./capture-activity.css";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCan } from "../app/capability";
import { Button, SegmentedControl, StatCard } from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { StatStrip } from "../design-system/statstrip";
import { SurfaceState } from "../design-system/surfacestate";
import { formatDateTime } from "../format/format";
import { viewerZone } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import { CaptureActivityDrawer } from "./capture-activity-drawer";
import { useProviderLabel } from "./channelproviders";
import { QueryGate, throwProblem } from "./common";

// Settings → Capture activity: what the pipeline did with the reader's own
// messages in the last 24 hours.
//
// Until this existed, every decision the pipeline made was written and none was
// readable: the activity that WAS created has an audit row, and the decisions
// that created nothing were operator breadcrumbs with no member on them. A rep
// whose message never appeared had no way to ask why.
//
// The scope is not a filter the reader picks. Personal rows come from their own
// connections and no grant widens them; the workspace section is shared-channel
// traffic (a bot binding) and takes the capture_trace object. The two are
// different endpoints for that reason, not one endpoint with a parameter.

type CaptureActivity = components["schemas"]["CaptureActivityResponse"];
type TraceEntry = components["schemas"]["CaptureTraceEntry"];

// The five outcomes, in the order a message meets them, so the strip reads as a
// path rather than as a legend sorted by size.
const OUTCOMES = [
  "captured",
  "internal",
  "suppressed",
  "deferred",
  "fault",
] as const;

type Outcome = (typeof OUTCOMES)[number];

// The reasons this screen can render, spelled as a closed set.
//
// It exists to kill an `as never` on the catalog lookup. That cast silenced the
// compile error for a missing key, and what shipped behind it was a row
// rendering `captureActivity.reason.transactional_infra` at a member — the
// catalog falls back to the key itself, so a missing entry is invisible until
// somebody sees one. A reason outside this set now renders NOTHING, which is
// the honest answer: the screen genuinely does not know what it means.
const REASONS = [
  "internal_only",
  "deferral_capped",
  "noise_prior",
  "decided_prior",
  "no_granting_human",
  "invisible_incumbent",
  "derivation_failed",
  "no_counterparty",
  "transactional_infra",
  "transactional_prefix",
] as const;

type KnownReason = (typeof REASONS)[number];

// `find` rather than `includes` + a cast: it narrows on its own, so this
// carries no assertion at all — which is the point, since an assertion is what
// let the missing key through in the first place.
function knownReason(reason: string | null | undefined): KnownReason | null {
  return REASONS.find((known) => known === reason) ?? null;
}

// The resolution statuses, closed for the same reason REASONS is: this value
// comes off the wire and is interpolated into a catalog key, so a status a
// newer binary writes would render `captureActivity.resolution.<status>` at a
// member. One guard for both, or the next one repeats the bug.
const RESOLUTIONS = [
  "pending",
  "unsure",
  "real",
  "noise",
  "rejected",
  "suppressed",
] as const;

type KnownResolution = (typeof RESOLUTIONS)[number];

function knownResolution(status: string): KnownResolution | null {
  return RESOLUTIONS.find((known) => known === status) ?? null;
}

const SCOPES = ["mine", "workspace"] as const;
type Scope = (typeof SCOPES)[number];

export function CaptureActivityTab() {
  const t = useT();
  // The workspace section is the gated half. Hidden rather than disabled when
  // the grant is absent: a toggle a reader cannot use is an invitation to ask
  // why, and shared-channel debugging is not part of every seat's job. Their
  // OWN activity — the reason they opened this page — is unaffected.
  const canReadWorkspace = useCan("capture_trace", "read");
  const [scope, setScope] = useState<Scope>("mine");

  return (
    // `title` on the Panel, not a SectionHeader nested inside it. Nested, the
    // heading landed as a direct child of `.panel` — which has zero padding —
    // so the card's own name sat flush against its left border while everything
    // under it was indented by PanelBody's 20px, and the panel drew no header
    // band and no rule at all. It also rendered at 15px/600 through
    // `.section-header h2` where every other settings card reads
    // `--fs-sm`/`fw-medium` through `.panel-head h2`: this was the one card in
    // the product whose title was a different size from the thirteen beside it.
    <Panel title={t("captureActivity.title")}>
      <PanelBody>
        {/* The description belongs in the body, which is where the other ten
            settings cards put theirs — Panel's header band holds the title
            alone, by design. */}
        <p className="settings-panel-sub">{t("captureActivity.sub")}</p>
        <SettingList>
          {/* Whose activity is a one-of-two ANSWER, so it sits beside its
              naming in the right column like every other answer on the page.
              The control keeps the same words as its own accessible name — the
              row draws them, the fieldset announces them. */}
          {canReadWorkspace && (
            <SettingRow
              label={t("captureActivity.scope.label")}
              control={
                <SegmentedControl<Scope>
                  label={t("captureActivity.scope.label")}
                  value={scope}
                  onChange={setScope}
                  options={SCOPES}
                  labels={{
                    mine: t("captureActivity.scope.mine"),
                    workspace: t("captureActivity.scope.workspace"),
                  }}
                />
              }
            />
          )}
          {/* The window contributes the card's other two rows itself: the
              funnel and the log are the SUBJECT here rather than answers to a
              question, and both need the full width below their naming. They
              are the query's children so a single read feeds both. */}
          <CaptureActivityWindow scope={canReadWorkspace ? scope : "mine"} />
        </SettingList>
      </PanelBody>
    </Panel>
  );
}

// One fetch per route, so each response is typed by the route that produced it.
async function fetchWindow(
  path: "/capture/activity",
  cursor: string | undefined,
): Promise<CaptureActivity>;
async function fetchWindow(
  path: "/capture/activity/workspace",
  cursor: string | undefined,
): Promise<CaptureActivity>;
async function fetchWindow(
  path: "/capture/activity" | "/capture/activity/workspace",
  cursor: string | undefined,
): Promise<CaptureActivity> {
  // Each branch names its route as a LITERAL. `api.GET` is typed per path, so
  // the union the overloads accept is not something it can be handed — the
  // narrowing is the reason the branch exists, and writing the literal is what
  // makes the two arms visibly two different calls rather than one repeated.
  const { data, error } =
    path === "/capture/activity/workspace"
      ? await api.GET("/capture/activity/workspace", {
          params: { query: { cursor } },
        })
      : await api.GET("/capture/activity", { params: { query: { cursor } } });
  if (error) throwProblem(error);
  return data;
}

function CaptureActivityWindow({ scope }: Readonly<{ scope: Scope }>) {
  const t = useT();
  const [filter, setFilter] = useState<Outcome | null>(null);
  const [openTrace, setOpenTrace] = useState<string | null>(null);
  // Paged, because the window is 24 hours and a busy mailbox fills more than
  // one page of it. Without this the funnel could honestly say 300 captured
  // while the list showed 50 and nothing said the rest existed.
  const query = useInfiniteQuery({
    queryKey: ["capture-activity", scope],
    initialPageParam: undefined as string | undefined,
    // Each route called on its own branch rather than through a union `path`.
    // openapi-fetch cannot infer one response type from two literal routes, and
    // the cast that papers over it is exactly the assertion the house rules
    // forbid — it would also have hidden a real shape change on either route.
    queryFn: ({ pageParam }) =>
      scope === "workspace"
        ? fetchWindow("/capture/activity/workspace", pageParam)
        : fetchWindow("/capture/activity", pageParam),
    getNextPageParam: (last) => last.page.next_cursor ?? undefined,
  });

  return (
    <QueryGate query={query}>
      {(loaded) => {
        // The funnel is a property of the WINDOW, not of the loaded pages, so
        // it comes off the first page and does not grow as more are fetched.
        const first = loaded.pages[0];
        const entries = loaded.pages.flatMap((page) => page.data);
        const shown = filter
          ? entries.filter((entry) => entry.outcome === filter)
          : entries;
        return (
          <>
            {/* The counters, and what they are counting — the note is the
                naming's own qualification rather than a paragraph floating
                between two blocks, which is where it read as a footnote to
                whichever of them the eye reached first. */}
            <SettingRow
              label={t("captureActivity.outcomes")}
              description={t("captureActivity.scopeNote")}
              layout="stack"
              control={
                <CaptureFunnel
                  funnel={first.funnel}
                  selected={filter}
                  onSelect={setFilter}
                />
              }
            />
            {/* The log. A row of its own rather than more content under the
                funnel: it is what a reader came here to read, and it takes the
                width the card has instead of the space left over. */}
            <SettingRow
              label={t("captureActivity.messages")}
              layout="stack"
              control={
                <div className="capture-activity__log">
                  {filter && (
                    // Both numbers, always. The funnel counts the WINDOW and
                    // this filters what is loaded, so a bare "12" under a
                    // counter reading 26 would look like the counter was wrong.
                    <p className="capture-activity__count">
                      {t("captureActivity.filtered", {
                        shown: shown.length,
                        total: first.funnel[filter] ?? 0,
                        outcome: t(`captureActivity.outcome.${filter}`),
                      })}
                    </p>
                  )}
                  {shown.length === 0 && (
                    // A filter that matched nothing LOADED is not an empty
                    // window. The counter above may say 3 while all three sit
                    // on pages nobody has fetched, and saying "no capture
                    // activity" there would contradict the number beside it.
                    <SurfaceState
                      state="empty"
                      emptyLabel={t(
                        filter
                          ? "captureActivity.emptyFiltered"
                          : "captureActivity.empty",
                      )}
                    >
                      {null}
                    </SurfaceState>
                  )}
                  {shown.length > 0 && (
                    <ul className="capture-activity__list">
                      {shown.map((entry) => (
                        <CaptureEntryRow
                          key={entry.id}
                          entry={entry}
                          payloads={first.payload_capture_enabled}
                          onOpen={() => setOpenTrace(entry.id)}
                        />
                      ))}
                    </ul>
                  )}
                  {/* Outside the rows, so a filter matching nothing on this
                      page can still reach the pages that hold its matches.
                      Hiding it there was a dead end: the counter promised rows
                      the reader had no way to fetch. */}
                  {query.hasNextPage && (
                    <Button
                      small
                      disabled={query.isFetchingNextPage}
                      onClick={() => void query.fetchNextPage()}
                    >
                      {t("captureActivity.loadMore")}
                    </Button>
                  )}
                </div>
              }
            />
            {openTrace && (
              <CaptureActivityDrawer
                traceId={openTrace}
                onClose={() => setOpenTrace(null)}
              />
            )}
          </>
        );
      }}
    </QueryGate>
  );
}

// The funnel doubles as the filter, because the counters ARE the question a
// reader arrives with: "26 waiting on a verdict — which 26?".
//
// It filters the rows already LOADED, not the window, so the count line beside
// it says both numbers. A filter that silently showed 12 of 26 would be a worse
// answer than no filter at all.
// A bucket counts every message that met an outcome, so its label has to hold
// for all of them at once.
//
// Four of the five already do. `deferred` does not: the funnel groups by
// outcome alone, with no ledger join, so one number covers the senders still
// being judged AND the ones already answered — and "Waiting on a verdict — 31"
// over rows that each say the verdict landed is the same contradiction the row
// avoids, one level up and louder, because the strip is what a reader sees
// first. What every message in the bucket has in common is that the ladder sent
// it for a verdict, so that is what the bucket says.
//
// The ROW keeps the tense: there it is one message, and whether that one is
// still waiting is knowable and worth saying.
function funnelLabel(outcome: Outcome): Outcome | "deferred_sent" {
  return outcome === "deferred" ? "deferred_sent" : outcome;
}

function CaptureFunnel({
  funnel,
  selected,
  onSelect,
}: Readonly<{
  funnel: CaptureActivity["funnel"];
  selected: Outcome | null;
  onSelect: (outcome: Outcome | null) => void;
}>) {
  const t = useT();
  return (
    <StatStrip
      className="capture-activity__funnel"
      testId="capture-activity-funnel"
    >
      {OUTCOMES.map((outcome) => (
        <button
          key={outcome}
          type="button"
          className="capture-activity__funnel-slot"
          aria-pressed={selected === outcome}
          onClick={() => onSelect(selected === outcome ? null : outcome)}
        >
          <StatCard
            label={t(`captureActivity.outcome.${funnelLabel(outcome)}`)}
            // Zero is a reading, not an absence: "no message was dropped as
            // internal today" is exactly what somebody comes here to confirm.
            value={String(funnel[outcome] ?? 0)}
          />
        </button>
      ))}
    </StatStrip>
  );
}

function CaptureEntryRow({
  entry,
  payloads,
  onOpen,
}: Readonly<{ entry: TraceEntry; payloads: boolean; onOpen: () => void }>) {
  const { locale } = useLocale();
  const t = useT();
  const providerLabel = useProviderLabel();
  // The reader's own zone: a trace is read to reconcile "I sent that at 9:04"
  // against what the pipeline did, and a UTC timestamp makes them do the
  // arithmetic themselves.
  const zone = viewerZone();
  return (
    <li className="capture-activity__row" data-outcome={entry.outcome}>
      {/* The whole row opens the ladder. A button rather than a click handler
          on the <li>, so it is reachable by keyboard and announces itself as
          something that opens. */}
      <button
        type="button"
        className="capture-activity__open"
        onClick={onOpen}
        aria-label={t("captureActivity.openTrace")}
      >
        <span className="capture-activity__when">
          {formatDateTime(entry.occurred_at, locale, zone)}
        </span>
        {/* The provider ID resolved to a name. The contract is explicit that a
            label is never stored — two deploys would disagree about the same
            transport — so it is resolved here, against the registry. */}
        <span className="capture-activity__connector">
          {providerLabel(entry.connector)}
        </span>
        <CaptureEntryOutcome entry={entry} />
        <CaptureEntryContent entry={entry} payloads={payloads} />
        <CaptureEntryResolution entry={entry} />
      </button>
    </li>
  );
}

// The outcome and the reason that qualifies it.
//
// Two deferral pairs would otherwise contradict themselves on screen, and both
// are read the same way: "Waiting on a verdict" is only true while one is
// actually outstanding.
//
// A CAPPED deferral is not waiting, because the ceiling refused to ask for a
// verdict at all — it says "Not queued", and the reason line explains why.
//
// A SETTLED one is not waiting either: the ledger has answered, and the
// resolution beside it says what the answer was. Left alone the row reads
// "Waiting on a verdict — judged a real contact", which is the screen arguing
// with itself. The ladder DID defer this sender, so the outcome still says so;
// it just says it in the past tense.
function CaptureEntryOutcome({ entry }: Readonly<{ entry: TraceEntry }>) {
  const t = useT();
  const reason = knownReason(entry.reason);
  const outcome =
    entry.outcome === "deferred" ? deferralLabel(entry, reason) : entry.outcome;
  return (
    <span className="capture-activity__outcome">
      {t(`captureActivity.outcome.${outcome}`)}
      {reason ? (
        <span className="capture-activity__reason">
          {t(`captureActivity.reason.${reason}`)}
        </span>
      ) : null}
    </span>
  );
}

// Which of the three things a deferral can be this row is.
//
// `pending` is the only status that leaves a question outstanding: every other
// one — including `unsure`, which passes it to a human — is the ledger having
// answered. An unknown status is treated as settled rather than waiting,
// because a status this build does not recognise is one a newer binary wrote,
// and the one thing it cannot be is the state that has no verdict yet.
function deferralLabel(
  entry: TraceEntry,
  reason: KnownReason | null,
): "deferred" | "deferred_capped" | "deferred_sent" {
  if (reason === "deferral_capped") {
    return "deferred_capped";
  }
  if (!entry.resolution || entry.resolution.status === "pending") {
    return "deferred";
  }
  return "deferred_sent";
}

// What the row can honestly show about the message itself.
//
// With payload capture off — the default — the pipeline stored no address and
// no subject, so the row says so rather than rendering an empty cell a reader
// would take for a message with no subject.
function CaptureEntryContent({
  entry,
  payloads,
}: Readonly<{ entry: TraceEntry; payloads: boolean }>) {
  const t = useT();
  if (!payloads) {
    return (
      <span className="capture-activity__content capture-activity__content--absent">
        {t("captureActivity.contentNotStored")}
      </span>
    );
  }
  if (!entry.counterparty && !entry.subject) {
    // Payload capture IS on, so this row genuinely carried neither — an erased
    // subject, or a message with no sender we could read.
    return (
      <span className="capture-activity__content capture-activity__content--absent">
        {t("captureActivity.contentNone")}
      </span>
    );
  }
  return (
    <span className="capture-activity__content">
      <span className="capture-activity__from">{entry.counterparty}</span>
      {entry.subject ? (
        <span className="capture-activity__subject">{entry.subject}</span>
      ) : null}
    </span>
  );
}

// What later became of a deferred message's sender, from the disposition
// ledger. Absent for every other outcome, because there is no open question.
function CaptureEntryResolution({ entry }: Readonly<{ entry: TraceEntry }>) {
  const t = useT();
  const status = entry.resolution
    ? knownResolution(entry.resolution.status)
    : null;
  if (!status) {
    return null;
  }
  return (
    <span className="capture-activity__resolution">
      {t(`captureActivity.resolution.${status}`)}
    </span>
  );
}
