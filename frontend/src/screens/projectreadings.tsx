// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { StatCard } from "../design-system/atoms";
import { StatStrip } from "../design-system/statstrip";
import { SurfaceState } from "../design-system/surfacestate";
import { formatDateAbbrev, formatMoneyOrAbsent } from "../format/format";
import { RECORD_ZONE } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import { type Project360, stateOf } from "./projectsections";

// The project page's band under its header: the readings plate and the one
// line about how well its correspondence is filed. Apart from the section
// cards because they are read ACROSS, once, rather than down a column.

/**
 * The header figures: what the deals on this project are worth, what is
 * still owed, and how much has been filed. Present only when the server
 * could compute all of them — it withholds the whole strip rather than half
 * of it, because a strip showing deals and no work reads as a project with
 * no work.
 */
export function RollupsStrip({ view }: Readonly<{ view: Project360 }>) {
  const t = useT();
  const { locale } = useLocale();
  const rollups = view.rollups;
  const state = stateOf(view, "rollups", Boolean(rollups), rollups ? 1 : 0);
  if (state !== "ready" || !rollups) {
    return (
      <SurfaceState state={state} emptyLabel={t("project.rollups.empty")}>
        {null}
      </SurfaceState>
    );
  }
  return (
    <StatStrip testId="project-rollups">
      <StatCard
        label={t("project.rollups.openValue")}
        value={formatMoneyOrAbsent(
          rollups.open_deal_value.amount_minor,
          rollups.open_deal_value.currency,
          locale,
        )}
        numeric
      />
      <StatCard
        label={t("project.rollups.wonValue")}
        value={formatMoneyOrAbsent(
          rollups.won_deal_value.amount_minor,
          rollups.won_deal_value.currency,
          locale,
        )}
        numeric
      />
      <StatCard
        label={t("project.rollups.openCommitments")}
        value={String(rollups.open_commitments)}
        numeric
      />
      <StatCard
        label={t("project.rollups.lastActivity")}
        value={
          rollups.last_activity_at
            ? formatDateAbbrev(rollups.last_activity_at, locale, RECORD_ZONE)
            : t("project.rollups.never")
        }
      />
      <StatCard
        label={t("project.rollups.activityCount")}
        value={String(rollups.activity_count)}
        numeric
      />
    </StatStrip>
  );
}

/**
 * CoverageLine says how well this project's correspondence is filed. It is
 * information, never a task: the unattributed count is the filing debt a rep
 * MAY work down, stated once under the figures rather than as a queue.
 */
export function CoverageLine({ view }: Readonly<{ view: Project360 }>) {
  const t = useT();
  const coverage = view.coverage;
  const state = stateOf(view, "coverage", Boolean(coverage), coverage ? 1 : 0);
  // Withheld is a fact the line states; a section that simply did not come
  // back has nothing honest to say and draws nothing.
  if (state === "withheld") {
    return (
      <div data-testid="project-coverage-withheld">
        <SurfaceState state={state} emptyLabel="">
          {null}
        </SurfaceState>
      </div>
    );
  }
  if (state !== "ready" || !coverage) {
    return null;
  }
  return (
    <p className="t-caption project-coverage" data-testid="project-coverage">
      {t("project.coverage", {
        attributed: coverage.attributed,
        awaiting: coverage.awaiting_decision,
        nearby: coverage.unattributed_nearby,
      })}
    </p>
  );
}
