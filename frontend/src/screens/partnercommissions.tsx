import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, DataTable, EmptyState } from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import { formatMoney, INTL_LOCALE } from "../format/format";

import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { QueryGate, throwProblem } from "./common";

// What a partner has earned, on the partner's own company page.
//
// The margin tier one screen up is what the arrangement SAYS; this is what it
// has actually produced. Showing the tier without ever showing the money is how
// a number nobody can check ends up in a contract.

type CommissionEntry = components["schemas"]["CommissionEntry"];
type CommissionStatus = CommissionEntry["status"];

// Each ledger state and how it reads. The tones say what a reader needs at a
// glance: money still owed, money agreed, money gone, money taken back.
const STATUS_LABELS: Record<CommissionStatus, MessageKey> = {
  accrued: "commission.status.accrued",
  approved: "commission.status.approved",
  paid: "commission.status.paid",
  void: "commission.status.void",
};

// Accrued is the one that still needs a decision, so it leads; approved and
// paid are both settled and read the same; void is the exception a reader must
// not skim past.
const STATUS_TONES: Record<CommissionStatus, "accent" | "success" | "warn"> = {
  accrued: "accent",
  approved: "success",
  paid: "success",
  void: "warn",
};

// The whole ledger, followed page by page.
//
// Reading page one and stopping would under-report what a partner earned the
// moment they pass one page of entries — and it would do it silently, which is
// the worst way for a money figure to be wrong. The panel totals nothing today,
// but a list that claims to be the ledger has to be the ledger.
async function fetchPartnerCommissions(
  organizationId: string,
): Promise<CommissionEntry[]> {
  const entries: CommissionEntry[] = [];
  let cursor: string | undefined;
  do {
    const { data, error } = await api.GET("/commissions", {
      params: {
        query: { partner_org_id: organizationId, limit: 50, cursor },
      },
    });
    if (error) {
      throwProblem(error);
    }
    entries.push(...(data?.data ?? []));
    cursor = data?.page?.has_more
      ? (data.page.next_cursor ?? undefined)
      : undefined;
  } while (cursor);
  return entries;
}

/**
 * PartnerCommissions lists what this partner earned, newest first.
 *
 * A reversal keeps its own row rather than being folded into the entry it
 * cancels: the ledger's whole premise is that what it recorded stays recorded,
 * and a partner asking "what happened to that one" needs to see both halves.
 */
export function PartnerCommissions({
  organizationId,
}: Readonly<{ organizationId: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const query = useQuery({
    queryKey: ["partner-commissions", organizationId],
    queryFn: () => fetchPartnerCommissions(organizationId),
  });

  return (
    <Panel title={t("commission.panelTitle")} sub={t("commission.panelSub")}>
      <QueryGate query={query}>
        {(entries) =>
          entries.length === 0 ? (
            <PanelBody>
              <EmptyState>{t("commission.none")}</EmptyState>
            </PanelBody>
          ) : (
            <PanelBody>
              <CommissionLedger entries={entries} locale={locale} />
            </PanelBody>
          )
        }
      </QueryGate>
    </Panel>
  );
}

function CommissionLedger({
  entries,
  locale,
}: Readonly<{
  entries: CommissionEntry[];
  locale: Locale;
}>) {
  const t = useT();
  return (
    <div data-testid="commission-ledger">
      <DataTable
        label={t("commission.panelTitle")}
        rows={entries}
        rowKey={(entry) => entry.id}
        columns={[
          {
            key: "amount",
            header: t("commission.column.amount"),
            render: (entry) =>
              formatMoney(entry.amount_minor, entry.currency, locale),
          },
          {
            key: "rate",
            header: t("commission.column.rate"),
            // Basis points render as the percentage a human agreed to: 1500 is
            // the tier's 15%, and nobody outside the schema thinks in bps.
            render: (entry) => formatRate(entry.rate_bps, locale),
          },
          {
            key: "basis",
            header: t("commission.column.basis"),
            render: (entry) =>
              formatMoney(entry.basis_amount_minor, entry.currency, locale),
          },
          {
            key: "status",
            header: t("commission.column.status"),
            render: (entry) => (
              <Badge tone={STATUS_TONES[entry.status]} quiet>
                {t(STATUS_LABELS[entry.status])}
              </Badge>
            ),
          },
        ]}
      />
    </div>
  );
}

/**
 * formatRate renders basis points as a percentage in the reader's locale.
 *
 * Trailing zeros are dropped so the common whole-percent tiers read as "15%"
 * rather than "15.00%", while a rate that genuinely carries a fraction still
 * shows it. Through Intl rather than string arithmetic: a German reader writes
 * "12,5 %", and a hand-built string would hand them a decimal point.
 */
export function formatRate(rateBps: number, locale: Locale): string {
  const percent = rateBps / 100;
  return new Intl.NumberFormat(INTL_LOCALE[locale], {
    style: "percent",
    maximumFractionDigits: Number.isInteger(percent) ? 0 : 2,
  }).format(percent / 100);
}
