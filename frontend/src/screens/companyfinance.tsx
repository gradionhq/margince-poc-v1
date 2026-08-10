import { Landmark } from "lucide-react";
import type { components } from "../api/schema";
import { Badge, Button } from "../design-system/atoms";
import { Sparkline } from "../design-system/readings";
import { formatDate, formatMoney } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemCodeOf, useFinanceSummary } from "./common";
import {
  medianDaysLabel,
  RECORD_ZONE,
  SectionCard,
  type SectionState,
} from "./company360";

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

// Which §7 card state each finance state renders as. The mapping is explicit
// rather than derived, because two of them are NOT what they look like:
// `unmapped` is a ready card with an action, not an error, and `error` still
// shows the last good figures.
const CARD_STATE: Record<FinanceState, SectionState> = {
  no_connection: "empty",
  unmapped: "empty",
  syncing: "loading",
  connected: "ready",
  // The last refresh failed, and the figures beside it are the last ones that
  // succeeded. `stale` rather than `failed`, because `failed` suppresses the
  // body — and a figure from this morning with its date on it is more useful
  // to a rep than an empty card with a retry button. The retry is offered
  // either way.
  error: "stale",
  stale: "stale",
};

/**
 * The lifecycles FIN-AC-3 authorises the card's absence for, and ONLY those.
 *
 * Named as the allowlist of absence rather than as an allowlist of presence,
 * because the two fail in opposite directions. A lifecycle this list forgets
 * gets a card that says "no accounting source connected" — a true statement
 * and a prompt to connect one. A lifecycle wrongly ON it gets NO card, and a
 * reader is never told the money is missing.
 *
 * `unknown` is the case that made this matter: every imported company carries
 * it, so an allowlist of presence hid finance from the majority of the book.
 * `disqualified` is the same shape — an account we stopped selling to may
 * still owe us money.
 */
const NEVER_INVOICED: ReadonlySet<string> = new Set([
  "target",
  "prospect",
  "opportunity",
]);

export function CompanyFinanceCard({
  orgId,
  lifecycle,
}: Readonly<{
  orgId: string;
  // The account's lifecycle. A target, a prospect or an opportunity has never
  // been invoiced, so the card is ABSENT for them rather than empty (FIN-AC-3)
  // — an empty finance card on a company we have never billed is a question
  // nobody asked.
  lifecycle?: string;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const query = useFinanceSummary(orgId);

  if (lifecycle && NEVER_INVOICED.has(lifecycle)) {
    return null;
  }
  // Resolved ONCE, above the branches. A former customer's money is history in
  // every state the card can be in, and the `error` state is where the mislabel
  // would mislead most: it keeps showing the last good figures, so a title
  // saying "Finance" there puts real money from a finished relationship under a
  // heading that reads as current.
  const title =
    lifecycle === "former_customer"
      ? t("finance.titleHistorical")
      : t("finance.title");
  if (query.isPending) {
    return (
      <SectionCard title={title} state="loading" emptyLabel={t("finance.none")}>
        {null}
      </SectionCard>
    );
  }
  if (query.isError) {
    // A refusal is not a failure. A reader whose role cannot see finance is
    // told so; retrying would refuse again, and a retry button that always
    // fails teaches them the card is broken.
    const withheld = problemCodeOf(query.error) === "permission_denied";
    return (
      <SectionCard
        title={title}
        state={withheld ? "withheld" : "failed"}
        emptyLabel={t("finance.none")}
        detail={withheld ? {} : { onRetry: () => void query.refetch() }}
      >
        {null}
      </SectionCard>
    );
  }
  const summary = query.data;
  return (
    <SectionCard
      title={title}
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
          value={amountOf(summary.net_invoiced, locale)}
        />
        <FinanceFigure
          label={t("finance.openBalance")}
          value={amountOf(summary.open_balance, locale)}
        />
        <FinanceFigure
          label={t("finance.overdue")}
          value={amountOf(summary.overdue, locale)}
          // Overdue money is the one reading on this card that is bad news
          // simply by being present.
          tone={(summary.overdue?.amount_minor ?? 0) > 0 ? "danger" : undefined}
        />
      </div>
      <PaymentBehaviour summary={summary} />
      <RecentInvoices summary={summary} />
    </>
  );
}

// A money reading, or nothing.
//
// BOTH halves are required. An amount with no currency cannot be rendered —
// defaulting to EUR would put a euro sign on a figure that might be dollars,
// which is a worse error than showing no figure. And a null amount is the
// server saying it could not compute one, so it must not become a zero: this
// card's whole rule is that "€0 open" and "we do not know" are different
// claims about a customer.
function amountOf(
  money: components["schemas"]["Money"] | undefined,
  locale: ReturnType<typeof useLocale>["locale"],
): string | undefined {
  if (money?.amount_minor == null || !money.currency) {
    return undefined;
  }
  return formatMoney(money.amount_minor, money.currency, locale);
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
        {medianDaysLabel(summary.median_days_after_due, t)}
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
