// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  EmptyState,
  SectionHeader,
  Skeleton,
} from "../design-system/atoms";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import "./network.css";
import { OverlayUnavailable, problemMessage, useSorMode } from "./common";

// The two relationship-graph cards (ADR-0078).
//
// "Who here knows them" answers the question a rep asks before they write a
// cold email, and the coverage card answers the one their manager asks before
// the forecast call. Both were server-only until now: the endpoints shipped and
// nothing rendered them, so the interaction projection was a fact the product
// held and never showed anybody.
//
// Ordering is the answer on both cards and is NOT re-sorted here. The server
// ranks colleagues by a strength it computes at read; a client that re-ordered
// them would be a second implementation of the decay formula, and the two would
// disagree the moment either changed.

type PersonNetworkColleague = components["schemas"]["PersonNetworkColleague"];
type DealCoverage = components["schemas"]["DealCoverage"];
type DealCoverageRisk = components["schemas"]["DealCoverageRisk"];

// The per-user band vocabulary is PO-F-3b's — none/weak/moderate/strong — and
// deliberately NOT the workspace-wide card's dormant/weak/warm/strong. The two
// measure different things, and giving them one set of words on screen would
// invite a reader to compare numbers that are not comparable.
const COLLEAGUE_TONE: Record<
  PersonNetworkColleague["strength_bucket"],
  "success" | "accent" | "warn" | undefined
> = {
  strong: "success",
  moderate: "accent",
  weak: "warn",
  none: undefined,
};

// Every risk kind is a warning; only the two that mean somebody is gone are
// rendered as danger. A card where six chips all shout is a card a rep stops
// reading.
const RISK_TONE: Record<DealCoverageRisk["kind"], "danger" | "warn"> = {
  champion_left: "danger",
  stakeholder_left: "danger",
  going_cold: "warn",
  single_threaded_theirs: "warn",
  single_threaded_ours: "warn",
  coverage_gap: "warn",
};

async function fetchPersonNetwork(
  id: string,
): Promise<components["schemas"]["PersonNetwork"]> {
  const { data, error } = await api.GET("/people/{id}/network", {
    params: { path: { id } },
  });
  if (error) {
    throw new Error(problemMessage(error));
  }
  return data;
}

async function fetchDealCoverage(id: string): Promise<DealCoverage> {
  const { data, error } = await api.GET("/deals/{id}/coverage", {
    params: { path: { id } },
  });
  if (error) {
    throw new Error(problemMessage(error));
  }
  return data;
}

/** The colleagues who know this contact, warmest first. */
export function PersonNetworkCard({ id }: Readonly<{ id: string }>) {
  const t = useT();
  const { locale } = useLocale();
  // The projection is folded from natively captured participants, which the
  // incumbent mirror does not hold. Show the honest unavailable state rather
  // than a doomed fetch that renders as "nobody knows them".
  const overlay = useSorMode() === "overlay";
  const query = useQuery({
    queryKey: ["person-network", id],
    queryFn: () => fetchPersonNetwork(id),
    enabled: !overlay,
  });
  const colleagues = query.data?.colleagues ?? [];

  return (
    <section className="card net-card">
      <SectionHeader title={t("network.title")} />
      {overlay && <OverlayUnavailable />}
      {!overlay && query.isPending && <Skeleton width="80%" />}
      {!overlay && query.isError && (
        <EmptyState>
          {query.error instanceof Error
            ? query.error.message
            : t("common.error")}
        </EmptyState>
      )}
      {!overlay && query.isSuccess && colleagues.length === 0 && (
        <EmptyState>{t("network.empty")}</EmptyState>
      )}
      {!overlay && colleagues.length > 0 && (
        <ul className="net-colleagues">
          {colleagues.map((colleague) => (
            <li key={colleague.user_id}>
              <span className="net-name">{colleague.display_name}</span>
              <Badge tone={COLLEAGUE_TONE[colleague.strength_bucket]}>
                {t(`network.bucket.${colleague.strength_bucket}`)}
              </Badge>
              {/* A `none` band carries NO count, for the same reason it
                  carries no score: never spoken and spoken-then-cold are
                  different facts, and a zero renders them identically. */}
              {colleague.strength_bucket !== "none" && (
                <span className="t-caption">
                  {t("network.interactions", {
                    count: colleague.interactions_90d,
                  })}
                </span>
              )}
              <span className="t-caption">
                {colleague.last_at
                  ? formatDateTime(colleague.last_at, locale, "Europe/Berlin")
                  : t("network.neverSpoken")}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/** What is wrong with how this deal is covered, and why. */
export function DealCoverageCard({ id }: Readonly<{ id: string }>) {
  const t = useT();
  const overlay = useSorMode() === "overlay";
  const query = useQuery({
    queryKey: ["deal-coverage", id],
    queryFn: () => fetchDealCoverage(id),
    enabled: !overlay,
  });
  const risks = query.data?.risks ?? [];

  return (
    <section className="card net-card">
      <SectionHeader title={t("coverage.title")} />
      {overlay && <OverlayUnavailable />}
      {!overlay && query.isPending && <Skeleton width="60%" />}
      {!overlay && query.isError && (
        <EmptyState>
          {query.error instanceof Error
            ? query.error.message
            : t("common.error")}
        </EmptyState>
      )}
      {/* No findings is a RESULT, not an empty state. A deal that passes every
          coverage rule has earned a sentence saying so — a blank card reads as
          a card that failed to load. */}
      {!overlay && query.isSuccess && risks.length === 0 && (
        <p className="t-caption">{t("coverage.clear")}</p>
      )}
      {!overlay && risks.length > 0 && (
        <ul className="net-risks">
          {risks.map((risk) => (
            <li key={risk.kind}>
              <Badge tone={RISK_TONE[risk.kind]}>
                {t(`coverage.risk.${risk.kind}`)}
              </Badge>
              {/* The rule's own explanation, not a client re-wording: the
                  server names the rule and says why, so a flag on this card
                  reads identically to the same flag in the assistant. */}
              <span className="net-risk-why">{risk.summary}</span>
              {risk.days_since_touch != null && (
                <span className="t-mono net-risk-days">
                  {t("coverage.daysSinceTouch", {
                    days: risk.days_since_touch,
                  })}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
