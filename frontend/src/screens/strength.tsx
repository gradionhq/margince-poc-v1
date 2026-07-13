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
import { problemMessage } from "./common";

// The relationship-strength card (Phase 3, P-4): "no mystery number" — the
// composite score NEVER renders alone. It always carries its bucket badge
// and the full recency/frequency/reciprocity/direction factor breakdown that
// explains it (spec ai-operational-spec.md), plus the receipts (last
// interaction, 90d in/out counts, contributing-activity count). A record
// with no qualifying interactions is bucket:dormant, score:0 — that's
// rendered plainly (0% bars, an honest "no interactions yet" caption), never
// hidden or dressed up as an error.

type RelationshipStrength = components["schemas"]["RelationshipStrength"];

const BUCKET_TONE: Record<
  RelationshipStrength["bucket"],
  "success" | "accent" | "warn" | undefined
> = {
  strong: "success",
  warm: "accent",
  weak: "warn",
  dormant: undefined,
};

async function fetchStrength(
  kind: "person" | "organization",
  id: string,
): Promise<RelationshipStrength> {
  if (kind === "person") {
    const { data, error } = await api.GET("/people/{id}/strength", {
      params: { path: { id } },
    });
    if (error) {
      throw new Error(problemMessage(error));
    }
    return data;
  }
  const { data, error } = await api.GET("/organizations/{id}/strength", {
    params: { path: { id } },
  });
  if (error) {
    throw new Error(problemMessage(error));
  }
  return data;
}

function factorPercent(value: number): number {
  return Math.round(value * 100);
}

export function StrengthCard({
  kind,
  id,
}: Readonly<{ kind: "person" | "organization"; id: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const query = useQuery({
    queryKey: ["strength", kind, id],
    queryFn: () => fetchStrength(kind, id),
  });

  return (
    <section className="card" style={{ marginBottom: 16 }}>
      <SectionHeader title={t("strength.title")} />
      {query.isPending && (
        <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
          <Skeleton width="40%" />
          <Skeleton width="90%" />
        </div>
      )}
      {query.isError && (
        <EmptyState>
          {query.error instanceof Error
            ? query.error.message
            : t("common.error")}
        </EmptyState>
      )}
      {query.isSuccess && (
        <StrengthBody strength={query.data} locale={locale} />
      )}
    </section>
  );
}

function StrengthBody({
  strength,
  locale,
}: Readonly<{
  strength: RelationshipStrength;
  locale: ReturnType<typeof useLocale>["locale"];
}>) {
  const t = useT();
  // The contract guarantees factors/bucket/score, but a single data-driven
  // card must never crash the whole 360 if a response arrives malformed —
  // degrade to the honest zero/dormant reading instead (craft T7).
  const factors = strength.factors ?? {
    recency: 0,
    frequency: 0,
    reciprocity: 0,
    direction: 0,
  };
  const bucket = strength.bucket ?? "dormant";
  const score = strength.score ?? 0;
  const factorRows: Array<{
    key: "recency" | "frequency" | "reciprocity" | "direction";
    value: number;
  }> = [
    { key: "recency", value: factors.recency },
    { key: "frequency", value: factors.frequency },
    { key: "reciprocity", value: factors.reciprocity },
    { key: "direction", value: factors.direction },
  ];
  const contributingCount = strength.contributing_activity_ids?.length ?? 0;

  return (
    <div>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 10,
          flexWrap: "wrap",
          marginBottom: 12,
        }}
      >
        <Badge tone={BUCKET_TONE[bucket]}>
          {t(`strength.bucket.${bucket}`)}
        </Badge>
        <span className="t-mono">{t("strength.score", { score })}</span>
      </div>
      <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
        {factorRows.map((row) => {
          const pct = factorPercent(row.value);
          return (
            <div key={row.key}>
              <div
                style={{
                  display: "flex",
                  justifyContent: "space-between",
                  fontSize: 13,
                }}
              >
                <span>{t(`strength.factor.${row.key}`)}</span>
                <span className="t-mono">{pct}%</span>
              </div>
              <div className="meterbar">
                <span style={{ width: `${pct}%` }} />
              </div>
            </div>
          );
        })}
      </div>
      <p className="t-caption" style={{ marginTop: 10 }}>
        {strength.last_interaction
          ? t("strength.lastInteraction", {
              when: formatDateTime(
                strength.last_interaction,
                locale,
                "Europe/Berlin",
              ),
            })
          : t("strength.none")}
      </p>
      {(strength.inbound_90d != null || strength.outbound_90d != null) && (
        <p className="t-caption">
          {t("strength.inout", {
            in: strength.inbound_90d ?? 0,
            out: strength.outbound_90d ?? 0,
          })}
        </p>
      )}
      {contributingCount > 0 && (
        <p className="t-caption">
          {t("strength.computedFrom", { count: contributingCount })}
        </p>
      )}
    </div>
  );
}
