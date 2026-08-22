// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useId, useState } from "react";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { SearchField } from "./atoms";
import { DateInput, type ISODate, isISODate } from "./dateinput";
import {
  ACTIVITY_KINDS,
  type ActivityKind,
  type TimelineFilters,
} from "./recordtimeline";
import { Select } from "./select";
import "./timelinefilterbar.css";

const KIND_LABEL: Readonly<Record<ActivityKind, MessageKey>> = {
  email: "timeline.filters.kind.email",
  message: "timeline.filters.kind.message",
  call: "timeline.filters.kind.call",
  meeting: "timeline.filters.kind.meeting",
  note: "timeline.filters.kind.note",
  task: "timeline.filters.kind.task",
};

function isActivityKind(value: string): value is ActivityKind {
  return ACTIVITY_KINDS.some((kind) => kind === value);
}

/**
 * TimelineFilterBar is the ONE row of dials every record timeline narrows
 * by: a kind, a search, and a date range. Every dial is a server parameter
 * (`kind`, `q`, `occurred_after` / `occurred_before`); the bar owns no list
 * and filters nothing itself, which is what keeps "tasks on this contact"
 * from meaning "tasks among the twenty newest rows".
 *
 * The search commits on Enter or when the field is left, not per keystroke:
 * each commit is a request, and a reader typing a word would otherwise fire
 * one per letter on a phone link.
 *
 * A search answers from a content-gated read (a limited conversation the
 * reader may not open is simply absent from a text match), and the bar says
 * so under the row while a search is active — silence would present the
 * matches as all there are.
 */
export function TimelineFilterBar({
  value,
  onChange,
}: Readonly<{
  value: TimelineFilters;
  onChange: (next: TimelineFilters) => void;
}>) {
  const t = useT();
  const id = useId();
  const [draft, setDraft] = useState(value.q ?? "");
  // The draft follows a filter reset from outside (a record change): a box
  // still showing the previous record's word would claim a search that is
  // not running.
  const [draftFor, setDraftFor] = useState(value.q ?? "");
  if ((value.q ?? "") !== draftFor) {
    setDraftFor(value.q ?? "");
    setDraft(value.q ?? "");
  }
  const commitSearch = () => {
    if (draft.trim() !== (value.q ?? "")) {
      onChange({ ...value, q: draft.trim() });
    }
  };
  const kindOptions = [
    { value: "", label: t("timeline.filters.kind.all") },
    ...ACTIVITY_KINDS.map((kind) => ({
      value: kind,
      label: t(KIND_LABEL[kind]),
    })),
  ];
  const day = (raw: string): ISODate | "" => (isISODate(raw) ? raw : "");
  return (
    <div className="timeline-filters">
      <div className="timeline-filters-row">
        <Select
          aria-label={t("timeline.filters.kind")}
          options={kindOptions}
          value={value.kind ?? ""}
          onChange={(next) =>
            onChange({
              ...value,
              kind: isActivityKind(next) ? next : undefined,
            })
          }
        />
        <SearchField
          value={draft}
          aria-label={t("timeline.filters.search")}
          placeholder={t("timeline.filters.search")}
          onChange={(event) => setDraft(event.target.value)}
          onBlur={commitSearch}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              commitSearch();
            }
          }}
        />
        <span className="timeline-filters-range">
          <label htmlFor={`${id}-after`} className="t-caption">
            {t("timeline.filters.from")}
          </label>
          <DateInput
            id={`${id}-after`}
            value={value.after ?? ""}
            max={value.before || undefined}
            onChange={(event) =>
              onChange({ ...value, after: day(event.target.value) })
            }
          />
          <label htmlFor={`${id}-before`} className="t-caption">
            {t("timeline.filters.to")}
          </label>
          <DateInput
            id={`${id}-before`}
            value={value.before ?? ""}
            min={value.after || undefined}
            onChange={(event) =>
              onChange({ ...value, before: day(event.target.value) })
            }
          />
        </span>
      </div>
      {value.q && (
        <p className="t-caption">{t("timeline.filters.searchOmitsLimited")}</p>
      )}
    </div>
  );
}
