import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCan } from "../app/capability";
import { navigate } from "../app/router";
import { Badge } from "../design-system/atoms";
import { PanelBody } from "../design-system/panel";
import { formatDate, formatMoney } from "../format/format";
import { RECORD_ZONE } from "../format/timezone";
import { type Locale, useLocale, useT } from "../i18n";
import { throwProblem } from "./common";

// The commercial relationship: what we last put in front of this account,
// read from the SAME open deals the Deals tab already lists — so it is
// content inside that Panel (the `extra` slot on `DealsCard`), not a card of
// its own with a duplicate deal list under a different heading.
//
// TWO blocks now: what we last put in front of this account, and what it is
// already under contract for (ADR-0109/A160). The second used to be absent
// because nothing stored a contract, and deriving it from an accepted offer was
// refused — an accepted offer is not a signed agreement and carries no renewal
// date, and calling one the other is the small lie that costs a reader trust in
// every other figure on the page. The record exists now, so the block reports
// it instead.

type Organization360 = components["schemas"]["Organization360"];
type Deal = NonNullable<Organization360["deals"]>["data"][number];
type Offer = components["schemas"]["Offer"];
type ContractStrip = NonNullable<
  NonNullable<Organization360["state_strip"]>["contracts"]
>;

/**
 * CompanyLastOffer is the `extra` handed into the Deals tab's `DealsCard`
 * Panel: the last offer put in front of this account, read off its leading
 * open deal — an offer hangs off a deal rather than off a company, so there
 * is no account-wide offer read, and the alternative (one request per open
 * deal) would cost a page load to answer a single line. The deal it came
 * from is NAMED, so a reader can tell which offer they are looking at rather
 * than assuming it is the only one. Nothing to say draws nothing — a section
 * with no offer to name is not a section, and an empty block under the deal
 * rows would read as a missing feature rather than as "there is none".
 */
export function CompanyLastOffer({
  view,
}: Readonly<{ view?: Organization360 }>) {
  const t = useT();
  const { locale } = useLocale();
  const deals = view?.deals?.data ?? [];
  const truncated = view?.deals?.page?.has_more === true;
  // Offers are their own RBAC object: a reader who may see deals may not see
  // what we quoted. Without this the request is fired to be refused, and the
  // refusal renders as "no offer" — which is a claim about the account rather
  // than about the reader's grants.
  const mayRead = useCan("offer", "read");
  const leading = leadingDeal(deals, truncated);
  const offers = useQuery({
    // A DISTINCT key. The deal screen caches its own full offer list under
    // ["deal-offers", id]; sharing it would let this one-row response stand in
    // for that list and leave the deal screen showing a single offer.
    queryKey: ["deal-latest-offer", leading?.deal_id],
    enabled: Boolean(leading) && mayRead,
    queryFn: async () => {
      const { data, error } = await api.GET("/deals/{id}/offers", {
        params: { path: { id: leading?.deal_id ?? "" }, query: { limit: 1 } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const offer = offers.data?.data?.[0];
  if (!leading || !offer) {
    return null;
  }
  return (
    <PanelBody className="com-block">
      <span className="t-caption">
        {t("commercial.lastOffer", { deal: leading.name })}
      </span>
      <span className="co-row-meta">
        <button
          type="button"
          className="co-rowlink"
          onClick={() => navigate({ screen: "deals", id: leading.deal_id })}
        >
          {offer.offer_number ?? t("commercial.offerUnnumbered")}
        </button>
        <Badge tone={OFFER_TONE[offer.status]}>
          {t(`commercial.offer.${offer.status}`)}
        </Badge>
        <span>{offerAmount(offer, locale)}</span>
        {offer.valid_until && (
          <span>
            {t("commercial.validUntil", {
              when: formatDate(offer.valid_until, locale, RECORD_ZONE),
            })}
          </span>
        )}
      </span>
    </PanelBody>
  );
}

// The account's leading deal: largest by amount, id as the tiebreak so two
// equal deals do not swap between renders. Undefined when no deal can honestly
// be called the leading one.
//
// TWO refusals, both because picking wrong sends the reader to the wrong
// offer:
//
//   - Mixed currencies. A deal's amount carries no base conversion, so
//     comparing 100 JPY with 100 EUR picks a winner by coincidence.
//   - A truncated deals page. The 360 caps its sections, so the largest deal
//     may not be on the page — and "the last offer" pointing at the
//     second-largest deal is a line the reader has no way to question.
//
// In both cases the block is omitted rather than filled from a guess.
// Exported for its own test file: this is the one place the account's
// commercial reading picks a single deal to represent the account, and a
// wrong pick here is a wrong number presented as a fact about the account.
export function leadingDeal(
  deals: readonly Deal[],
  truncated: boolean,
): Deal | undefined {
  if (deals.length === 0 || truncated) {
    return undefined;
  }
  const currencies = new Set(
    deals.map((deal) => deal.amount?.currency).filter(Boolean),
  );
  if (currencies.size > 1) {
    return undefined;
  }
  return [...deals].sort((a, b) => {
    const left = a.amount?.amount_minor ?? -1;
    const right = b.amount?.amount_minor ?? -1;
    return right !== left ? right - left : a.deal_id.localeCompare(b.deal_id);
  })[0];
}

export function offerAmount(
  offer: Offer,
  locale: ReturnType<typeof useLocale>["locale"],
): string {
  // The GROSS, which is what a buyer sees on the document. `net_minor` is the
  // line sum before tax and would understate the offer beside every other
  // money figure on this page.
  if (offer.gross_minor == null || !offer.currency) {
    return "—";
  }
  return formatMoney(offer.gross_minor, offer.currency, locale);
}

const OFFER_TONE: Record<
  Offer["status"],
  "success" | "warn" | "danger" | undefined
> = {
  draft: undefined,
  sent: undefined,
  accepted: "success",
  rejected: "danger",
  expired: "warn",
  superseded: undefined,
};

/**
 * CompanyContractState is what the account is under contract for: the block the
 * mockup asks for, drawn from the state strip rather than a read of its own.
 *
 * TWO FIGURES, NEVER ONE. A three-year total and a per-year figure span
 * different periods, so a single number covering both would describe nothing.
 * Each is drawn only when the server sent it, and each is labelled with which
 * kind of figure it is — the server keeps them apart for the same reason.
 *
 * A pending cancellation reads as still under contract with an end date,
 * because that is what a notice period is. Rendering it as though the customer
 * had already gone would be wrong on the one day the reader most needs it right.
 */
export function CompanyContractState({
  view,
}: Readonly<{ view?: Organization360 }>) {
  const t = useT();
  const { locale } = useLocale();
  const contracts = view?.state_strip?.contracts;

  // Absent means the reader has no contract grant — a different fact from an
  // account with no agreements, and not this block's to report. The section
  // simply is not here, rather than saying "none".
  if (!contracts) {
    return null;
  }
  if (contracts.active_count === 0) {
    return (
      <PanelBody className="com-block">
        <span className="t-caption">{t("contracts.state.none")}</span>
      </PanelBody>
    );
  }

  return (
    <PanelBody className="com-block">
      <span className="t-caption">
        {t("contracts.state.title", { count: contracts.active_count })}
      </span>
      <span className="co-row-meta">
        {contractValues(contracts, locale, (amount) =>
          t("contracts.perYear", { amount }),
        ).map((value) => (
          <span key={value}>{value}</span>
        ))}
        {contracts.priced_count != null &&
          contracts.priced_count < contracts.active_count && (
            <span className="t-caption">
              {t("contracts.state.partial", {
                priced: contracts.priced_count,
                total: contracts.active_count,
              })}
            </span>
          )}
        {contracts.cancellation_pending &&
          contracts.cancellation_effective_on && (
            <Badge tone="warn">
              {t("contracts.state.endsOn", {
                when: formatDate(
                  contracts.cancellation_effective_on,
                  locale,
                  RECORD_ZONE,
                ),
              })}
            </Badge>
          )}
        {contracts.nearest_renewal_on && (
          <span>
            {t("contracts.state.renewsOn", {
              when: formatDate(
                contracts.nearest_renewal_on,
                locale,
                RECORD_ZONE,
              ),
            })}
          </span>
        )}
      </span>
    </PanelBody>
  );
}

// The account's contracted value, as one or two labelled figures.
//
// The two bases are never added together and never rendered as a bare number:
// a reader who cannot tell a three-year total from a per-year figure has been
// handed a number they will misuse. Exported for its own test, because getting
// this wrong is a wrong figure presented as a fact about the account.
export function contractValues(
  contracts: ContractStrip,
  locale: Locale,
  perYear: (amount: string) => string,
): readonly string[] {
  const currency = contracts.base_currency;
  if (!currency) {
    return [];
  }
  const figures: string[] = [];
  if (contracts.total_basis_value_minor_base != null) {
    figures.push(
      formatMoney(contracts.total_basis_value_minor_base, currency, locale),
    );
  }
  if (contracts.annualized_value_minor_base != null) {
    figures.push(
      perYear(
        formatMoney(contracts.annualized_value_minor_base, currency, locale),
      ),
    );
  }
  return figures;
}
