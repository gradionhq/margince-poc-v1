import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, EmptyState } from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import { formatMoney } from "../format/format";
import { useLocale, useT } from "../i18n";
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

async function fetchPartnerCommissions(
  organizationId: string,
): Promise<CommissionEntry[]> {
  const { data, error } = await api.GET("/commissions", {
    params: { query: { partner_org_id: organizationId, limit: 50 } },
  });
  if (error) {
    throwProblem(error);
  }
  return data?.data ?? [];
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
  locale: ReturnType<typeof useLocale>["locale"];
}>) {
  const t = useT();
  return (
    <table className="table" data-testid="commission-ledger">
      <thead>
        <tr>
          <th scope="col">{t("commission.column.amount")}</th>
          <th scope="col">{t("commission.column.rate")}</th>
          <th scope="col">{t("commission.column.basis")}</th>
          <th scope="col">{t("commission.column.status")}</th>
        </tr>
      </thead>
      <tbody>
        {entries.map((entry) => (
          <tr key={entry.id} data-testid="commission-row">
            <td>{formatMoney(entry.amount_minor, entry.currency, locale)}</td>
            {/* Basis points render as the percentage a human agreed to: 1500 is
                the tier's 15%, and nobody outside the schema thinks in bps. */}
            <td>{formatRate(entry.rate_bps)}</td>
            <td>
              {formatMoney(entry.basis_amount_minor, entry.currency, locale)}
            </td>
            <td>
              <Badge tone={STATUS_TONES[entry.status]} quiet>
                {t(STATUS_LABELS[entry.status])}
              </Badge>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/**
 * formatRate renders basis points as a percentage.
 *
 * Trailing zeros are dropped so the common whole-percent tiers read as "15%"
 * rather than "15.00%", while a rate that genuinely carries a fraction still
 * shows it.
 */
export function formatRate(rateBps: number): string {
  const percent = rateBps / 100;
  return `${Number.isInteger(percent) ? percent : percent.toFixed(2)}%`;
}
