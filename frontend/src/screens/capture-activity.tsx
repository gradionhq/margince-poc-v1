// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import "./capture-activity.css";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCan } from "../app/capability";
import {
  SectionHeader,
  SegmentedControl,
  StatCard,
} from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import { StatStrip } from "../design-system/statstrip";
import { SurfaceState } from "../design-system/surfacestate";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
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
    <Panel>
      <SectionHeader
        title={t("captureActivity.title")}
        sub={t("captureActivity.sub")}
      />
      <PanelBody>
        {canReadWorkspace && (
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
        )}
        <CaptureActivityWindow scope={canReadWorkspace ? scope : "mine"} />
      </PanelBody>
    </Panel>
  );
}

function CaptureActivityWindow({ scope }: Readonly<{ scope: Scope }>) {
  const t = useT();
  const query = useQuery({
    queryKey: ["capture-activity", scope],
    queryFn: async () => {
      const path =
        scope === "workspace"
          ? "/capture/activity/workspace"
          : "/capture/activity";
      const { data, error } = await api.GET(path, {});
      if (error) throwProblem(error);
      return data as CaptureActivity;
    },
  });

  return (
    <QueryGate query={query}>
      {(window) => (
        <>
          <CaptureFunnel funnel={window.funnel} />
          <p className="capture-activity__scope-note">
            {t("captureActivity.scopeNote")}
          </p>
          {window.data.length === 0 ? (
            <SurfaceState state="empty" emptyLabel={t("captureActivity.empty")}>
              {null}
            </SurfaceState>
          ) : (
            <ul className="capture-activity__list">
              {window.data.map((entry) => (
                <CaptureEntryRow
                  key={entry.id}
                  entry={entry}
                  payloads={window.payload_capture_enabled}
                />
              ))}
            </ul>
          )}
        </>
      )}
    </QueryGate>
  );
}

function CaptureFunnel({
  funnel,
}: Readonly<{ funnel: CaptureActivity["funnel"] }>) {
  const t = useT();
  return (
    <StatStrip testId="capture-activity-funnel">
      {OUTCOMES.map((outcome) => (
        <StatCard
          key={outcome}
          label={t(`captureActivity.outcome.${outcome}` as never)}
          // Zero is a reading, not an absence: "no message was dropped as
          // internal today" is exactly what somebody comes here to confirm.
          value={String(funnel[outcome] ?? 0)}
        />
      ))}
    </StatStrip>
  );
}

function CaptureEntryRow({
  entry,
  payloads,
}: Readonly<{ entry: TraceEntry; payloads: boolean }>) {
  const t = useT();
  const { locale } = useLocale();
  // The reader's own zone: a trace is read to reconcile "I sent that at 9:04"
  // against what the pipeline did, and a UTC timestamp makes them do the
  // arithmetic themselves.
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  return (
    <li className="capture-activity__row" data-outcome={entry.outcome}>
      <span className="capture-activity__when">
        {formatDateTime(entry.occurred_at, locale, zone)}
      </span>
      <span className="capture-activity__connector">{entry.connector}</span>
      <span className="capture-activity__outcome">
        {t(`captureActivity.outcome.${entry.outcome}` as never)}
        {entry.reason ? (
          <span className="capture-activity__reason">
            {t(`captureActivity.reason.${entry.reason}` as never)}
          </span>
        ) : null}
      </span>
      <CaptureEntryContent entry={entry} payloads={payloads} />
      <CaptureEntryResolution entry={entry} />
    </li>
  );
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
  if (!entry.resolution) {
    return null;
  }
  return (
    <span className="capture-activity__resolution">
      {t(`captureActivity.resolution.${entry.resolution.status}` as never)}
    </span>
  );
}
