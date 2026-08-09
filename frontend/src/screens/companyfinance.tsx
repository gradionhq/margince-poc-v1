import { useQuery } from "@tanstack/react-query";
import { Landmark } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button } from "../design-system/atoms";
import { Sparkline } from "../design-system/readings";
import { formatDate, formatMoney } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { throwProblem } from "./common";
import { RECORD_ZONE, SectionCard, type SectionState } from "./company360";

// The finance card: does this customer actually pay us, and on time?
//
// THE RULE THIS CARD IS BUILT AROUND: no figure is invented, and the absence
// of one is never drawn as a zero. "€0 open" says the customer is square with
// us; "—" says we do not know. Rendering the second as the first tells a rep
// an account is healthy on the strength of a missing connector, which is the
// one thing §6 State B forbids outright.
//
// So the card renders the STATE first and the figures only where the server
// sent them. Six states, and five of them look identical if you draw only the
// numbers — which is why the server sends the state at all.

type FinanceSummary = components["schemas"]["OrganizationFinanceSummary"];
type FinanceState = components["schemas"]["FinanceSummaryState"];
type FinanceInvoice = components["schemas"]["FinanceInvoice"];

function useFinanceSummary(orgId: string) {
  return useQuery<FinanceSummary>({
    queryKey: ["finance-summary", orgId],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/organizations/{id}/finance-summary",
        { params: { path: { id: orgId } } },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
}

// Which §7 card state each finance state renders as. The mapping is explicit
// rather than derived, because two of them are NOT what they look like:
// `unmapped` is a ready card with an action, not an error, and `error` still
// shows the last good figures.
const CARD_STATE: Record<FinanceState, SectionState> = {
  no_connection: "empty",
  unmapped: "empty",
  syncing: "loading",
  connected: "ready",
  stale: "stale",
  error: "failed",
};

export function CompanyFinanceCard({
  orgId,
  lifecycle,
}: Readonly<{
  orgId: string;
  // The account's lifecycle. A target or a prospect has never been invoiced,
  // so the card is ABSENT for them rather than empty (FIN-AC-3) — an empty
  // finance card on a company we have never billed is a question nobody asked.
  lifecycle?: string;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const billable = lifecycle === "customer" || lifecycle === "former_customer";
  const query = useFinanceSummary(orgId);

  if (!billable) {
    return null;
  }
  if (query.isPending) {
    return (
      <SectionCard
        title={t("finance.title")}
        state="loading"
        emptyLabel={t("finance.none")}
      >
        {null}
      </SectionCard>
    );
  }
  if (query.isError) {
    return (
      <SectionCard
        title={t("finance.title")}
        state="failed"
        emptyLabel={t("finance.none")}
        detail={{ onRetry: () => void query.refetch() }}
      >
        {null}
      </SectionCard>
    );
  }
  const summary = query.data;
  return (
    <SectionCard
      title={t("finance.title")}
      state={CARD_STATE[summary.state]}
      emptyLabel={t(EMPTY_LABEL[summary.state] ?? "finance.none")}
      detail={{
        onRetry: () => void query.refetch(),
        staleAsOf: summary.last_synced_at
          ? formatDate(summary.last_synced_at, locale, RECORD_ZONE)
          : undefined,
      }}
      footer={<FinanceProvenance summary={summary} />}
    >
      <FinanceBody summary={summary} />
    </SectionCard>
  );
}

// What the card says when it has no figures. Two different sentences, because
// "nobody has connected an accounting system" and "this customer is not mapped
// to one of its customers" have different fixes.
const EMPTY_LABEL: Partial<Record<FinanceState, MessageKey>> = {
  no_connection: "finance.noConnection",
  unmapped: "finance.unmapped",
};

function FinanceBody({ summary }: Readonly<{ summary: FinanceSummary }>) {
  const t = useT();
  const { locale } = useLocale();
  return (
    <>
      <div className="fin-figures">
        <FinanceFigure
          label={t("finance.netInvoiced")}
          value={
            summary.net_invoiced
              ? formatMoney(
                  summary.net_invoiced.amount_minor ?? 0,
                  summary.net_invoiced.currency ?? "EUR",
                  locale,
                )
              : undefined
          }
        />
        <FinanceFigure
          label={t("finance.openBalance")}
          value={
            summary.open_balance
              ? formatMoney(
                  summary.open_balance.amount_minor ?? 0,
                  summary.open_balance.currency ?? "EUR",
                  locale,
                )
              : undefined
          }
        />
        <FinanceFigure
          label={t("finance.overdue")}
          value={
            summary.overdue
              ? formatMoney(
                  summary.overdue.amount_minor ?? 0,
                  summary.overdue.currency ?? "EUR",
                  locale,
                )
              : undefined
          }
          // Overdue money is the one reading on this card that is bad news
          // when it is present at all.
          tone={(summary.overdue?.amount_minor ?? 0) > 0 ? "danger" : undefined}
        />
      </div>
      <PaymentBehaviour summary={summary} />
      <RecentInvoices summary={summary} />
    </>
  );
}

// One reading. An absent value renders as a dash with its label intact, so the
// reader sees WHICH figure is missing rather than a shorter row.
function FinanceFigure({
  label,
  value,
  tone,
}: Readonly<{ label: string; value?: string; tone?: "danger" }>) {
  return (
    <div className="fin-figure">
      <span className="t-caption">{label}</span>
      <span className={tone ? `fin-value fin-value-${tone}` : "fin-value"}>
        {value ?? "—"}
      </span>
    </div>
  );
}

// How they pay, as a shape and a number. Both are absent together: the server
// withholds them under the same sample floor, because a line drawn from one
// settled invoice states a habit the number beside it refuses to.
function PaymentBehaviour({ summary }: Readonly<{ summary: FinanceSummary }>) {
  const t = useT();
  const series = summary.payment_behaviour ?? [];
  if (summary.median_days_after_due == null) {
    return null;
  }
  return (
    <div className="fin-behaviour">
      <span className="t-caption">{t("finance.behaviour")}</span>
      <Sparkline points={series} label={t("finance.behaviour")} />
      <span className="t-caption">
        {t("finance.medianAfterDue", { days: summary.median_days_after_due })}
      </span>
    </div>
  );
}

function RecentInvoices({ summary }: Readonly<{ summary: FinanceSummary }>) {
  const t = useT();
  const { locale } = useLocale();
  const invoices = summary.recent_invoices ?? [];
  if (invoices.length === 0) {
    return null;
  }
  return (
    <>
      <table className="table fin-table">
        <thead>
          <tr>
            <th>{t("finance.col.invoice")}</th>
            <th>{t("finance.col.issued")}</th>
            <th>{t("finance.col.due")}</th>
            <th>{t("finance.col.amount")}</th>
            <th>{t("finance.col.status")}</th>
          </tr>
        </thead>
        <tbody>
          {invoices.map((invoice) => (
            <InvoiceRow key={invoice.id} invoice={invoice} locale={locale} />
          ))}
        </tbody>
      </table>
      {summary.truncated && (
        <p className="co-empty">{t("finance.moreInvoices")}</p>
      )}
    </>
  );
}

function InvoiceRow({
  invoice,
  locale,
}: Readonly<{
  invoice: FinanceInvoice;
  locale: ReturnType<typeof useLocale>["locale"];
}>) {
  const t = useT();
  return (
    <tr>
      <td>{invoice.number ?? t("finance.unnumbered")}</td>
      <td>{formatDate(invoice.issued_at, locale, RECORD_ZONE)}</td>
      <td>
        {invoice.due_at ? formatDate(invoice.due_at, locale, RECORD_ZONE) : "—"}
      </td>
      <td>{formatMoney(invoice.gross_minor, invoice.currency, locale)}</td>
      <td>
        <Badge tone={STATUS_TONE[invoice.status]}>
          {t(STATUS_LABEL[invoice.status])}
        </Badge>
      </td>
    </tr>
  );
}

const STATUS_LABEL: Record<FinanceInvoice["status"], MessageKey> = {
  draft: "finance.status.draft",
  open: "finance.status.open",
  partially_paid: "finance.status.partiallyPaid",
  paid: "finance.status.paid",
  overdue: "finance.status.overdue",
  disputed: "finance.status.disputed",
  credited: "finance.status.credited",
  void: "finance.status.void",
};

const STATUS_TONE: Record<
  FinanceInvoice["status"],
  "success" | "warn" | "danger" | undefined
> = {
  draft: undefined,
  open: undefined,
  partially_paid: "warn",
  paid: "success",
  overdue: "danger",
  disputed: "warn",
  credited: undefined,
  void: undefined,
};

// Where the figures came from and when. Both are the card's own honesty: a
// reader looking at money needs to know which system said so, and how long
// ago — and `offline_demo` says outright that these are demonstration data.
function FinanceProvenance({ summary }: Readonly<{ summary: FinanceSummary }>) {
  const t = useT();
  const { locale } = useLocale();
  if (!summary.provider) {
    return (
      <div className="co-card-actions">
        <Button small>{t("finance.connect")}</Button>
      </div>
    );
  }
  return (
    <p className="t-caption fin-provenance">
      <Landmark size={12} aria-hidden="true" />
      {summary.last_synced_at
        ? t("finance.syncedFrom", {
            provider: summary.provider,
            when: formatDate(summary.last_synced_at, locale, RECORD_ZONE),
          })
        : t("finance.fromNeverSynced", { provider: summary.provider })}
    </p>
  );
}
