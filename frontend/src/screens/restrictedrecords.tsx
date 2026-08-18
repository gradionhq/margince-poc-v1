import { useQuery } from "@tanstack/react-query";
import type { CSSProperties } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCan } from "../app/capability";
import { Badge, DataTable, EmptyState } from "../design-system/atoms";
import { CardBoundary } from "../design-system/cardboundary";
import { Panel, PanelBody } from "../design-system/panel";
import { formatDate } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { humanizeToken } from "./audit";
import { QueryGate, QueryStates, throwProblem, useMe } from "./common";
import "./retention.css";

// Settings → Privacy → Restricted records (A165/ADR-0114 §4): what a
// statutory obligation is holding after an erasure — which record, why, and
// until when — stated without the correspondence itself. The audit log proves
// what happened; this answers what is being held right now, which is the
// question a supervisory authority asks the controller.
//
// It reads through the same authority as the retention ladder above it, so a
// role that may not see how long records are kept may not see which are being
// kept either.

export type RestrictedRecord = components["schemas"]["RestrictedRecord"];

export const RESTRICTED_RECORDS_KEY = ["retention", "restrictions"] as const;

const PANEL_SUB: CSSProperties = { marginBottom: "var(--space-3)" };

// The obligation's class is the first token of the server's reason
// ("commercial_correspondence · §257 HGB / §147 AO"); the statute after the
// separator is shown as written, because it is the citation and not a label.
function splitReason(reason: string): { cls: string; basis: string } {
  const [cls, ...rest] = reason.split(" · ");
  return { cls, basis: rest.join(" · ") };
}

// The classes the schema admits today, by name. A class this build has not
// heard of renders as its own token rather than a missing key — a newer
// server must not make the list unreadable.
const CLASS_LABEL: Readonly<Record<string, MessageKey>> = {
  commercial_correspondence: "restricted.class.commercialCorrespondence",
};

// The interaction kinds the timeline knows, by name; same fallback.
const KIND_LABEL: Readonly<Record<string, MessageKey>> = {
  email: "restricted.kind.email",
  call: "restricted.kind.call",
  meeting: "restricted.kind.meeting",
  message: "restricted.kind.message",
};

export function RestrictedRecordsCard() {
  const t = useT();
  const me = useMe();
  const { locale } = useLocale();
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const canRead = useCan("retention_policy", "read");

  const records = useQuery({
    queryKey: RESTRICTED_RECORDS_KEY,
    enabled: canRead,
    queryFn: async () => {
      const { data, error } = await api.GET("/retention/restrictions");
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  if (!canRead) {
    return (
      <Panel title={t("restricted.title")}>
        <PanelBody>
          <p className="t-sub" style={PANEL_SUB}>
            {t("restricted.sub")}
          </p>
          <QueryGate query={me}>
            {() => (
              <EmptyState>
                <p className="t-small">{t("restricted.withheld")}</p>
              </EmptyState>
            )}
          </QueryGate>
        </PanelBody>
      </Panel>
    );
  }

  const columns = [
    {
      key: "kind",
      header: t("restricted.kind"),
      render: (row: RestrictedRecord) => (
        <Badge>
          {KIND_LABEL[row.kind]
            ? t(KIND_LABEL[row.kind])
            : humanizeToken(row.kind)}
        </Badge>
      ),
    },
    {
      key: "occurred",
      header: t("restricted.occurred"),
      render: (row: RestrictedRecord) =>
        formatDate(row.occurred_at, locale, tz),
    },
    {
      key: "deals",
      header: t("restricted.deals"),
      render: (row: RestrictedRecord) =>
        row.deals.length === 0
          ? t("restricted.noDeal")
          : row.deals.map((deal) => deal.name).join(", "),
    },
    {
      key: "reason",
      header: t("restricted.reason"),
      render: (row: RestrictedRecord) => {
        const { cls, basis } = splitReason(row.reason);
        return (
          <span className="retention-scope">
            <span>
              {CLASS_LABEL[cls] ? t(CLASS_LABEL[cls]) : humanizeToken(cls)}
            </span>
            <span className="t-caption">{basis}</span>
          </span>
        );
      },
    },
    {
      key: "until",
      header: t("restricted.until"),
      render: (row: RestrictedRecord) =>
        formatDate(row.restricted_until, locale, tz),
    },
    {
      key: "redacted",
      header: t("restricted.redacted"),
      render: (row: RestrictedRecord) =>
        (row.redacted_fields ?? []).length === 0
          ? t("restricted.nothingRedacted")
          : t("restricted.redactedCount", {
              count: (row.redacted_fields ?? []).length,
            }),
    },
  ];

  return (
    <Panel title={t("restricted.title")}>
      <PanelBody>
        <p className="t-sub" style={PANEL_SUB}>
          {t("restricted.sub")}
        </p>
        <CardBoundary>
          <QueryStates query={records}>
            {records.data &&
              (records.data.data.length === 0 ? (
                <EmptyState>{t("restricted.empty")}</EmptyState>
              ) : (
                <DataTable
                  columns={columns}
                  rows={records.data.data}
                  rowKey={(row) => row.activity_id}
                />
              ))}
          </QueryStates>
        </CardBoundary>
      </PanelBody>
    </Panel>
  );
}
