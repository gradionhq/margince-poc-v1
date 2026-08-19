import { Landmark } from "lucide-react";
import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { Badge, Button } from "../design-system/atoms";
import { Eyebrow } from "../design-system/eyebrow";
import { Panel, PanelBody } from "../design-system/panel";
import { Meter, Sparkline } from "../design-system/readings";
import { type SectionState, SurfaceState } from "../design-system/surfacestate";
import { formatDate, formatMoney } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemCodeOf, useFinanceSummary } from "./common";
import { medianDaysLabel, RECORD_ZONE } from "./company360";

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
      <Panel title={title}>
        <PanelBody>
          <SurfaceState state="loading" emptyLabel={t("finance.none")}>
            {null}
          </SurfaceState>
        </PanelBody>
      </Panel>
    );
  }
  if (query.isError) {
    // A refusal is not a failure. A reader whose role cannot see finance is
    // told so; retrying would refuse again, and a retry button that always
    // fails teaches them the card is broken.
    const withheld = problemCodeOf(query.error) === "permission_denied";
    return (
      <Panel title={title}>
        <PanelBody>
          <SurfaceState
            state={withheld ? "withheld" : "failed"}
            emptyLabel={t("finance.none")}
            detail={withheld ? {} : { onRetry: () => void query.refetch() }}
          >
            {null}
          </SurfaceState>
        </PanelBody>
      </Panel>
    );
  }
  const summary = query.data;
  const cardState = CARD_STATE[summary.state];
  // `stale` and `partial` still carry real rows, so the figures and the
  // provenance footer belong with them — a stale figure from this morning is
  // still a figure, with its `as of` beside it.
  const present =
    cardState === "ready" ||
    cardState === "empty" ||
    cardState === "stale" ||
    cardState === "partial";
  return (
    <Panel title={title} {...chromeOf(summary, present, t)}>
      <PanelBody>
        <SurfaceState
          state={cardState}
          emptyLabel={t(EMPTY_LABEL[summary.state] ?? "finance.none")}
          detail={{
            onRetry: () => void query.refetch(),
            staleAsOf: summary.last_synced_at
              ? formatDate(summary.last_synced_at, locale, RECORD_ZONE)
              : undefined,
          }}
        >
          <FinanceBody summary={summary} />
        </SurfaceState>
      </PanelBody>
    </Panel>
  );
}

// The three slots the panel's chrome fills, decided together because all three
// hang on the same question: does this panel have real figures to qualify?
//
// Where the figures came from and how fresh they are belongs beside the
// panel's NAME rather than under its last row — it qualifies every number in
// the panel, and a reader who has scrolled past the invoice table has stopped
// looking for it. The footer carries what the table could not fit, and the
// offer to connect a source is an action rather than a reading.
function chromeOf(
  summary: FinanceSummary,
  present: boolean,
  t: ReturnType<typeof useT>,
): { titleAction?: ReactNode; actions?: ReactNode; footer?: ReactNode } {
  if (!present) {
    return {};
  }
  return {
    titleAction: <FinanceProvenance summary={summary} />,
    actions: summary.provider ? undefined : <ConnectFinance />,
    footer: summary.truncated ? (
      <span className="t-caption">{t("finance.moreInvoices")}</span>
    ) : undefined,
  };
}

// What the card says when it has no figures. Two different sentences, because
// "nobody has connected an accounting system" and "this customer is not mapped
// to one of its customers" have different fixes.
const EMPTY_LABEL: Partial<Record<FinanceState, MessageKey>> = {
  no_connection: "finance.noConnection",
  unmapped: "finance.unmapped",
};

// The panel leads with the one figure that asks for a decision, and states
// the rest around it. Overdue money is what a rep acts on; net invoiced is
// context for the size of the relationship, and the payment habit is context
// for whether the overdue figure is a blip or the pattern.
function FinanceBody({ summary }: Readonly<{ summary: FinanceSummary }>) {
  const t = useT();
  const { locale } = useLocale();
  return (
    <>
      <div className="fin-split">
        <OverdueLead summary={summary} />
        <div className="fin-aside">
          <FinanceFigure
            label={t("finance.netInvoiced")}
            value={amountOf(summary.net_invoiced, locale)}
          />
          <PaymentBehaviour summary={summary} />
        </div>
      </div>
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
  hero,
}: Readonly<{ label: string; value?: string; hero?: boolean }>) {
  return (
    <div className="fin-figure">
      <Eyebrow>{label}</Eyebrow>
      <span className={hero ? "fin-amount fin-amount-hero" : "fin-amount"}>
        {value ?? "—"}
      </span>
    </div>
  );
}

// Overdue money, at the size of the decision it asks for, with everything the
// panel knows about WHY it is that size underneath: what share of the open
// balance it is, how late this customer usually pays, and the two halves of
// the open balance drawn against each other.
//
// The hero figure is drawn whatever the rest of the panel can say. The
// sentence and the bar under it each appear only when their own inputs
// arrived, so a customer with no settled invoices gets the figure and nothing
// invented beneath it.
function OverdueLead({ summary }: Readonly<{ summary: FinanceSummary }>) {
  const t = useT();
  const { locale } = useLocale();
  const split = openSplitOf(summary);
  return (
    <div className="fin-lead">
      <FinanceFigure
        label={t("finance.overdue")}
        value={amountOf(summary.overdue, locale)}
        hero
      />
      <FinanceLede summary={summary} split={split} />
      {split && (
        <>
          <Meter
            value={split.overdue}
            max={split.open}
            label={t("finance.overdueShareLabel")}
            tone="danger"
            restTone="accent"
          />
          {/* The bar's two colours, named. Without this the reader is left to
              infer which half is which from the tones alone, and the tones are
              what a reader who cannot distinguish them needs the words for. */}
          <p className="fin-legend">
            <Badge tone="danger" quiet>
              {t("finance.legendOverdue", {
                amount: formatMoney(split.overdue, split.currency, locale),
              })}
            </Badge>
            <Badge tone="accent" quiet>
              {t("finance.legendOpen", {
                amount: formatMoney(split.open, split.currency, locale),
              })}
            </Badge>
          </p>
        </>
      )}
    </div>
  );
}

// What the overdue figure MEANS, in whichever clauses the data supports: its
// share of the open balance, and how late this customer settles.
//
// Each clause is a sentence and is terminated here rather than in the catalog,
// because `medianDaysLabel` is also read on its own elsewhere on the record,
// where a trailing full stop mid-line would be wrong. One terminator, spelled
// once, so the two clauses cannot end differently from each other.
function FinanceLede({
  summary,
  split,
}: Readonly<{ summary: FinanceSummary; split: OpenSplit | null }>) {
  const t = useT();
  const clauses: string[] = [];
  if (split) {
    clauses.push(
      t("finance.shareOfOpen", {
        percent: Math.round((split.overdue / split.open) * 100),
      }),
    );
  }
  if (summary.median_days_after_due != null) {
    clauses.push(medianDaysLabel(summary.median_days_after_due, t));
  }
  if (clauses.length === 0) {
    return null;
  }
  return (
    <p className="fin-lede">{clauses.map((one) => `${one}.`).join(" ")}</p>
  );
}

// The open balance as its two halves in ONE currency: what is overdue, and
// the whole it is part of.
//
// Null unless the halves make a proportion at all. Different currencies is a
// proportion of nothing. Nothing open would put a full bar over "€0 open",
// which reads as an account entirely in arrears rather than one that owes us
// nothing. And overdue ABOVE open is not a share that has run high, it is two
// figures that contradict each other: the mirror says money is late that it
// also says is not outstanding.
//
// The third case is refused rather than clamped because clamping picks a
// winner between two figures the panel cannot choose between, and picks it
// silently — a bar pinned at 100% beside a sentence reading "115% of
// everything open" is the same disagreement with a coat of paint. The overdue
// figure above still draws: it is what the mirror reported, and it is the one
// number a reader can act on. What refuses is only the claim about a
// relationship between the two.
type OpenSplit = { overdue: number; open: number; currency: string };

function openSplitOf(summary: FinanceSummary): OpenSplit | null {
  const open = summary.open_balance;
  const overdue = summary.overdue;
  if (
    open?.amount_minor == null ||
    !open.currency ||
    overdue?.amount_minor == null ||
    open.currency !== overdue.currency ||
    open.amount_minor <= 0 ||
    overdue.amount_minor > open.amount_minor
  ) {
    return null;
  }
  return {
    overdue: overdue.amount_minor,
    open: open.amount_minor,
    currency: open.currency,
  };
}

// How they pay, as the shape of it: days late per settled invoice, oldest
// first. A line climbing to the right is a customer slipping, which is the one
// thing the median beside the overdue figure cannot say, because averaging is
// what hides it.
function PaymentBehaviour({ summary }: Readonly<{ summary: FinanceSummary }>) {
  const t = useT();
  const series = summary.payment_behaviour ?? [];
  if (series.length < 2) {
    return null;
  }
  return (
    <div className="fin-behaviour">
      <Eyebrow>{t("finance.behaviour")}</Eyebrow>
      <Sparkline points={series} label={t("finance.behaviourShape")} />
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
    // Four columns, and nothing is dropped to get there: the issue, due and
    // settlement dates read as one life of one invoice rather than three
    // columns a reader has to line up by eye. It is still every date the
    // server sent, which is what an invoice is checkable against. The table
    // scrolls sideways inside its panel rather than widening the record page,
    // the containment DataTable gives every list built from it (atoms.tsx).
    <div className="table-scroll">
      <table className="table fin-table">
        <thead>
          <tr>
            <th>{t("finance.col.invoice")}</th>
            <th>{t("finance.col.dates")}</th>
            <th className="fin-col-amount">{t("finance.col.amount")}</th>
            <th className="fin-col-status">{t("finance.col.status")}</th>
          </tr>
        </thead>
        <tbody>
          {invoices.map((invoice) => (
            <InvoiceRow key={invoice.id} invoice={invoice} locale={locale} />
          ))}
        </tbody>
      </table>
    </div>
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
  const late = invoice.days_late != null && invoice.days_late > 0;
  return (
    // A row the customer still owes past its due date carries the tint, so the
    // rows that need chasing are findable without reading the status column of
    // every one of them. A settled invoice that was paid late is history, not
    // work, and takes no tint however late it was.
    <tr className={late && invoice.status !== "paid" ? "fin-row-late" : ""}>
      <td className="fin-cell-id">
        {invoice.number ?? t("finance.unnumbered")}
      </td>
      <td className="fin-cell-dates">
        {formatDate(invoice.issued_at, locale, RECORD_ZONE)} →{" "}
        {invoice.due_at ? formatDate(invoice.due_at, locale, RECORD_ZONE) : "—"}
        {/* When it was actually settled, appended rather than given a column
            of its own: an unpaid invoice has no date to put there, and a
            column of dashes states nothing the status does not. */}
        {invoice.paid_at && (
          <span className="fin-cell-paid">
            {" · "}
            {t("finance.paidOn", {
              when: formatDate(invoice.paid_at, locale, RECORD_ZONE),
            })}
          </span>
        )}
      </td>
      <td className="fin-col-amount">
        {formatMoney(invoice.gross_minor, invoice.currency, locale)}
      </td>
      {/* HOW late, and whether it was, as ONE reading. "Paid" and "paid 22
          days late" are different facts about a customer, and splitting them
          across a badge and a caption made the reader assemble the sentence.
          Zero and negative say nothing worth a line: on time is what the
          status alone already says. */}
      <td className="fin-col-status">
        <Badge tone={STATUS_TONE[invoice.status]} quiet>
          {late && invoice.days_late != null
            ? t(daysLateKey(invoice.status, invoice.days_late), {
                days: invoice.days_late,
              })
            : t(STATUS_LABEL[invoice.status])}
        </Badge>
      </td>
    </tr>
  );
}

// One day late is one day late, not "1 days late". The three shipped locales
// (A100 pins de-DE and en-GB; vi ships alongside) all split at exactly one, so
// the count picks the key and no plural engine is needed — a locale whose rules
// differ would need one, and would announce itself by adding a fourth catalog.
function daysLateKey(
  status: FinanceInvoice["status"],
  days: number,
): MessageKey {
  if (status === "paid") {
    return days === 1 ? "finance.paidDayLate" : "finance.paidDaysLate";
  }
  return days === 1 ? "finance.overdueDay" : "finance.overdueDays";
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
// Which accounting source the figures came from and how fresh they are — the
// qualification every number in the panel inherits, so it sits beside the
// panel's name rather than after its last row.
//
// Null when nothing is connected: there is no source to name, and the offer to
// connect one is an ACTION, which the panel places with its other actions
// instead of in the line that reports provenance.
function FinanceProvenance({ summary }: Readonly<{ summary: FinanceSummary }>) {
  const t = useT();
  const { locale } = useLocale();
  if (!summary.provider) {
    return null;
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

// The offer to connect an accounting source, for a panel that has none. An
// action rather than a reading, so it never sits in the provenance line, and
// never on a panel that already has a source to report.
function ConnectFinance() {
  const t = useT();
  return (
    <div className="co-card-actions">
      <Button small>{t("finance.connect")}</Button>
    </div>
  );
}
